// Package console serves the conversation viewer: a local HTTP server that
// hosts the built page from console/dist and a small JSON API backed by
// this machine's identity. The API shape is the S14 read contract and the
// S13 post contract of the plan, served locally first; hosting it later
// is the gateway shell over the same handlers.
package console

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quantumwake/parley/pkg/agentaccess"
	"github.com/quantumwake/parley/pkg/conversation"
	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/naming"
	"github.com/quantumwake/parley/pkg/plugin"
	"github.com/quantumwake/parley/pkg/store"
	"github.com/quantumwake/statefs/pkg/identityfile"
)

// Server holds the store and the identity the API acts as. The identity
// can be switched while the console runs, so handlers read all three
// through actor, never the fields directly.
//
// The API is reachable only by the page this launch opened: every /v1/
// request must be addressed to this listener on a loopback host (so a
// rebound DNS name is refused) and carry this launch's token, and every
// write must also send JSON from the console's own origin. The token lives
// in the URL fragment the browser opens, which is never sent to a server
// or written to an access log; the page moves it into sessionStorage and
// clears it from the address bar.
type Server struct {
	mu     sync.RWMutex
	env    plugin.Env
	st     store.Store
	claims plugin.Claims
	page   fs.FS
	token  string // per launch; required on every API request
	port   string // the listener's port; the Host header must name it

	// started remembers each conversation's first-row time (0 when it has
	// none): a row-0 read loads the namespace's first block, so each
	// conversation pays it once per console, not once per list refresh.
	startedMu sync.Mutex
	started   map[string]int64

	peopleMu sync.Mutex
	people   map[string]*agentaccess.Client // per identity file, so its statefs.ai token is reused
}

// actor is the identity a request acts as, read once per request so a
// switch mid-request cannot mix two identities.
type actor struct {
	env    plugin.Env
	st     store.Store
	claims plugin.Claims
}

func (s *Server) actor() actor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return actor{env: s.env, st: s.st, claims: s.claims}
}

// New builds a server for env over the store env selects.
func New(ctx context.Context, env plugin.Env, page fs.FS) (*Server, error) {
	st, err := plugin.StoreFromEnv(env)
	if err != nil {
		return nil, err
	}

	token, err := newToken()
	if err != nil {
		return nil, err
	}

	return &Server{env: env, st: st, claims: plugin.MyClaims(ctx, env), page: page, token: token}, nil
}

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("console token: %w", err)
	}

	return hex.EncodeToString(b[:]), nil
}

// Handler is the routed API behind the console's guard, plus the static
// page.
func (s *Server) Handler() http.Handler {
	return s.guard(s.routes())
}

// guard admits an API request only from this launch's own page: a
// loopback Host naming this listener's port (403), this launch's bearer
// token (401), and for anything but a read, JSON from the same origin
// (403). The static page is served without it, since that is where the
// token is picked up.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}

		if !s.ownHost(r.Host) {
			writeJSON(w, 403, map[string]string{"error": "the console answers only on its own loopback address"})
			return
		}

		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || s.token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeJSON(w, 401, map[string]string{"error": "open the console from the link `parley console` prints"})
			return
		}

		switch r.Method {
		case http.MethodGet, http.MethodHead:
		default:
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || r.Header.Get("Origin") != "http://"+r.Host {
				writeJSON(w, 403, map[string]string{"error": "the console accepts changes only from its own page"})
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// ownHost answers whether a Host header names this console: localhost or
// a loopback address, on the listener's port.
func (s *Server) ownHost(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || port != s.port {
		return false
	}

	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// routes is the API without the guard.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/me", s.me)
	mux.HandleFunc("GET /v1/identities", s.identities)
	mux.HandleFunc("POST /v1/identity", s.switchIdentity)
	mux.HandleFunc("GET /v1/tenants", s.tenants)
	mux.HandleFunc("POST /v1/tenant", s.switchTenant)
	mux.HandleFunc("GET /v1/conversations", s.list)
	mux.HandleFunc("POST /v1/conversations", s.create)
	mux.HandleFunc("PATCH /v1/conversations/{id}", s.rename)
	mux.HandleFunc("DELETE /v1/conversations/{id}", s.remove)
	mux.HandleFunc("GET /v1/people", s.lookup)
	mux.HandleFunc("GET /v1/conversations/{id}/grants", s.grants)
	mux.HandleFunc("POST /v1/conversations/{id}/grants", s.grant)
	mux.HandleFunc("DELETE /v1/conversations/{id}/grants/{username}", s.revoke)
	mux.HandleFunc("GET /v1/conversations/{id}/events", s.events)
	mux.HandleFunc("GET /v1/conversations/{id}/head", s.head)
	mux.HandleFunc("POST /v1/conversations/{id}/posts", s.post)
	mux.HandleFunc("GET /v1/subscriptions", s.subscriptions)
	mux.HandleFunc("POST /v1/subscriptions", s.subscribe)
	mux.HandleFunc("DELETE /v1/subscriptions/{name}", s.unsubscribe)
	mux.Handle("/", s.static())
	return mux
}

// Serve listens on addr (127.0.0.1:0 picks a free port), optionally opens
// the browser, and blocks until ctx is done.
func (s *Server) Serve(ctx context.Context, addr string, open bool) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	_, s.port, _ = net.SplitHostPort(ln.Addr().String())
	url := "http://" + ln.Addr().String()
	link := url + "/#token=" + s.token
	if open {
		// The link carries the launch token, so it goes to the browser, not
		// to output a recorded session would capture.
		fmt.Printf("parley console: %s  (identity %s; opened in your browser)\n", url, s.actor().claims.Sub)
		openBrowser(link)
	} else {
		fmt.Printf("parley console: %s  (identity %s)\n", link, s.actor().claims.Sub)
	}

	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); _ = srv.Close() }()
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return err
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	a := s.actor()
	notices := []string{}
	if info, ok := plugin.CheckServer(r.Context(), a.env, false); ok {
		notices = append(notices, plugin.FloorNotices(info, plugin.ClientVersion)...)
	}

	writeJSON(w, 200, map[string]any{"username": a.claims.Sub, "membership": a.claims.Membership, "tenant": a.claims.Tenant,
		"is_admin": a.claims.IsAdmin, "directory": a.env.Directory, "caps": a.claims.Caps,
		"version": plugin.ClientVersion, "notices": notices})
}

type convOut struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Mode        string      `json:"mode"`
	Title       string      `json:"title,omitempty"`
	Description string      `json:"description,omitempty"`
	StartedMs   any         `json:"started_ms,omitempty"`
	ActiveMs    any         `json:"active_ms,omitempty"` // newest delivered row; sessions recorded from this machine only
	Tags        any         `json:"tags,omitempty"`
	Agent       string      `json:"agent,omitempty"`
	Session     string      `json:"session,omitempty"`
	Access      string      `json:"access"`
	Subscribed  string      `json:"subscribed,omitempty"`
	Scope       store.Scope `json:"scope"`
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	a := s.actor()
	filter := store.Scope{"kind": "conversation"}
	if m := r.URL.Query().Get("mode"); m != "" {
		filter["mode"] = m
	}

	if t := r.URL.Query().Get("tag"); t != "" {
		filter["tags"] = []string{t}
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 200
	}

	metas, err := a.st.Find(r.Context(), filter, limit)
	if err != nil {
		writeErr(w, err)
		return
	}

	subs := map[string]string{}
	for _, sub := range plugin.Subscriptions(a.env) {
		subs[sub.ID] = sub.Mode
	}

	// Last activity is known for sessions recorded from this machine (the
	// daemon stamps it locally); the viewer falls back to started_ms for
	// the rest. The directory has no last-append time to read instead.
	active := plugin.ActivityByID(a.env)
	out := make([]convOut, 0, len(metas))
	for _, m := range metas {
		access := "grant?"
		switch {
		case m.Owner == "":
			access = "tenant"
		case a.claims.Membership != "" && m.Owner == a.claims.Membership:
			access = "owner"
		case a.claims.IsAdmin:
			access = "admin"
		}

		// Conversations born before the scope carried started_ms still
		// have their start time in the display name; recover it so the
		// index can group them by day instead of a single undated pile.
		started := m.Scope["started_ms"]
		if started == nil {
			if t, ok := naming.StartedFromName(m.DisplayName); ok {
				started = t.UnixMilli()
			}
		}

		var activeMs any
		if t, ok := active[m.ID]; ok {
			activeMs = t.UnixMilli()
		}

		out = append(out, convOut{ID: m.ID, Name: m.DisplayName, Mode: str(m.Scope["mode"]), Title: str(m.Scope["title"]), Description: str(m.Scope["description"]), StartedMs: started, ActiveMs: activeMs,
			Tags: m.Scope["tags"], Agent: str(m.Scope["agent"]), Session: str(m.Scope["session"]), Access: access, Subscribed: subs[m.ID], Scope: m.Scope})
	}

	s.backfillStarted(r.Context(), a.st, out)
	writeJSON(w, 200, map[string]any{"conversations": out})
}

// create opens a shared conversation owned by the console's current
// identity, the same call `parley create` makes.
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	a := s.actor()
	var in struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		writeJSON(w, 400, map[string]string{"error": "body must be {name, description?, tags?}"})
		return
	}

	ns, err := a.st.Open(r.Context(), in.Name, naming.Shared{Name: in.Name, Description: in.Description, Tags: in.Tags}.Scope())
	if err != nil {
		writeErr(w, err)
		return
	}

	plugin.NamesPut(a.env, ns.DisplayName, ns.ID)
	writeJSON(w, 200, map[string]any{"id": ns.ID, "name": ns.DisplayName})
}

// rename relabels a conversation's title, description or tags: the same
// call `parley describe` makes. A namespace label rename needs the own
// capability; when the identity lacks it, the meta.purpose row still
// records the change (labelRenamed is false so the console can say so).
func (s *Server) rename(w http.ResponseWriter, r *http.Request) {
	a := s.actor()
	var in struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "body must be {title?, description?, tags?}"})
		return
	}

	if in.Title == "" && in.Description == "" && len(in.Tags) == 0 {
		writeJSON(w, 400, map[string]string{"error": "rename needs title, description or tags"})
		return
	}

	var out strings.Builder
	if err := plugin.Describe(r.Context(), a.env, r.PathValue("id"), in.Title, in.Description, in.Tags, &out); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, map[string]any{"ok": strings.TrimSpace(out.String()), "labelRenamed": !strings.Contains(out.String(), "own capability")})
}

// remove deletes a conversation everywhere (directory pin and every
// member's copy): the same call `parley delete` makes. Needs a credential
// with the own capability on this identity; switch to one first with
// POST /v1/identity if the console's current identity lacks it.
func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	a := s.actor()
	var out strings.Builder
	if err := plugin.DeleteConversation(r.Context(), a.env, r.PathValue("id"), &out); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, map[string]any{"ok": strings.TrimSpace(out.String())})
}

// grants, grant and revoke are `parley grant` and its inverse: who may read
// or write a shared conversation. statefs enforces who may change them.
func (s *Server) grants(w http.ResponseWriter, r *http.Request) {
	g, err := plugin.ListAccess(r.Context(), s.actor().env, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, map[string]any{"grants": g})
}

func (s *Server) grant(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Access   string `json:"access"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || strings.TrimSpace(in.Username) == "" {
		writeJSON(w, 400, map[string]string{"error": "body must be {username, access}"})
		return
	}

	if in.Access != "read" && in.Access != "write" {
		writeJSON(w, 400, map[string]string{"error": "access must be read or write (write implies read)"})
		return
	}

	if err := plugin.GrantAccess(r.Context(), s.actor().env, r.PathValue("id"), strings.TrimSpace(in.Username), in.Access, io.Discard); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if err := plugin.RevokeAccess(r.Context(), s.actor().env, r.PathValue("id"), r.PathValue("username")); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, map[string]any{"ok": true})
}

// lookup finds people in statefs.ai to grant a conversation to: the acting
// identity signs in there with its own key. What statefs.ai refuses comes
// back as a note beside an empty list, so the grant form falls back to an
// exact username instead of failing.
func (s *Server) lookup(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) < 2 {
		writeJSON(w, 200, map[string]any{"people": []agentaccess.Person{}})
		return
	}

	a := s.actor()
	env := a.env
	local := localPeople(env, a.claims.Sub, q)
	c, err := s.peopleClient(env)
	if err != nil {
		writeJSON(w, 200, map[string]any{"people": local, "note": err.Error()})
		return
	}

	p, err := c.People(r.Context(), q, 10)
	switch {
	case errors.Is(err, agentaccess.ErrSignIn):
		writeJSON(w, 200, map[string]any{"people": local, "note": "statefs.ai answers only for agents it issued (created in its portal), and " + c.Username + " was not, so only identities on this machine are suggested"})
	case errors.Is(err, agentaccess.ErrLookupOff), errors.Is(err, agentaccess.ErrUnavailable):
		writeJSON(w, 200, map[string]any{"people": local, "note": err.Error() + "; only identities on this machine are suggested"})
	case err != nil:
		writeErr(w, err)
	default:
		writeJSON(w, 200, map[string]any{"people": mergePeople(p, local)})
	}
}

// localPeople are the identities enrolled on this machine whose username or
// name contains q, other than the one acting (by username: the default
// identity file and a named copy of it are the same identity): people
// search that works without statefs.ai, for the identities this machine
// already knows.
func localPeople(env plugin.Env, acting, q string) []agentaccess.Person {
	q = strings.ToLower(q)
	out := []agentaccess.Person{}
	seen := map[string]bool{acting: true}
	for _, id := range plugin.Identities(env.IdentityPath) {
		if id.Current || id.Username == "" || seen[id.Username] {
			continue
		}
		seen[id.Username] = true
		if !strings.Contains(strings.ToLower(id.Username), q) && !strings.Contains(strings.ToLower(id.Name), q) {
			continue
		}
		out = append(out, agentaccess.Person{Name: "on this machine", Agents: []agentaccess.Agent{{Label: id.Name, Identity: id.Username}}})
	}

	return out
}

// mergePeople appends the local suggestions statefs.ai did not already name.
func mergePeople(remote, local []agentaccess.Person) []agentaccess.Person {
	seen := map[string]bool{}
	for _, p := range remote {
		for _, a := range p.Agents {
			seen[a.Identity] = true
		}
	}

	for _, p := range local {
		if !seen[p.Agents[0].Identity] {
			remote = append(remote, p)
		}
	}

	return remote
}

func (s *Server) peopleClient(env plugin.Env) (*agentaccess.Client, error) {
	path := env.IdentityPath
	if path == "" {
		path = identityfile.DefaultPath()
	}

	s.peopleMu.Lock()
	defer s.peopleMu.Unlock()

	if c, ok := s.people[path]; ok {
		return c, nil
	}

	f, err := identityfile.Read(path)
	if err != nil {
		return nil, fmt.Errorf("no identity to sign in to statefs.ai with: %w", err)
	}

	key, err := f.Private()
	if err != nil {
		return nil, err
	}

	if s.people == nil {
		s.people = map[string]*agentaccess.Client{}
	}
	c := &agentaccess.Client{Base: agentaccess.Base(), Username: f.Username, Key: key, UserAgent: plugin.UserAgent()}
	s.people[path] = c
	return c, nil
}

// maxDateProbes caps how many conversations the index will read a row from
// to find their start time. Names have carried a timestamp since 0.2.15, so
// this only touches older ones and the cost is bounded.
const maxDateProbes = 64

// backfillStarted fills started_ms for conversations whose scope and
// display name carry no time, by reading their first row: the earliest row
// is when the session began. Bounded and run in parallel, because it costs
// one member read each.
func (s *Server) backfillStarted(ctx context.Context, st store.Store, out []convOut) {
	s.startedMu.Lock()
	if s.started == nil {
		s.started = map[string]int64{}
	}
	var todo []int
	for i := range out {
		if out[i].StartedMs != nil {
			continue
		}
		if ms, ok := s.started[out[i].ID]; ok {
			if ms > 0 {
				out[i].StartedMs = ms
			}
			continue
		}
		todo = append(todo, i)
	}
	s.startedMu.Unlock()

	if len(todo) > maxDateProbes {
		todo = todo[:maxDateProbes]
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, i := range todo {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ms, ok := int64(0), true
			for e, err := range st.Scan(ctx, out[i].ID, 0, 1) {
				ok = err == nil
				if ok {
					ms = e.TSMs
				}

				break
			}
			if !ok {
				return
			}
			if ms > 0 {
				out[i].StartedMs = ms
			}
			s.startedMu.Lock()
			s.started[out[i].ID] = ms
			s.startedMu.Unlock()
		}(i)
	}

	wg.Wait()
}

// events answers rows of a conversation from a position. With ?tail=N and no
// from, it answers the last N rows instead, which is how a reader opens a
// conversation: at its end, not at its first row.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	a := s.actor()
	id := r.PathValue("id")
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 500
	}

	conv := conversation.Attach(a.st, id)
	if tail, _ := strconv.ParseInt(r.URL.Query().Get("tail"), 10, 64); tail > 0 && r.URL.Query().Get("from") == "" {
		h, err := conv.Head(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}

		from = max(0, int64(h)-tail)
		if int64(limit) < tail {
			limit = int(tail)
		}
	}

	var rows []map[string]any
	pos := from
	for e, err := range conv.Scan(r.Context(), store.Position(from), store.Position(to)) {
		if err != nil {
			writeErr(w, err)
			return
		}

		m, _ := e.Record()
		m["position"] = pos
		pos++
		rows = append(rows, m)
		if len(rows) >= limit {
			break
		}
	}

	// An open-ended read's head is where it stopped (a full page may trail the
	// true head by a page; the next poll closes the gap). Asking Head on every
	// live poll cost a row-0 read each time, which on a namespace of fat rows
	// is a full block decode (the group-d-1 OOM of 2026-09-18).
	head := store.Position(pos)
	if to > 0 {
		head, _ = conv.Head(r.Context())
	}
	if rows == nil {
		rows = []map[string]any{}
	}

	writeJSON(w, 200, map[string]any{"events": rows, "from": from, "next": pos, "head": head})
}

func (s *Server) head(w http.ResponseWriter, r *http.Request) {
	a := s.actor()
	h, err := a.st.Head(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, map[string]any{"head": h})
}

func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	a := s.actor()
	var in struct {
		Kind    string   `json:"kind"`
		Text    string   `json:"text"`
		To      string   `json:"to"`
		ReplyTo string   `json:"reply_to"`
		Tags    []string `json:"tags"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil || strings.TrimSpace(in.Text) == "" {
		writeJSON(w, 400, map[string]string{"error": "body must be {kind?, text, to?, reply_to?, tags?}"})
		return
	}

	if in.Kind == "" {
		in.Kind = "comment"
	}

	// Work posts carry rules (who may claim or close, and with what outcome)
	// that the parley post path enforces; the console posts exchange only.
	switch strings.TrimPrefix(in.Kind, "post.") {
	case "request", "claim", "close":
		writeJSON(w, 400, map[string]string{"error": "work posts (request, claim, close) are made with `parley post` or the post_message tool"})
		return
	}

	if in.To == "" {
		in.To = "*"
	}

	e := event.Event{ID: event.NewID(), TSMs: time.Now().UnixMilli(), Source: event.SourceProduct,
		Kind: event.Kind("post." + strings.TrimPrefix(in.Kind, "post.")), Identity: a.claims.Sub, To: in.To, ReplyTo: in.ReplyTo, Tags: in.Tags}
	if in.ReplyTo != "" {
		e.ParentID, e.Thread = in.ReplyTo, in.ReplyTo
	} else {
		e.Thread = e.ID
	}

	e.Content, _ = json.Marshal(map[string]any{"text": in.Text})
	if err := e.Validate(); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}

	pos, err := conversation.Attach(a.st, r.PathValue("id")).Append(r.Context(), false, e)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, map[string]any{"event_id": e.ID, "position": pos})
}

type subOut struct {
	plugin.Subscription
	Head   int64 `json:"head"`
	Unread int64 `json:"unread"`
}

// subscriptions lists what this agent follows with how much is unread
// (head minus cursor); heads are read in parallel, bounded.
func (s *Server) subscriptions(w http.ResponseWriter, r *http.Request) {
	a := s.actor()
	subs := plugin.Subscriptions(a.env)
	out := make([]subOut, len(subs))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, sub := range subs {
		wg.Add(1)
		go func(i int, sub plugin.Subscription) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			o := subOut{Subscription: sub, Head: -1}
			if h, err := a.st.Head(r.Context(), sub.ID); err == nil {
				o.Head = int64(h)
				if o.Unread = int64(h) - sub.Cursor; o.Unread < 0 {
					o.Unread = 0
				}
			}

			out[i] = o
		}(i, sub)
	}

	wg.Wait()
	writeJSON(w, 200, map[string]any{"subscriptions": out})
}

func (s *Server) subscribe(w http.ResponseWriter, r *http.Request) {
	a := s.actor()
	var in struct {
		Name string `json:"name"`
		Mode string `json:"mode"`
		As   string `json:"as"` // handle to speak under in this conversation
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
		writeJSON(w, 400, map[string]string{"error": "body must be {name, mode?, as?}"})
		return
	}

	var out strings.Builder
	if err := plugin.Join(r.Context(), a.env, in.Name, in.Mode, "all", in.As, &out); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, map[string]any{"ok": strings.TrimSpace(out.String())})
}

func (s *Server) unsubscribe(w http.ResponseWriter, r *http.Request) {
	a := s.actor()
	var out strings.Builder
	if err := plugin.Leave(a.env, r.PathValue("name"), &out); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, map[string]any{"ok": strings.TrimSpace(out.String())})
}

// identities lists this machine's identities, marking the one the console
// acts as.
func (s *Server) identities(w http.ResponseWriter, r *http.Request) {
	a := s.actor()
	writeJSON(w, 200, map[string]any{"identities": plugin.Identities(a.env.IdentityPath)})
}

// switchIdentity makes the console act as another identity on this
// machine. It changes this console only, not the machine's default, and
// switches only once the new identity has logged in.
func (s *Server) switchIdentity(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || in.Name == "" {
		writeJSON(w, 400, map[string]string{"error": "body must be {name}"})
		return
	}

	id, err := plugin.ResolveIdentity(in.Name)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}

	env, claims, err := plugin.SignIn(r.Context(), s.actor().env.WithIdentity(id))
	if err != nil || claims.Sub == "" {
		// 403, not 401: the page reads a 401 as its own launch token expiring.
		writeJSON(w, 403, map[string]string{"error": "could not sign in as " + id.Username + ": " + fmt.Sprint(err)})
		return
	}

	st, err := plugin.StoreFromEnv(env)
	if err != nil {
		writeErr(w, err)
		return
	}

	s.mu.Lock()
	s.env, s.st, s.claims = env, st, claims
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"username": claims.Sub, "tenant": claims.Tenant})
}

// tenants lists the tenants the acting identity is seated in, and which
// its key can sign in to, for the console's tenant picker.
func (s *Server) tenants(w http.ResponseWriter, r *http.Request) {
	a := s.actor()
	ts, err := plugin.Tenants(r.Context(), a.env)
	if err != nil {
		writeJSON(w, 200, map[string]any{"tenants": []plugin.Tenant{}, "current": a.claims.Tenant, "note": err.Error()})
		return
	}

	writeJSON(w, 200, map[string]any{"tenants": ts, "current": a.claims.Tenant})
}

// switchTenant acts as the same identity in another of its tenants, for
// this console only (the machine's default is left as it is).
func (s *Server) switchTenant(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Tenant string `json:"tenant"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || in.Tenant == "" {
		writeJSON(w, 400, map[string]string{"error": "body must be {tenant}"})
		return
	}

	env := s.actor().env
	env.Tenant = in.Tenant
	env, claims, err := plugin.SignIn(r.Context(), env)
	if err != nil || claims.Sub == "" {
		writeJSON(w, 403, map[string]string{"error": "could not sign in to " + in.Tenant + ": " + fmt.Sprint(err)})
		return
	}

	st, err := plugin.StoreFromEnv(env)
	if err != nil {
		writeErr(w, err)
		return
	}

	s.mu.Lock()
	s.env, s.st, s.claims = env, st, claims
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"username": claims.Sub, "tenant": claims.Tenant})
}

func (s *Server) static() http.Handler {
	if s.page == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "console page not built", 404) })
	}

	files := http.FS(s.page)
	fsrv := http.FileServer(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f, err := files.Open(r.URL.Path); err == nil {
			f.Close()
			fsrv.ServeHTTP(w, r)
			return
		}

		r.URL.Path = "/"
		fsrv.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code := 500
	switch {
	case errors.Is(err, store.ErrNotFound):
		code = 404
	case errors.Is(err, store.ErrRefused):
		code = 403
	case errors.Is(err, store.ErrInvalidEvent):
		code = 400
	}

	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

var _ = naming.ModeShared

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}

	_ = cmd.Start()
}

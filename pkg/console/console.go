// Package console serves the conversation viewer: a local HTTP server that
// hosts the built page from console/dist and a small JSON API backed by
// this machine's identity. The API shape is the S14 read contract and the
// S13 post contract of the plan, served locally first; hosting it later
// is the gateway shell over the same handlers.
package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quantumwake/statefs.ai/pkg/conversation"
	"github.com/quantumwake/statefs.ai/pkg/event"
	"github.com/quantumwake/statefs.ai/pkg/naming"
	"github.com/quantumwake/statefs.ai/pkg/plugin"
	"github.com/quantumwake/statefs.ai/pkg/store"
)

// Server holds the store and the identity the API acts as.
type Server struct {
	env    plugin.Env
	st     store.Store
	claims plugin.Claims
	page   fs.FS
}

// New builds a server for env over the store env selects.
func New(ctx context.Context, env plugin.Env, page fs.FS) (*Server, error) {
	st, err := plugin.StoreFromEnv(env)
	if err != nil {
		return nil, err
	}

	return &Server{env: env, st: st, claims: plugin.MyClaims(ctx, env), page: page}, nil
}

// Handler is the routed API plus the static page.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/me", s.me)
	mux.HandleFunc("GET /v1/conversations", s.list)
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

	url := "http://" + ln.Addr().String()
	fmt.Printf("parley console: %s  (identity %s)\n", url, s.claims.Sub)
	if open {
		openBrowser(url)
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
	writeJSON(w, 200, map[string]any{"username": s.claims.Sub, "membership": s.claims.Membership, "tenant": s.claims.Tenant,
		"is_admin": s.claims.IsAdmin, "directory": s.env.Directory, "caps": s.claims.Caps})
}

type convOut struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Mode        string      `json:"mode"`
	Title       string      `json:"title,omitempty"`
	Description string      `json:"description,omitempty"`
	StartedMs   any         `json:"started_ms,omitempty"`
	Tags        any         `json:"tags,omitempty"`
	Agent       string      `json:"agent,omitempty"`
	Session     string      `json:"session,omitempty"`
	Access      string      `json:"access"`
	Subscribed  string      `json:"subscribed,omitempty"`
	Scope       store.Scope `json:"scope"`
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
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

	metas, err := s.st.Find(r.Context(), filter, limit)
	if err != nil {
		writeErr(w, err)
		return
	}

	subs := map[string]string{}
	for _, sub := range plugin.Subscriptions(s.env) {
		subs[sub.ID] = sub.Mode
	}

	out := make([]convOut, 0, len(metas))
	for _, m := range metas {
		access := "grant?"
		switch {
		case m.Owner == "":
			access = "tenant"
		case s.claims.Membership != "" && m.Owner == s.claims.Membership:
			access = "owner"
		case s.claims.IsAdmin:
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

		out = append(out, convOut{ID: m.ID, Name: m.DisplayName, Mode: str(m.Scope["mode"]), Title: str(m.Scope["title"]), Description: str(m.Scope["description"]), StartedMs: started,
			Tags: m.Scope["tags"], Agent: str(m.Scope["agent"]), Session: str(m.Scope["session"]), Access: access, Subscribed: subs[m.ID], Scope: m.Scope})
	}

	backfillStarted(r.Context(), s.st, out)
	writeJSON(w, 200, map[string]any{"conversations": out})
}

// maxDateProbes caps how many conversations the index will read a row from
// to find their start time. Names have carried a timestamp since 0.2.15, so
// this only touches older ones and the cost is bounded.
const maxDateProbes = 64

// backfillStarted fills started_ms for conversations whose scope and
// display name carry no time, by reading their first row: the earliest row
// is when the session began. Bounded and run in parallel, because it costs
// one member read each.
func backfillStarted(ctx context.Context, st store.Store, out []convOut) {
	var todo []int
	for i := range out {
		if out[i].StartedMs == nil {
			todo = append(todo, i)
		}
	}

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
			for e, err := range st.Scan(ctx, out[i].ID, 0, 1) {
				if err == nil && e.TSMs > 0 {
					out[i].StartedMs = e.TSMs
				}

				break
			}
		}(i)
	}

	wg.Wait()
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 500
	}

	conv := conversation.Attach(s.st, id)
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

	head, _ := conv.Head(r.Context())
	if rows == nil {
		rows = []map[string]any{}
	}

	writeJSON(w, 200, map[string]any{"events": rows, "next": pos, "head": head})
}

func (s *Server) head(w http.ResponseWriter, r *http.Request) {
	h, err := s.st.Head(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, map[string]any{"head": h})
}

func (s *Server) post(w http.ResponseWriter, r *http.Request) {
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

	if in.To == "" {
		in.To = "*"
	}

	e := event.Event{ID: event.NewID(), TSMs: time.Now().UnixMilli(), Source: event.SourceProduct,
		Kind: event.Kind("post." + strings.TrimPrefix(in.Kind, "post.")), Identity: s.claims.Sub, To: in.To, ReplyTo: in.ReplyTo, Tags: in.Tags}
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

	pos, err := conversation.Attach(s.st, r.PathValue("id")).Append(r.Context(), false, e)
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
	subs := plugin.Subscriptions(s.env)
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
			if h, err := s.st.Head(r.Context(), sub.ID); err == nil {
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
	if err := plugin.Join(r.Context(), s.env, in.Name, in.Mode, "all", in.As, &out); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, map[string]any{"ok": strings.TrimSpace(out.String())})
}

func (s *Server) unsubscribe(w http.ResponseWriter, r *http.Request) {
	var out strings.Builder
	if err := plugin.Leave(s.env, r.PathValue("name"), &out); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, map[string]any{"ok": strings.TrimSpace(out.String())})
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

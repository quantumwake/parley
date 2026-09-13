package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/quantumwake/statefs/pkg/identityfile"

	"github.com/quantumwake/parley/pkg/conversation"
	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/naming"
	"github.com/quantumwake/parley/pkg/store"
	adapter "github.com/quantumwake/parley/pkg/store/statefs"
)

// Shared conversations (story 2): create, list, join (subscribe), post,
// read, and the turn-boundary injection the UserPromptSubmit hook does.
// Discovery is the directory's tenant-wide listing (the deployed directory
// stamps only the tenant claim, so every member sees every conversation);
// access is the ticket rule (owner, tenant admin, or a grant). Subscriptions
// and cursors are client-owned files under the data dir, per session
// (session.go).

// Subscription is one file under <data>/subscriptions/.
type Subscription struct {
	Name       string `json:"name"`
	ID         string `json:"id"`
	Mode       string `json:"mode"`        // full | digest
	DigestPick string `json:"digest_pick"` // all | first | <persona>
	Cursor     int64  `json:"cursor"`      // next position to read
	JoinedMs   int64  `json:"joined_ms"`
	// Participant is the handle this agent speaks under in this
	// conversation, declared with `join --as`. It distinguishes speakers
	// that share one identity (several sessions, one key). Display and
	// addressing only: nothing attests it.
	Participant string `json:"participant,omitempty"`
}

// MaxSubscriptions is the per-agent cap (plan 3.9).
const MaxSubscriptions = 20

func subsDir(env Env) string { return filepath.Join(env.DataDir, "subscriptions") }

func subFile(env Env, name string) string {
	return filepath.Join(subsDir(env), strings.NewReplacer("/", "%2F", "#", "%23", " ", "%20").Replace(name))
}

// Subscriptions lists the local subscriptions, oldest first.
func Subscriptions(env Env) []Subscription {
	entries, err := os.ReadDir(subsDir(env))
	if err != nil {
		return nil
	}

	var out []Subscription
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}

		b, err := os.ReadFile(filepath.Join(subsDir(env), e.Name()))
		if err != nil {
			continue
		}

		var s Subscription
		if json.Unmarshal(b, &s) == nil && s.ID != "" {
			out = append(out, overlaySession(env, s))
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].JoinedMs < out[j].JoinedMs })
	return out
}

// CreateShared opens a shared conversation owned by the caller's identity.
func CreateShared(ctx context.Context, env Env, name, description string, tags []string, w io.Writer) error {
	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	ns, err := st.Open(ctx, name, naming.Shared{Name: name, Description: description, Tags: tags}.Scope())
	if err != nil {
		return err
	}

	NamesPut(env, ns.DisplayName, ns.ID)
	fmt.Fprintf(w, "created %s (%s)\nothers in the tenant can list it; share it with `parley grant <name> --user <identity> --access read,write` (you own it)\n", ns.DisplayName, ns.ID)
	return nil
}

// ListShared prints shared conversations in the tenant: one directory
// query. The access column comes from ownership against this identity's
// membership (owner, admin, tenant-wide); a namespace owned by someone
// else shows "grant?" because a bearer cannot list its own grants yet
// (handoff delta 9); `join` finds out for one conversation by reading it.
func ListShared(ctx context.Context, env Env, tag, q string, w io.Writer) error {
	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	filter := store.Scope{"kind": "conversation", "mode": string(naming.ModeShared)}
	if tag != "" {
		filter["tags"] = []string{tag}
	}

	metas, err := st.Find(ctx, filter, 200)
	if err != nil {
		return err
	}

	me := MyClaims(ctx, env)
	subs := map[string]Subscription{}
	for _, s := range Subscriptions(env) {
		subs[s.ID] = s
	}

	fmt.Fprintf(w, "%-28s %-10s %-10s  %s\n", "name", "access", "subscribed", "description [tags]")
	for _, m := range metas {
		if q != "" && !strings.Contains(strings.ToLower(m.DisplayName+" "+str(m.Scope["description"])), strings.ToLower(q)) {
			continue
		}

		access := "grant?"
		switch {
		case m.Owner == "":
			access = "tenant"
		case me.Membership != "" && m.Owner == me.Membership:
			access = "owner"
		case me.IsAdmin:
			access = "admin"
		}

		sub := "-"
		if s, ok := subs[m.ID]; ok {
			sub = s.Mode
		}

		fmt.Fprintf(w, "%-28s %-10s %-10s  %s %v\n", m.DisplayName, access, sub, str(m.Scope["description"]), m.Scope["tags"])
	}

	return nil
}

// Join subscribes this agent to a shared conversation after proving it can
// read it. mode is full or digest.
func Join(ctx context.Context, env Env, name, mode, pick, as string, w io.Writer) error {
	if mode == "" {
		mode = "full"
	}

	if pick == "" {
		pick = "all"
	}

	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	id, err := resolveShared(ctx, env, st, name)
	if err != nil {
		return err
	}

	_, following := readMachine(env, name)
	if !following && len(Subscriptions(env)) >= MaxSubscriptions {
		return fmt.Errorf("join: this agent already follows %d conversations (the cap); leave one first", MaxSubscriptions)
	}

	head, err := st.Head(ctx, id)
	if errors.Is(err, store.ErrRefused) {
		return fmt.Errorf("join: no read access to %q; ask a tenant admin for a grant", name)
	}

	if err != nil {
		return err
	}

	s := Subscription{Name: name, ID: id, Mode: mode, DigestPick: pick, Cursor: int64(head), JoinedMs: time.Now().UnixMilli(), Participant: as}
	if err := saveSub(env, s); err != nil {
		return err
	}

	who := ""
	if as != "" {
		who = fmt.Sprintf(" as %q", as)
	}

	fmt.Fprintf(w, "subscribed to %s (%s)%s in %s mode from position %d\n", name, id, who, mode, head)
	fmt.Fprintln(w, WaitAdvice)
	return nil
}

// Leave drops the subscription.
func Leave(env Env, name string, w io.Writer) error {
	if err := os.Remove(subFile(env, name)); err != nil {
		return fmt.Errorf("leave: not subscribed to %q", name)
	}

	removeSessions(env, name)

	fmt.Fprintf(w, "left %s\n", name)
	return nil
}

// Post appends one post to a shared conversation as this identity.
func Post(ctx context.Context, env Env, name, kind, text, to, replyTo string, tags []string, w io.Writer) error {
	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	id, err := resolveShared(ctx, env, st, name)
	if err != nil {
		return err
	}

	k := event.Kind("post." + strings.TrimPrefix(kind, "post."))
	e := event.Event{
		ID: event.NewID(), TSMs: time.Now().UnixMilli(), Source: event.SourceClaudeCode, Kind: k,
		SessionID: env.Session, Identity: authorOf(env), Participant: participantOf(env, id), To: to, ReplyTo: replyTo, Tags: tags,
	}
	if replyTo != "" {
		e.ParentID, e.Thread = replyTo, replyTo
	} else {
		e.Thread = e.ID
	}

	body, _ := json.Marshal(map[string]any{"text": text})
	e.Content = body
	if err := e.Validate(); err != nil {
		return err
	}

	pos, err := conversation.Attach(st, id).Append(ctx, false, e)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "posted %s to %s at position %d (event %s)\n", k, name, pos, e.ID)
	return nil
}

// Read prints the rows of a shared conversation from a position (default:
// the subscription cursor) and advances the cursor unless peek. With wait
// > 0 it blocks, polling the head every two seconds, until a row that this
// session did not write exists or the wait is over, so an agent can wait
// for the others inside its own turn without waking on its own post.
func Read(ctx context.Context, env Env, name string, from int64, peek bool, wait time.Duration, w io.Writer) error {
	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	id, err := resolveShared(ctx, env, st, name)
	if err != nil {
		return err
	}

	var sub *Subscription
	for _, s := range Subscriptions(env) {
		if s.ID == id {
			c := s
			sub = &c
		}
	}

	if from < 0 {
		from = 0
		if sub != nil {
			from = sub.Cursor
		}
	}

	conv := conversation.Attach(st, id)
	if wait > 0 {
		me := authorOf(env)
		deadline := time.Now().Add(wait)
		checked := from
		for {
			head, err := conv.Head(ctx)
			if err != nil {
				return err
			}

			others := false
			for e, err := range conv.Scan(ctx, store.Position(checked), head) {
				if err != nil {
					return err
				}

				checked++
				others = others || !fromMe(env, me, e)
			}

			if others || time.Now().After(deadline) {
				break
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}

	last := from
	n := 0
	for e, err := range conv.Scan(ctx, store.Position(from), 0) {
		if err != nil {
			return err
		}

		n++
		last++
		fmt.Fprintln(w, formatPost(e, name, last-1, 0))
	}

	if peek {
		fmt.Fprintf(w, "%d rows (peek: cursor unchanged); next position %d\n", n, last)
	} else {
		fmt.Fprintf(w, "%d new rows; next position %d\n", n, last)
	}
	if sub != nil && !peek && last > sub.Cursor {
		sub.Cursor = last
		_ = saveSub(env, *sub)
	}

	return nil
}

// InjectBudget bounds what one turn receives across subscriptions (plan 3.10).
const (
	InjectMaxMessages  = 20
	InjectMaxBytes     = 8 << 10
	InjectMaxPostBytes = 2 << 10 // one post's body in the digest; the rest is one `parley read` away
)

// pendingPost is one row a session has not been shown yet.
type pendingPost struct {
	sub  Subscription
	e    event.Event
	pos  int64 // position after the row
	mine bool  // addressed to this reader
}

// pending collects the rows past each subscription's cursor that this
// session should see, honoring mode, and advances and saves the cursors,
// including over its own posts.
//
// Delivery is exclusive per session: a background wait, the Stop hook and
// prompt injection can run at the same moment, and each must read the
// cursor the last one saved, or two of them deliver the same row.
func pending(ctx context.Context, env Env, st store.Store, subs []Subscription) []pendingPost {
	unlock, ok := lockDelivery(env)
	if !ok {
		return nil // another delivery for this session holds it; the next round sees what it saved
	}
	defer unlock()

	me := authorOf(env)
	var items []pendingPost
	for _, s := range subs {
		if cur, ok := readSession(env, s.Name); ok {
			s.Cursor = cur.Cursor
		}

		pos := s.Cursor
		for e, err := range conversation.Attach(st, s.ID).Scan(ctx, store.Position(s.Cursor), 0) {
			if err != nil {
				break
			}

			pos++
			if s.Mode == "digest" && !isDigest(e) {
				continue
			}

			if fromMe(env, me, e) {
				continue
			}

			items = append(items, pendingPost{s, e, pos, addressesMe(e.To, me, s.Participant)})
		}

		if pos != s.Cursor {
			s.Cursor = pos
			_ = saveSub(env, s)
		}
	}

	sort.SliceStable(items, func(i, j int) bool { return items[i].mine && !items[j].mine })
	return items
}

// Inject collects new rows from every subscription for the agent's next
// turn, honoring mode and budget, and advances this session's cursors.
// Returns "" when there is nothing new. Rows addressed to this reader come
// first.
func Inject(ctx context.Context, env Env) string {
	subs := Subscriptions(env)
	if len(subs) == 0 {
		return ""
	}

	st, err := StoreFromEnv(env)
	if err != nil {
		return ""
	}

	items := pending(ctx, env, st, subs)
	if len(items) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("statefs.ai parley: new posts in conversations you follow (reply with the post_message tool or `parley post <name> --reply-to <event> ...`):\n")
	n, bytes := 0, 0
	for _, it := range items {
		line := fmt.Sprintf("- [%s] %s\n", it.sub.Name, formatPost(it.e, it.sub.Name, it.pos-1, InjectMaxPostBytes))
		if n >= InjectMaxMessages || bytes+len(line) > InjectMaxBytes {
			b.WriteString(fmt.Sprintf("- (%d more: `parley read <name>` shows them)\n", len(items)-n))
			break
		}

		b.WriteString(line)
		n++
		bytes += len(line)
	}

	return b.String()
}

func isDigest(e event.Event) bool {
	return e.Kind == event.KindPostReport || e.Kind == event.KindPostStatus || e.Kind == event.KindMetaSummary || e.Indexable
}

// formatPost renders one post for a reader that is a program (parley read,
// the injected digest). The header is one line; the body follows in full,
// indented, with its newlines kept: an agent that gets a cut-off function
// cannot review it. limit > 0 caps the body and says how to fetch the rest.
func formatPost(e event.Event, name string, pos int64, limit int) string {
	var m map[string]any
	_ = json.Unmarshal(e.Content, &m)
	text, _ := m["text"].(string)
	text = strings.TrimRight(text, "\n")

	who := speakerOf(e)

	extra := ""
	if e.To != "" && e.To != "*" {
		extra += " to:" + e.To
	}

	if e.ReplyTo != "" {
		extra += " reply-to:" + e.ReplyTo
	}

	if limit > 0 && len(text) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}

		text = text[:cut] + fmt.Sprintf("\n[... %d more bytes: `parley read %s --from %d --peek`]", len(text)-cut, name, pos)
	}

	head := fmt.Sprintf("%s %s%s (%s) @%d", e.Kind, who, extra, e.ID, pos)
	if !strings.Contains(text, "\n") && len(text) <= 120 {
		return head + ": " + text
	}

	return head + ":\n" + indent(text, "    ")
}

func indent(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}

	return strings.Join(lines, "\n")
}

func resolveShared(ctx context.Context, env Env, st store.Store, name string) (string, error) {
	if looksLikeID(name) {
		return name, nil
	}

	if id := namesGet(env, name); id != "" {
		return id, nil
	}

	metas, err := st.Find(ctx, store.Scope{"kind": "conversation", "mode": string(naming.ModeShared), "name": name}, 5)
	if err != nil {
		return "", err
	}

	for _, m := range metas {
		if strings.EqualFold(m.DisplayName, name) {
			NamesPut(env, m.DisplayName, m.ID)
			return m.ID, nil
		}
	}

	return "", fmt.Errorf("no shared conversation named %q in the tenant", name)
}

// GrantAccess gives a member read or write on a shared conversation. Needs
// a tenant-admin credential with manage (STATEFS_KEY_FILE may point at one).
func GrantAccess(ctx context.Context, env Env, name, username, access string, w io.Writer) error {
	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	id, err := resolveShared(ctx, env, st, name)
	if err != nil {
		return err
	}

	a, ok := st.(*adapter.Store)
	if !ok {
		return errors.New("grant: only meaningful against statefs.io")
	}

	for _, acc := range strings.Split(access, ",") {
		if err := a.Client().GrantNamespace(ctx, username, id, strings.TrimSpace(acc)); err != nil {
			return err
		}
	}

	fmt.Fprintf(w, "granted %s %s on %s (%s)\n", username, access, name, id)
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

var _ = identityfile.DefaultPath // keep the identity import honest for authorOf's package

// participantOf is the handle this session declared for a conversation at
// join time, or "" when it joined without one.
func participantOf(env Env, id string) string {
	for _, s := range Subscriptions(env) {
		if s.ID == id {
			return s.Participant
		}
	}

	return ""
}

// speakerOf renders who spoke: the handle when the writer declared one,
// qualified by the identity and the session, since handles are
// self-declared and sessions share an identity.
func speakerOf(e event.Event) string {
	who := e.Identity
	if who != "" && e.SessionID != "" {
		who += "#" + shortSession(e.SessionID)
	}

	switch {
	case e.Participant != "" && who != "":
		return e.Participant + " (" + who + ")"
	case e.Participant != "":
		return e.Participant
	case who != "":
		return who
	default:
		return "?"
	}
}

// addressesMe answers whether a post's --to names this reader, by
// identity or by the handle it speaks under in that conversation.
func addressesMe(to, identity, participant string) bool {
	if to == "" || to == "*" {
		return false
	}

	return to == identity || (participant != "" && to == participant)
}

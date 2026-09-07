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
	"strconv"
	"strings"
	"time"

	"github.com/quantumwake/statefs/pkg/identityfile"

	"github.com/quantumwake/statefs.ai/pkg/conversation"
	"github.com/quantumwake/statefs.ai/pkg/event"
	"github.com/quantumwake/statefs.ai/pkg/naming"
	"github.com/quantumwake/statefs.ai/pkg/store"
	adapter "github.com/quantumwake/statefs.ai/pkg/store/statefs"
)

// Shared conversations (story 2): create, list, join (subscribe), post,
// read, and the turn-boundary injection the UserPromptSubmit hook does.
// Discovery is the directory's tenant-wide listing (the deployed directory
// stamps only the tenant claim, so every member sees every conversation);
// access is the ticket rule (owner, tenant admin, or a grant). Subscriptions
// and cursors are client-owned files under the plugin data dir.

// Subscription is one file under <data>/subscriptions/.
type Subscription struct {
	Name       string `json:"name"`
	ID         string `json:"id"`
	Mode       string `json:"mode"`        // full | digest
	DigestPick string `json:"digest_pick"` // all | first | <persona>
	Cursor     int64  `json:"cursor"`      // next position to read
	JoinedMs   int64  `json:"joined_ms"`
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
		b, err := os.ReadFile(filepath.Join(subsDir(env), e.Name()))
		if err != nil {
			continue
		}

		var s Subscription
		if json.Unmarshal(b, &s) == nil && s.ID != "" {
			out = append(out, s)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].JoinedMs < out[j].JoinedMs })
	return out
}

func saveSub(env Env, s Subscription) error {
	if err := os.MkdirAll(subsDir(env), 0o700); err != nil {
		return err
	}

	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := subFile(env, s.Name) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, subFile(env, s.Name))
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
	fmt.Fprintf(w, "created %s (%s)\nothers in the tenant can list it; a tenant admin grants read or write with `parley grant`\n", ns.DisplayName, ns.ID)
	return nil
}

// ListShared prints shared conversations in the tenant, with whether this
// identity can read each (a head read either works or is refused).
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

	subs := map[string]Subscription{}
	for _, s := range Subscriptions(env) {
		subs[s.ID] = s
	}

	var shown []store.Namespace
	for _, m := range metas {
		if q != "" && !strings.Contains(strings.ToLower(m.DisplayName+" "+str(m.Scope["description"])), strings.ToLower(q)) {
			continue
		}

		shown = append(shown, m)
	}

	// The access probe is a route, a ticket and a member read per
	// conversation; run them in parallel so a long list costs one round
	// trip, not one per row.
	access := make([]string, len(shown))
	rows := make([]string, len(shown))
	Parallel(len(shown), 8, func(i int) {
		access[i], rows[i] = "no", "-"
		if h, err := st.Head(ctx, shown[i].ID); err == nil {
			access[i], rows[i] = "read", strconv.FormatInt(int64(h), 10)
		} else if !errors.Is(err, store.ErrRefused) {
			access[i] = "?"
		}
	})

	fmt.Fprintf(w, "%-28s %-9s %-10s %6s  %s\n", "name", "access", "subscribed", "rows", "description [tags]")
	for i, m := range shown {
		sub := "-"
		if s, ok := subs[m.ID]; ok {
			sub = s.Mode
		}

		fmt.Fprintf(w, "%-28s %-9s %-10s %6s  %s %v\n", m.DisplayName, access[i], sub, rows[i], str(m.Scope["description"]), m.Scope["tags"])
	}

	return nil
}

// Join subscribes this agent to a shared conversation after proving it can
// read it. mode is full or digest.
func Join(ctx context.Context, env Env, name, mode, pick string, w io.Writer) error {
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

	if len(Subscriptions(env)) >= MaxSubscriptions {
		return fmt.Errorf("join: this agent already follows %d conversations (the cap); leave one first", MaxSubscriptions)
	}

	head, err := st.Head(ctx, id)
	if errors.Is(err, store.ErrRefused) {
		return fmt.Errorf("join: no read access to %q; ask a tenant admin for a grant", name)
	}

	if err != nil {
		return err
	}

	s := Subscription{Name: name, ID: id, Mode: mode, DigestPick: pick, Cursor: int64(head), JoinedMs: time.Now().UnixMilli()}
	if err := saveSub(env, s); err != nil {
		return err
	}

	fmt.Fprintf(w, "subscribed to %s (%s) in %s mode from position %d\n", name, id, mode, head)
	return nil
}

// Leave drops the subscription.
func Leave(env Env, name string, w io.Writer) error {
	if err := os.Remove(subFile(env, name)); err != nil {
		return fmt.Errorf("leave: not subscribed to %q", name)
	}

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
		Author: authorOf(env), To: to, ReplyTo: replyTo, Tags: tags,
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

// Read prints rows of a shared conversation from the subscription cursor
// (or --from) and advances the cursor unless peek is set.
func Read(ctx context.Context, env Env, name string, from int64, peek bool, w io.Writer) error {
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

	last := from
	n := 0
	for e, err := range conversation.Attach(st, id).Scan(ctx, store.Position(from), 0) {
		if err != nil {
			return err
		}

		n++
		last++
		fmt.Fprintln(w, formatPost(e))
	}

	fmt.Fprintf(w, "%d new rows\n", n)
	if sub != nil && !peek && last > sub.Cursor {
		sub.Cursor = last
		_ = saveSub(env, *sub)
	}

	return nil
}

// InjectBudget bounds what one turn receives across subscriptions (plan 3.10).
const (
	InjectMaxMessages = 20
	InjectMaxBytes    = 8 << 10
)

// Inject collects new rows from every subscription for the agent's next
// turn, honoring mode and budget, and advances cursors. Returns "" when
// there is nothing new. Rows addressed to this identity come first.
func Inject(ctx context.Context, env Env) string {
	subs := Subscriptions(env)
	if len(subs) == 0 {
		return ""
	}

	st, err := StoreFromEnv(env)
	if err != nil {
		return ""
	}

	me := authorOf(env)
	type item struct {
		sub  Subscription
		e    event.Event
		pos  int64
		mine bool
	}
	var items []item
	for _, s := range subs {
		pos := s.Cursor
		for e, err := range conversation.Attach(st, s.ID).Scan(ctx, store.Position(s.Cursor), 0) {
			if err != nil {
				break
			}

			pos++
			if s.Mode == "digest" && !isDigest(e) {
				continue
			}

			if e.Author == me && e.IsPost() {
				continue // my own posts
			}

			items = append(items, item{s, e, pos, e.To == me})
		}

		s.Cursor = pos
		_ = saveSub(env, s)
	}

	if len(items) == 0 {
		return ""
	}

	sort.SliceStable(items, func(i, j int) bool { return items[i].mine && !items[j].mine })
	var b strings.Builder
	b.WriteString("statefs.ai parley: new posts in conversations you follow (reply with `parley post <name> --reply-to <event> ...`):\n")
	n, bytes := 0, 0
	for _, it := range items {
		line := fmt.Sprintf("- [%s @%d] %s\n", it.sub.Name, it.pos-1, formatPost(it.e))
		if n >= InjectMaxMessages || bytes+len(line) > InjectMaxBytes {
			b.WriteString(fmt.Sprintf("- (%d more held for the next turn)\n", len(items)-n))
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

func formatPost(e event.Event) string {
	var m map[string]any
	_ = json.Unmarshal(e.Content, &m)
	text, _ := m["text"].(string)
	text = strings.ReplaceAll(text, "\n", " ")
	if len(text) > 240 {
		text = text[:240] + "..."
	}

	who := e.Author
	if who == "" {
		who = "?"
	}

	extra := ""
	if e.To != "" && e.To != "*" {
		extra += " to:" + e.To
	}

	if e.ReplyTo != "" {
		extra += " reply-to:" + e.ReplyTo
	}

	return fmt.Sprintf("%s %s%s (%s): %s", e.Kind, who, extra, e.ID, text)
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

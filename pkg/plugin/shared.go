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
// In a session, only subscriptions this session has joined are returned.
// Without a session (plain terminal), all machine subscriptions are returned.
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
			// In a session, check if this session has joined this subscription
			if env.Session != "" {
				if st, ok := readSession(env, s.Name); !ok || !st.Joined {
					continue
				}
			}
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

	// Follow what you just made. Owning a conversation you do not follow
	// has no use: the creator invites people into it and then hears
	// nothing, which is how `product proposals` nearly went unread on the
	// day it was made.
	followed := "you follow it"
	if err := Join(ctx, env, ns.DisplayName, "full", "all", "", io.Discard); err != nil {
		followed = "could not follow it (" + err.Error() + "); run `parley join " + ns.DisplayName + "`"
	}

	fmt.Fprintf(w, "created %s (%s); %s\nothers in the tenant can list it; share it with `parley grant <name> --user <identity> --access read,write` (you own it)\n%s\n", ns.DisplayName, ns.ID, followed, WaitAdvice)
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
// read it. mode is full or digest. In a session, join marks the subscription
// as joined and preserves any existing cursor; without a session, it behaves
// as before (machine-wide join).
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

	cursor := int64(head)
	// In a session, preserve any existing cursor when joining.
	if env.Session != "" {
		if st, ok := readSession(env, name); ok {
			cursor = st.Cursor
		}
	}

	s := Subscription{Name: name, ID: id, Mode: mode, DigestPick: pick, Cursor: cursor, JoinedMs: time.Now().UnixMilli(), Participant: as}
	if err := saveSub(env, s); err != nil {
		return err
	}

	who := ""
	if as != "" {
		who = fmt.Sprintf(" as %q", as)
	}

	fmt.Fprintf(w, "subscribed to %s (%s)%s in %s mode from position %d\n", name, id, who, mode, cursor)
	fmt.Fprintln(w, WaitAdvice)
	return nil
}

// Leave unsubscribes from a conversation. In a session, only this session's
// membership is cleared (cursor is preserved). Without a session (plain
// terminal), the machine record and all session records are removed.
func Leave(env Env, name string, w io.Writer) error {
	if env.Session != "" {
		// In a session, clear the joined flag but preserve the cursor.
		st, ok := readSession(env, name)
		if !ok {
			return fmt.Errorf("leave: not subscribed to %q", name)
		}
		st.Joined = false
		if err := writeJSONFile(sessionFile(env, name), st); err != nil {
			return err
		}
		fmt.Fprintf(w, "left %s\n", name)
		return nil
	}

	// Without a session, remove the machine record and all session records.
	if err := os.Remove(subFile(env, name)); err != nil {
		return fmt.Errorf("leave: not subscribed to %q", name)
	}

	removeSessions(env, name)

	fmt.Fprintf(w, "left %s\n", name)
	return nil
}

// Post appends one post to a shared conversation as this identity.
func Post(ctx context.Context, env Env, name, kind, text, to, replyTo string, tags []string, w io.Writer, opts ...PostOption) error {
	var o postOptions
	for _, opt := range opts {
		opt(&o)
	}

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

	content := map[string]any{"text": text}
	if k == event.KindPostClose {
		content["outcome"] = o.outcome
	}

	body, _ := json.Marshal(content)
	e.Content = body

	// Work posts are checked first, so a refusal says what to do instead
	// of a bare validation error.
	var work *workLog
	if isWork(k) || o.outcome != "" {
		if work, err = readWork(ctx, env, st, id); err != nil {
			return err
		}

		if err := checkWork(work, k, e.Identity, replyTo, o.outcome); err != nil {
			return fmt.Errorf("post %s: %w", strings.TrimPrefix(string(k), "post."), err)
		}
	}

	if err := e.Validate(); err != nil {
		return err
	}

	pos, err := conversation.Attach(st, id).Append(ctx, false, e)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "posted %s to %s at position %d (event %s)\n", k, name, pos, e.ID)

	// Two claims can pass the check at once; the earlier one holds. Say so
	// to the one that lost.
	if k == event.KindPostClaim && replyTo != "" {
		if after, err := readWork(ctx, env, st, id); err == nil {
			if note := claimOutcome(after, e.ID); note != "" {
				fmt.Fprintln(w, note)
			}
		}
	}

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
	pos  int64  // position after the row
	mine bool   // addressed to this reader
	hold bool   // worth holding this session's turn for (see holdsTurn)
	work string // for a work post, where its item stands now
}

// holdsTurn says whether a post should stop this session from going idle,
// as against merely being shown to it. Every post is delivered either way;
// this decides which ones are the reader's to act on.
//
// A post addressed to someone else never holds a turn: a channel of agents
// all being told to "handle" a question meant for one of them is how they
// all answer it. What is left — addressed to this reader, or addressed to
// nobody — holds only when it asks for something: a question or a request.
// Talk (answer, comment, report, status, artifact) is read, not answered.
// A question already claimed or answered is talk by the time it arrives.
//
// A person is the exception, whatever they post. Their posts carry no
// session (an agent's always do), and their asides are direction: "lets go
// over the top priority items" arrived as a comment, and a channel of
// agents that quietly files the person's words as talk is worse than one
// that answers too often.
func holdsTurn(e event.Event, mine bool, l *workLog) bool {
	if fromPerson(e) {
		return true
	}

	if e.To != "" && e.To != "*" {
		return mine
	}

	if e.Kind != event.KindPostQuestion && e.Kind != event.KindPostRequest {
		return false
	}

	if l != nil {
		if item := l.Items[e.ID]; item != nil && item.State != WorkOpen {
			return false
		}
	}

	return true
}

// pending collects the rows past each subscription's cursor that this
// session should see, honoring mode, and advances and saves the cursors,
// including over its own posts.
//
// Delivery is exclusive per session: a background wait, the Stop hook and
// prompt injection can run at the same moment, and each must read the
// cursor the last one saved, or two of them deliver the same row.
func pending(ctx context.Context, env Env, st store.Store, subs []Subscription) []pendingPost {
	items, _, _ := pendingRound(ctx, env, st, subs)
	return items
}

// pendingRound is pending for callers that must know how the round went:
// ran is false when another delivery held the lock, and failed maps each
// conversation that could not be read to its error. A read error never
// passes for a quiet conversation; the rows read before it still count.
func pendingRound(ctx context.Context, env Env, st store.Store, subs []Subscription) (items []pendingPost, ran bool, failed map[string]error) {
	unlock, ok := lockDelivery(env)
	if !ok {
		return nil, false, nil // another delivery for this session holds it; the next round sees what it saved
	}
	defer unlock()

	me := authorOf(env)
	failed = map[string]error{}

	// Work marks share one deadline across the round, so several busy
	// conversations cannot add up past a hook's timeout.
	markCtx, cancelMarks := context.WithTimeout(ctx, workFoldTimeout)
	defer cancelMarks()
	for _, s := range subs {
		if cur, ok := readSession(env, s.Name); ok {
			s.Cursor = cur.Cursor
		}

		pos := s.Cursor
		first, hasWork := len(items), false
		for e, err := range conversation.Attach(st, s.ID).Scan(ctx, store.Position(s.Cursor), 0) {
			if err != nil {
				failed[s.Name] = err
				break
			}

			pos++
			if s.Mode == "digest" && !isDigest(e) {
				continue
			}

			if fromMe(env, me, e) {
				continue
			}

			items = append(items, pendingPost{sub: s, e: e, pos: pos, mine: addressesMe(e.To, me, s.Participant)})
			hasWork = hasWork || folded(e.Kind)
		}

		if pos != s.Cursor {
			s.Cursor = pos
			_ = saveSub(env, s)
		}

		// Work posts are delivered with where their item stands now. The
		// fold continues from its cache, and is bounded so a large
		// conversation never holds up delivery: past the bound, no mark.
		var fold *workLog
		if hasWork && markCtx.Err() == nil {
			if l, err := readWork(markCtx, env, st, s.ID); err == nil {
				fold = l
				for i := first; i < len(items); i++ {
					items[i].work = workMark(l, items[i].e)
				}
			}
		}

		// A fold that could not be read leaves questions holding the turn:
		// unread state must not silence a post, only a known one may.
		for i := first; i < len(items); i++ {
			items[i].hold = holdsTurn(items[i].e, items[i].mine, fold)
		}
	}

	sort.SliceStable(items, func(i, j int) bool { return items[i].mine && !items[j].mine })
	return items, true, failed
}

// Inject collects new rows from every subscription for the agent's next
// turn, honoring mode and budget, and advances this session's cursors.
// Returns "" when there is nothing new. Rows addressed to this reader come
// first.
func Inject(ctx context.Context, env Env) string {
	text, _, _ := injectLines(ctx, env)
	return text
}

// InjectHold is Inject, and also says whether any of the posts is this
// session's to act on (see holdsTurn). The Stop hook holds a turn only for
// those; everything else is delivered and left to be read.
func InjectHold(ctx context.Context, env Env) (string, bool) {
	text, hold, _ := injectLines(ctx, env)
	return text, hold
}

// injectLines is InjectHold, and also answers the rendered lines it read.
// A caller that decides not to show them (the Stop hook, when nothing is
// this session's to act on) must keep them: the cursors have already moved
// past these rows, so dropping the lines loses the posts.
func injectLines(ctx context.Context, env Env) (string, bool, []string) {
	subs := Subscriptions(env)
	if len(subs) == 0 {
		return "", false, nil
	}

	st, err := StoreFromEnv(env)
	if err != nil {
		return "", false, nil
	}

	items := pending(ctx, env, st, subs)
	kept := drainContext(env)
	if len(items) == 0 && len(kept) == 0 {
		return "", false, nil
	}

	hold := false
	for _, it := range items {
		hold = hold || it.hold
	}

	// One list, so kept lines and new rows share the budget: a session
	// idle for days must not hand its next turn a megabyte of backlog.
	lines := make([]string, 0, len(kept)+len(items))
	lines = append(lines, kept...)
	for _, it := range items {
		lines = append(lines, fmt.Sprintf("- [%s]%s %s", it.sub.Name, it.work, formatPost(it.e, it.sub.Name, it.pos-1, InjectMaxPostBytes)))
	}

	var b strings.Builder
	b.WriteString("statefs.ai parley: new posts in conversations you follow (reply with the post_message tool or `parley post <name> --reply-to <event> ...`):\n")
	n, bytes := 0, 0
	for _, line := range lines {
		if n >= InjectMaxMessages || bytes+len(line) > InjectMaxBytes {
			b.WriteString(fmt.Sprintf("- (%d more: `parley read <name>` shows them)\n", len(lines)-n))
			break
		}

		b.WriteString(line + "\n")
		n++
		bytes += len(line)
	}

	return b.String(), hold, lines
}

func isDigest(e event.Event) bool {
	return e.Kind == event.KindPostReport || e.Kind == event.KindPostStatus || e.Kind == event.KindPostRequest || e.Kind == event.KindMetaSummary || e.Indexable
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
		return errors.New("grant: only meaningful against the enrolled directory")
	}

	for _, acc := range strings.Split(access, ",") {
		if err := a.Client().GrantNamespace(ctx, username, id, strings.TrimSpace(acc)); err != nil {
			return err
		}
	}

	fmt.Fprintf(w, "granted %s %s on %s (%s)\n", username, access, name, id)
	return nil
}

// Grant is one member's access to a shared conversation.
type Grant struct {
	Username string `json:"username"`
	Access   string `json:"access"`
}

// ListAccess lists who holds a grant on a shared conversation. statefs keeps
// one grant per member and namespace, read or write (write implies read),
// and decides what the caller may see: every grant for an admin, grants on
// its own namespaces otherwise.
func ListAccess(ctx context.Context, env Env, name string) ([]Grant, error) {
	a, id, err := grantTarget(ctx, env, name)
	if err != nil {
		return nil, err
	}

	rows, err := a.Client().ListGrantsWhere(ctx, id, "")
	if err != nil {
		return nil, err
	}

	out := []Grant{}
	for _, g := range rows {
		if g.Namespace != id {
			continue
		}
		out = append(out, Grant{Username: g.Username, Access: g.Access})
	}

	return out, nil
}

// RevokeAccess removes every grant username holds on a shared conversation.
func RevokeAccess(ctx context.Context, env Env, name, username string) error {
	a, id, err := grantTarget(ctx, env, name)
	if err != nil {
		return err
	}

	return a.Client().RevokeNamespace(ctx, username, id)
}

func grantTarget(ctx context.Context, env Env, name string) (*adapter.Store, string, error) {
	st, err := StoreFromEnv(env)
	if err != nil {
		return nil, "", err
	}

	id, err := resolveShared(ctx, env, st, name)
	if err != nil {
		return nil, "", err
	}

	a, ok := st.(*adapter.Store)
	if !ok {
		return nil, "", errors.New("grants: only meaningful against the enrolled directory")
	}

	return a, id, nil
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
// fromPerson reports whether a post came from someone typing rather than
// from an agent's session. Every agent post carries the session it was
// written in; a person posting from the portal or the command line carries
// none.
func fromPerson(e event.Event) bool {
	return e.SessionID == "" && e.Participant == ""
}

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

// PostOption adjusts a post.
type PostOption func(*postOptions)

type postOptions struct {
	outcome string
}

// WithOutcome sets a close's outcome: resolved, handed_over or dropped.
func WithOutcome(outcome string) PostOption {
	return func(o *postOptions) { o.outcome = outcome }
}

package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
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

	fmt.Fprintf(w, "created %s (%s); %s\nothers in the tenant can list it; share it with `parley grant <name> --user <identity> --access read,write` (you own it)\n%s\n", ns.DisplayName, ns.ID, followed, WaitAdviceFor(OnClaude()))
	return nil
}

// ListShared prints shared conversations in the tenant: one directory
// query. The access column comes from ownership against this identity's
// membership (owner, admin, tenant-wide); a namespace owned by someone
// else shows "grant?" because a bearer cannot list its own grants yet
// (handoff delta 9); `join` finds out for one conversation by reading it.
// SharedRow is one shared conversation for list and the TUI.
type SharedRow struct {
	Name, ID, Access, Subscribed, Description string
	Tags                                      string
}

func ListSharedRows(ctx context.Context, env Env, tag, q string) ([]SharedRow, error) {
	st, err := StoreFromEnv(env)
	if err != nil {
		return nil, err
	}

	filter := store.Scope{"kind": "conversation", "mode": string(naming.ModeShared)}
	if tag != "" {
		filter["tags"] = []string{tag}
	}

	metas, err := st.Find(ctx, filter, 200)
	if err != nil {
		return nil, err
	}

	me := MyClaims(ctx, env)
	subs := map[string]Subscription{}
	for _, s := range Subscriptions(env) {
		subs[s.ID] = s
	}

	var rows []SharedRow
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

		rows = append(rows, SharedRow{Name: m.DisplayName, ID: m.ID, Access: access, Subscribed: sub, Description: str(m.Scope["description"]), Tags: fmt.Sprint(m.Scope["tags"])})
	}
	return rows, nil
}

func ListShared(ctx context.Context, env Env, tag, q string, w io.Writer) error {
	rows, err := ListSharedRows(ctx, env, tag, q)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "%-28s %-10s %-10s  %s\n", "name", "access", "subscribed", "description [tags]")
	for _, r := range rows {
		fmt.Fprintf(w, "%-28s %-10s %-10s  %s %s\n", r.Name, r.Access, r.Subscribed, r.Description, r.Tags)
	}
	return nil
}

// Join subscribes this agent to a shared conversation after proving it can
// read it. mode is full or digest. In a session, join marks the subscription
// as joined and preserves any existing cursor; without a session, it behaves
// as before (machine-wide join).
func Join(ctx context.Context, env Env, name, mode, pick, as string, w io.Writer) error {
	if as != "" {
		if err := ValidHandle(as); err != nil {
			return err
		}
	}

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

	if as == "" {
		// A join that names no handle speaks under the session's, so a
		// seat does not go back to being a laptop name by forgetting a
		// flag (participant.go).
		as = Participant(env)
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
	fmt.Fprintln(w, WaitAdviceFor(OnClaude()))
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
	to, cc := SplitRecipients([]string{to}, text)

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
		SessionID: env.Session, Identity: authorOf(env), Participant: ParticipantFor(env, id), To: to, CC: event.CC(cc), ReplyTo: replyTo, Tags: tags,
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

	subject := strings.TrimSpace(o.subject)
	if subject != "" && k == event.KindPostClaim && replyTo == "" {
		content["subject"] = subject
	}
	if obj := strings.TrimSpace(o.objective); obj != "" && (k == event.KindPostRequest || k == event.KindPostClaim) {
		content["objective"] = obj
	}
	stage, project := strings.TrimSpace(o.stage), strings.TrimSpace(o.project)
	if stage != "" {
		content["stage"] = stage
	}
	if project != "" {
		content["project"] = project
	}
	if ev := strings.TrimSpace(o.evidence); ev != "" && k == event.KindPostMove {
		content["evidence"] = ev
	}
	if isLodestar(k) {
		body, err := checkLodestar(k, text, o)
		if err != nil {
			return fmt.Errorf("post %s: %w", strings.TrimPrefix(string(k), "post."), err)
		}
		content = body
	}

	// A request on a stage that names nobody goes to the agents placed on
	// that stage (design 21 slice 4, routing by stage). statefs.ai never
	// rewrites a post, so the poster addresses it.
	var routed []string
	if k == event.KindPostRequest && e.To == "" && len(e.CC) == 0 && stage != "" && project != "" {
		if routed = stageRecipients(ctx, env, project, stage, e.Identity, e.Participant); len(routed) > 0 {
			e.To, e.CC = routed[0], event.CC(routed[1:])
		}
	}

	body, _ := json.Marshal(content)
	e.Content = body

	// Work posts are checked first, so a refusal says what to do instead
	// of a bare validation error.
	var work *workLog
	if isWork(k) || o.outcome != "" || subject != "" || stage != "" || project != "" {
		if work, err = readWork(ctx, env, st, id); err != nil {
			return err
		}

		var gate moveGate
		if k == event.KindPostMove && stage != "" {
			gate = workflowGate(ctx, env, project, stage)
		}

		if err := checkWork(work, k, e.Identity, replyTo, o.outcome, subject, stage, project, gate); err != nil {
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
	if len(routed) > 0 {
		fmt.Fprintf(w, "routed to stage %s: %s\n", stage, strings.Join(routed, ", "))
	}

	// Two claims can pass the check at once, and a claim on a subject is
	// never refused by it; the earlier one holds. Say so to the one that lost.
	if k == event.KindPostClaim {
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
	var shown []string
	for e, err := range conv.Scan(ctx, store.Position(from), 0) {
		if err != nil {
			return err
		}

		n++
		last++
		shown = append(shown, e.ID)
		fmt.Fprintln(w, formatPost(e, name, last-1, 0))
	}

	// What this printed is now seen, so it is not kept for the next prompt
	// as well, whether or not the cursor moves (a peek shows it too).
	unspool(env, shown)

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
// A post wakes a seat only when that seat is a recipient, or the post
// says @everyone, whoever wrote it. A post with no @ at all wakes only
// for a question or a request, or the author of the post it replies to
// (mine, see repliesToMine).
func holdsTurn(e event.Event, mine bool, l *workLog) bool {
	if len(recipients(e)) > 0 {
		for _, to := range recipients(e) {
			if toEveryone(to) {
				return true
			}
		}

		return mine
	}

	if mine {
		return true
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

	// Each conversation is its own round trip. Reading them one after
	// another is what made UserPromptSubmit take about a second per
	// follow. The delivery lock is already held, so the parallel reads
	// cannot interleave with another delivery of this session.
	parts := make([][]pendingPost, len(subs))
	errs := make([]error, len(subs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, pendingParallel)
	for i := range subs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			parts[i], errs[i] = readSubPending(ctx, markCtx, env, st, subs[i], me)
		}(i)
	}
	wg.Wait()
	for i, s := range subs {
		if errs[i] != nil {
			failed[s.Name] = errs[i]
		}
		items = append(items, parts[i]...)
	}

	sort.SliceStable(items, func(i, j int) bool { return items[i].mine && !items[j].mine })
	return items, true, failed
}

// pendingParallel is how many followed conversations one delivery may read
// at once. Four is enough that a session following a handful overlaps the
// round trips, and small enough that a session following dozens does not
// open dozens of member connections together.
const pendingParallel = 4

// readSubPending reads one conversation past its cursor and saves that
// cursor. It does not touch any other conversation's rows.
func readSubPending(ctx, markCtx context.Context, env Env, st store.Store, s Subscription, me string) ([]pendingPost, error) {
	if cur, ok := readSession(env, s.Name); ok {
		s.Cursor = cur.Cursor
	}

	var items []pendingPost
	var scanErr error
	pos := s.Cursor
	hasWork := false
	for e, err := range conversation.Attach(st, s.ID).Scan(ctx, store.Position(s.Cursor), 0) {
		if err != nil {
			if errors.Is(err, event.ErrBadRow) {
				pos++
				fmt.Fprintf(os.Stderr, "parley: skipped a row that will not decode in %s at %d: %v\n", s.Name, pos, err)
				continue
			}

			scanErr = err
			break
		}

		pos++
		mine := addressesAny(e, me, s.Participant, env.Session)
		if s.Mode == "digest" && !digestKeeps(e, mine) && !unaddressedReply(e) {
			continue
		}

		if fromMe(env, me, e) {
			continue
		}

		items = append(items, pendingPost{sub: s, e: e, pos: pos, mine: mine})
		hasWork = hasWork || needsFold(e)
	}

	if pos != s.Cursor {
		s.Cursor = pos
		_ = saveSub(env, s)
	}

	var fold *workLog
	if hasWork && markCtx.Err() == nil {
		if l, err := readWork(markCtx, env, st, s.ID); err == nil {
			fold = l
			for i := range items {
				items[i].work = workMark(l, items[i].e)
			}
		}
	}

	items = markReplies(items, fold, s, env.Session)
	for i := range items {
		items[i].hold = holdsTurn(items[i].e, items[i].mine, fold)
	}
	return items, scanErr
}

// unaddressedReply is a reply that names nobody. It is meant for the
// author of the post it answers, as a reply in any chat is. Claims and
// closes are bookkeeping on work, not a word to its author.
func unaddressedReply(e event.Event) bool {
	return e.ReplyTo != "" && len(recipients(e)) == 0 && e.Kind != event.KindPostClaim && e.Kind != event.KindPostClose
}

// needsFold says a row needs the conversation's fold: work marks, or the
// author of the post a reply answers.
func needsFold(e event.Event) bool {
	return folded(e.Kind) || unaddressedReply(e)
}

// repliesToMine says e is an unaddressed reply to a post this reader wrote,
// under this session or under the handle it speaks with here.
func repliesToMine(l *workLog, e event.Event, participant, session string) bool {
	if l == nil || !unaddressedReply(e) {
		return false
	}

	a, ok := l.Authors[e.ReplyTo]
	if !ok {
		return false
	}

	return (session != "" && a.Session == session) || (participant != "" && a.Participant == participant)
}

// markReplies makes an unaddressed reply to this reader's post its own,
// and drops, for a digest, the unaddressed replies that turn out to be
// someone else's.
func markReplies(items []pendingPost, fold *workLog, s Subscription, session string) []pendingPost {
	out := items[:0]
	for _, it := range items {
		if !it.mine && repliesToMine(fold, it.e, s.Participant, session) {
			it.mine = true
		}

		if s.Mode == "digest" && !digestKeeps(it.e, it.mine) {
			continue
		}

		out = append(out, it)
	}

	return out
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

// injectLines is InjectHold, and also answers the lines it rendered into
// the text; lines past the budget are already kept for the next turn.
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
	// A wait that died mid-delivery left these behind; its cursors have
	// already moved past them, so this turn is where they surface.
	kept := append(drainDelivery(env), drainContext(env)...)
	if len(items) == 0 && len(kept) == 0 {
		return "", false, nil
	}

	hold := false
	for _, it := range items {
		hold = hold || it.hold
	}

	// One list, so kept lines and new rows share the budget: a session
	// idle for days must not hand its next turn a megabyte of backlog.
	// A post is shown once even when two routes hand it over: a killed
	// wait's delivery and the kept spool, or the spool and a fresh read.
	lines := make([]string, 0, len(kept)+len(items))
	shown := make(map[string]bool, len(kept)+len(items))
	add := func(l string) {
		if k := lineKey(l, ""); !shown[k] {
			shown[k] = true
			lines = append(lines, l)
		}
	}

	for _, l := range kept {
		add(l)
	}

	for _, it := range items {
		add(fmt.Sprintf("- [%s]%s %s", it.sub.Name, it.work, formatPost(it.e, it.sub.Name, it.pos-1, InjectMaxPostBytes)))
	}

	var b strings.Builder
	b.WriteString("statefs.ai parley: new posts in conversations you follow (reply with the post_message tool or `parley post <name> --reply-to <event> ...`):\n")
	n, bytes := 0, 0
	for _, line := range lines {
		if n >= InjectMaxMessages || bytes+len(line) > InjectMaxBytes {
			rest := lines[n:]
			spoolContext(env, rest)
			b.WriteString("- (" + overflowNote(rest) + ")\n")
			break
		}

		b.WriteString(line + "\n")
		n++
		bytes += len(line)
	}

	// Only the lines rendered above: the overflow is already kept, and a
	// caller that keeps these too must not keep it a second time.
	return b.String(), hold, lines[:n]
}

// overflowNote names the conversations holding posts past this turn's
// budget, and the first unshown position in each, so they can be read
// before the next turn delivers them.
func overflowNote(rest []string) string {
	type group struct {
		name string
		from int64
		n    int
	}
	// One group per conversation, wherever its lines fall in the batch, from
	// the first unshown position in it: interleaved lines once listed the
	// same conversation over and over in one note.
	var groups []group
	at := map[string]int{}
	for _, line := range rest {
		name, pos := lineWhere(line)
		i, ok := at[name]
		if !ok {
			at[name] = len(groups)
			groups = append(groups, group{name, pos, 1})
			continue
		}

		groups[i].n++
		if pos > 0 && (groups[i].from == 0 || pos < groups[i].from) {
			groups[i].from = pos
		}
	}
	parts := make([]string, 0, len(groups))
	for _, g := range groups {
		parts = append(parts, fmt.Sprintf("%d more in %s from @%d", g.n, g.name, g.from))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d more", len(rest))
	}
	return strings.Join(parts, "; ")
}

var lineWhereRe = regexp.MustCompile(`^- \[([^\]]+)\].*@(\d+)`)

func lineWhere(line string) (string, int64) {
	head := line
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		head = line[:i]
	}
	m := lineWhereRe.FindStringSubmatch(head)
	if m == nil {
		return "?", 0
	}
	pos, _ := strconv.ParseInt(m[2], 10, 64)
	return m[1], pos
}

// digestKeeps says whether digest mode shows a row: a report, status or
// request, or anything the reader is meant to act on. An @everyone
// comment and a post addressed to this seat are direction. An unaddressed
// comment is chatter, whoever wrote it.
func digestKeeps(e event.Event, mine bool) bool {
	return isDigest(e) || holdsTurn(e, mine, nil)
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
// recipients is every addressee on a post. An old row has only To.
func recipients(e event.Event) []string {
	var out []string
	if to := strings.TrimSpace(e.To); to != "" {
		out = append(out, to)
	}

	for _, c := range e.CC {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}

	return out
}

// addressesAny is true when any addressee names this reader, including
// @everyone.
func addressesAny(e event.Event, identity, participant, session string) bool {
	for _, to := range recipients(e) {
		if addressesMe(to, identity, participant, session) {
			return true
		}
	}

	return false
}

var mentionRe = regexp.MustCompile(`(?:^|\s)@(\*|[A-Za-z][^\s]*)`)

// SplitRecipients turns an explicit --to and the @-mentions in the text
// into the stored To and CC. An explicit to is never replaced by a mention
// in the text. @everyone is an address only outside quotes and code; inside
// them it is words. A mention starts with a letter, so @1248 is not one.
// The same name twice is one recipient.
func SplitRecipients(explicit []string, text string) (string, []string) {
	seen := map[string]struct{}{}
	var all []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		s = strings.TrimRight(s, ".,;:!?")
		if s == "" {
			return
		}

		if s == "*" || strings.EqualFold(s, "everyone") {
			s = "everyone"
		}

		key := strings.ToLower(s)
		if _, ok := seen[key]; ok {
			return
		}

		seen[key] = struct{}{}
		all = append(all, s)
	}

	var pinned string
	for _, s := range explicit {
		before := len(all)
		add(s)
		if pinned == "" && len(all) > before {
			pinned = all[len(all)-1]
		}
	}

	for _, m := range mentionRe.FindAllStringSubmatch(stripQuoted(text), -1) {
		add(m[1])
	}

	if pinned == "" {
		for _, s := range all {
			if s == "everyone" {
				return "everyone", nil
			}
		}
	}

	if len(all) == 0 {
		return "", nil
	}

	return all[0], all[1:]
}

// stripQuoted blanks "...", '...', `...`, ``` fences, and markdown quote
// lines, so an @ inside them is not an address.
func stripQuoted(s string) string {
	var lines strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ">") {
			lines.WriteByte('\n')
			continue
		}

		lines.WriteString(line)
		lines.WriteByte('\n')
	}

	s = lines.String()
	return stripSpans(s)
}

func stripSpans(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if strings.HasPrefix(s[i:], "```") {
			end := strings.Index(s[i+3:], "```")
			if end < 0 {
				break
			}

			i += 3 + end + 3
			b.WriteByte(' ')
			continue
		}

		c := s[i]
		if c == '"' || c == '\'' || c == '`' {
			j := i + 1
			for j < len(s) && s[j] != c {
				if s[j] == '\\' && j+1 < len(s) {
					j += 2
					continue
				}

				j++
			}

			if j < len(s) {
				i = j + 1
				b.WriteByte(' ')
				continue
			}
		}

		b.WriteByte(c)
		i++
	}

	return b.String()
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

// addressesMe answers whether a post's --to names this reader: by
// identity, by the handle it speaks under, by the identity#session
// form speakerOf prints, or by a prefix of this session id.
func addressesMe(to, identity, participant, session string) bool {
	if toEveryone(to) {
		return true
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return false
	}
	if to == identity || (participant != "" && to == participant) {
		return true
	}
	if session == "" {
		return false
	}
	short := shortSession(session)
	if identity != "" && short != "" && (to == identity+"#"+short || strings.HasPrefix(to, identity+"#"+short)) {
		return true
	}
	if len(to) >= 8 && (to == short || strings.HasPrefix(session, to)) {
		return true
	}
	printed := speakerOf(event.Event{Identity: identity, SessionID: session, Participant: participant})
	return printed != "?" && to == printed
}

// toEveryone is an explicit ping of every subscriber (@everyone, @*, --to everyone).
func toEveryone(to string) bool {
	return strings.EqualFold(strings.TrimSpace(to), "everyone")
}

// PostOption adjusts a post.
type PostOption func(*postOptions)

type postOptions struct {
	outcome      string
	subject      string
	goal         string
	doneWhen     string
	owner        string
	state        string
	amends       string
	objective    string
	claim        string
	evidence     string
	mark         string
	evidenceKind string
	notChecked   string
	whoSaid      string
	whoMay       string
	judge        string
	refusedWho   string
	refusedBy    string
	stage        string
	project      string
}

// WithLodestar fills objective / assessment fields (and a work post's
// optional link to an objective).
func WithLodestar(goal, doneWhen, owner, state, amends, objective, claim, evidence, mark, evidenceKind, notChecked, whoSaid, whoMay, judge string) PostOption {
	return func(o *postOptions) {
		o.goal, o.doneWhen, o.owner, o.state, o.amends = goal, doneWhen, owner, state, amends
		o.objective, o.claim, o.evidence, o.mark = objective, claim, evidence, mark
		o.evidenceKind, o.notChecked, o.whoSaid, o.whoMay, o.judge = evidenceKind, notChecked, whoSaid, whoMay, judge
	}
}

// WithRefused records an optional assessment refusal (who was refused, by whom).
func WithRefused(who, by string) PostOption {
	return func(o *postOptions) { o.refusedWho, o.refusedBy = who, by }
}

// WithSubject names what an unprompted claim works on: a repo-relative path, a
// branch, a PR url, or free text. A second claim on the same subject is told
// who holds it.
func WithSubject(subject string) PostOption {
	return func(o *postOptions) { o.subject = subject }
}

// WithStage names the board stage a request or a claim starts at, or the
// stage a move advances the work to.
func WithStage(stage, project string) PostOption {
	return func(o *postOptions) { o.stage, o.project = stage, project }
}

// WithOutcome sets a close's outcome: resolved, handed_over or dropped.
func WithOutcome(outcome string) PostOption {
	return func(o *postOptions) { o.outcome = outcome }
}

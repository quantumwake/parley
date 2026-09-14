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

	"github.com/quantumwake/parley/pkg/conversation"
	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/store"
)

// A shared conversation carries two sorts of post. Exchange (question,
// answer, comment, report, status, artifact) is talk: anyone may reply and
// nobody owns it. Work (request, claim, close) is something to be done and
// has one owner at a time:
//
//	request   names work for someone to take
//	claim     takes it: a reply to open work (a request, or unprompted work
//	          handed over), or, with no reply, work started unprompted (the
//	          claim itself is the work item)
//	close     ends a claim, or a request, with an outcome:
//	          resolved     the work is done
//	          handed_over  (on a claim) the work is open again for the next claim
//	          dropped      (on a claim) a request is open again; unprompted work ends
//	                       (on a request, by its requester) the request is withdrawn
//
// Only a claim's holder may close it; only a request's requester (or its
// current holder) may close the request itself. The earliest claim on open
// work holds it. Parley folds each conversation's rows into this state,
// caches the fold (rows never change), refuses posts that would not mean
// what their author thinks, shows the state on delivery, and lists it.

// WorkGuide is what an agent is told about posts and work. cmd names the
// parley command it can reach.
func WorkGuide(cmd string) string {
	return "Work posts: `request` names a task; `claim` it (reply to the request) before starting; work nobody requested is a `claim` with no reply, naming the goal or error and the repo and branch; " +
		"`close` your claim with outcome resolved, handed_over or dropped. Exchange posts (question, answer, comment, report, status, artifact) need no claim. " +
		"Where a repo's checkout is claimed, work in your own git worktree. `" + cmd + " work` lists open and claimed work"
}

// Outcomes a close may carry.
const (
	OutcomeResolved   = "resolved"
	OutcomeHandedOver = "handed_over"
	OutcomeDropped    = "dropped"
)

func validOutcome(o string) bool {
	return o == OutcomeResolved || o == OutcomeHandedOver || o == OutcomeDropped
}

// WorkState is where a work item stands.
type WorkState string

const (
	WorkOpen    WorkState = "open"    // nobody holds it
	WorkClaimed WorkState = "claimed" // someone holds it
	WorkClosed  WorkState = "closed"  // resolved, dropped or withdrawn
)

// WorkItem is one request, or one unprompted claim, and where it stands.
type WorkItem struct {
	ID      string     `json:"id"` // the request's event id, or the unprompted claim's
	Pos     int64      `json:"pos"`
	Kind    event.Kind `json:"kind"` // post.request or post.claim
	By      string     `json:"by"`   // who opened it, as spoken
	ByID    string     `json:"by_id"`
	BySn    string     `json:"by_session,omitempty"`
	Text    string     `json:"text"`
	AtMs    int64      `json:"at_ms"`
	State   WorkState  `json:"state"`
	Holder  string     `json:"holder,omitempty"`
	HoldID  string     `json:"holder_id,omitempty"`
	HoldSn  string     `json:"holder_session,omitempty"`
	ClaimID string     `json:"claim_id,omitempty"` // the holding claim
	ClaimAt int64      `json:"claim_pos,omitempty"`
	Outcome string     `json:"outcome,omitempty"` // the last effective close's outcome
}

// workLog is a conversation's work, folded from its rows up to Next.
type workLog struct {
	Next   int64                `json:"next"`   // the position after the last row folded
	Items  map[string]*WorkItem `json:"items"`  // by item id
	Order  []string             `json:"order"`  // item ids in position order
	Claims map[string]string    `json:"claims"` // every claim that took effect -> its item
	// Effects says what each work post did, for delivery marks and for
	// refusals that name the reason.
	Effects map[string]string `json:"effects"`
}

func newWorkLog() *workLog {
	return &workLog{Items: map[string]*WorkItem{}, Claims: map[string]string{}, Effects: map[string]string{}}
}

// workFoldTimeout bounds a fold done on the delivery path.
var workFoldTimeout = 2 * time.Second

// readWork folds a conversation's work, continuing a cached fold from where
// it stopped. Rows never change, so the cache is a derivation, not state.
func readWork(ctx context.Context, env Env, st store.Store, id string) (*workLog, error) {
	l := loadWorkCache(env, id)
	folded := l.Next
	pos := l.Next - 1
	for e, err := range conversation.Attach(st, id).Scan(ctx, store.Position(l.Next), 0) {
		if err != nil {
			if pos+1 != folded {
				l.Next = pos + 1
				saveWorkCache(env, id, l)
			}

			return nil, err
		}

		pos++
		l.apply(e, pos)
	}

	l.Next = pos + 1
	if l.Next != folded {
		saveWorkCache(env, id, l)
	}

	return l, nil
}

func workCachePath(env Env, id string) string {
	return filepath.Join(env.DataDir, "work", filepath.Base(id)+".json")
}

func loadWorkCache(env Env, id string) *workLog {
	if env.DataDir == "" {
		return newWorkLog()
	}

	b, err := os.ReadFile(workCachePath(env, id))
	if err != nil {
		return newWorkLog()
	}

	l := newWorkLog()
	if json.Unmarshal(b, l) != nil || l.Items == nil || l.Claims == nil || l.Effects == nil {
		return newWorkLog()
	}

	return l
}

func saveWorkCache(env Env, id string, l *workLog) {
	if env.DataDir != "" {
		_ = writeJSONFile(workCachePath(env, id), l)
	}
}

// apply folds one row into the log. Rows that are not work change nothing;
// work posts that refer to nothing record why.
func (l *workLog) apply(e event.Event, pos int64) {
	if !isWork(e.Kind) {
		return
	}

	text, outcome := postText(e)
	switch e.Kind {
	case event.KindPostRequest:
		l.add(&WorkItem{ID: e.ID, Pos: pos, Kind: e.Kind, By: speakerOf(e), ByID: e.Identity, BySn: e.SessionID, Text: text, AtMs: e.TSMs, State: WorkOpen})
		l.Effects[e.ID] = "opened"
	case event.KindPostClaim:
		if e.ParentID == "" {
			item := &WorkItem{ID: e.ID, Pos: pos, Kind: e.Kind, By: speakerOf(e), ByID: e.Identity, BySn: e.SessionID, Text: text, AtMs: e.TSMs}
			l.add(item)
			l.hold(item, e, pos)
			return
		}

		item := l.Items[e.ParentID]
		switch {
		case item == nil:
			l.Effects[e.ID] = "ignored: it does not reply to open work"
		case item.State == WorkClaimed:
			l.Effects[e.ID] = fmt.Sprintf("lost: %s claimed it first at @%d", item.Holder, item.ClaimAt)
		case item.State == WorkClosed:
			l.Effects[e.ID] = "ignored: the work was already closed"
		default:
			l.hold(item, e, pos)
		}
	case event.KindPostClose:
		l.Effects[e.ID] = l.close(e, outcome)
	}
}

func (l *workLog) add(item *WorkItem) {
	l.Items[item.ID] = item
	l.Order = append(l.Order, item.ID)
}

func (l *workLog) hold(item *WorkItem, e event.Event, pos int64) {
	item.State, item.Holder, item.HoldID, item.HoldSn, item.ClaimID, item.ClaimAt = WorkClaimed, speakerOf(e), e.Identity, e.SessionID, e.ID, pos
	l.Claims[e.ID] = item.ID
	l.Effects[e.ID] = "holds"
}

// close applies a close and answers its effect: "closed: <outcome>",
// "reopened: <outcome>", or "ignored: <why>".
func (l *workLog) close(e event.Event, outcome string) string {
	if !validOutcome(outcome) {
		return "ignored: no valid outcome"
	}

	if itemID, onClaim := l.Claims[e.ParentID]; onClaim {
		item := l.Items[itemID]
		switch {
		case item.ClaimID != e.ParentID || item.State != WorkClaimed:
			return "ignored: that claim no longer holds the work"
		case e.Identity != item.HoldID:
			return "ignored: only " + item.Holder + " can close their claim"
		}

		if outcome == OutcomeResolved || (outcome == OutcomeDropped && item.Kind == event.KindPostClaim) {
			item.State, item.Outcome = WorkClosed, outcome
			return "closed: " + outcome
		}

		item.State, item.Outcome = WorkOpen, outcome
		item.Holder, item.HoldID, item.HoldSn, item.ClaimID, item.ClaimAt = "", "", "", "", 0
		return "reopened: " + outcome
	}

	item := l.Items[e.ParentID]
	switch {
	case item == nil || item.Kind != event.KindPostRequest:
		return "ignored: a close replies to a claim or a request"
	case item.State == WorkClosed:
		return "ignored: already closed"
	case outcome == OutcomeHandedOver:
		return "ignored: hand work over by closing the claim, not the request"
	case e.Identity != item.ByID && !(item.State == WorkClaimed && e.Identity == item.HoldID):
		return "ignored: only the requester or the holder can close the request"
	}

	if outcome == OutcomeDropped {
		outcome = "withdrawn"
	}

	item.State, item.Outcome = WorkClosed, outcome
	return "closed: " + outcome
}

// postText is a post's text and, for a close, its outcome.
func postText(e event.Event) (text, outcome string) {
	var m map[string]any
	_ = json.Unmarshal(e.Content, &m)
	text, _ = m["text"].(string)
	outcome, _ = m["outcome"].(string)
	return text, outcome
}

// checkWork refuses a work post that would not mean what its author thinks.
// actor is the posting identity. Every refusal says what to do instead.
func checkWork(l *workLog, kind event.Kind, actor, replyTo, outcome string) error {
	if kind != event.KindPostClose && outcome != "" {
		return errors.New("an outcome belongs on a close")
	}

	switch kind {
	case event.KindPostClaim:
		if replyTo == "" {
			return nil // unprompted work
		}

		item := l.Items[replyTo]
		if item == nil {
			if _, isWorkPost := l.Effects[replyTo]; isWorkPost {
				return errors.New("that is a claim or a close, not open work: reply with a comment to coordinate with whoever holds it")
			}

			return errors.New("only a request (or handed-over work) can be claimed: answer a question with kind answer, and to start work nobody requested, post a claim with no reply")
		}

		switch item.State {
		case WorkClaimed:
			return fmt.Errorf("already claimed by %s at @%d: reply with a comment to coordinate, or ask them to close it as handed_over", item.Holder, item.ClaimAt)
		case WorkClosed:
			return fmt.Errorf("that work is closed (%s): post a new request if there is more to do", item.Outcome)
		}
	case event.KindPostClose:
		if replyTo == "" {
			return errors.New("a close replies to your claim (or to your request)")
		}

		if !validOutcome(outcome) {
			return fmt.Errorf("a close needs an outcome: %s, %s or %s", OutcomeResolved, OutcomeHandedOver, OutcomeDropped)
		}

		if effect := l.clone().close(event.Event{ParentID: replyTo, Identity: actor}, outcome); strings.HasPrefix(effect, "ignored: ") {
			why := strings.TrimPrefix(effect, "ignored: ")
			if e := l.Effects[replyTo]; strings.HasPrefix(e, "lost: ") {
				why = "that claim never held the work (" + strings.TrimPrefix(e, "lost: ") + "), so there is nothing to close"
			}

			return errors.New(why)
		}
	}

	return nil
}

// clone copies the log deeply enough to try a close on it.
func (l *workLog) clone() *workLog {
	c := newWorkLog()
	c.Next = l.Next
	for id, item := range l.Items {
		cp := *item
		c.Items[id] = &cp
	}

	for k, v := range l.Claims {
		c.Claims[k] = v
	}

	c.Order = append(c.Order, l.Order...)
	return c
}

// workMark is what delivery shows beside a work post: what it did and where
// its item stands now.
func workMark(l *workLog, e event.Event) string {
	if l == nil {
		return ""
	}

	effect := l.Effects[e.ID]
	switch e.Kind {
	case event.KindPostRequest:
		return itemMark(l.Items[e.ID])
	case event.KindPostClaim:
		if itemID, ok := l.Claims[e.ID]; ok {
			if item := l.Items[itemID]; item != nil && item.ClaimID == e.ID {
				return itemMark(item)
			}

			return " [claim no longer holds the work]"
		}

		if effect != "" {
			return " [claim " + effect + "]"
		}
	case event.KindPostClose:
		if effect != "" {
			return " [close " + effect + "]"
		}
	}

	return ""
}

func itemMark(item *WorkItem) string {
	if item == nil {
		return ""
	}

	switch item.State {
	case WorkOpen:
		return " [work: open, unclaimed]"
	case WorkClaimed:
		return fmt.Sprintf(" [work: claimed by %s at @%d]", item.Holder, item.ClaimAt)
	default:
		return " [work: closed, " + item.Outcome + "]"
	}
}

// isWork reports whether a kind is part of the work protocol.
func isWork(k event.Kind) bool {
	return k == event.KindPostRequest || k == event.KindPostClaim || k == event.KindPostClose
}

// claimOutcome says, after a claim was appended, whether it holds the work.
func claimOutcome(l *workLog, claimID string) string {
	if itemID, ok := l.Claims[claimID]; ok {
		if item := l.Items[itemID]; item != nil && item.ClaimID == claimID {
			return ""
		}
	}

	if e := l.Effects[claimID]; strings.HasPrefix(e, "lost: ") {
		return "your claim does not hold: " + strings.TrimPrefix(e, "lost: ") + "; no close is needed"
	}

	return ""
}

// ListWork prints the work in followed conversations (or those named): what
// is open, what is claimed and by whom, and what is mine.
func ListWork(ctx context.Context, env Env, names []string, all bool, w io.Writer) error {
	subs, err := waitSet(ctx, env, names)
	if err != nil {
		return err
	}

	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	shown, failed := 0, 0
	for _, s := range subs {
		l, err := readWork(ctx, env, st, s.ID)
		if err != nil {
			fmt.Fprintf(w, "[%s] cannot read: %v\n", s.Name, err)
			failed++
			continue
		}

		items := make([]*WorkItem, 0, len(l.Order))
		for _, id := range l.Order {
			if item := l.Items[id]; all || item.State != WorkClosed {
				items = append(items, item)
			}
		}

		sort.SliceStable(items, func(i, j int) bool { return rank(items[i].State) < rank(items[j].State) })
		for _, item := range items {
			fmt.Fprintf(w, "[%s] @%d %s · %s · by %s%s · %s\n    %s\n", s.Name, item.Pos, strings.TrimPrefix(string(item.Kind), "post."),
				age(time.UnixMilli(item.AtMs)), item.By, whose(env, item.ByID, item.BySn), describeItem(env, item), firstLine(item.Text, 140))
			shown++
		}
	}

	if failed > 0 {
		return fmt.Errorf("work: %d conversation(s) could not be read", failed)
	}

	if shown == 0 {
		fmt.Fprintln(w, "no open or claimed work in the conversations followed")
	}

	return nil
}

func describeItem(env Env, item *WorkItem) string {
	switch item.State {
	case WorkOpen:
		if item.Outcome != "" {
			return "open again (" + item.Outcome + ")"
		}

		return "open"
	case WorkClaimed:
		if item.ClaimID == item.ID {
			return "in progress" + whose(env, item.HoldID, item.HoldSn)
		}

		return fmt.Sprintf("claimed by %s @%d%s", item.Holder, item.ClaimAt, whose(env, item.HoldID, item.HoldSn))
	default:
		return "closed, " + item.Outcome
	}
}

// whose marks what this reader wrote: by session when both sides have one,
// else by identity, which sessions sharing an identity cannot tell apart.
func whose(env Env, identity, session string) string {
	me := authorOf(env)
	switch {
	case identity == "" || identity != me:
		return ""
	case env.Session != "" && session != "":
		if session == env.Session {
			return " (mine)"
		}

		return ""
	default:
		return " (my identity)"
	}
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func rank(s WorkState) int {
	switch s {
	case WorkOpen:
		return 0
	case WorkClaimed:
		return 1
	}

	return 2
}

func firstLine(text string, limit int) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if len(line) > limit {
		return line[:limit] + "…"
	}

	return line
}

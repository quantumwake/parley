package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
//	claim     takes it: a reply to a request, or, with no reply, work started
//	          unprompted (the claim itself is the work item)
//	close     ends a claim or a request, with an outcome: resolved,
//	          handed_over (the request is open again) or dropped
//
// The earliest open claim on a request holds it; a later one is refused.
// Parley derives each work item's state from the conversation, shows it on
// delivery, lists it with `parley work`, and tells agents the rules.

// WorkGuide is what an agent is told about posts and work.
const WorkGuide = "Post kinds: question, answer, comment, report, status and artifact are exchange (reply when useful; nobody owns them, and answering a question needs no claim). " +
	"request names work; claim it (kind claim, reply_to the request) before starting on it; " +
	"to start work nobody requested, post a claim with no reply_to that names the goal or exact error and the repo and branch you will touch; " +
	"in a repo someone else has claimed, work in its own git worktree, not the shared checkout; " +
	"when done, post kind close replying to your claim with outcome resolved, handed_over or dropped. " +
	"`parley work` lists open and claimed work"

// Outcomes a close may carry.
const (
	OutcomeResolved   = "resolved"
	OutcomeHandedOver = "handed_over"
	OutcomeDropped    = "dropped"
)

// WorkState is where a work item stands.
type WorkState string

const (
	WorkOpen    WorkState = "open"    // requested, nobody holds it
	WorkClaimed WorkState = "claimed" // someone holds it
	WorkClosed  WorkState = "closed"  // resolved or dropped
)

// WorkItem is one request, or one unprompted claim, and where it stands.
type WorkItem struct {
	ID       string // the request's event id, or the unprompted claim's
	Pos      int64
	Kind     event.Kind // post.request or post.claim
	By       string     // who asked (request) or started it (unprompted claim)
	Text     string
	At       time.Time
	State    WorkState
	Holder   string // who holds it while claimed
	ClaimID  string // the holding claim
	holderID string // the holding claim's identity and session, to recognise my own
	holderSn string
	ClaimPos int64
	Outcome  string // the last close's outcome
}

// workLog is a conversation's work, derived from its rows.
type workLog struct {
	items  map[string]*WorkItem // by item id
	claims map[string]string    // claim event id -> item id
	order  []string
}

// readWork derives the work items of one conversation from all its rows.
func readWork(ctx context.Context, st store.Store, id string) (*workLog, error) {
	l := &workLog{items: map[string]*WorkItem{}, claims: map[string]string{}}
	pos := int64(-1)
	for e, err := range conversation.Attach(st, id).Scan(ctx, 0, 0) {
		if err != nil {
			return nil, err
		}

		pos++
		l.apply(e, pos)
	}

	return l, nil
}

// apply folds one row into the log. Rows that are not work, or that refer
// to something the log does not know, change nothing.
func (l *workLog) apply(e event.Event, pos int64) {
	text, outcome := postText(e)
	switch e.Kind {
	case event.KindPostRequest:
		l.add(&WorkItem{ID: e.ID, Pos: pos, Kind: e.Kind, By: speakerOf(e), Text: text, At: time.UnixMilli(e.TSMs), State: WorkOpen})
	case event.KindPostClaim:
		if e.ParentID == "" {
			item := &WorkItem{ID: e.ID, Pos: pos, Kind: e.Kind, By: speakerOf(e), Text: text, At: time.UnixMilli(e.TSMs),
				State: WorkClaimed, Holder: speakerOf(e), ClaimID: e.ID, ClaimPos: pos, holderID: e.Identity, holderSn: e.SessionID}
			l.add(item)
			l.claims[e.ID] = e.ID
			return
		}

		item := l.items[e.ParentID]
		if item == nil || item.State != WorkOpen {
			return // not a request, or already held: the earlier claim wins
		}

		item.State, item.Holder, item.ClaimID, item.ClaimPos = WorkClaimed, speakerOf(e), e.ID, pos
		item.holderID, item.holderSn = e.Identity, e.SessionID
		l.claims[e.ID] = item.ID
	case event.KindPostClose:
		itemID, ok := l.claims[e.ParentID]
		if !ok {
			itemID = e.ParentID
		}

		item := l.items[itemID]
		if item == nil || item.State == WorkClosed {
			return
		}

		// A close on a claim counts only while that claim holds the item.
		if ok && item.ClaimID != e.ParentID {
			return
		}

		item.Outcome = outcome
		reopen := (outcome == OutcomeHandedOver || outcome == OutcomeDropped) && item.Kind == event.KindPostRequest && ok
		if reopen {
			item.State, item.Holder, item.ClaimID, item.ClaimPos, item.holderID, item.holderSn = WorkOpen, "", "", 0, "", ""
			return
		}

		item.State = WorkClosed
	}
}

func (l *workLog) add(item *WorkItem) {
	l.items[item.ID] = item
	l.order = append(l.order, item.ID)
}

// itemFor is the work item an event belongs to: a request or unprompted
// claim itself, the item a claim holds, or nil.
func (l *workLog) itemFor(e event.Event) *WorkItem {
	if item := l.items[e.ID]; item != nil {
		return item
	}

	if itemID, ok := l.claims[e.ID]; ok {
		return l.items[itemID]
	}

	return nil
}

// postText is a post's text and, for a close, its outcome.
func postText(e event.Event) (text, outcome string) {
	var m map[string]any
	_ = json.Unmarshal(e.Content, &m)
	text, _ = m["text"].(string)
	outcome, _ = m["outcome"].(string)
	return text, outcome
}

// checkWork refuses a work post that would not mean what its author thinks:
// claiming something that is not a request, claiming what someone already
// holds, or closing something that is not open work.
func checkWork(l *workLog, kind event.Kind, replyTo, outcome string) error {
	switch kind {
	case event.KindPostClaim:
		if replyTo == "" {
			return nil // unprompted work
		}

		item := l.items[replyTo]
		if item == nil || item.Kind != event.KindPostRequest {
			if _, isClaim := l.claims[replyTo]; isClaim {
				return errors.New("that is a claim, not a request: only a request can be claimed; reply with a comment to coordinate with its holder")
			}

			return errors.New("only a request can be claimed: answer a question with kind answer, and to start work nobody requested, post a claim with no reply_to")
		}

		switch item.State {
		case WorkClaimed:
			return fmt.Errorf("already claimed by %s at @%d: reply with a comment to coordinate, or ask them to close it as handed_over", item.Holder, item.ClaimPos)
		case WorkClosed:
			return fmt.Errorf("that request is closed (%s): post a new request if there is more to do", item.Outcome)
		}
	case event.KindPostClose:
		switch outcome {
		case OutcomeResolved, OutcomeHandedOver, OutcomeDropped:
		default:
			return fmt.Errorf("a close needs an outcome: %s, %s or %s", OutcomeResolved, OutcomeHandedOver, OutcomeDropped)
		}

		item := l.items[replyTo]
		if itemID, isClaim := l.claims[replyTo]; isClaim {
			item = l.items[itemID]
			if item != nil && item.ClaimID != replyTo {
				return errors.New("that claim no longer holds its request, so there is nothing to close")
			}
		}

		if item == nil {
			return errors.New("a close replies to a claim or a request")
		}

		if item.State == WorkClosed {
			return fmt.Errorf("already closed (%s)", item.Outcome)
		}
	}

	return nil
}

// workMark is what delivery shows beside a work post: where its item stands
// now.
func workMark(l *workLog, e event.Event) string {
	if l == nil {
		return ""
	}

	switch e.Kind {
	case event.KindPostRequest, event.KindPostClaim:
		item := l.itemFor(e)
		if item == nil {
			if e.Kind == event.KindPostClaim {
				return " [not holding: the request was already claimed or closed]"
			}

			return ""
		}

		switch item.State {
		case WorkOpen:
			return " [work: open, unclaimed]"
		case WorkClaimed:
			return fmt.Sprintf(" [work: claimed by %s at @%d]", item.Holder, item.ClaimPos)
		default:
			return fmt.Sprintf(" [work: closed, %s]", item.Outcome)
		}
	case event.KindPostClose:
		_, outcome := postText(e)
		return " [closes: " + outcome + "]"
	}

	return ""
}

// ListWork prints the work in followed conversations (or those named): what
// is open, what is claimed and by whom, and what this session holds.
func ListWork(ctx context.Context, env Env, names []string, all bool, w io.Writer) error {
	subs, err := waitSet(ctx, env, names)
	if err != nil {
		return err
	}

	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	shown := 0
	for _, s := range subs {
		l, err := readWork(ctx, st, s.ID)
		if err != nil {
			fmt.Fprintf(w, "[%s] cannot read: %v\n", s.Name, err)
			continue
		}

		items := make([]*WorkItem, 0, len(l.order))
		for _, id := range l.order {
			if item := l.items[id]; all || item.State != WorkClosed {
				items = append(items, item)
			}
		}

		sort.SliceStable(items, func(i, j int) bool { return rank(items[i].State) < rank(items[j].State) })
		for _, item := range items {
			mine := ""
			if item.State == WorkClaimed && item.mine(env) {
				mine = " (mine)"
			}

			state := string(item.State)
			switch item.State {
			case WorkClaimed:
				state = fmt.Sprintf("claimed by %s at @%d%s", item.Holder, item.ClaimPos, mine)
			case WorkClosed:
				state = "closed, " + item.Outcome
			}

			fmt.Fprintf(w, "[%s] @%d %s, %s ago (%s): %s\n    %s\n", s.Name, item.Pos, strings.TrimPrefix(string(item.Kind), "post."),
				time.Since(item.At).Round(time.Minute), item.By, state, firstLine(item.Text, 140))
			shown++
		}
	}

	if shown == 0 {
		fmt.Fprintln(w, "no open or claimed work in the conversations followed")
	}

	return nil
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

// mine reports whether this session holds the item: by session when both
// sides have one, else by identity.
func (item *WorkItem) mine(env Env) bool {
	if env.Session != "" && item.holderSn != "" {
		return item.holderSn == env.Session
	}

	return item.holderID != "" && item.holderID == authorOf(env)
}

// isWork reports whether a kind is part of the work protocol.
func isWork(k event.Kind) bool {
	return k == event.KindPostRequest || k == event.KindPostClaim || k == event.KindPostClose
}

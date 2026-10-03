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
//	          claim itself is the work item). Unprompted work may name a
//	          subject (a path, a branch, a PR, or free text); a second claim on
//	          a subject somebody holds is told who holds it
//	close     ends a claim, or a request, with an outcome:
//	          resolved     the work is done
//	          handed_over  (on a claim) the work is open again for the next claim
//	          dropped      (on a claim) a request is open again; unprompted work ends
//	                       (on a request, by its requester) the request is withdrawn
//
// Only a claim's holder may close it; only a request's requester (or its
// current holder) may close the request itself. The earliest claim on open
// work holds it, and so does the earliest claim on a subject. Parley folds
// each conversation's rows into this state, caches the fold (rows never
// change), refuses posts that would not mean what their author thinks, shows
// the state on delivery, and lists it.

// WorkGuide is what an agent is told about posts and work. cmd names the
// parley command it can reach.
func WorkGuide(cmd string) string {
	return "Work posts: `request` names a task; `claim` it (reply to the request) before starting; work nobody requested is a `claim` with no reply and a `subject` (path, branch or PR); " +
		"reply to your claim to `move` it (--stage) or `close` it: resolved, handed_over or dropped. A question needs no claim, but claim it when answering is real work; its first answer ends it for everyone. " +
		"Comment, report, status and artifact are talk. Where a repo's checkout is claimed, work in your own git worktree. `" + cmd + " work` lists the work"
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

// WorkItem is one request, one unprompted claim, or one question, and
// where it stands. A question is held the same way work is — the first
// claim takes it, an answer ends it — so that a channel of agents does
// not all answer the same question.
type WorkItem struct {
	ID      string     `json:"id"` // the request's event id, or the unprompted claim's
	Pos     int64      `json:"pos"`
	Kind    event.Kind `json:"kind"` // post.request, post.claim or post.question
	By      string     `json:"by"`   // who opened it, as spoken
	ByID    string     `json:"by_id"`
	BySn    string     `json:"by_session,omitempty"`
	Text    string     `json:"text"`
	Subject string     `json:"subject,omitempty"` // what an unprompted claim says it works on, as written
	AtMs    int64      `json:"at_ms"`
	State   WorkState  `json:"state"`
	Holder  string     `json:"holder,omitempty"`
	HoldID  string     `json:"holder_id,omitempty"`
	HoldSn  string     `json:"holder_session,omitempty"`
	ClaimID string     `json:"claim_id,omitempty"` // the holding claim
	ClaimAt int64      `json:"claim_pos,omitempty"`
	Outcome string     `json:"outcome,omitempty"` // the last effective close's outcome
	// Stage is where the work stands on the project's board: named on the
	// request or the claim, and advanced by a move. Empty is the project's
	// first stage.
	Stage   string `json:"stage,omitempty"`
	StageAt int64  `json:"stage_pos,omitempty"` // the post that set it
	// Project is whose stage it is: a channel can be in two projects, so a
	// stage key alone does not say which board it is on.
	Project string `json:"project,omitempty"`
	// Objective is the objective the work points at: the event id a request
	// or a claim named. Work with none is flagged on the board.
	Objective string `json:"objective,omitempty"`
	// HoldPt is the holder's handle, so a pass can be told apart from the
	// holder when every seat on a machine shares one identity.
	HoldPt string `json:"holder_participant,omitempty"`
	// Passes are the assessments posted on the work while a claim held it.
	Passes []WorkPass `json:"passes,omitempty"`
}

// WorkPass is one assessment on a piece of work: who posted it, and the
// claim it was posted on. A pass on an earlier claim does not count for a
// new holder.
type WorkPass struct {
	ClaimID     string `json:"claim_id"`
	Identity    string `json:"identity"`
	Session     string `json:"session,omitempty"`
	Participant string `json:"participant,omitempty"`
	Pos         int64  `json:"pos"`
}

// workFoldVersion names the fold's rules. A cache from other rules is
// discarded, so every reader folds the same rows the same way.
const workFoldVersion = 9

// workLog is a conversation's work, folded from its rows up to Next.
type workLog struct {
	Version int                  `json:"version"`
	Next    int64                `json:"next"`   // the position after the last row folded
	Items   map[string]*WorkItem `json:"items"`  // by item id
	Order   []string             `json:"order"`  // item ids in position order
	Claims  map[string]string    `json:"claims"` // every claim that took effect -> its item
	// Subjects maps a normalised subject to the newest item opened on it. A
	// second unprompted claim collides only while that item is held.
	Subjects map[string]string `json:"subjects"`
	// Effects says what each work post did, for delivery marks and for
	// refusals that name the reason.
	Effects map[string]string `json:"effects"`
	// Authors says who wrote each post, so a reply that names nobody can
	// reach the author of the post it answers.
	Authors map[string]postAuthor `json:"authors"`
}

// postAuthor is the session and handle a post was written under.
type postAuthor struct {
	Session     string `json:"sn,omitempty"`
	Participant string `json:"p,omitempty"`
}

func newWorkLog() *workLog {
	return &workLog{Version: workFoldVersion, Items: map[string]*WorkItem{}, Claims: map[string]string{}, Subjects: map[string]string{}, Effects: map[string]string{}, Authors: map[string]postAuthor{}}
}

// workFoldTimeout bounds a fold done on the delivery path.
var workFoldTimeout = 2 * time.Second

// readWork folds a conversation's work, continuing a cached fold from where
// it stopped. Rows never change, so the cache is a derivation, not state.
func readWork(ctx context.Context, env Env, st store.Store, id string) (*workLog, error) {
	l := loadWorkCache(env, id)
	if l.Next > 0 {
		// A cache past the conversation's head belongs to another
		// conversation that had this id (a reset store): fold afresh.
		if head, err := st.Head(ctx, id); err == nil && int64(head) < l.Next {
			l = newWorkLog()
		}
	}

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
	if json.Unmarshal(b, l) != nil || l.Version != workFoldVersion || l.Items == nil || l.Claims == nil || l.Subjects == nil || l.Effects == nil || l.Authors == nil {
		return newWorkLog()
	}

	return l
}

func saveWorkCache(env Env, id string, l *workLog) {
	if env.DataDir != "" {
		_ = writeJSONFile(workCachePath(env, id), l)
	}
}

// apply folds one row into the log. Rows that are neither work nor a
// question change nothing; work posts that refer to nothing record why.
func (l *workLog) apply(e event.Event, pos int64) {
	if e.IsPost() && (e.SessionID != "" || e.Participant != "") {
		l.Authors[e.ID] = postAuthor{Session: e.SessionID, Participant: e.Participant}
	}

	if !folded(e.Kind) {
		return
	}

	text, outcome := postText(e)
	switch e.Kind {
	case event.KindPostQuestion:
		// A question is folded so it can be claimed and so an answer
		// silences it, but it is not listed as work: Order is what
		// `parley work` walks, and a question is talk, not a task.
		l.Items[e.ID] = &WorkItem{ID: e.ID, Pos: pos, Kind: e.Kind, By: speakerOf(e), ByID: e.Identity, BySn: e.SessionID, Text: firstLine(text, 140), AtMs: e.TSMs, State: WorkOpen}
		return
	case event.KindPostAnswer:
		// The first answer ends the question for everyone else. A later
		// answer is still delivered; it simply no longer holds turns.
		if item := l.Items[e.ParentID]; item != nil && item.Kind == event.KindPostQuestion && item.State != WorkClosed {
			item.State, item.Outcome, item.Holder, item.HoldID = WorkClosed, "answered", speakerOf(e), e.Identity
		}

		return
	}

	stage, project := postStage(e)
	objective := postObjective(e)
	switch e.Kind {
	case event.KindPostRequest:
		item := &WorkItem{ID: e.ID, Pos: pos, Kind: e.Kind, By: speakerOf(e), ByID: e.Identity, BySn: e.SessionID, Text: firstLine(text, 140), AtMs: e.TSMs, State: WorkOpen}
		l.add(item)
		item.setStage(stage, project, pos)
		item.setObjective(objective)
		l.Effects[e.ID] = "opened"
	case event.KindPostClaim:
		if e.ParentID == "" {
			subject := firstN(postSubject(e), maxSubject)
			key := subjectKey(subject)
			if held := l.heldOn(key); held != nil {
				// The earlier claim on a subject holds it, as the earlier
				// claim on a request does. This one is not a work item: it
				// records why, and `parley work` does not list it.
				l.Effects[e.ID] = fmt.Sprintf("lost: %s claimed the same subject first at @%d", holderLabel(held), held.ClaimAt)
				return
			}

			item := &WorkItem{ID: e.ID, Pos: pos, Kind: e.Kind, By: speakerOf(e), ByID: e.Identity, BySn: e.SessionID, Text: firstLine(text, 140), Subject: subject, AtMs: e.TSMs}
			l.add(item)
			l.hold(item, e, pos)
			item.setStage(stage, project, pos)
			item.setObjective(objective)
			if key != "" {
				l.Subjects[key] = item.ID
			}

			return
		}

		item := l.Items[e.ParentID]
		switch {
		case item == nil:
			l.Effects[e.ID] = "ignored: it does not reply to open work"
		case item.State == WorkClaimed:
			l.Effects[e.ID] = fmt.Sprintf("lost: %s claimed it first at @%d", holderLabel(item), item.ClaimAt)
		case item.State == WorkClosed:
			l.Effects[e.ID] = "ignored: the work was already closed"
		default:
			l.hold(item, e, pos)
			item.setStage(stage, project, pos)
			item.setObjective(objective)
		}
	case event.KindPostClose:
		l.Effects[e.ID] = l.close(e, outcome)
	case event.KindPostMove:
		l.Effects[e.ID] = l.move(e, stage, project, pos)
	case event.KindPostAssessment:
		l.pass(e, pos)
	}
}

// pass records an assessment that replies to the work's holding claim,
// posted after that claim took hold, with a passing mark and not refused.
// A newer claim voids every earlier pass: a pass is kept with its claim
// and counts only while that claim holds.
func (l *workLog) pass(e event.Event, pos int64) {
	itemID, onClaim := l.Claims[e.ParentID]
	if !onClaim {
		return // a pass judges the holder's work, so it replies to the claim
	}

	item := l.Items[itemID]
	if item == nil || item.ClaimID != e.ParentID || item.State != WorkClaimed || pos <= item.ClaimAt || !assessmentPasses(e) {
		return
	}

	p := WorkPass{ClaimID: item.ClaimID, Identity: e.Identity, Session: e.SessionID, Participant: e.Participant, Pos: pos}
	item.Passes = append(item.Passes, p)
	switch passTier(item, p) {
	case passVerified:
		l.Effects[e.ID] = "passes: verified, another identity"
	case passAttested:
		l.Effects[e.ID] = "passes: attested, same identity (" + p.Participant + " for " + item.HoldPt + ")"
	default:
		l.Effects[e.ID] = "does not pass: the holder's own seat"
	}
}

// assessmentPasses says an assessment is a pass: a verified or attested
// mark, and nobody refused.
func assessmentPasses(e event.Event) bool {
	var m map[string]any
	_ = json.Unmarshal(e.Content, &m)
	if _, refused := m["refused"]; refused {
		return false
	}

	mark, _ := m["mark"].(string)
	return mark == "verified" || mark == "attested"
}

// How independent a pass is (statefs @864). statefs authenticates the
// identity; the session and the handle are written by the client.
const (
	passNone     = ""         // the holder's own seat: same session or same handle
	passVerified = "verified" // another identity
	passAttested = "attested" // same identity, another session and another handle
)

// passTier is the one rule statefs.ai's Board fold applies too. A pass on
// an earlier claim is none. Attested counts, so seats sharing one machine's
// identity are not blocked, and is shown as not proven.
func passTier(item *WorkItem, p WorkPass) string {
	switch {
	case p.ClaimID != item.ClaimID:
		return passNone
	case p.Identity != item.HoldID:
		return passVerified
	case p.Session != item.HoldSn && p.Participant != item.HoldPt && p.Participant != "":
		return passAttested
	}

	return passNone
}

// independent says a pass came from someone other than the holder.
func independent(item *WorkItem, p WorkPass) bool {
	return passTier(item, p) != passNone
}

// hasPass says the work holds an independent pass, from one of approvers
// when approvers is given.
func hasPass(item *WorkItem, approvers []string) bool {
	for _, p := range item.Passes {
		if !independent(item, p) {
			continue
		}

		if approvers == nil {
			return true
		}

		for _, a := range approvers {
			if strings.EqualFold(strings.TrimSpace(a), p.Identity) {
				return true
			}
		}
	}

	return false
}

// Milestones whose stages need a pass before work moves into them.
const (
	MilestoneApproved = "approved"
	MilestoneReady    = "ready"
)

// moveGate is what a move must satisfy, from the project's workflow: the
// target stage's milestone and who may approve. A zero gate checks nothing:
// with no workflow to read, the board is the authority.
type moveGate struct {
	Milestone string
	Approvers []string
}

// gateRefusal answers why a move into the gate's stage is refused, or "".
func gateRefusal(item *WorkItem, stage string, g moveGate) string {
	switch {
	case g.Milestone == MilestoneReady && !hasPass(item, nil):
		return "a move into " + stage + " (milestone ready) needs a pass from someone other than you: ask another seat to post an assessment (mark verified or attested) replying to your claim"
	case g.Milestone == MilestoneApproved && len(g.Approvers) > 0 && !hasPass(item, g.Approvers):
		return "a move into " + stage + " (milestone approved) needs a pass from an approver (" + strings.Join(g.Approvers, ", ") + "): ask one to post an assessment replying to your claim"
	}

	return ""
}

// setStage records a stage a post named, with its project. A post that
// named none leaves the stage where it was.
func (item *WorkItem) setStage(stage, project string, pos int64) {
	if stage != "" {
		item.Stage, item.StageAt, item.Project = stage, pos, project
	}
}

// setObjective records the objective a request or a claim pointed at. A
// claim that names none keeps the request's.
func (item *WorkItem) setObjective(objective string) {
	if objective != "" {
		item.Objective = objective
	}
}

// postObjective is the objective a request or a claim names, if any.
func postObjective(e event.Event) string {
	var m map[string]any
	_ = json.Unmarshal(e.Content, &m)
	objective, _ := m["objective"].(string)
	return strings.TrimSpace(objective)
}

// move applies a move and answers its effect: "moved: <stage>" or
// "ignored: <why>". A move replies to the claim that holds the work, and
// only that claim's author moves it, as only they close it.
func (l *workLog) move(e event.Event, stage, project string, pos int64) string {
	itemID, onClaim := l.Claims[e.ParentID]
	if !onClaim {
		return "ignored: a move replies to the claim that holds the work"
	}

	item := l.Items[itemID]
	switch {
	case stage == "":
		return "ignored: a move names the stage it moves to"
	case item.ClaimID != e.ParentID || item.State != WorkClaimed:
		return "ignored: that claim no longer holds the work"
	case e.Identity != item.HoldID:
		return "ignored: only " + item.Holder + " can move their claim"
	case item.Project != "" && project != item.Project:
		return "ignored: the work is on project " + item.Project + "'s board"
	case stage == item.Stage:
		return "ignored: the work is already at " + stage
	}

	item.setStage(stage, project, pos)
	return "moved: " + stage
}

// heldOn is the item somebody holds on a normalised subject, or nil.
func (l *workLog) heldOn(key string) *WorkItem {
	if key == "" {
		return nil
	}

	if item := l.Items[l.Subjects[key]]; item != nil && item.State == WorkClaimed {
		return item
	}

	return nil
}

// maxSubject bounds a subject: it names a thing, it is not the description.
const maxSubject = 200

// subjectKey normalises a subject so that "Docs/Ideas.md" and " docs/ideas.md "
// are the same subject. It reduces collisions between agents naming one thing
// two ways; it cannot join "the ideas log" to a path, and free text stays
// best-effort.
func subjectKey(subject string) string {
	return strings.ToLower(strings.Join(strings.Fields(subject), " "))
}

// holderLabel names a holder. speakerOf already includes #session, so we
// do not append it again.
func holderLabel(item *WorkItem) string {
	return item.Holder
}

func sessionTag(session string) string {
	if session == "" {
		return ""
	}

	return " (session " + firstN(session, 8) + ")"
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}

	return s
}

func (l *workLog) add(item *WorkItem) {
	l.Items[item.ID] = item
	l.Order = append(l.Order, item.ID)
}

func (l *workLog) hold(item *WorkItem, e event.Event, pos int64) {
	item.State, item.Holder, item.HoldID, item.HoldSn, item.ClaimID, item.ClaimAt = WorkClaimed, speakerOf(e), e.Identity, e.SessionID, e.ID, pos
	item.HoldPt = e.Participant
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

		// Unprompted work that was handed over and not yet taken again can
		// be ended by whoever started it.
		if item.ID == e.ParentID && item.State == WorkOpen && e.Identity == item.ByID {
			if outcome == OutcomeHandedOver {
				return "ignored: it is already open for someone to claim"
			}

			item.State, item.Outcome = WorkClosed, outcome
			return "closed: " + outcome
		}

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
	if item == nil || item.Kind != event.KindPostRequest {
		return "ignored: a close replies to a claim or a request"
	}

	requester := e.Identity == item.ByID
	holder := item.State == WorkClaimed && e.Identity == item.HoldID
	switch {
	case item.State == WorkClosed:
		return "ignored: already closed"
	case !requester && !holder:
		return "ignored: only the requester or the holder can close the request"
	case outcome == OutcomeHandedOver:
		return "ignored: hand work over by closing the claim, not the request"
	case outcome == OutcomeDropped && !requester:
		return "ignored: only the requester can withdraw the request; to give the work back, close your claim as handed_over or dropped"
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

// postStage is the stage a request, a claim or a move names, if any, and
// the project whose stage it is.
func postStage(e event.Event) (stage, project string) {
	var m map[string]any
	_ = json.Unmarshal(e.Content, &m)
	stage, _ = m["stage"].(string)
	project, _ = m["project"].(string)
	return strings.TrimSpace(stage), strings.TrimSpace(project)
}

// projectID is the shape of a statefs.ai project id.
var projectID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

// maxStage bounds a stage key.
const maxStage = 40

// stageKey is the shape of a stage: the project stage's key (statefs.ai
// project_stages.key, design 21 addendum #261), such as build or review,
// not its display name. The board folds an unknown key into proposed and
// flags it; parley only refuses what cannot be a key at all.
var stageKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// postSubject is the subject an unprompted claim names, if it named one.
func postSubject(e event.Event) string {
	var m map[string]any
	_ = json.Unmarshal(e.Content, &m)
	subject, _ := m["subject"].(string)
	return strings.TrimSpace(subject)
}

// checkWork refuses a work post that would not mean what its author thinks.
// actor is the posting identity. Every refusal says what to do instead.
//
// A second claim on a subject somebody holds is not refused here. Subjects
// are matched as text, so two claims can share one and still be different
// work; the append is advisory. The fold decides which claim holds, and the
// poster is told after the append (see claimOutcome), which is also the only
// place that sees a claim another host's cache had not yet folded.
func checkWork(l *workLog, kind event.Kind, actor, replyTo, outcome, subject, stage, project string, gate moveGate) error {
	if kind != event.KindPostClose && outcome != "" {
		return errors.New("an outcome belongs on a close")
	}

	if stage != "" && kind != event.KindPostRequest && kind != event.KindPostClaim && kind != event.KindPostMove {
		return errors.New("a stage belongs on a request, a claim or a move")
	}

	if len(stage) > maxStage {
		return fmt.Errorf("a stage is the stage's key, such as build or review, at most %d characters", maxStage)
	}

	if stage != "" && !stageKey.MatchString(stage) {
		return fmt.Errorf("a stage is the stage's key, such as build or review: lower case letters, digits, _ and -, not %q", stage)
	}

	if stage != "" && project == "" {
		return errors.New("a stage needs --project <project id>: a channel can be in two projects, and the stage key alone does not say whose board it is on")
	}

	if project != "" && stage == "" {
		return errors.New("a project goes with a stage: add --stage <key>, or leave --project off")
	}

	if project != "" && !projectID.MatchString(project) {
		return fmt.Errorf("a project is the project's id from statefs.ai, not %q", project)
	}

	if subject != "" && (kind != event.KindPostClaim || replyTo != "") {
		return errors.New("a subject names work nobody requested: it belongs on a claim with no reply, which is how you say what you are starting")
	}

	switch kind {
	case event.KindPostClaim:
		if replyTo == "" {
			if len(subject) > maxSubject {
				return fmt.Errorf("a subject names a path, a branch or a PR, at most %d characters: put the description in the text", maxSubject)
			}

			return nil // unprompted work
		}

		item := l.Items[replyTo]
		if item == nil {
			if _, isWorkPost := l.Effects[replyTo]; isWorkPost {
				return errors.New("that is a claim or a close, not open work: reply with a comment to coordinate with whoever holds it")
			}

			return errors.New("only a request, a question or handed-over work can be claimed: to start work nobody requested, post a claim with no reply")
		}

		switch item.State {
		case WorkClaimed:
			return fmt.Errorf("already claimed by %s at @%d: reply with a comment to coordinate, or ask them to close it as handed_over", holderLabel(item), item.ClaimAt)
		case WorkClosed:
			if item.Kind == event.KindPostQuestion {
				if item.Outcome == "answered" {
					return fmt.Errorf("%s already answered that question: reply with a comment if there is more to say", item.Holder)
				}

				return fmt.Errorf("%s took that question and closed it (%s): post it again if it still needs an answer", item.Holder, item.Outcome)
			}

			return fmt.Errorf("that work is closed (%s): post a new request if there is more to do", item.Outcome)
		}
	case event.KindPostMove:
		if replyTo == "" {
			return errors.New("a move replies to your claim: --reply-to <your claim's event id> --stage <next>")
		}

		if effect := l.clone().move(event.Event{ParentID: replyTo, Identity: actor}, stage, project, 0); strings.HasPrefix(effect, "ignored: ") {
			why := strings.TrimPrefix(effect, "ignored: ")
			if e := l.Effects[replyTo]; strings.HasPrefix(e, "lost: ") {
				why = "that claim never held the work (" + strings.TrimPrefix(e, "lost: ") + "), so there is nothing to move"
			}

			return errors.New(why)
		}

		if item := l.Items[l.Claims[replyTo]]; item != nil {
			if why := gateRefusal(item, stage, gate); why != "" {
				return errors.New(why)
			}
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

	if e.Kind == event.KindPostAnswer {
		if item := l.Items[e.ParentID]; item != nil && item.Kind == event.KindPostQuestion {
			return " [answers @" + fmt.Sprint(item.Pos) + "]"
		}
	}

	effect := l.Effects[e.ID]
	switch e.Kind {
	case event.KindPostRequest, event.KindPostQuestion:
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
	case event.KindPostAssessment:
		if effect != "" {
			return " [" + effect + "]"
		}
	case event.KindPostMove:
		if effect != "" {
			return " [move " + effect + "]"
		}
	}

	return ""
}

// WorkMark is what delivery shows beside one request, question or claim
// event, structured for a caller that renders it rather than logs it (the
// console): itemMark's information without the leading space and brackets.
type WorkMark struct {
	Kind    string `json:"kind"`  // request | question | claim
	State   string `json:"state"` // open | claimed | closed
	Holder  string `json:"holder,omitempty"`
	Outcome string `json:"outcome,omitempty"` // answered, resolved, handed_over, dropped, withdrawn
	Stage   string `json:"stage,omitempty"`   // the board stage, when one was named
}

// WorkMarks folds one conversation and answers the mark for every request,
// question and claim event in it, keyed by that event's own id: a request
// or question keys its own item; a claim that took effect keys the item it
// holds, so a reader can look up any of the three kinds by the id on the
// row it is showing. A claim that lost a race has no mark.
func WorkMarks(ctx context.Context, env Env, st store.Store, id string) (map[string]WorkMark, error) {
	l, err := readWork(ctx, env, st, id)
	if err != nil {
		return nil, err
	}

	out := make(map[string]WorkMark, len(l.Items)+len(l.Claims))
	for _, item := range l.Items {
		out[item.ID] = markOf(item)
	}

	for claimID, itemID := range l.Claims {
		if item := l.Items[itemID]; item != nil {
			out[claimID] = markOf(item)
		}
	}

	return out, nil
}

func markOf(item *WorkItem) WorkMark {
	return WorkMark{Kind: strings.TrimPrefix(string(item.Kind), "post."), State: string(item.State), Holder: item.Holder, Outcome: item.Outcome, Stage: item.Stage}
}

func itemMark(item *WorkItem) string {
	if item == nil {
		return ""
	}

	if item.Kind == event.KindPostQuestion {
		switch {
		case item.State == WorkOpen:
			return " [question: unanswered, unclaimed]"
		case item.State == WorkClaimed:
			return fmt.Sprintf(" [question: claimed by %s at @%d]", item.Holder, item.ClaimAt)
		case item.Outcome == "answered":
			return " [question: answered by " + item.Holder + "]"
		default:
			// A claim closed without an answer: taken and let go, not answered.
			return " [question: " + item.Holder + " closed their claim (" + item.Outcome + "), still unanswered]"
		}
	}

	at := ""
	if item.Stage != "" {
		at = ", at " + item.Stage
	}

	switch item.State {
	case WorkOpen:
		return " [work: open, unclaimed" + at + "]"
	case WorkClaimed:
		return fmt.Sprintf(" [work: claimed by %s at @%d%s]", item.Holder, item.ClaimAt, at)
	default:
		return " [work: closed, " + item.Outcome + at + "]"
	}
}

// isWork reports whether a kind is part of the work protocol.
func isWork(k event.Kind) bool {
	return k == event.KindPostRequest || k == event.KindPostClaim || k == event.KindPostClose || k == event.KindPostMove
}

// folded reports whether a kind changes the fold: the work protocol, plus
// questions and the answers that end them.
func folded(k event.Kind) bool {
	return isWork(k) || k == event.KindPostQuestion || k == event.KindPostAnswer || k == event.KindPostAssessment
}

// claimOutcome says, after a claim was appended, whether it holds the work.
func claimOutcome(l *workLog, claimID string) string {
	if itemID, ok := l.Claims[claimID]; ok {
		if item := l.Items[itemID]; item != nil && item.ClaimID == claimID {
			return ""
		}
	}

	if e := l.Effects[claimID]; strings.HasPrefix(e, "lost: ") {
		note := "your claim does not hold: " + strings.TrimPrefix(e, "lost: ") + "; no close is needed"
		if strings.Contains(e, "same subject") {
			note += ". If the work differs, reply with a comment to say how; if it is the same, coordinate with the holder"
		}

		return note
	}

	return ""
}

// ListWork prints the work in followed conversations (or those named): what
// is open, what is claimed and by whom, and what is mine.
// WorkFilter narrows `parley work`: closed work too, one stage, one
// objective, or ("none") the work that points at no objective.
type WorkFilter struct {
	All       bool
	Stage     string
	Objective string
}

// NoObjective is the WorkFilter.Objective that lists unlinked work.
const NoObjective = "none"

func (f WorkFilter) keeps(item *WorkItem) bool {
	switch {
	case !f.All && item.State == WorkClosed:
		return false
	case f.Stage != "" && item.Stage != f.Stage:
		return false
	case f.Objective == NoObjective:
		return item.Objective == ""
	case f.Objective != "" && item.Objective != f.Objective:
		return false
	}

	return true
}

// ListWork prints the work in the conversations followed. A conversation
// whose work names stages is grouped by stage, in the order the stages first
// appear, with work that names none first: the board puts it in the
// project's first stage. Work that points at no objective is flagged.
func ListWork(ctx context.Context, env Env, names []string, f WorkFilter, w io.Writer) error {
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
		staged := false
		for _, id := range l.Order {
			if item := l.Items[id]; f.keeps(item) {
				items = append(items, item)
				staged = staged || item.Stage != ""
			}
		}

		sort.SliceStable(items, func(i, j int) bool { return rank(items[i].State) < rank(items[j].State) })
		if staged {
			items = byStage(items)
		}

		group := "\x00"
		for _, item := range items {
			if staged && item.Stage != group {
				group = item.Stage
				label := item.Stage
				if label == "" {
					label = "no stage (the project's first)"
				}
				fmt.Fprintf(w, "[%s] stage %s\n", s.Name, label)
			}

			flag := ""
			if item.Objective == "" {
				flag = " · no objective"
			}

			fmt.Fprintf(w, "[%s] @%d %s · %s · by %s%s · %s%s\n    %s\n", s.Name, item.Pos, strings.TrimPrefix(string(item.Kind), "post."),
				age(time.UnixMilli(item.AtMs)), item.By, whose(env, item.ByID, item.BySn), describeItem(env, item), flag, firstLine(item.Text, 140))
			shown++
		}
	}

	if failed > 0 {
		return fmt.Errorf("work: %d conversation(s) could not be read", failed)
	}

	if shown == 0 && (f.Stage != "" || f.Objective != "") {
		fmt.Fprintln(w, "no work at that stage or objective in the conversations followed")
	} else if shown == 0 {
		fmt.Fprintln(w, "no open or claimed work in the conversations followed")
	}

	return nil
}

// byStage orders items into stage groups: no stage first, then each stage
// in the order it first appears, keeping the order within a group.
func byStage(items []*WorkItem) []*WorkItem {
	first := map[string]int{"": -1}
	for i, item := range items {
		if _, ok := first[item.Stage]; !ok {
			first[item.Stage] = i
		}
	}

	sort.SliceStable(items, func(i, j int) bool { return first[items[i].Stage] < first[items[j].Stage] })
	return items
}

func describeItem(env Env, item *WorkItem) string {
	at := ""
	if item.Stage != "" {
		at = " · stage " + item.Stage
	}

	return describeState(env, item) + at
}

func describeState(env Env, item *WorkItem) string {
	switch item.State {
	case WorkOpen:
		if item.Outcome != "" {
			return "open again (" + item.Outcome + ")"
		}

		return "open"
	case WorkClaimed:
		if item.ClaimID == item.ID {
			return "in progress" + sessionTag(item.HoldSn) + whose(env, item.HoldID, item.HoldSn)
		}

		return fmt.Sprintf("claimed by %s @%d%s", holderLabel(item), item.ClaimAt, whose(env, item.HoldID, item.HoldSn))
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

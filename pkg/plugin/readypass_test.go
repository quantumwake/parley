package plugin

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/quantumwake/parley/pkg/agentaccess"
)

// stubWorkflow answers wf for every project, or err.
func stubWorkflow(t *testing.T, wf agentaccess.Workflow, err error) {
	t.Helper()
	prev := fetchWorkflow
	fetchWorkflow = func(context.Context, Env, string) (agentaccess.Workflow, error) { return wf, err }
	t.Cleanup(func() { fetchWorkflow = prev })
}

var shipSoftware = agentaccess.Workflow{
	Stages: []agentaccess.WorkflowStage{
		{Key: "idea", Milestone: "proposed"}, {Key: "design", Milestone: "approved"},
		{Key: "build", Milestone: "in_progress"}, {Key: "review", Milestone: "ready"}, {Key: "deploy", Milestone: "done"},
	},
	Approvers: []string{"kasra@example.com"},
}

func handle(t *testing.T, env Env, name string) {
	t.Helper()
	if _, err := SetParticipant(env, name); err != nil {
		t.Fatal(err)
	}
}

func passOn(t *testing.T, env Env, replyTo string, refused bool) {
	t.Helper()
	opts := []PostOption{WithLodestar("", "", "", "", "", "obj1", "it holds", "ran the tests", "verified", "measured", "load", "me", "anyone", "me")}
	if refused {
		opts = append(opts, WithRefused("bob", "carol"))
	}
	post(t, env, "assessment", "it holds", replyTo, opts...)
}

func moveErr(env Env, claim, stage string) error {
	return Post(context.Background(), env, "issues", "move", "moving", "", claim, nil, &bytes.Buffer{}, WithStage(stage, "studio"))
}

// The holder's own pass, a pass from the same session, or from the same
// handle in another session, does not let work into a Ready stage. A pass
// from another seat (another session and handle) or another machine does.
func TestReadyNeedsAPassFromSomeoneElse(t *testing.T) {
	stubWorkflow(t, shipSoftware, nil)
	a, b, o := workSessions(t)
	handle(t, a, "requester")
	handle(t, b, "builder")
	req, _ := post(t, a, "request", "members join", "", WithStage("idea", "studio"))
	claim, _ := post(t, b, "claim", "mine", req, WithStage("build", "studio"))

	if err := moveErr(b, claim, "review"); err == nil || !strings.Contains(err.Error(), "needs a pass from someone other than you") {
		t.Fatalf("no pass: %v", err)
	}

	passOn(t, b, claim, false)
	if err := moveErr(b, claim, "review"); err == nil {
		t.Fatal("the holder passing their own work does not count")
	}

	handle(t, b, "builder-renamed")
	passOn(t, b, claim, false)
	handle(t, b, "builder")
	if err := moveErr(b, claim, "review"); err == nil {
		t.Fatal("the holder's own session under another handle is still the holder")
	}

	restarted := b
	restarted.Session = "bbbbbbbb-9999"
	handle(t, restarted, "builder")
	passOn(t, restarted, claim, false)
	if err := moveErr(b, claim, "review"); err == nil {
		t.Fatal("the same handle in a new session is the same seat restarted")
	}

	passOn(t, a, claim, true)
	if err := moveErr(b, claim, "review"); err == nil {
		t.Fatal("a refused assessment is not a pass")
	}

	passOn(t, a, claim, false)
	if err := moveErr(b, claim, "review"); err != nil {
		t.Fatalf("another seat's pass lets it in: %v", err)
	}
	_ = o
}

// Another machine's identity is someone else, whatever its handle.
func TestAnotherMachinesPassCounts(t *testing.T) {
	stubWorkflow(t, shipSoftware, nil)
	_, b, o := workSessions(t)
	handle(t, b, "builder")
	handle(t, o, "builder")
	claim, _ := post(t, b, "claim", "unprompted", "", WithSubject("x"), WithStage("build", "studio"))
	passOn(t, o, claim, false)
	if err := moveErr(b, claim, "review"); err != nil {
		t.Fatalf("another identity's pass counts: %v", err)
	}
}

// A pass on an earlier claim does not carry over to the next holder.
func TestAPassDoesNotCarryToANewHolder(t *testing.T) {
	stubWorkflow(t, shipSoftware, nil)
	a, b, o := workSessions(t)
	handle(t, a, "requester")
	handle(t, b, "builder")
	req, _ := post(t, a, "request", "members join", "")
	first, _ := post(t, o, "claim", "mine first", req, WithStage("build", "studio"))
	passOn(t, a, first, false)
	post(t, o, "close", "handing over", first, WithOutcome(OutcomeHandedOver))

	second, _ := post(t, b, "claim", "mine now", req, WithStage("build", "studio"))
	if err := moveErr(b, second, "review"); err == nil {
		t.Fatal("the earlier holder's pass does not count for the new one")
	}

	passOn(t, a, first, false)
	if err := moveErr(b, second, "review"); err == nil {
		t.Fatal("a pass replying to the earlier claim, posted now, does not count either")
	}

	passOn(t, a, req, false)
	if err := moveErr(b, second, "review"); err == nil {
		t.Fatal("a pass replying to the request, not the claim, does not count")
	}

	passOn(t, a, second, false)
	if err := moveErr(b, second, "review"); err != nil {
		t.Fatalf("a pass replying to the current claim counts: %v", err)
	}
}

// A pass is an assessment with a verified or attested mark. A reported
// mark is hearsay.
func TestOnlyAVerifiedOrAttestedMarkPasses(t *testing.T) {
	stubWorkflow(t, shipSoftware, nil)
	a, b, _ := workSessions(t)
	handle(t, a, "reviewer")
	handle(t, b, "builder")
	claim, _ := post(t, b, "claim", "unprompted", "", WithSubject("m"), WithStage("build", "studio"))

	post(t, a, "assessment", "heard it works", claim, WithLodestar("", "", "", "", "", "obj1", "heard it works", "someone said", "reported", "reported", "all", "them", "anyone", "reviewer"))
	if err := moveErr(b, claim, "review"); err == nil {
		t.Fatal("a reported mark is not a pass")
	}

	post(t, a, "assessment", "I watched it run", claim, WithLodestar("", "", "", "", "", "obj1", "it runs", "watched it", "attested", "read", "load", "me", "anyone", "reviewer"))
	if err := moveErr(b, claim, "review"); err != nil {
		t.Fatalf("an attested mark passes: %v", err)
	}
}

// Delivery says how independent each pass is: verified for another
// identity, attested (not proven) for another seat on the same identity.
func TestAPassShowsItsTier(t *testing.T) {
	a, b, o := workSessions(t)
	handle(t, a, "reviewer")
	handle(t, b, "builder")
	claim, _ := post(t, b, "claim", "unprompted", "", WithSubject("t"), WithStage("build", "studio"))
	InjectHold(context.Background(), b)
	InjectHold(context.Background(), o)

	passOn(t, a, claim, false)
	passOn(t, o, claim, false)
	text, _ := InjectHold(context.Background(), b)
	for _, want := range []string{"[passes: attested, same identity (reviewer for builder)]", "[passes: verified, another identity]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("delivery shows %q: %s", want, text)
		}
	}

	passOn(t, b, claim, false)
	if text, _ := InjectHold(context.Background(), o); !strings.Contains(text, "[does not pass: the holder's own seat]") {
		t.Fatalf("the holder's own pass is shown as none: %s", text)
	}
}

// Approved needs the pass from an approver; with no approvers listed it is
// words only.
func TestApprovedNeedsAnApprover(t *testing.T) {
	a, b, o := workSessions(t)
	handle(t, a, "requester")
	handle(t, b, "builder")
	claim, _ := post(t, b, "claim", "unprompted", "", WithSubject("y"), WithStage("idea", "studio"))
	passOn(t, a, claim, false)

	stubWorkflow(t, shipSoftware, nil)
	if err := moveErr(b, claim, "design"); err == nil || !strings.Contains(err.Error(), "needs a pass from an approver (kasra@example.com)") {
		t.Fatalf("a pass from a non-approver: %v", err)
	}

	wf := shipSoftware
	wf.Approvers = []string{strings.ToUpper(authorOf(o))}
	stubWorkflow(t, wf, nil)
	if err := moveErr(b, claim, "design"); err == nil {
		t.Fatal("an approver has not passed it yet")
	}
	passOn(t, o, claim, false)
	if err := moveErr(b, claim, "design"); err != nil {
		t.Fatalf("an approver's pass lets it in, matched without case: %v", err)
	}

	wf.Approvers = nil
	stubWorkflow(t, wf, nil)
	claim2, _ := post(t, b, "claim", "another", "", WithSubject("z"), WithStage("idea", "studio"))
	if err := moveErr(b, claim2, "design"); err != nil {
		t.Fatalf("no approvers listed: words only: %v", err)
	}
}

// Without a workflow parley checks nothing: the board is the authority.
// A stage on an In progress milestone needs no pass.
func TestNoWorkflowChecksNothing(t *testing.T) {
	_, b, _ := workSessions(t)
	handle(t, b, "builder")
	claim, _ := post(t, b, "claim", "unprompted", "", WithSubject("w"), WithStage("idea", "studio"))

	stubWorkflow(t, shipSoftware, nil)
	if err := moveErr(b, claim, "build"); err != nil {
		t.Fatalf("in_progress needs no pass: %v", err)
	}

	for _, err := range []error{agentaccess.ErrNoWorkflow, errors.New("statefs.ai is unavailable")} {
		stubWorkflow(t, agentaccess.Workflow{}, err)
		if e := moveErr(b, claim, "review"); e != nil {
			t.Fatalf("workflow %v: the move is not refused here: %v", err, e)
		}
		post(t, b, "move", "back", claim, WithStage("build", "studio"))
	}
}

func TestAPassWithNoHandleIsNotIndependent(t *testing.T) {
	if got := independent(&WorkItem{ClaimID: "c", HoldID: "m", HoldSn: "s1", HoldPt: "builder"}, WorkPass{ClaimID: "c", Identity: "m", Session: "s2", Participant: ""}); got {
		t.Fatal("a pass with no handle cannot be told apart from the holder")
	}
}

// The attested tier compares client-written values: both must be present on
// both sides, and they are trimmed and case-folded (reviewer, #146; #273).
func TestAttestedNeedsRealDifferences(t *testing.T) {
	holder := &WorkItem{ClaimID: "c", HoldID: "m", HoldSn: "s1", HoldPt: "builder"}
	for _, tc := range []struct {
		name string
		item *WorkItem
		pass WorkPass
		want string
	}{
		{"no session on the pass", holder, WorkPass{ClaimID: "c", Identity: "m", Session: "", Participant: "reviewer"}, passNone},
		{"no session on the holder", &WorkItem{ClaimID: "c", HoldID: "m", HoldSn: "", HoldPt: "builder"}, WorkPass{ClaimID: "c", Identity: "m", Session: "s2", Participant: "reviewer"}, passNone},
		{"no handle on the holder", &WorkItem{ClaimID: "c", HoldID: "m", HoldSn: "s1", HoldPt: ""}, WorkPass{ClaimID: "c", Identity: "m", Session: "s2", Participant: "reviewer"}, passNone},
		{"the holder's handle in another case", holder, WorkPass{ClaimID: "c", Identity: "m", Session: "s2", Participant: "Builder"}, passNone},
		{"the holder's handle with spaces", holder, WorkPass{ClaimID: "c", Identity: "m", Session: "s2", Participant: " builder "}, passNone},
		{"the holder's session with spaces", holder, WorkPass{ClaimID: "c", Identity: "m", Session: " s1 ", Participant: "reviewer"}, passNone},
		{"another seat", holder, WorkPass{ClaimID: "c", Identity: "m", Session: "s2", Participant: "reviewer"}, passAttested},
		{"another machine", holder, WorkPass{ClaimID: "c", Identity: "other", Session: "", Participant: ""}, passVerified},
	} {
		if got := passTier(tc.item, tc.pass); got != tc.want {
			t.Fatalf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

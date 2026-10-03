package plugin

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/quantumwake/parley/pkg/agentaccess"
	"github.com/quantumwake/parley/pkg/conversation"
)

func routedWorkflow(agents ...agentaccess.StageAgent) agentaccess.Workflow {
	return agentaccess.Workflow{Stages: []agentaccess.WorkflowStage{
		{Key: "build", Milestone: "in_progress"},
		{Key: "review", Milestone: "ready", Agents: agents},
	}}
}

func lastPost(t *testing.T, env Env) (to string, cc []string) {
	t.Helper()
	for e, err := range conversation.Attach(mustStore(t, env), mustID(t, env, "issues")).Scan(context.Background(), 0, 0) {
		if err == nil && e.IsPost() {
			to, cc = e.To, e.CC
		}
	}
	return to, cc
}

// A request on a stage that names nobody goes to the agents placed on that
// stage, and wakes them; everyone else still sees it.
func TestARequestOnAStageGoesToItsAgents(t *testing.T) {
	a, b, o := workSessions(t)
	handle(t, a, "champion")
	handle(t, b, "reviewer")
	stubWorkflow(t, routedWorkflow(agentaccess.StageAgent{Identity: authorOf(b), Handle: "reviewer"}, agentaccess.StageAgent{Identity: authorOf(o)}), nil)
	InjectHold(context.Background(), b)
	InjectHold(context.Background(), o)

	var out bytes.Buffer
	if err := Post(context.Background(), a, "issues", "request", "review the roster", "", "", nil, &out, WithStage("review", "studio")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "routed to stage review: reviewer, "+authorOf(o)) {
		t.Fatalf("the poster is told who it went to: %q", out.String())
	}
	if to, cc := lastPost(t, a); to != "reviewer" || len(cc) != 1 || cc[0] != authorOf(o) {
		t.Fatalf("addressed to %q cc %v", to, cc)
	}
	if _, hold := InjectHold(context.Background(), b); !hold {
		t.Fatal("the stage's handle is woken")
	}
	if _, hold := InjectHold(context.Background(), o); !hold {
		t.Fatal("the stage's identity is woken")
	}
}

// The poster is not routed to themselves, an explicit --to or an @ wins,
// and only requests are routed.
func TestRoutingLeavesOtherPostsAlone(t *testing.T) {
	a, _, _ := workSessions(t)
	handle(t, a, "reviewer")
	stubWorkflow(t, routedWorkflow(agentaccess.StageAgent{Identity: authorOf(a), Handle: "reviewer"}, agentaccess.StageAgent{Identity: "mac-2", Handle: "Reviewer"}, agentaccess.StageAgent{Identity: "mac-3"}), nil)

	var out bytes.Buffer
	if err := Post(context.Background(), a, "issues", "request", "x", "", "", nil, &out, WithStage("review", "studio")); err != nil {
		t.Fatal(err)
	}
	if to, cc := lastPost(t, a); to != "mac-3" || len(cc) != 0 {
		t.Fatalf("the poster's own handle, in any case, is left out: %q %v", to, cc)
	}

	if err := Post(context.Background(), a, "issues", "request", "x", "champion", "", nil, &out, WithStage("review", "studio")); err != nil {
		t.Fatal(err)
	}
	if to, _ := lastPost(t, a); to != "champion" {
		t.Fatalf("an explicit --to wins: %q", to)
	}

	if err := Post(context.Background(), a, "issues", "request", "@closer please", "", "", nil, &out, WithStage("review", "studio")); err != nil {
		t.Fatal(err)
	}
	if to, _ := lastPost(t, a); to != "closer" {
		t.Fatalf("an @ in the text wins: %q", to)
	}

	claim, _ := post(t, a, "claim", "mine", "", WithSubject("s"), WithStage("review", "studio"))
	_ = claim
	if to, _ := lastPost(t, a); to != "" {
		t.Fatalf("a claim is not routed: %q", to)
	}
}

// No workflow, an unknown stage, or a down API posts it unaddressed.
func TestRoutingWithoutAWorkflowPostsUnaddressed(t *testing.T) {
	a, _, _ := workSessions(t)
	for _, tc := range []struct {
		wf    agentaccess.Workflow
		err   error
		stage string
	}{
		{agentaccess.Workflow{}, agentaccess.ErrNoWorkflow, "review"},
		{agentaccess.Workflow{}, errors.New("statefs.ai is unavailable"), "review"},
		{routedWorkflow(agentaccess.StageAgent{Identity: "mac-3"}), nil, "deploy"},
	} {
		stubWorkflow(t, tc.wf, tc.err)
		var out bytes.Buffer
		if err := Post(context.Background(), a, "issues", "request", "x", "", "", nil, &out, WithStage(tc.stage, "studio")); err != nil {
			t.Fatal(err)
		}
		if to, _ := lastPost(t, a); to != "" || strings.Contains(out.String(), "routed") {
			t.Fatalf("%v: posted to %q: %s", tc.err, to, out.String())
		}
	}
}

// The poster is left out by identity as well as by handle; the same seat
// placed twice is addressed once; and a post that already copies someone
// is not rerouted (reviewer, #149).
func TestRoutingSkipsThePosterDedupsAndKeepsAnExplicitCC(t *testing.T) {
	a, _, _ := workSessions(t)
	handle(t, a, "champion")
	stubWorkflow(t, routedWorkflow(
		agentaccess.StageAgent{Identity: authorOf(a)},
		agentaccess.StageAgent{Identity: "mac-2", Handle: "reviewer"},
		agentaccess.StageAgent{Identity: "mac-3", Handle: "Reviewer"},
		agentaccess.StageAgent{Identity: "mac-4"},
	), nil)

	var out bytes.Buffer
	if err := Post(context.Background(), a, "issues", "request", "x", "", "", nil, &out, WithStage("review", "studio")); err != nil {
		t.Fatal(err)
	}
	to, cc := lastPost(t, a)
	if to != "reviewer" || len(cc) != 1 || cc[0] != "mac-4" {
		t.Fatalf("the poster's own identity is left out and one handle is addressed once: to %q cc %v", to, cc)
	}

	if err := Post(context.Background(), a, "issues", "request", "x @closer and @security", "", "", nil, &out, WithStage("review", "studio")); err != nil {
		t.Fatal(err)
	}
	if to, cc := lastPost(t, a); to != "closer" || len(cc) != 1 || cc[0] != "security" {
		t.Fatalf("named recipients are kept as named: to %q cc %v", to, cc)
	}
}

// Routing reads only To: whoever a post names, the first is To and the rest
// CC, so a post with a CC always has a To.
func TestANamedPostAlwaysHasATo(t *testing.T) {
	for _, tc := range []struct{ explicit, text string }{
		{"", "@closer and @security"},
		{"", "cc @security only"},
		{"champion", "and @security"},
	} {
		to, cc := SplitRecipients([]string{tc.explicit}, tc.text)
		if to == "" && len(cc) > 0 {
			t.Fatalf("%q %q: cc %v with no to", tc.explicit, tc.text, cc)
		}
	}
}

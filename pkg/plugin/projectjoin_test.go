package plugin

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quantumwake/parley/pkg/agentaccess"
)

// projectSeat is a session named reviewer on a machine where another
// identity created the channels "studio" and "studio-design"; this session
// follows neither.
func projectSeat(t *testing.T) (seat Env, main, extra string) {
	t.Helper()
	s, other := sessions(t, "aaaaaaaa-1111")
	seat = s["aaaaaaaa-1111"]
	var sink bytes.Buffer
	for _, name := range []string{"studio", "studio-design"} {
		if err := CreateShared(context.Background(), other, name, "", nil, &sink); err != nil {
			t.Fatal(err)
		}
	}
	stubPersona(t, "reviewer", func(context.Context, Env, string) (agentaccess.Persona, error) {
		return agentaccess.Persona{}, agentaccess.ErrNoPersona
	})
	stubWorkflow(t, agentaccess.Workflow{}, agentaccess.ErrNoWorkflow)
	st := mustStore(t, seat)
	for _, name := range []string{"studio", "studio-design"} {
		id, err := resolveShared(context.Background(), seat, st, name)
		if err != nil {
			t.Fatal(err)
		}
		if name == "studio" {
			main = id
		} else {
			extra = id
		}
	}
	return seat, main, extra
}

func follows(env Env, id string) bool {
	for _, s := range Subscriptions(env) {
		if s.ID == id {
			return true
		}
	}
	return false
}

// A seat in a project comes up following the project's channels and armed,
// and is told to start its wait.
func TestASeatJoinsItsProjectChannelsAndArms(t *testing.T) {
	seat, main, extra := projectSeat(t)
	stubProjects(t, func(context.Context, Env, string) ([]agentaccess.AgentProject, error) {
		return []agentaccess.AgentProject{{ID: "p1", Name: "Studio", Channel: main, Channels: []string{extra}}}, nil
	})

	got := workflowContext(context.Background(), seat)
	if !follows(seat, main) || !follows(seat, extra) {
		t.Fatalf("the main and the extra channel are followed: %+v", Subscriptions(seat))
	}
	if state, _, _ := ArmStatus(seat); state != "armed" {
		t.Fatalf("the seat is armed: %q", state)
	}
	if !strings.Contains(got, "Joined this session to its project channels") || !strings.Contains(got, "wait -timeout") {
		t.Fatalf("the agent is told to listen: %s", got)
	}
	log, _ := os.ReadFile(filepath.Join(seat.DataDir, "hooks.log"))
	if strings.Count(string(log), "project p1: joined") != 2 {
		t.Fatalf("each join is logged:\n%s", log)
	}

	again := workflowContext(context.Background(), seat)
	if strings.Contains(again, "Joined") {
		t.Fatalf("a channel already followed is not joined again: %s", again)
	}
}

// A channel this session left by hand stays left.
func TestALeftProjectChannelStaysLeft(t *testing.T) {
	seat, main, _ := projectSeat(t)
	stubProjects(t, func(context.Context, Env, string) ([]agentaccess.AgentProject, error) {
		return []agentaccess.AgentProject{{ID: "p1", Channel: main}}, nil
	})
	if err := Join(context.Background(), seat, "studio", "full", "all", "", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := Leave(seat, "studio", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	got := workflowContext(context.Background(), seat)
	if follows(seat, main) || strings.Contains(got, "Joined") {
		t.Fatalf("a channel left by hand is not joined again: %s", got)
	}
}

// A channel the identity cannot read is skipped and logged, and nothing is
// armed for it.
func TestAProjectChannelThatCannotBeReadIsSkipped(t *testing.T) {
	seat, _, _ := projectSeat(t)
	missing := "00000000-0000-0000-0000-000000000000"
	stubProjects(t, func(context.Context, Env, string) ([]agentaccess.AgentProject, error) {
		return []agentaccess.AgentProject{{ID: "p1", Channel: missing}}, nil
	})

	got := workflowContext(context.Background(), seat)
	if follows(seat, missing) || strings.Contains(got, "Joined") {
		t.Fatalf("an unreadable channel is not joined: %s", got)
	}
	if state, _, _ := ArmStatus(seat); state == "armed" {
		t.Fatal("nothing joined, nothing armed")
	}
	log, _ := os.ReadFile(filepath.Join(seat.DataDir, "hooks.log"))
	if !strings.Contains(string(log), "project p1: "+missing) {
		t.Fatalf("the skip is logged:\n%s", log)
	}
}

// A disarmed seat still follows its project, but stays disarmed.
func TestADisarmedSeatJoinsButStaysQuiet(t *testing.T) {
	seat, main, _ := projectSeat(t)
	if err := Disarm(seat); err != nil {
		t.Fatal(err)
	}
	stubProjects(t, func(context.Context, Env, string) ([]agentaccess.AgentProject, error) {
		return []agentaccess.AgentProject{{ID: "p1", Channel: main}}, nil
	})

	_ = workflowContext(context.Background(), seat)
	if !follows(seat, main) {
		t.Fatal("the channel is followed")
	}
	if state, _, _ := ArmStatus(seat); state != "disarmed" {
		t.Fatalf("a disarmed seat stays disarmed: %q", state)
	}
}

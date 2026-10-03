package plugin

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
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

// Without a session there is no seat: joining would follow the channel for
// the whole machine. Nothing is joined (reviewer, #150).
func TestNoSessionJoinsNothing(t *testing.T) {
	seat, main, _ := projectSeat(t)
	machine := seat
	machine.Session = ""
	projects := []agentaccess.AgentProject{{ID: "p1", Channel: main}}
	if got := joinProjectChannels(context.Background(), machine, projects); len(got) != 0 {
		t.Fatalf("joined %v", got)
	}
	if follows(machine, main) || follows(seat, main) {
		t.Fatal("nothing is followed, for the machine or the seat")
	}
}

// Only a namespace id is joined. A name, even one of a real channel, is
// skipped and never looked up (reviewer, #150).
func TestAProjectChannelNameIsNeverResolved(t *testing.T) {
	seat, main, _ := projectSeat(t)
	projects := []agentaccess.AgentProject{{ID: "p1", Channel: "studio", Channels: []string{"studio-design"}}}
	if got := joinProjectChannels(context.Background(), seat, projects); len(got) != 0 {
		t.Fatalf("joined by name: %v", got)
	}
	if follows(seat, main) {
		t.Fatal("the channel named studio is not followed")
	}
}

// One start joins at most maxStartJoins channels; the rest are logged.
func TestAStartJoinsAtMostAFewChannels(t *testing.T) {
	seat, _, _ := projectSeat(t)
	var ids []string
	st := mustStore(t, seat)
	for i := 0; i < maxStartJoins+2; i++ {
		name := "chan-" + strconv.Itoa(i)
		var sink bytes.Buffer
		if err := CreateShared(context.Background(), seat, name, "", nil, &sink); err != nil {
			t.Fatal(err)
		}
		id, err := resolveShared(context.Background(), seat, st, name)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// CreateShared follows for the creator; leave the machine record so the
	// seat starts following none of them.
	for i := range ids {
		_ = Leave(Env{DataDir: seat.DataDir, IdentityPath: seat.IdentityPath}, "chan-"+strconv.Itoa(i), &bytes.Buffer{})
	}

	got := joinProjectChannels(context.Background(), seat, []agentaccess.AgentProject{{ID: "p1", Channels: ids}})
	if len(got) != maxStartJoins {
		t.Fatalf("joined %d, want %d", len(got), maxStartJoins)
	}
	log, _ := os.ReadFile(filepath.Join(seat.DataDir, "hooks.log"))
	if strings.Count(string(log), "not joined, this start already joined") != 2 {
		t.Fatalf("the rest are logged:\n%s", log)
	}
}

package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quantumwake/parley/pkg/agentaccess"
	"github.com/quantumwake/parley/pkg/enroll"
)

func stubProjects(t *testing.T, fetch func(context.Context, Env, string) ([]agentaccess.AgentProject, error)) {
	t.Helper()
	prev := fetchProjects
	fetchProjects = fetch
	t.Cleanup(func() { fetchProjects = prev })
}

// enrolledSeat is an enrolled machine whose seat is named reviewer, with no
// persona, so a session start reads only the workflow.
func enrolledSeat(t *testing.T) Env {
	t.Helper()
	dir := enroll.NewFakeDirectory()
	t.Cleanup(dir.Close)
	tmp := t.TempDir()
	env := Env{EnrollURL: dir.EnrollURL("laptop-agent"), IdentityPath: filepath.Join(tmp, "identity"), DataDir: tmp, Directory: dir.URL()}
	stubPersona(t, "reviewer", func(context.Context, Env, string) (agentaccess.Persona, error) {
		return agentaccess.Persona{}, agentaccess.ErrNoPersona
	})
	_ = run(t, env, map[string]any{"hook_event_name": "SessionStart", "session_id": "s0"})
	env.EnrollURL = ""
	return env
}

var studioWorkflow = agentaccess.Workflow{
	Stages: []agentaccess.WorkflowStage{
		{Key: "build", Milestone: "in_progress", Checks: []string{"tests pass"}},
		{Key: "review", Milestone: "ready", Checks: []string{"passed at the merging commit"}},
		{Key: "deploy", Milestone: "done", Checks: []string{"healthz 200"}},
	},
	Objectives: []agentaccess.WorkflowObjective{{Objective: "01OBJ", Goal: "10 organizations on Cloud", Namespace: "ns1"}},
}

// Session start tells the seat its projects: the stages in order, which are
// its own and their checks, the objectives to point work at, and the
// --project to post with. A prompt does not read it again.
func TestSessionStartInjectsTheSeatsWorkflow(t *testing.T) {
	env := enrolledSeat(t)
	var sessions []string
	stubProjects(t, func(_ context.Context, _ Env, session string) ([]agentaccess.AgentProject, error) {
		sessions = append(sessions, session)
		return []agentaccess.AgentProject{{ID: "p1", Name: "Studio", Channel: "ns1", Stages: []string{"review"}}}, nil
	})
	var projects []string
	stubWorkflow(t, studioWorkflow, nil)
	prev := fetchWorkflow
	fetchWorkflow = func(ctx context.Context, e Env, p string) (agentaccess.Workflow, error) {
		projects = append(projects, p)
		return prev(ctx, e, p)
	}

	o := run(t, env, map[string]any{"hook_event_name": "SessionStart", "session_id": "s1"})
	for _, want := range []string{
		"Workflow:", `Project "Studio" (--project p1)`, "build (in_progress)", "review (ready, yours) checks: passed at the merging commit",
		"deploy (done)", "10 organizations on Cloud (--objective 01OBJ)", "into approved, from an approver",
	} {
		if !strings.Contains(o.AdditionalContext, want) {
			t.Fatalf("context lacks %q: %s", want, o.AdditionalContext)
		}
	}
	if strings.Contains(o.AdditionalContext, "tests pass") {
		t.Fatalf("only the seat's own stages show their checks: %s", o.AdditionalContext)
	}
	if len(sessions) != 1 || sessions[0] != "reviewer" || len(projects) != 1 || projects[0] != "p1" {
		t.Fatalf("one read, by seat name and project id: %v %v", sessions, projects)
	}

	o = run(t, env, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s1", "prompt": "hi"})
	if strings.Contains(o.AdditionalContext, "Workflow:") || len(sessions) != 1 {
		t.Fatalf("a prompt does not read the workflow again: %s", o.AdditionalContext)
	}
}

// No projects, no workflow, or a down API adds nothing and the session
// still starts; only a real failure is logged.
func TestSessionStartWithNoWorkflowStillStarts(t *testing.T) {
	env := enrolledSeat(t)
	for _, tc := range []struct {
		name     string
		projects error
		workflow error
		logged   bool
	}{
		{"no projects", agentaccess.ErrNoProjects, nil, false},
		{"no workflow", nil, agentaccess.ErrNoWorkflow, false},
		{"projects down", errors.New("statefs.ai is unavailable: projects"), nil, true},
	} {
		stubProjects(t, func(context.Context, Env, string) ([]agentaccess.AgentProject, error) {
			if tc.projects != nil {
				return nil, tc.projects
			}
			return []agentaccess.AgentProject{{ID: "p1", Name: "Studio"}}, nil
		})
		stubWorkflow(t, studioWorkflow, tc.workflow)
		_ = os.Remove(filepath.Join(env.DataDir, "hooks.log"))
		o := run(t, env, map[string]any{"hook_event_name": "SessionStart", "session_id": "s-" + tc.name})
		if strings.Contains(o.AdditionalContext, "Workflow:") || !strings.Contains(o.AdditionalContext, "enrolled") {
			t.Fatalf("%s: %s", tc.name, o.AdditionalContext)
		}
		log, _ := os.ReadFile(filepath.Join(env.DataDir, "hooks.log"))
		if got := strings.Contains(string(log), `"what":"workflow"`); got != tc.logged {
			t.Fatalf("%s: logged %v:\n%s", tc.name, got, log)
		}
	}
}

// Many projects are cut short rather than filling the start.
func TestSessionStartDescribesAtMostAFewProjects(t *testing.T) {
	env := enrolledSeat(t)
	var many []agentaccess.AgentProject
	for i := 0; i < maxStartProjects+3; i++ {
		many = append(many, agentaccess.AgentProject{ID: "p" + string(rune('a'+i)), Name: "P"})
	}
	stubProjects(t, func(context.Context, Env, string) ([]agentaccess.AgentProject, error) { return many, nil })
	stubWorkflow(t, studioWorkflow, nil)
	o := run(t, env, map[string]any{"hook_event_name": "SessionStart", "session_id": "s1"})
	if strings.Count(o.AdditionalContext, "--project p") != maxStartProjects || !strings.Contains(o.AdditionalContext, "(and 3 more projects)") {
		t.Fatalf("context: %s", o.AdditionalContext)
	}
}

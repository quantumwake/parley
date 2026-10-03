package plugin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/quantumwake/parley/pkg/agentaccess"
	"github.com/quantumwake/statefs/pkg/identityfile"
)

// fetchWorkflow is the API read of a project's workflow. Tests replace it.
var fetchWorkflow = fetchWorkflowFromAPI

// workflowTimeout bounds the read a move makes before it is posted.
var workflowTimeout = 3 * time.Second

// agentClient signs this machine's identity in to statefs.ai's agent
// routes. The caller's context bounds every call; the client's own
// timeout is a backstop.
func agentClient(env Env) (*agentaccess.Client, error) {
	path := env.IdentityPath
	if path == "" {
		path = identityfile.DefaultPath()
	}
	f, err := identityfile.Read(path)
	if err != nil {
		return nil, err
	}
	key, err := f.Private()
	if err != nil {
		return nil, err
	}
	base := env.StatefsAI
	if base == "" {
		base = agentaccess.Base()
	}
	return &agentaccess.Client{
		Base: base, Username: f.Username, Key: key,
		HTTP:      &http.Client{Timeout: 10 * time.Second},
		UserAgent: UserAgent(),
		CacheDir:  env.DataDir,
	}, nil
}

func fetchWorkflowFromAPI(ctx context.Context, env Env, project string) (agentaccess.Workflow, error) {
	c, err := agentClient(env)
	if err != nil {
		return agentaccess.Workflow{}, err
	}
	return c.Workflow(ctx, project)
}

// fetchProjects is the API read of the projects this seat is in. Tests
// replace it.
var fetchProjects = func(ctx context.Context, env Env, session string) ([]agentaccess.AgentProject, error) {
	c, err := agentClient(env)
	if err != nil {
		return nil, err
	}
	return c.Projects(ctx, session)
}

// Session start describes at most this much of what project owners wrote:
// it goes into every placed seat's context.
const (
	maxStartProjects   = 5
	maxStartStages     = 12
	maxStartObjectives = 8
	maxStartChecks     = 4
	maxStartField      = 80 // a key, a milestone, a channel, a check, a name
	maxStartGoal       = 120
)

// workflowContext is, for each project this seat is in, the stages it works
// with their milestone and checks, and the project's objectives: what the
// agent needs to post work onto the board. Read once at session start; no
// projects, a refused sign-in or a down API adds nothing.
func workflowContext(ctx context.Context, env Env) string {
	session := seatSession(env)
	if session == "" {
		return ""
	}
	projects, err := fetchProjects(ctx, env, session)
	if err != nil {
		if !errors.Is(err, agentaccess.ErrNoProjects) {
			logLine(env, "workflow", err.Error())
		}
		return ""
	}

	joined := joinProjectChannels(ctx, env, projects)

	var b strings.Builder
	for i, p := range projects {
		if i == maxStartProjects {
			fmt.Fprintf(&b, "(and %d more projects)\n", len(projects)-i)
			break
		}
		wf, err := fetchWorkflow(ctx, env, p.ID)
		if err != nil {
			if !errors.Is(err, agentaccess.ErrNoWorkflow) {
				logLine(env, "workflow", err.Error())
			}
			continue
		}
		b.WriteString(formatWorkflow(p, wf))
	}
	if len(joined) > 0 {
		fmt.Fprintf(&b, "Joined this session to its project channels: %s.", strings.Join(joined, ", "))
		if !WaitLive(env) {
			fmt.Fprintf(&b, " Start listening on them now: run `%s` as a tracked background task%s.", WaitCommandFor("parley", OnClaude()), bashTimeout(OnClaude()))
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return "\nWorkflow:\n" + b.String()
}

func formatWorkflow(p agentaccess.AgentProject, wf agentaccess.Workflow) string {
	field := func(s string) string { return firstLine(s, maxStartField) }
	name := field(p.Name)
	if name == "" {
		name = field(p.ID)
	}
	placed := map[string]bool{}
	for _, k := range p.Stages {
		placed[k] = true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Project %s (--project %s).", strconv.Quote(name), field(p.ID))
	if c := field(p.Channel); c != "" {
		fmt.Fprintf(&b, " Channel %s.", c)
	}
	b.WriteString(" Stages, in order:")
	for i, s := range wf.Stages {
		if i == maxStartStages {
			fmt.Fprintf(&b, " (and %d more)", len(wf.Stages)-i)
			break
		}
		mine := ""
		if placed[s.Key] {
			mine = ", yours"
		}
		fmt.Fprintf(&b, " %s (%s%s)", field(s.Key), field(s.Milestone), mine)
		if len(s.Checks) > 0 && placed[s.Key] {
			var checks []string
			for j, c := range s.Checks {
				if j == maxStartChecks {
					checks = append(checks, fmt.Sprintf("and %d more", len(s.Checks)-j))
					break
				}
				checks = append(checks, field(c))
			}
			fmt.Fprintf(&b, " checks: %s;", strings.Join(checks, "; "))
		}
	}
	b.WriteString(".")
	if len(wf.Objectives) > 0 {
		b.WriteString(" Objectives:")
		for i, o := range wf.Objectives {
			if i == maxStartObjectives {
				fmt.Fprintf(&b, " (and %d more);", len(wf.Objectives)-i)
				break
			}
			fmt.Fprintf(&b, " %s (--objective %s);", firstLine(o.Goal, maxStartGoal), field(o.EventID()))
		}
	}
	b.WriteString(" A move into a ready stage needs an assessment on your claim from another seat; into approved, from an approver.\n")
	return b.String()
}

// workflowGate is what a move into stage must satisfy, read from the
// project's workflow. No workflow, an unknown stage or an unreachable API
// checks nothing here: the board refuses the move server-side.
func workflowGate(ctx context.Context, env Env, project, stage string) moveGate {
	ctx, cancel := context.WithTimeout(ctx, workflowTimeout)
	defer cancel()
	wf, err := fetchWorkflow(ctx, env, project)
	if err != nil {
		if !errors.Is(err, agentaccess.ErrNoWorkflow) {
			logLine(env, "workflow", err.Error())
		}
		return moveGate{}
	}

	for _, s := range wf.Stages {
		if s.Key == stage {
			return moveGate{Milestone: strings.TrimSpace(s.Milestone), Approvers: wf.Approvers}
		}
	}

	return moveGate{}
}

// stageRecipients are the agents a request on stage is routed to: the
// seats placed on that stage in the project's workflow, minus the poster.
// No workflow, an unknown stage or a down API routes nowhere; the post is
// still posted, unaddressed.
func stageRecipients(ctx context.Context, env Env, project, stage, me, myHandle string) []string {
	ctx, cancel := context.WithTimeout(ctx, workflowTimeout)
	defer cancel()
	wf, err := fetchWorkflow(ctx, env, project)
	if err != nil {
		if !errors.Is(err, agentaccess.ErrNoWorkflow) {
			logLine(env, "workflow", err.Error())
		}
		return nil
	}

	var out []string
	seen := map[string]bool{}
	for _, s := range wf.Stages {
		if s.Key != stage {
			continue
		}
		for _, a := range s.Agents {
			addr := a.Address()
			key := strings.ToLower(addr)
			if addr == "" || seen[key] || strings.EqualFold(addr, me) || (myHandle != "" && strings.EqualFold(addr, myHandle)) {
				continue
			}
			seen[key] = true
			out = append(out, addr)
		}
	}
	return out
}

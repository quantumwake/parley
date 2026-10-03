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

// maxStartProjects bounds how many projects session start describes.
const maxStartProjects = 5

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
	if b.Len() == 0 {
		return ""
	}
	return "\nWorkflow:\n" + b.String()
}

func formatWorkflow(p agentaccess.AgentProject, wf agentaccess.Workflow) string {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = p.ID
	}
	placed := map[string]bool{}
	for _, k := range p.Stages {
		placed[k] = true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Project %s (--project %s).", strconv.Quote(name), p.ID)
	if p.Channel != "" {
		fmt.Fprintf(&b, " Channel %s.", p.Channel)
	}
	b.WriteString(" Stages, in order:")
	for _, s := range wf.Stages {
		mine := ""
		if placed[s.Key] {
			mine = ", yours"
		}
		fmt.Fprintf(&b, " %s (%s%s)", s.Key, s.Milestone, mine)
		if len(s.Checks) > 0 && placed[s.Key] {
			fmt.Fprintf(&b, " checks: %s;", strings.Join(s.Checks, "; "))
		}
	}
	b.WriteString(".")
	if len(wf.Objectives) > 0 {
		b.WriteString(" Objectives:")
		for _, o := range wf.Objectives {
			fmt.Fprintf(&b, " %s (--objective %s);", firstLine(o.Goal, 120), o.ID)
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

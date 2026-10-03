package plugin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/quantumwake/parley/pkg/agentaccess"
	"github.com/quantumwake/statefs/pkg/identityfile"
)

// fetchWorkflow is the API read of a project's workflow. Tests replace it.
var fetchWorkflow = fetchWorkflowFromAPI

// workflowTimeout bounds the read a move makes before it is posted.
var workflowTimeout = 3 * time.Second

func fetchWorkflowFromAPI(ctx context.Context, env Env, project string) (agentaccess.Workflow, error) {
	path := env.IdentityPath
	if path == "" {
		path = identityfile.DefaultPath()
	}
	f, err := identityfile.Read(path)
	if err != nil {
		return agentaccess.Workflow{}, err
	}
	key, err := f.Private()
	if err != nil {
		return agentaccess.Workflow{}, err
	}
	base := env.StatefsAI
	if base == "" {
		base = agentaccess.Base()
	}
	c := &agentaccess.Client{
		Base: base, Username: f.Username, Key: key,
		HTTP:      &http.Client{Timeout: 10 * time.Second},
		UserAgent: UserAgent(),
		CacheDir:  env.DataDir,
	}
	return c.Workflow(ctx, project)
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

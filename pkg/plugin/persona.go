package plugin

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/quantumwake/parley/pkg/agentaccess"
	"github.com/quantumwake/statefs/pkg/identityfile"
)

// tmuxSessionName is the Cloud seat when this process is inside a tmux
// session. Tests replace it. A failure is no name, not a failed start.
var tmuxSessionName = readTmuxSession

func readTmuxSession() string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#S").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// seatSession is the name GET /api/v1/agent/persona keys on. On Cloud that
// is the tmux session. Elsewhere it is the handle chosen at setup.
func seatSession(env Env) string {
	if name := strings.TrimSpace(tmuxSessionName()); name != "" && ValidHandle(name) == nil {
		return name
	}
	return Participant(env)
}

// fetchPersona is the API read. Tests replace it.
var fetchPersona = fetchPersonaFromAPI

func fetchPersonaFromAPI(ctx context.Context, env Env, session string) (agentaccess.Persona, error) {
	path := env.IdentityPath
	if path == "" {
		path = identityfile.DefaultPath()
	}
	f, err := identityfile.Read(path)
	if err != nil {
		return agentaccess.Persona{}, err
	}
	key, err := f.Private()
	if err != nil {
		return agentaccess.Persona{}, err
	}
	base := env.StatefsAI
	if base == "" {
		base = agentaccess.Base()
	}
	c := &agentaccess.Client{
		Base: base, Username: f.Username, Key: key,
		HTTP: &http.Client{Timeout: 2 * time.Second}, UserAgent: UserAgent(),
		CacheDir: env.DataDir,
	}
	return c.Persona(ctx, session)
}

// personaContext is the persona's instructions, or "" when this seat has
// none or the API cannot answer. A failure is one log line.
func personaContext(ctx context.Context, env Env) string {
	session := seatSession(env)
	if session == "" {
		return ""
	}
	p, err := fetchPersona(ctx, env, session)
	if err != nil {
		if !errors.Is(err, agentaccess.ErrNoPersona) {
			logLine(env, "persona", err.Error())
		}
		return ""
	}
	return formatPersona(p)
}

func formatPersona(p agentaccess.Persona) string {
	text := strings.TrimSpace(p.Instructions)
	if text == "" {
		return ""
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return "\nPersona:\n" + text + "\n"
	}
	return "\nPersona " + strconv.Quote(name) + ":\n" + text + "\n"
}

package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/quantumwake/parley/pkg/agentaccess"
	"github.com/quantumwake/statefs/pkg/identityfile"
)

// Presence pings are display-only. They must never sit on wait's path:
// a down statefs.ai must not change wait's exit, and must not slow a
// delivery. Owner: "parley should not break if it cannot communicate
// with the presence service.. nor should it block."

var (
	presenceMu   sync.Mutex
	presenceNext time.Time
	presenceGap  = 5 * time.Second

	// presenceSend is the ping. Tests replace it with a call that never
	// returns, which must not stall Wait.
	presenceSend = sendPresence
)

func sendPresence(ctx context.Context, env Env, state string) error {
	path := env.IdentityPath
	if path == "" {
		path = identityfile.DefaultPath()
	}
	f, err := identityfile.Read(path)
	if err != nil {
		return err
	}
	key, err := f.Private()
	if err != nil {
		return err
	}
	base := env.StatefsAI
	if base == "" {
		base = agentaccess.Base()
	}
	// The API matches a ping against the seat's grants by the statefs
	// namespace id, which is what parley scans by; a conversation's display
	// name matches nothing, and a ping of names is answered 200 with every
	// namespace skipped, so no agent seat ever reaches the presence store.
	var namespaces []string
	// The ping is per session, so the session's own handle names its chip
	// (participant.go). A handle declared for one conversation at join
	// time is a fallback for a seat that never chose a session-wide one.
	participant := Participant(env)
	for _, s := range Subscriptions(env) {
		if s.ID != "" {
			namespaces = append(namespaces, s.ID)
		}

		if participant == "" {
			participant = s.Participant
		}
	}
	// Every ping is its own process, so the token has to outlive it: the
	// cache under the data dir is why a ping costs one call rather than two.
	c := &agentaccess.Client{
		Base: base, Username: f.Username, Key: key,
		HTTP: &http.Client{Timeout: 2 * time.Second}, UserAgent: UserAgent(),
		CacheDir: env.DataDir,
	}
	return c.Presence(ctx, env.Session, state, participant, namespaces)
}

func touchPresence(env Env, state string) {
	presenceMu.Lock()
	if time.Now().Before(presenceNext) {
		presenceMu.Unlock()
		return
	}
	send := presenceSend
	presenceMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := send(ctx, env, state)
		presenceMu.Lock()
		defer presenceMu.Unlock()
		if err != nil {
			if presenceGap < 60*time.Second {
				presenceGap *= 2
			}
			if presenceGap > 60*time.Second {
				presenceGap = 60 * time.Second
			}
			presenceNext = time.Now().Add(presenceGap)
			return
		}
		presenceGap = 5 * time.Second
		presenceNext = time.Now().Add(5 * time.Second)
	}()
}

// Hook states. A hook is a short-lived process, so a goroutine ping would die
// with it: the hook records the session's state in a file and hands the ping
// to a detached `parley presence` process, so no hook ever waits on the
// network. The file also lets a background wait, which pings every few
// seconds, carry the hook's fresh busy state instead of overwriting it with
// "listening", and keeps a long "thinking" from expiring on the server.

const (
	// hookStateFresh is how long a busy state recorded by a hook stays the
	// session's state without another hook. Past it the session is idle.
	hookStateFresh = 90 * time.Second
	// hookResend is how often an unchanged state is sent again from hooks.
	hookResend = 10 * time.Second
	// hookBackoff is how long hooks stop sending after a failed ping.
	hookBackoff = 60 * time.Second
)

// hookStates maps a hook event to the presence state it reports. An event not
// listed reports nothing.
var hookStates = map[string]string{
	"SessionStart":     "starting",
	"UserPromptSubmit": "thinking",
	"PreInvocation":    "thinking",
	"PreToolUse":       "working",
	"PostToolUse":      "working",
	"Stop":             "listening",
}

// presenceSpawn starts the detached ping. Tests replace it.
var presenceSpawn = spawnPresence

type presenceFile struct {
	State       string `json:"state"`
	AtMs        int64  `json:"at_ms"`
	SentState   string `json:"sent_state,omitempty"`
	SentMs      int64  `json:"sent_ms,omitempty"`
	DownUntilMs int64  `json:"down_until_ms,omitempty"`
}

func presencePath(env Env) string {
	if env.Session == "" {
		return ""
	}
	return filepath.Join(sessionsDir(env), env.Session, "presence.json")
}

func readPresence(env Env) presenceFile {
	var p presenceFile
	path := presencePath(env)
	if path == "" {
		return p
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return p
	}
	_ = json.Unmarshal(b, &p)
	return p
}

func writePresence(env Env, p presenceFile) {
	path := presencePath(env)
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = writeJSONFile(path, p)
}

// hookStateFor is the state a hook reports. A Stop that blocks keeps the
// agent working, so it is thinking, not listening.
func hookStateFor(event string, blocked bool) string {
	if event == "Stop" && blocked {
		return "thinking"
	}
	return hookStates[event]
}

// hookPresence records a hook's state and, unless the same state
// went out moments ago or the service is backing off, spawns the ping. It
// never waits on the network and never fails the hook.
func hookPresence(env Env, state, cwd string, now time.Time) {
	if state == "" || env.Session == "" {
		return
	}
	p := readPresence(env)
	p.State, p.AtMs = state, now.UnixMilli()
	send := now.UnixMilli() >= p.DownUntilMs &&
		(p.SentState != state || now.UnixMilli()-p.SentMs >= hookResend.Milliseconds())
	if send {
		p.SentState, p.SentMs = state, now.UnixMilli()
	}
	writePresence(env, p)
	if !send {
		return
	}
	if err := presenceSpawn(env, state, cwd); err != nil {
		logLine(env, "presence", err.Error())
	}
}

// waitPresenceState is the state a wait reports: the hook's busy state while
// it is fresh, otherwise listening.
func waitPresenceState(env Env, now time.Time) string {
	p := readPresence(env)
	if p.State == "" || p.State == "listening" || now.UnixMilli()-p.AtMs > hookStateFresh.Milliseconds() {
		return "listening"
	}
	return p.State
}

// PresencePing is `parley presence`: one bounded ping, run detached by a hook.
// A failure backs the session's hooks off rather than retrying.
func PresencePing(ctx context.Context, env Env, state string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err := presenceSend(ctx, env, state)
	if err == nil {
		return nil
	}
	p := readPresence(env)
	p.DownUntilMs = time.Now().Add(hookBackoff).UnixMilli()
	writePresence(env, p)
	return err
}

func spawnPresence(env Env, state, cwd string) error {
	if env.Self == "" {
		return nil
	}
	cmd := exec.Command(env.Self, "presence", "--session", env.Session, "--state", state, "--cwd", cwd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

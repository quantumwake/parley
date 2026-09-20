// Package plugin is the agent-CLI side of the product: it reads hook JSON on
// stdin (Claude Code, and mapped Antigravity/Codex payloads), decides what
// to do, and answers with the JSON that host expects. On SessionStart it
// makes sure this machine holds an enrolled identity, tells the agent the
// state, and records every hook it saw in a log.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/quantumwake/statefs/pkg/identityfile"

	"github.com/quantumwake/parley/pkg/capture"
	"github.com/quantumwake/parley/pkg/enroll"
	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/spool"
)

// Input is the hook stdin document (S4), shared with capture.
type Input = capture.HookInput

// Output is what the plugin prints for hooks declared with outputFormat
// json. additionalContext is honored on SessionStart and UserPromptSubmit;
// it is carried in both places Claude Code has accepted it. Decision
// "block" with a Reason on Stop keeps the agent working, the reason being
// what it is told.
type Output struct {
	AdditionalContext  string         `json:"additionalContext,omitempty"`
	HookSpecificOutput map[string]any `json:"hookSpecificOutput,omitempty"`
	Decision           string         `json:"decision,omitempty"`
	Reason             string         `json:"reason,omitempty"`
}

// Env is everything the plugin takes from its environment.
type Env struct {
	Directory    string // STATEFS_DIRECTORY; also derived from the enrollment URL
	StatefsAI    string // STATEFS_AI_APP, else the config's statefs_ai, else DefaultStatefsAI
	EnrollURL    string // STATEFS_ENROLL_URL: auto-enroll on first start
	IdentityPath string // STATEFS_KEY_FILE or the SDK default
	DataDir      string // CLAUDE_PLUGIN_DATA or ~/.statefs-ai
	Tenant       string // STATEFS_TENANT
	Self         string // path of this binary, for spawning the daemon
	Thinking     bool   // capture thinking blocks (STATEFS_AI_THINKING != "off")
	Session      string // the Claude Code session this process serves: CLAUDE_CODE_SESSION_ID, or the hook input's session_id
	HookEvent    string // optional: parley hook --event NAME (Antigravity omits the name on stdin)
	Gates        []Gate // the configured last-stage delivery gates; none by default
}

// EnvFromProcess reads the environment, then the config file written by
// `parley enroll` for anything the environment leaves unset. Product
// state (spool, names, subscriptions, logs) always lives in ~/.statefs-ai
// so the command line and the hooks see the same thing and a plugin
// uninstall never deletes it; CLAUDE_PLUGIN_DATA only caches the binary.
func EnvFromProcess() Env {
	cfg := LoadConfig()
	e := Env{
		Directory:    strings.TrimRight(os.Getenv("STATEFS_DIRECTORY"), "/"),
		EnrollURL:    os.Getenv("STATEFS_ENROLL_URL"),
		IdentityPath: os.Getenv("STATEFS_KEY_FILE"),
		DataDir:      os.Getenv("STATEFS_AI_DATA"),
		Tenant:       os.Getenv("STATEFS_TENANT"),
		Thinking:     os.Getenv("STATEFS_AI_THINKING") != "off",
		Session:      os.Getenv("CLAUDE_CODE_SESSION_ID"),
	}
	e.Self, _ = os.Executable()
	if e.Directory == "" {
		e.Directory = strings.TrimRight(cfg.Directory, "/")
	}

	if e.Directory == "" && os.Getenv("STATEFS_AI_STORE") == "" {
		e.Directory = DefaultDirectory
	}

	e.StatefsAI = strings.TrimRight(os.Getenv("STATEFS_AI_APP"), "/")
	if e.StatefsAI == "" {
		e.StatefsAI = strings.TrimRight(cfg.StatefsAI, "/")
	}
	if e.StatefsAI == "" {
		e.StatefsAI = DefaultStatefsAI
	}

	if e.IdentityPath == "" {
		e.IdentityPath = cfg.Identity
	}

	if e.IdentityPath == "" {
		e.IdentityPath = identityfile.DefaultPath()
	}

	if e.Tenant == "" {
		e.Tenant = cfg.Tenant
	}

	e.Gates = cfg.Gates

	if e.DataDir == "" {
		home, _ := os.UserHomeDir()
		e.DataDir = filepath.Join(home, ".statefs-ai")
	}

	migrateDataDir(e.DataDir)
	return e
}

// Handle runs one hook invocation: read stdin, act, write stdout. It
// never returns a non-nil error for a product problem (Claude Code would
// surface it as a hook failure and block); problems go into the context
// message and the log instead.
func Handle(ctx context.Context, env Env, stdin io.Reader, stdout io.Writer) error {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("plugin: hook input: %w", err)
	}
	in, host, err := capture.DecodeHook(raw, env.HookEvent)
	if err != nil {
		return fmt.Errorf("plugin: hook input: %w", err)
	}

	if in.SessionID != "" {
		env.Session = in.SessionID
	}

	out := Output{}
	author := authorOf(env)
	// The row is spooled before the daemon check below: a daemon that is
	// handing off looks for exactly this row after releasing its lock.
	if e, ok := capture.FromHook(in, author, time.Now()); ok {
		if e.Kind == event.KindSubagentStart {
			e.Content = subagentStartContent(in)
		}

		sp := spool.Session{Dir: SpoolDir(env), ID: in.SessionID}
		if err := sp.Append(e, in.HookEventName == "SessionEnd"); err != nil {
			logLine(env, "spool", err.Error())
		}
	}

	capturing := author != "" || os.Getenv("STATEFS_AI_STORE") != ""
	// Claude Code and Codex have on-disk transcripts the daemon can tail.
	// Antigravity's JSONL is a different shape; do not start the daemon there.
	tailTranscript := host == capture.HostClaude || host == capture.HostCodex
	switch in.HookEventName {
	case "SessionStart":
		out.AdditionalContext = sessionStart(ctx, env) + EnsurePath(env)
		if capturing && tailTranscript {
			if err := ensureDaemon(env, in); err != nil {
				logLine(env, "daemon", err.Error())
				out.AdditionalContext += " (capture daemon failed to start: " + err.Error() + ")"
			}
		}
	case "UserPromptSubmit", "PreInvocation":
		// Every prompt also makes sure the session's daemon is alive, so a
		// daemon that went idle or died comes back with the next turn.
		// Antigravity PreInvocation has no prompt text; it is only injection.
		if capturing && tailTranscript {
			if err := ensureDaemon(env, in); err != nil {
				logLine(env, "daemon", err.Error())
			}
		}

		out.AdditionalContext = Inject(ctx, env)
		if n := listenerNotice(env, out.AdditionalContext != ""); n != "" {
			out.AdditionalContext += n
		}
	case "Stop":
		// A session with a live background wait is never held here: the
		// wait delivers posts as a wake, and Claude Code shows every Stop
		// block as an error. Without one, posts that arrived during the turn
		// are handed over before the agent goes idle, where nothing would
		// reach it. Once per stop: when this stop already follows a block,
		// let the agent rest.
		if !in.StopHookActive && !WaitLive(env) {
			// Only posts this session is meant to act on hold the turn.
			// The rest have had their cursors advanced by the read above,
			// so they are kept for the next prompt rather than dropped.
			posts, hold, lines := injectLines(ctx, env)
			switch {
			case posts != "" && hold:
				out.Decision, out.Reason = "block", posts+"Handle these before ending the turn. No live `parley wait` is armed for this session: "+WaitAdvice+"."
			case posts != "":
				spoolContext(env, lines)
			}
		}
	}

	logHook(env, in)
	if out.AdditionalContext != "" {
		out.HookSpecificOutput = map[string]any{"hookEventName": in.HookEventName, "additionalContext": out.AdditionalContext}
	}

	return encodeHookOutput(host, env.HookEvent, in.HookEventName, out, stdout)
}

func encodeHookOutput(host capture.Host, eventArg, eventName string, out Output, stdout io.Writer) error {
	if host != capture.HostAntigravity {
		return json.NewEncoder(stdout).Encode(out)
	}
	event := eventArg
	if event == "" {
		event = eventName
	}
	switch event {
	case "PreToolUse":
		return json.NewEncoder(stdout).Encode(map[string]any{"decision": "allow"})
	case "Stop":
		if out.Decision == "block" {
			return json.NewEncoder(stdout).Encode(map[string]any{"decision": "continue", "reason": out.Reason})
		}
		return json.NewEncoder(stdout).Encode(map[string]any{"decision": "stop"})
	case "PreInvocation":
		if out.AdditionalContext == "" {
			return json.NewEncoder(stdout).Encode(map[string]any{})
		}
		return json.NewEncoder(stdout).Encode(map[string]any{
			"injectSteps": []map[string]any{{"ephemeralMessage": out.AdditionalContext}},
		})
	default:
		return json.NewEncoder(stdout).Encode(map[string]any{})
	}
}

// migrateDataDir adopts state left under the pre-0.2 plugin data
// directories (spool, names, subscriptions) the first time parley runs.
func migrateDataDir(dst string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	for _, old := range []string{
		filepath.Join(home, ".claude", "plugins", "data", "statefs-ai-statefs-ai"),
		filepath.Join(home, ".claude", "plugins", "data", "statefs-ai-inline"),
	} {
		for _, sub := range []string{"spool", "names", "subscriptions"} {
			src := filepath.Join(old, sub)
			if _, err := os.Stat(src); err != nil {
				continue
			}

			target := filepath.Join(dst, sub)
			if _, err := os.Stat(target); err == nil {
				mergeDir(src, target)
				continue
			}

			_ = os.MkdirAll(dst, 0o700)
			_ = os.Rename(src, target)
		}
	}
}

// mergeDir moves files from src into dst without overwriting.
func mergeDir(src, dst string) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}

	for _, e := range entries {
		to := filepath.Join(dst, e.Name())
		if _, err := os.Stat(to); err == nil {
			continue
		}

		_ = os.Rename(filepath.Join(src, e.Name()), to)
	}
}

// authorOf is the enrolled identity's username, or "" when not enrolled.
func authorOf(env Env) string {
	f, err := identityfile.Read(env.IdentityPath)
	if err != nil {
		return ""
	}

	return f.Username
}

// spawnDaemon starts `parley daemon` detached: it tails the transcript
// and pushes the spool. The daemon takes the session's lock itself and
// exits at once when another daemon holds it, so a spawn that races
// another is harmless.
func spawnDaemon(env Env, in Input) error {
	_ = os.MkdirAll(env.DataDir, 0o700)
	logf, err := os.OpenFile(filepath.Join(env.DataDir, "daemon.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}

	defer logf.Close()
	cmd := exec.Command(env.Self, "daemon", "--session", in.SessionID, "--transcript", in.TranscriptPath, "--cwd", in.CWD)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Stdin = nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}

	return cmd.Process.Release()
}

// logLine appends a diagnostic line to hooks.log.
func logLine(env Env, what, msg string) {
	_ = os.MkdirAll(env.DataDir, 0o700)
	f, err := os.OpenFile(filepath.Join(env.DataDir, "hooks.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}

	defer f.Close()
	line, _ := json.Marshal(map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "what": what, "error": msg})
	_, _ = f.Write(append(line, '\n'))
}

// ListenerNoticeEvery bounds how often a quiet session is reminded.
const ListenerNoticeEvery = 30 * time.Minute

// listenerNotice tells a session that follows conversations, and has no
// live wait, to arm one.
//
// Nothing parley can do reaches an idle Claude Code session: the wake is
// the background shell task ending, which only the session itself can
// start. A session that owns a conversation and never arms a wait hears
// nothing in it until its next turn, so the one honest remedy is to say
// so while it is true.
//
// Said whenever posts came with this prompt — that is the moment the
// agent can act on it — and otherwise at most once every
// ListenerNoticeEvery, so a session that legitimately never needed a
// listener is not nagged on a screen this work exists to quieten.
func listenerNotice(env Env, delivered bool) string {
	if WaitLive(env) || env.Session == "" || len(Subscriptions(env)) == 0 {
		return ""
	}

	path := filepath.Join(sessionsDir(env), env.Session, "notice")
	if !delivered {
		if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) < ListenerNoticeEvery {
			return ""
		}
	}

	if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		now := time.Now()
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_ = f.Close()
			_ = os.Chtimes(path, now, now)
		}
	}

	return "\nstatefs.ai parley: no listener is armed for this session, so posts will only reach you when you next finish a turn. " + WaitAdvice + ".\n"
}

// sessionStart ensures an identity and describes the state to the agent.
func sessionStart(ctx context.Context, env Env) string {
	if f, err := identityfile.Read(env.IdentityPath); err == nil {
		if env.Directory == "" && os.Getenv("STATEFS_AI_STORE") == "" {
			return fmt.Sprintf("statefs.ai parley: enrolled as %q but no directory is configured; run `parley enroll` again or set STATEFS_DIRECTORY. Capture is off.", f.Username)
		}

		// Name the command the agent will actually reach: `parley` on the
		// PATH follows the installed plugin, while this binary's own path
		// may belong to an older plugin root this session started with.
		cmd := "parley"
		if _, err := exec.LookPath("parley"); err != nil && env.Self != "" {
			cmd = env.Self
		}

		line := fmt.Sprintf("statefs.ai parley: this machine is enrolled as %q; this session is being recorded. Shared conversations: `%s list|join|post|read|wait`. Address every subscriber with @everyone (or --to everyone) so they evaluate the post and respond if needed. Posts from conversations you follow are shown when the user sends a prompt and when a turn ends; nothing reaches you while idle.", f.Username, cmd)
		if len(Subscriptions(env)) > 0 {
			line += " This session follows conversations: " + WaitAdvice + ". " + WorkGuide(cmd) + "."
		}

		if s, ok := CheckServer(ctx, env, false); ok {
			for _, n := range FloorNotices(s, ClientVersion) {
				line += " Tell the user: " + n + "."
			}
		}

		return line
	}

	if env.EnrollURL == "" {
		return "statefs.ai parley: this machine is not enrolled for this user. Ask the user for an enrollment URL from their statefs.ai organization and run `parley enroll <url>` (or `parley status` to see the current setup); capture stays off until then."
	}

	req, err := enroll.ParseURL(env.EnrollURL)
	if err != nil {
		return "statefs.ai parley: STATEFS_ENROLL_URL is not an enrollment URL (" + err.Error() + "); capture stays off."
	}

	if req.Directory == "" {
		req.Directory = env.Directory
	}

	res, err := enroll.Enroll(ctx, req, enroll.Options{Path: env.IdentityPath})
	if err != nil {
		if errors.Is(err, enroll.ErrTokenRejected) {
			return "statefs.ai parley: the enrollment token was rejected (used, expired, or invalid). Ask the user for a fresh URL; capture stays off."
		}

		return "statefs.ai parley: enrollment failed: " + err.Error() + "; capture stays off."
	}

	if _, err := enroll.Verify(ctx, res.Directory, res.Path, env.Tenant); err != nil {
		return fmt.Sprintf("statefs.ai parley: enrolled as %q but the token exchange failed (%v); capture stays off.", res.Username, err)
	}

	if cfg := LoadConfig(); cfg.Identity == "" || res.Path == identityfile.DefaultPath() {
		// Keep everything enrollment does not decide (statefs.ai's URL,
		// the gates): re-enrolling a machine must not silently undo its
		// configuration.
		cfg.Directory, cfg.Identity, cfg.Tenant = res.Directory, res.Path, env.Tenant
		_ = SaveConfig(cfg)
	}

	return fmt.Sprintf("statefs.ai parley: enrolled this machine as %q with %s and verified the token exchange. Conversation capture is active.", res.Username, res.Directory)
}

// logHook appends one line per hook so tests and people can see the plugin ran.
func logHook(env Env, in Input) {
	_ = os.MkdirAll(env.DataDir, 0o700)
	f, err := os.OpenFile(filepath.Join(env.DataDir, "hooks.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}

	defer f.Close()
	line, _ := json.Marshal(map[string]any{
		"at": time.Now().UTC().Format(time.RFC3339Nano), "event": in.HookEventName, "session": in.SessionID,
		"tool": in.ToolName, "tool_use_id": in.ToolUseID, "cwd": in.CWD,
		"plugin_root": os.Getenv("CLAUDE_PLUGIN_ROOT"), "binary": env.Self,
	})
	_, _ = f.Write(append(line, '\n'))
}

// subagentStartContent labels a subagent.start with what a reader needs:
// the description it was launched with (best effort) and its transcript.
func subagentStartContent(in Input) json.RawMessage {
	content := map[string]any{}
	if in.TranscriptPath != "" && in.AgentID != "" {
		content["transcript_path"] = SubagentTranscript(in.TranscriptPath, in.AgentID)
		if d := subagentDescription(in.TranscriptPath, in.AgentID); d != "" {
			content["description"] = d
		}
	}

	b, _ := json.Marshal(content)
	return b
}

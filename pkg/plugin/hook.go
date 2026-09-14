// Package plugin is the Claude Code side of the product: it reads the hook
// JSON Claude Code pipes on stdin, decides what to do, and answers with the
// JSON Claude Code expects back. This first cut proves the extension
// mechanics: on SessionStart it makes sure this machine holds an enrolled
// identity (auto-enrolling from STATEFS_ENROLL_URL when present), tells
// the agent the state in additionalContext, and records every hook it saw
// in a log so a test can see the plugin ran.
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
	EnrollURL    string // STATEFS_ENROLL_URL: auto-enroll on first start
	IdentityPath string // STATEFS_KEY_FILE or the SDK default
	DataDir      string // CLAUDE_PLUGIN_DATA or ~/.statefs-ai
	Tenant       string // STATEFS_TENANT
	Self         string // path of this binary, for spawning the daemon
	Thinking     bool   // capture thinking blocks (STATEFS_AI_THINKING != "off")
	Session      string // the Claude Code session this process serves: CLAUDE_CODE_SESSION_ID, or the hook input's session_id
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

	if e.IdentityPath == "" {
		e.IdentityPath = cfg.Identity
	}

	if e.IdentityPath == "" {
		e.IdentityPath = identityfile.DefaultPath()
	}

	if e.Tenant == "" {
		e.Tenant = cfg.Tenant
	}

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
	var in Input
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
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
	switch in.HookEventName {
	case "SessionStart":
		out.AdditionalContext = sessionStart(ctx, env) + EnsurePath(env)
		if capturing {
			if err := ensureDaemon(env, in); err != nil {
				logLine(env, "daemon", err.Error())
				out.AdditionalContext += " (capture daemon failed to start: " + err.Error() + ")"
			}
		}
	case "UserPromptSubmit":
		// Every prompt also makes sure the session's daemon is alive, so a
		// daemon that went idle or died comes back with the next turn.
		if capturing {
			if err := ensureDaemon(env, in); err != nil {
				logLine(env, "daemon", err.Error())
			}
		}

		out.AdditionalContext = Inject(ctx, env)
	case "Stop":
		// A session with a live background wait is never held here: the
		// wait delivers posts as a wake, and Claude Code shows every Stop
		// block as an error. Without one, posts that arrived during the turn
		// are handed over before the agent goes idle, where nothing would
		// reach it. Once per stop: when this stop already follows a block,
		// let the agent rest.
		if !in.StopHookActive && !WaitLive(env) {
			if posts := Inject(ctx, env); posts != "" {
				out.Decision, out.Reason = "block", posts+"Handle these before ending the turn. No live `parley wait` is armed for this session: "+WaitAdvice+"."
			}
		}
	}

	logHook(env, in)
	if out.AdditionalContext != "" {
		out.HookSpecificOutput = map[string]any{"hookEventName": in.HookEventName, "additionalContext": out.AdditionalContext}
	}

	return json.NewEncoder(stdout).Encode(out)
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

		line := fmt.Sprintf("statefs.ai parley: this machine is enrolled as %q; this session is being recorded. Shared conversations: `%s list|join|post|read|wait`. Posts from conversations you follow are shown when the user sends a prompt and when a turn ends; nothing reaches you while idle.", f.Username, cmd)
		if len(Subscriptions(env)) > 0 {
			line += " This session follows conversations: " + WaitAdvice + ". " + WorkGuide(cmd) + "."
		}

		return line
	}

	if env.EnrollURL == "" {
		return "statefs.ai parley: this machine is not enrolled for this user. Ask the user for an enrollment URL from the statefs.io tenant console and run `parley enroll <url>` (or `parley status` to see the current setup); capture stays off until then."
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

	if LoadConfig().Identity == "" || res.Path == identityfile.DefaultPath() {
		_ = SaveConfig(Config{Directory: res.Directory, Identity: res.Path, Tenant: env.Tenant})
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

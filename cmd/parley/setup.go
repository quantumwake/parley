package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quantumwake/parley/pkg/plugin"
)

func cmdSetup(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: parley setup <auto|claude|antigravity|grok|codex|cursor>")
	}
	target := args[0]

	switch target {
	case "auto":
		if setupIsCurrent() {
			return nil
		}
		ok := true
		if commandExists("claude") {
			if err := setupClaude(ctx); err != nil {
				ok = false
				fmt.Fprintf(os.Stderr, "failed to setup claude: %v\n", err)
			}
		} else {
			fmt.Println("Claude Code not found, skipping Claude setup.")
			fmt.Println("note: inside Claude Code run  /plugin marketplace add quantumwake/parley  then  /plugin install parley@parley")
		}

		if commandExists("agy") || commandExists("antigravity") || hasAntigravityConfigDir() {
			if err := setupAntigravity(ctx); err != nil {
				ok = false
				fmt.Fprintf(os.Stderr, "failed to setup antigravity: %v\n", err)
			}
		} else {
			fmt.Println("Antigravity CLI not found, skipping Antigravity setup.")
		}

		if commandExists("grok") || hasGrokConfig() {
			if err := setupGrok(ctx); err != nil {
				ok = false
				fmt.Fprintf(os.Stderr, "failed to setup grok: %v\n", err)
			}
		} else {
			fmt.Println("Grok CLI not found, skipping Grok setup.")
		}

		if commandExists("codex") || hasCodexConfig() {
			if err := setupCodex(ctx); err != nil {
				ok = false
				fmt.Fprintf(os.Stderr, "failed to setup codex: %v\n", err)
			}
		} else {
			fmt.Println("Codex CLI not found, skipping Codex setup.")
		}

		if commandExists("cursor-agent") {
			if err := setupCursor(ctx); err != nil {
				ok = false
				fmt.Fprintf(os.Stderr, "failed to setup cursor: %v\n", err)
			}
		} else {
			fmt.Println("Cursor Agent not found, skipping Cursor setup.")
		}
		if ok {
			_ = writeSetupStamp()
		}
		return nil
	case "claude":
		return setupClaude(ctx)
	case "antigravity":
		return setupAntigravity(ctx)
	case "grok":
		return setupGrok(ctx)
	case "codex":
		return setupCodex(ctx)
	case "cursor":
		return setupCursor(ctx)
	default:
		return fmt.Errorf("unknown setup target: %s", target)
	}
}

func installHostSkill(rel string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	if err := plugin.InstallSkills(filepath.Join(home, rel)); err != nil {
		return fmt.Errorf("skills: %w", err)
	}

	return nil
}

func commandExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

func hasAntigravityConfigDir() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".gemini", "config"))
	return err == nil
}

func hasGrokConfig() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".grok", "config.toml"))
	return err == nil
}

func hasCodexConfig() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".codex", "config.toml"))
	return err == nil
}

func parleyExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}

// A setup stamp records the binary, version, and CLIs that `setup auto`
// last configured. A later auto with the same record is a no-op, so a
// resume does not pay the setup cost again. A replaced binary, a rewrite
// of that file, a new version, or a newly installed CLI still runs setup.
type setupStamp struct {
	Binary  string   `json:"binary"`
	Version string   `json:"version"`
	CLIs    []string `json:"clis"`
	Size    int64    `json:"size"`
	ModTime int64    `json:"mtime_unix_nano"`
}

func presentCLIs() []string {
	var out []string
	if commandExists("claude") {
		out = append(out, "claude")
	}
	if commandExists("agy") || commandExists("antigravity") || hasAntigravityConfigDir() {
		out = append(out, "antigravity")
	}
	if commandExists("grok") || hasGrokConfig() {
		out = append(out, "grok")
	}
	if commandExists("codex") || hasCodexConfig() {
		out = append(out, "codex")
	}
	if commandExists("cursor-agent") {
		out = append(out, "cursor")
	}
	return out
}

func setupStampPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".statefs-ai", "setup.json")
}

func readSetupStamp() (setupStamp, error) {
	b, err := os.ReadFile(setupStampPath())
	if err != nil {
		return setupStamp{}, err
	}
	var s setupStamp
	err = json.Unmarshal(b, &s)
	return s, err
}

func writeSetupStamp() error {
	s, err := setupStampNow()
	if err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	path := setupStampPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func setupStampNow() (setupStamp, error) {
	exe, err := parleyExecutable()
	if err != nil {
		return setupStamp{}, err
	}
	return stampOf(exe, strings.TrimSpace(version), presentCLIs())
}

// stampOf is the identity of one binary file. Size and mtime catch a
// rewrite that keeps the path and the version string. Mtime is stored in
// nanoseconds and compared to the whole second: ext3, HFS+, FAT, tar,
// zip, and Fly's root filesystem do not keep a finer fraction, so a
// nanosecond compare re-ran setup on every boot.
func stampOf(path, version string, clis []string) (setupStamp, error) {
	st, err := os.Stat(path)
	if err != nil {
		return setupStamp{}, err
	}
	return setupStamp{
		Binary: path, Version: version, CLIs: clis,
		Size: st.Size(), ModTime: st.ModTime().UnixNano(),
	}, nil
}

func sameSetup(a, b setupStamp) bool {
	return a.Binary == b.Binary && a.Version == b.Version && slices.Equal(a.CLIs, b.CLIs) &&
		a.Size == b.Size && a.ModTime/int64(time.Second) == b.ModTime/int64(time.Second)
}

func setupIsCurrent() bool {
	want, err := setupStampNow()
	if err != nil {
		return false
	}
	got, err := readSetupStamp()
	if err != nil {
		return false
	}
	return sameSetup(got, want)
}

func grokMcpAddArgs(exe string) []string {
	return []string{"mcp", "add", "--scope", "user", "parley", "--", exe, "mcp"}
}

func setupGrok(ctx context.Context) error {
	// Grok's hooks are ~/.grok/hooks/*.json. The file shape matches Codex
	// (a matcher group around `parley hook --event NAME`). The stdin does
	// not: Grok sends sessionId, toolName, and hookEventName. `parley hook`
	// reads those. SessionEnd stays short because Grok's teardown queue is
	// about a second and a half.
	if !commandExists("grok") {
		return fmt.Errorf("grok CLI not found on PATH; install it, then re-run parley setup grok")
	}
	exe, err := parleyExecutable()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	fmt.Println("Registering Parley MCP and hooks for Grok CLI...")
	if err := writeGrokHooks(home, exe); err != nil {
		return fmt.Errorf("failed to update Grok hooks: %w", err)
	}
	cmd := exec.CommandContext(ctx, "grok", grokMcpAddArgs(exe)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("grok mcp add failed: %w", err)
	}
	if err := writeGrokStatusLine(filepath.Join(home, ".grok", "config.toml"), exe); err != nil {
		fmt.Fprintf(os.Stderr, "warning: Grok status line left unchanged: %v\n", err)
	}
	fmt.Println("Grok CLI MCP, hooks, and status line registered (restart Grok CLI sessions to load them)")
	return installHostSkill(".grok/skills")
}

func writeGrokHooks(home, exe string) error {
	dir := filepath.Join(home, ".grok", "hooks")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, "parley.json")
	data := map[string]any{}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &data); err != nil {
			return fmt.Errorf("parse %s: %w (left unchanged)", path, err)
		}
	}
	hooks, _ := data["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		data["hooks"] = hooks
	}
	for _, event := range grokHookEvents {
		hooks[event.name] = mergeNestedHook(hooks[event.name], exe, event.name, event.timeout)
	}
	return writeJSON(path, data)
}

// grokHookEvents are Grok's own names. UserPromptSubmit and Stop read
// followed conversations, so they get 30s. SessionEnd only records; Grok's
// teardown queue is about 1.5s.
var grokHookEvents = []struct {
	name    string
	timeout int
}{
	{"SessionStart", 30},
	{"UserPromptSubmit", 30},
	{"PreToolUse", 10},
	{"PostToolUse", 10},
	{"PostToolUseFailure", 10},
	{"SubagentStart", 10},
	{"SubagentStop", 10},
	{"Stop", 30},
	{"SessionEnd", 3},
}

func mergeNestedHook(existing any, exe, event string, timeout int) []any {
	entry := map[string]any{"hooks": []any{hookCmd(exe, event, timeout)}}
	arr, ok := existing.([]any)
	if !ok {
		return []any{entry}
	}
	kept := make([]any, 0, len(arr)+1)
	for _, item := range arr {
		if jsonHasParleyHook(item) {
			continue
		}
		kept = append(kept, item)
	}
	return append(kept, entry)
}

func setupCursor(_ context.Context) error {
	// Cursor's own hooks live in ~/.cursor/hooks.json. The event names are
	// sessionStart, preToolUse, postToolUse, sessionEnd, and the rest of
	// the map in writeCursorHooks. `parley hook` reads the name from stdin.
	// A Cursor session hears posts when `parley wait` is running. The session
	// id is CURSOR_CONVERSATION_ID, which Cursor Agent already exports.
	if !commandExists("cursor-agent") {
		return fmt.Errorf("cursor-agent not found on PATH; install it, then re-run parley setup cursor")
	}
	exe, err := parleyExecutable()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	fmt.Println("Registering Parley MCP and hooks for Cursor Agent...")
	if err := writeCursorMCP(home, exe); err != nil {
		return fmt.Errorf("failed to update Cursor MCP config: %w", err)
	}
	if err := writeCursorHooks(home, exe); err != nil {
		return fmt.Errorf("failed to update Cursor hooks: %w", err)
	}
	if err := writeStatusLine(filepath.Join(home, ".cursor", "cli-config.json"), exe); err != nil {
		fmt.Fprintf(os.Stderr, "warning: Cursor status line left unchanged: %v\n", err)
	}
	fmt.Println("Cursor Agent MCP, hooks, and status line registered (restart Cursor Agent sessions to load them)")
	return installHostSkill(".cursor/skills")
}

// cursorHookEvents are the names Cursor puts in ~/.cursor/hooks.json.
// Timeouts match the Claude plugin: prompt and stop hooks read followed
// conversations, so they get 30s; a tool hook only records and gets 5s.
var cursorHookEvents = []struct {
	name    string
	timeout int
}{
	{"sessionStart", 30},
	{"beforeSubmitPrompt", 30},
	{"preToolUse", 5},
	{"postToolUse", 5},
	{"postToolUseFailure", 5},
	{"subagentStart", 5},
	{"subagentStop", 5},
	{"stop", 30},
	{"sessionEnd", 30},
}

func writeCursorHooks(home, exe string) error {
	path := filepath.Join(home, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data := map[string]any{}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &data); err != nil {
			return fmt.Errorf("parse %s: %w (left unchanged)", path, err)
		}
	}
	if _, ok := data["version"]; !ok {
		data["version"] = 1
	}
	hooks, _ := data["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		data["hooks"] = hooks
	}
	cmd := strconv.Quote(exe) + " hook"
	for _, event := range cursorHookEvents {
		hooks[event.name] = mergeCursorEvent(hooks[event.name], cmd, event.timeout)
	}
	return writeJSON(path, data)
}

func mergeCursorEvent(existing any, cmd string, timeout int) []any {
	entry := map[string]any{"command": cmd, "timeout": timeout}
	arr, ok := existing.([]any)
	if !ok {
		return []any{entry}
	}
	kept := make([]any, 0, len(arr)+1)
	for _, item := range arr {
		if jsonHasParleyHook(item) {
			continue
		}
		kept = append(kept, item)
	}
	return append(kept, entry)
}

func writeCursorMCP(home, exe string) error {
	dir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return updateJSON(filepath.Join(dir, "mcp.json"), "mcpServers", "parley", map[string]any{
		"command": exe,
		"args":    []string{"mcp"},
	})
}

func setupClaude(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	exe, err := parleyExecutable()
	if err != nil {
		return err
	}
	if err := writeStatusLine(filepath.Join(home, ".claude", "settings.json"), exe); err != nil {
		fmt.Fprintf(os.Stderr, "warning: Claude status line left unchanged: %v\n", err)
	} else {
		fmt.Println("Claude Code status line set (restart Claude Code sessions to load it)")
	}

	repo := "quantumwake/parley"
	fmt.Println("Installing Claude Code plugin parley@parley...")
	_ = exec.CommandContext(ctx, "claude", "plugin", "marketplace", "add", repo).Run()
	out, err := runClaudePlugin(ctx, "install", "parley@parley", "--scope", "user", "-y")
	if err != nil {
		if strings.TrimSpace(out) != "" {
			fmt.Fprint(os.Stderr, out)
		}
		return fmt.Errorf("claude plugin install failed: %w", err)
	}
	if claudeInstallNeedsUpdate(out) {
		fmt.Println("Updating Claude Code plugin parley@parley...")
		uout, uerr := runClaudePlugin(ctx, "update", "parley@parley", "--scope", "user", "-y")
		if uerr != nil {
			if strings.TrimSpace(uout) != "" {
				fmt.Fprint(os.Stderr, uout)
			}
			return fmt.Errorf("claude plugin update failed: %w", uerr)
		}
		fmt.Println("Claude Code plugin parley@parley updated (restart Claude Code sessions to load it)")
		return installHostSkill(".claude/skills")
	}
	fmt.Println("Claude Code plugin parley@parley installed (restart Claude Code sessions to load it)")
	return installHostSkill(".claude/skills")
}

func runClaudePlugin(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "claude", append([]string{"plugin"}, args...)...)
	b, err := cmd.CombinedOutput()
	return string(b), err
}

func claudeInstallNeedsUpdate(out string) bool {
	return strings.Contains(out, "already installed") && strings.Contains(out, "marketplace now offers")
}

func setupAntigravity(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	configDir := filepath.Join(home, ".gemini", "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}

	exe, err := parleyExecutable()
	if err != nil {
		return err
	}

	fmt.Println("Registering Parley for Antigravity CLI...")
	mcpPath := filepath.Join(configDir, "mcp_config.json")
	if err := updateJSON(mcpPath, "mcpServers", "parley", map[string]any{
		"command": exe,
		"args":    []string{"mcp"},
	}); err != nil {
		return fmt.Errorf("failed to update mcp_config.json: %w", err)
	}

	hooksPath := filepath.Join(configDir, "hooks.json")
	tool := func(event string) map[string]any {
		return map[string]any{
			"matcher": "*",
			"hooks":   []any{hookCmd(exe, event, 10)},
		}
	}
	if err := updateJSON(hooksPath, "parley", "PreToolUse", []any{tool("PreToolUse")}); err != nil {
		return fmt.Errorf("failed to update hooks.json (PreToolUse): %w", err)
	}
	if err := updateJSON(hooksPath, "parley", "PostToolUse", []any{tool("PostToolUse")}); err != nil {
		return fmt.Errorf("failed to update hooks.json (PostToolUse): %w", err)
	}
	for _, event := range []string{"PreInvocation", "PostInvocation", "Stop"} {
		if err := updateJSON(hooksPath, "parley", event, []any{hookCmd(exe, event, 30)}); err != nil {
			return fmt.Errorf("failed to update hooks.json (%s): %w", event, err)
		}
	}
	if err := writeStatusLine(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), exe); err != nil {
		fmt.Fprintf(os.Stderr, "warning: Antigravity status line left unchanged: %v\n", err)
	}
	fmt.Println("Parley MCP, hooks, and status line registered for Antigravity CLI (restart Antigravity sessions to load the status line)")
	return installHostSkill(".gemini/config/skills")
}

func hookCmd(exe, event string, timeout int) map[string]any {
	return map[string]any{
		"type":    "command",
		"command": strconv.Quote(exe) + " hook --event " + event,
		"timeout": timeout,
	}
}

func setupCodex(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	exe, err := parleyExecutable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0755); err != nil {
		return err
	}

	fmt.Println("Registering Parley for Codex CLI...")
	wroteTOML := false
	if commandExists("codex") {
		cmd := exec.CommandContext(ctx, "codex", "mcp", "add", "parley", "--", exe, "mcp")
		out, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Println("codex mcp add failed; writing ~/.codex/config.toml")
			wroteTOML = true
		} else if len(out) > 0 {
			os.Stdout.Write(out)
		}
	} else {
		wroteTOML = true
	}
	if wroteTOML {
		if err := upsertTOMLTable(filepath.Join(home, ".codex", "config.toml"), "mcp_servers.parley", fmt.Sprintf("command = %s\nargs = [\"mcp\"]\n", strconv.Quote(exe))); err != nil {
			return fmt.Errorf("failed to update Codex MCP config: %w", err)
		}
	}

	if err := upsertCodexHooks(filepath.Join(home, ".codex", "hooks.json"), exe); err != nil {
		return fmt.Errorf("failed to update Codex hooks: %w", err)
	}
	fmt.Println("Parley MCP and hooks registered for Codex CLI (trust the hooks in /hooks)")
	fmt.Println("Codex has no command status line; its footer is the built-in tui.status_line list, so the handle is not shown there")
	return installHostSkill(".codex/skills")
}

func upsertCodexHooks(path, exe string) error {
	data := map[string]any{}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &data); err != nil {
			return fmt.Errorf("parse %s: %w (left unchanged)", path, err)
		}
	}
	hooks, _ := data["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		data["hooks"] = hooks
	}
	events := []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "SubagentStart", "SubagentStop", "Stop", "SessionEnd"}
	for _, event := range events {
		hooks[event] = mergeCodexEvent(hooks[event], exe, event)
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func mergeCodexEvent(existing any, exe, event string) []any {
	timeout := 10
	switch event {
	case "SessionStart", "UserPromptSubmit", "Stop":
		timeout = 30
	case "SessionEnd":
		// Codex allows at most 3s for SessionEnd. The hook only records
		// the end; it does not read the directory.
		timeout = 3
	}

	entry := map[string]any{"hooks": []any{hookCmd(exe, event, timeout)}}
	arr, ok := existing.([]any)
	if !ok {
		return []any{entry}
	}
	kept := make([]any, 0, len(arr)+1)
	for _, item := range arr {
		if jsonHasParleyHook(item) {
			continue
		}
		kept = append(kept, item)
	}
	return append(kept, entry)
}

func jsonHasParleyHook(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), "parley") && strings.Contains(string(b), "hook")
}

func upsertTOMLTable(path, header, body string) error {
	table := "[" + header + "]\n" + strings.TrimSuffix(body, "\n") + "\n"
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	start := tomlTableStart(string(b), header)
	if start >= 0 {
		end := tomlTableEnd(string(b), start)
		b = append(append([]byte(string(b)[:start]), table...), b[end:]...)
	} else {
		if len(b) > 0 && b[len(b)-1] != '\n' {
			b = append(b, '\n')
		}
		if len(b) > 0 {
			b = append(b, '\n')
		}
		b = append(b, table...)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func tomlTableStart(s, header string) int {
	needle := "[" + header + "]"
	if s == needle || strings.HasPrefix(s, needle+"\n") {
		return 0
	}
	idx := strings.Index(s, "\n"+needle+"\n")
	if idx >= 0 {
		return idx + 1
	}
	if strings.HasSuffix(s, "\n"+needle) {
		return len(s) - len(needle)
	}
	return -1
}

func tomlTableEnd(s string, start int) int {
	rest := s[start:]
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		return len(s)
	}
	i := start + nl + 1
	for i < len(s) {
		line := s[i:]
		if strings.HasPrefix(line, "[") {
			return i
		}
		n := strings.IndexByte(line, '\n')
		if n < 0 {
			return len(s)
		}
		i += n + 1
	}
	return len(s)
}

func updateJSON(path string, topKey string, subKey string, value any) error {
	data := make(map[string]any)
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) > 0 {
			if err := json.Unmarshal(b, &data); err != nil {
				return fmt.Errorf("parse %s: %w (left unchanged)", path, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	var topMap map[string]any
	if existing, ok := data[topKey].(map[string]any); ok {
		topMap = existing
	} else {
		topMap = make(map[string]any)
	}

	topMap[subKey] = value
	data[topKey] = topMap

	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// statusLineCommand is the argv a CLI status line runs. A path with a space
// is quoted so a shell split still finds the binary.
func statusLineCommand(exe string) string {
	if strings.ContainsAny(exe, " \t") {
		return strconv.Quote(exe) + " statusline"
	}
	return exe + " statusline"
}

// isParleyStatusLine reports whether command is the status line parley
// installs: the binary's basename is parley and the last argument is
// statusline. A path that only contains both words, such as
// ~/src/parley/scripts/my-statusline.sh, is someone else's command.
func isParleyStatusLine(command string) bool {
	args := splitStatusCommand(command)
	if len(args) < 2 || filepath.Base(args[0]) != "parley" {
		return false
	}
	return args[len(args)-1] == "statusline"
}

// splitStatusCommand splits a status-line command on spaces, after removing
// the quotes parley writes around a binary path that contains a space.
func splitStatusCommand(s string) []string {
	var args []string
	var b strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' && quote == '"' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
				continue
			}
			if c == quote {
				quote = 0
				continue
			}
			b.WriteByte(c)
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case ' ', '\t':
			if b.Len() > 0 {
				args = append(args, b.String())
				b.Reset()
			}
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		args = append(args, b.String())
	}
	return args
}

// writeStatusLine sets statusLine to parley statusline. A command that is
// already set is left alone. Every other byte of an existing file stays,
// because this runs on install and on every Cloud workspace boot.
func writeStatusLine(path, exe string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	cmd := statusLineCommand(exe)
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return writeFreshStatusLine(path, cmd)
	}
	next, changed, err := spliceStatusLine(b, cmd)
	if err != nil {
		return fmt.Errorf("parse %s: %w (left unchanged)", path, err)
	}
	if !changed {
		return nil
	}
	return writeBytesAtomic(path, next)
}

func writeFreshStatusLine(path, cmd string) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{
		"statusLine": map[string]any{"type": "command", "command": cmd},
	}); err != nil {
		return err
	}
	return writeBytesAtomic(path, buf.Bytes())
}

func writeBytesAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// spliceStatusLine inserts or updates the top-level statusLine value.
// Bytes outside that value are copied unchanged.
func spliceStatusLine(b []byte, cmd string) ([]byte, bool, error) {
	if !json.Valid(b) {
		return nil, false, fmt.Errorf("not valid JSON")
	}
	loc, err := locateTopLevelKey(b, "statusLine")
	if err != nil {
		return nil, false, err
	}
	if loc.found {
		val, changed, err := mergedStatusLine(b[loc.valStart:loc.valEnd], cmd)
		if err != nil || !changed {
			return b, false, err
		}
		out := make([]byte, 0, len(b)+len(val))
		out = append(out, b[:loc.valStart]...)
		out = append(out, val...)
		out = append(out, b[loc.valEnd:]...)
		return out, true, nil
	}
	val, _, err := mergedStatusLine(nil, cmd)
	if err != nil {
		return nil, false, err
	}
	insert := append([]byte(`"statusLine": `), val...)
	at := loc.close
	if !loc.empty {
		insert = append([]byte(",\n  "), insert...)
		at = loc.lastValEnd
	}
	out := make([]byte, 0, len(b)+len(insert))
	out = append(out, b[:at]...)
	out = append(out, insert...)
	out = append(out, b[at:]...)
	return out, true, nil
}

// mergedStatusLine adds type and command to an existing statusLine object
// and keeps every other field, such as padding. A command that is already
// set is left alone.
func mergedStatusLine(existing []byte, cmd string) ([]byte, bool, error) {
	obj := map[string]json.RawMessage{}
	trimmed := bytes.TrimSpace(existing)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		dec := json.NewDecoder(bytes.NewReader(existing))
		if err := dec.Decode(&obj); err != nil {
			return nil, false, err
		}
		if raw, ok := obj["command"]; ok {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil || s != "" {
				return nil, false, nil
			}
		}
	}
	cmdRaw, err := marshalJSONNoHTML(cmd)
	if err != nil {
		return nil, false, err
	}
	typeRaw, err := marshalJSONNoHTML("command")
	if err != nil {
		return nil, false, err
	}
	obj["command"] = cmdRaw
	obj["type"] = typeRaw
	out, err := marshalJSONNoHTML(obj)
	return out, true, err
}

func marshalJSONNoHTML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

type jsonKeyLoc struct {
	found            bool
	empty            bool
	valStart, valEnd int
	lastValEnd       int
	close            int
}

func locateTopLevelKey(b []byte, want string) (jsonKeyLoc, error) {
	var loc jsonKeyLoc
	i := skipJSONSpace(b, 0)
	if i >= len(b) || b[i] != '{' {
		return loc, fmt.Errorf("root is not a JSON object")
	}
	i++
	loc.empty = true
	for {
		i = skipJSONSpace(b, i)
		if i >= len(b) {
			return loc, fmt.Errorf("unclosed object")
		}
		if b[i] == '}' {
			loc.close = i
			return loc, nil
		}
		loc.empty = false
		if b[i] != '"' {
			return loc, fmt.Errorf("expected a key")
		}
		keyEnd, err := skipJSONString(b, i)
		if err != nil {
			return loc, err
		}
		var name string
		if err := json.Unmarshal(b[i:keyEnd], &name); err != nil {
			return loc, err
		}
		i = skipJSONSpace(b, keyEnd)
		if i >= len(b) || b[i] != ':' {
			return loc, fmt.Errorf("expected a colon")
		}
		i++
		valStart := skipJSONSpace(b, i)
		valEnd, err := skipJSONValue(b, valStart)
		if err != nil {
			return loc, err
		}
		loc.lastValEnd = valEnd
		if name == want {
			loc.found = true
			loc.valStart = valStart
			loc.valEnd = valEnd
		}
		i = skipJSONSpace(b, valEnd)
		if i < len(b) && b[i] == ',' {
			i++
			continue
		}
		if i < len(b) && b[i] == '}' {
			loc.close = i
			return loc, nil
		}
		return loc, fmt.Errorf("expected a comma or the end of the object")
	}
}

func skipJSONSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	return i
}

func skipJSONValue(b []byte, i int) (int, error) {
	if i >= len(b) {
		return 0, fmt.Errorf("unexpected end of JSON")
	}
	switch b[i] {
	case '"':
		return skipJSONString(b, i)
	case '{', '[':
		return skipJSONContainer(b, i)
	case 't', 'f', 'n', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		j := i + 1
		for j < len(b) && !isJSONDelim(b[j]) {
			j++
		}
		return j, nil
	default:
		return 0, fmt.Errorf("bad JSON value")
	}
}

func isJSONDelim(c byte) bool {
	return c == ',' || c == '}' || c == ']' || c == ' ' || c == '\n' || c == '\r' || c == '\t'
}

func skipJSONString(b []byte, i int) (int, error) {
	if i >= len(b) || b[i] != '"' {
		return 0, fmt.Errorf("expected a string")
	}
	i++
	for i < len(b) {
		if b[i] == '\\' {
			if i+1 >= len(b) {
				return 0, fmt.Errorf("bad escape")
			}
			i += 2
			continue
		}
		if b[i] == '"' {
			return i + 1, nil
		}
		i++
	}
	return 0, fmt.Errorf("unclosed string")
}

func skipJSONContainer(b []byte, i int) (int, error) {
	depth := 1
	i++
	for i < len(b) && depth > 0 {
		switch b[i] {
		case '"':
			var err error
			i, err = skipJSONString(b, i)
			if err != nil {
				return 0, err
			}
		case '{', '[':
			depth++
			i++
		case '}', ']':
			depth--
			i++
		default:
			i++
		}
	}
	if depth != 0 {
		return 0, fmt.Errorf("unclosed JSON value")
	}
	return i, nil
}

// writeGrokStatusLine sets [ui.status_line] to the parley command. A table
// that is already configured as something else is left alone.
func writeGrokStatusLine(path, exe string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if start := tomlTableStart(string(b), "ui.status_line"); start >= 0 {
		table := string(b)[start:tomlTableEnd(string(b), start)]
		if strings.TrimSpace(table) != "[ui.status_line]" {
			return nil
		}
	}
	body := "type = \"command\"\ncommand = " + strconv.Quote(statusLineCommand(exe)) + "\n"
	return upsertTOMLTable(path, "ui.status_line", body)
}

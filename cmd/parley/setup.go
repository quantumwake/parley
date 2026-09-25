package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

func cmdSetup(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: parley setup <auto|claude|antigravity|grok|codex>")
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
	default:
		return fmt.Errorf("unknown setup target: %s", target)
	}
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
// rewrite that keeps the path and the version string.
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
		a.Size == b.Size && a.ModTime == b.ModTime
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
	// MCP only: Grok hook JSON is not the Claude decoder `parley hook` speaks.
	if !commandExists("grok") {
		return fmt.Errorf("grok CLI not found on PATH; install it, then re-run parley setup grok")
	}
	exe, err := parleyExecutable()
	if err != nil {
		return err
	}

	fmt.Println("Registering Parley MCP for Grok CLI...")
	cmd := exec.CommandContext(ctx, "grok", grokMcpAddArgs(exe)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("grok mcp add failed: %w", err)
	}
	fmt.Println("Grok CLI MCP server parley registered (restart Grok CLI sessions to load it)")
	return nil
}

func setupClaude(ctx context.Context) error {
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
		return nil
	}
	fmt.Println("Claude Code plugin parley@parley installed (restart Claude Code sessions to load it)")
	return nil
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
	fmt.Println("Parley MCP and hooks registered for Antigravity CLI")
	return nil
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
	return nil
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
	case "SessionStart", "UserPromptSubmit", "Stop", "SessionEnd":
		timeout = 30
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

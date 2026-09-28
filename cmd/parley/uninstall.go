package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/quantumwake/parley/pkg/plugin"
)

// cmdUninstall lists what it would remove. It deletes only with --yes.
// Identities under ~/.statefs and the recorded sessions in ~/.statefs-ai stay.
func cmdUninstall(ctx context.Context, args []string) error {
	yes := false
	for _, a := range args {
		switch a {
		case "--yes":
			yes = true
		default:
			return fmt.Errorf("usage: parley uninstall [--yes]")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return runUninstall(ctx, home, yes, unhookCLIs)
}

// runUninstall is cmdUninstall against a chosen home. unhook runs only with
// --yes, so a dry run never calls the host CLIs.
func runUninstall(ctx context.Context, home string, yes bool, unhook func(context.Context)) error {
	lines, err := uninstallPlan(home)
	if err != nil {
		return err
	}
	if !yes {
		if len(lines) == 0 {
			fmt.Println("parley uninstall: nothing to remove. Identities and ~/.statefs-ai sessions stay.")
			return nil
		}
		fmt.Println("parley uninstall would remove:")
		for _, l := range lines {
			fmt.Println(" ", l)
		}
		fmt.Println("nothing was removed. Pass --yes to remove these. Identities and ~/.statefs-ai sessions stay.")
		return nil
	}
	if err := applyUninstall(home); err != nil {
		return err
	}
	if unhook != nil {
		unhook(ctx)
	}
	fmt.Println("parley hooks, skills, and launcher removed. Identities and ~/.statefs-ai sessions were left in place.")
	return nil
}

func applyUninstall(home string) error {
	if err := plugin.RemoveSkillsUnder(home); err != nil {
		return err
	}
	if err := stripCodexParley(filepath.Join(home, ".codex")); err != nil {
		return err
	}
	if err := stripAntigravityParley(filepath.Join(home, ".gemini", "config")); err != nil {
		return err
	}
	if err := stripCursorParley(home); err != nil {
		return err
	}
	return removeParleyBinary(home)
}

// uninstallPlan lists the files a --yes run would change. It does not
// change them, and it does not call the host CLIs.
func uninstallPlan(home string) ([]string, error) {
	var lines []string
	for _, rel := range []string{".grok/skills", ".claude/skills", ".codex/skills", ".gemini/config/skills", ".cursor/skills"} {
		for _, name := range []string{"arm", "disarm"} {
			p := filepath.Join(home, rel, name, "SKILL.md")
			b, err := os.ReadFile(p)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			if strings.Contains(string(b), "author: parley") {
				lines = append(lines, p)
			}
		}
	}
	hooks := filepath.Join(home, ".codex", "hooks.json")
	if b, err := os.ReadFile(hooks); err == nil && strings.Contains(string(b), "parley") && strings.Contains(string(b), "hook") {
		lines = append(lines, "parley hooks in "+hooks)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	tomlPath := filepath.Join(home, ".codex", "config.toml")
	if b, err := os.ReadFile(tomlPath); err == nil && strings.Contains(string(b), "[mcp_servers.parley]") {
		lines = append(lines, "table [mcp_servers.parley] in "+tomlPath)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	cursorMCP := filepath.Join(home, ".cursor", "mcp.json")
	if b, err := os.ReadFile(cursorMCP); err == nil && strings.Contains(string(b), `"parley"`) {
		lines = append(lines, "mcpServers.parley in "+cursorMCP)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	mcp := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	if b, err := os.ReadFile(mcp); err == nil && strings.Contains(string(b), `"parley"`) {
		lines = append(lines, "mcpServers.parley in "+mcp)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	agyHooks := filepath.Join(home, ".gemini", "config", "hooks.json")
	if b, err := os.ReadFile(agyHooks); err == nil && strings.Contains(string(b), `"parley"`) {
		lines = append(lines, "parley hooks in "+agyHooks)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	link := filepath.Join(home, ".local", "bin", "parley")
	if _, err := os.Lstat(link); err == nil {
		lines = append(lines, link)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	bin := filepath.Join(home, ".statefs-ai", "bin", "parley")
	if _, err := os.Stat(bin); err == nil {
		lines = append(lines, bin)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return lines, nil
}

func stripCodexParley(dir string) error {
	if err := stripParleyHooks(filepath.Join(dir, "hooks.json")); err != nil {
		return err
	}
	return removeTomlTable(filepath.Join(dir, "config.toml"), "mcp_servers.parley")
}

func stripCursorParley(home string) error {
	return deleteJSONKey(filepath.Join(home, ".cursor", "mcp.json"), "mcpServers", "parley")
}

func stripAntigravityParley(dir string) error {
	if err := deleteJSONKey(filepath.Join(dir, "mcp_config.json"), "mcpServers", "parley"); err != nil {
		return err
	}
	return deleteJSONKey(filepath.Join(dir, "hooks.json"), "parley")
}

// stripParleyHooks drops hook entries whose command is parley's. Other
// hooks in the same file stay.
func stripParleyHooks(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	data := map[string]any{}
	if err := json.Unmarshal(b, &data); err != nil {
		return fmt.Errorf("parse %s: %w (left unchanged)", path, err)
	}
	hooks, _ := data["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}
	for event, v := range hooks {
		arr, ok := v.([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(arr))
		for _, item := range arr {
			if jsonHasParleyHook(item) {
				continue
			}
			kept = append(kept, item)
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	return writeJSON(path, data)
}

// deleteJSONKey removes key, or key then sub, from a JSON object file.
// A missing file is left missing.
func deleteJSONKey(path string, key string, sub ...string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	data := map[string]any{}
	if err := json.Unmarshal(b, &data); err != nil {
		return fmt.Errorf("parse %s: %w (left unchanged)", path, err)
	}
	if len(sub) == 0 {
		delete(data, key)
	} else if top, ok := data[key].(map[string]any); ok {
		delete(top, sub[0])
	}
	return writeJSON(path, data)
}

func writeJSON(path string, data any) error {
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// removeTomlTable deletes one [header] table. The rest of the file stays.
func removeTomlTable(path, header string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	s := string(b)
	start := tomlTableStart(s, header)
	if start < 0 {
		return nil
	}
	end := tomlTableEnd(s, start)
	s = s[:start] + s[end:]
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(s), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// removeParleyBinary removes the launcher link and the binary boot places
// in ~/.statefs-ai/bin. The rest of that directory, including recorded
// sessions, stays.
func removeParleyBinary(home string) error {
	link := filepath.Join(home, ".local", "bin", "parley")
	if err := removeLauncher(link); err != nil {
		return err
	}
	// Only a launcher under this home. A parley elsewhere on PATH is left
	// alone, so a test or another install is not removed.
	if p, err := exec.LookPath("parley"); err == nil && strings.HasPrefix(p, home+string(os.PathSeparator)) {
		if err := removeLauncher(p); err != nil {
			return err
		}
	}
	bin := filepath.Join(home, ".statefs-ai", "bin", "parley")
	if err := os.Remove(bin); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func removeLauncher(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return os.Remove(path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.Contains(string(b), "parley launcher") {
		return os.Remove(path)
	}
	fmt.Fprintf(os.Stderr, "%s is not the parley launcher; leaving it alone\n", path)
	return nil
}

func unhookCLIs(ctx context.Context) {
	if commandExists("claude") {
		cmd := exec.CommandContext(ctx, "claude", "plugin", "uninstall", "parley@parley", "--scope", "user", "-y")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "claude plugin uninstall: %v\n", err)
		}
	}
	if commandExists("grok") {
		cmd := exec.CommandContext(ctx, "grok", "mcp", "remove", "parley", "--scope", "user")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "grok mcp remove: %v\n", err)
		}
	}
	if commandExists("codex") {
		cmd := exec.CommandContext(ctx, "codex", "mcp", "remove", "parley")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "codex mcp remove: %v\n", err)
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func cmdSetup(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: parley setup <auto|claude|antigravity|grok>")
	}
	target := args[0]

	switch target {
	case "auto":
		if commandExists("claude") {
			if err := setupClaude(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "failed to setup claude: %v\n", err)
			}
		} else {
			fmt.Println("Claude Code not found, skipping Claude setup.")
			fmt.Println("note: inside Claude Code run  /plugin marketplace add quantumwake/parley  then  /plugin install parley@parley")
		}

		if commandExists("agy") || commandExists("antigravity") || hasAntigravityConfigDir() {
			if err := setupAntigravity(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "failed to setup antigravity: %v\n", err)
			}
		} else {
			fmt.Println("Antigravity CLI not found, skipping Antigravity setup.")
		}

		if commandExists("grok") || hasGrokConfig() {
			if err := setupGrok(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "failed to setup grok: %v\n", err)
			}
		} else {
			fmt.Println("Grok CLI not found, skipping Grok setup.")
		}
		return nil
	case "claude":
		return setupClaude(ctx)
	case "antigravity":
		return setupAntigravity(ctx)
	case "grok":
		return setupGrok(ctx)
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
	exec.CommandContext(ctx, "claude", "plugin", "marketplace", "add", repo).Run()
	cmd := exec.CommandContext(ctx, "claude", "plugin", "install", "parley@parley", "--scope", "user")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("claude plugin install failed: %w", err)
	}
	fmt.Println("Claude Code plugin parley@parley installed (restart Claude Code sessions to load it)")
	return nil
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

	// MCP only: parley hook still speaks Claude's JSON, not Antigravity's.
	fmt.Println("Registering Parley MCP for Antigravity CLI...")
	mcpPath := filepath.Join(configDir, "mcp_config.json")
	if err := updateJSON(mcpPath, "mcpServers", "parley", map[string]any{
		"command": exe,
		"args":    []string{"mcp"},
	}); err != nil {
		return fmt.Errorf("failed to update mcp_config.json: %w", err)
	}
	fmt.Println("Parley MCP registered for Antigravity CLI (hooks deferred until an adapter exists)")
	return nil
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

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
		return fmt.Errorf("usage: parley setup <auto|claude|antigravity>")
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
		}
		
		if commandExists("agy") || commandExists("antigravity") || hasAntigravityConfigDir() {
			if err := setupAntigravity(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "failed to setup antigravity: %v\n", err)
			}
		} else {
			fmt.Println("Antigravity CLI not found, skipping Antigravity setup.")
		}
		return nil
	case "claude":
		return setupClaude(ctx)
	case "antigravity":
		return setupAntigravity(ctx)
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

	fmt.Println("Registering Parley for Antigravity CLI...")

	mcpPath := filepath.Join(configDir, "mcp_config.json")
	if err := updateJSON(mcpPath, "mcpServers", "parley", map[string]any{
		"command": "parley",
		"args":    []string{"mcp"},
	}); err != nil {
		return fmt.Errorf("failed to update mcp_config.json: %w", err)
	}

	hooksPath := filepath.Join(configDir, "hooks.json")
	hookCmd := map[string]any{
		"type":    "command",
		"command": "parley hook",
		"timeout": 10,
	}
	toolHook := map[string]any{
		"matcher": "*",
		"hooks":   []any{hookCmd},
	}
	
	if err := updateJSON(hooksPath, "parley", "PreToolUse", []any{toolHook}); err != nil {
		return fmt.Errorf("failed to update hooks.json (PreToolUse): %w", err)
	}
	if err := updateJSON(hooksPath, "parley", "PostToolUse", []any{toolHook}); err != nil {
		return fmt.Errorf("failed to update hooks.json (PostToolUse): %w", err)
	}
	if err := updateJSON(hooksPath, "parley", "PreInvocation", []any{hookCmd}); err != nil {
		return fmt.Errorf("failed to update hooks.json (PreInvocation): %w", err)
	}
	if err := updateJSON(hooksPath, "parley", "PostInvocation", []any{hookCmd}); err != nil {
		return fmt.Errorf("failed to update hooks.json (PostInvocation): %w", err)
	}
	if err := updateJSON(hooksPath, "parley", "Stop", []any{hookCmd}); err != nil {
		return fmt.Errorf("failed to update hooks.json (Stop): %w", err)
	}

	fmt.Println("Parley successfully registered for Antigravity CLI.")
	return nil
}

func updateJSON(path string, topKey string, subKey string, value any) error {
	data := make(map[string]any)
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &data)
	}

	var topMap map[string]any
	if existing, ok := data[topKey].(map[string]any); ok {
		topMap = existing
	} else {
		topMap = make(map[string]any)
	}

	topMap[subKey] = value
	data[topKey] = topMap

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

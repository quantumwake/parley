package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quantumwake/parley/pkg/plugin"
)

func TestUninstallStripsParleyAndLeavesTheRest(t *testing.T) {
	home := t.TempDir()
	if err := plugin.InstallSkillsUnder(home); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(home, ".claude", "skills", "arm", "SKILL.md")
	if err := os.WriteFile(foreign, []byte("---\nname: arm\ndescription: mine\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	codexHooks := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(codexHooks), 0o755); err != nil {
		t.Fatal(err)
	}
	hooks := `{
  "hooks": {
    "Stop": [
      {"hooks": [{"command": "/usr/bin/parley hook --event Stop"}]},
      {"hooks": [{"command": "/usr/bin/other"}]}
    ]
  }
}`
	if err := os.WriteFile(codexHooks, []byte(hooks), 0o644); err != nil {
		t.Fatal(err)
	}
	tomlPath := filepath.Join(home, ".codex", "config.toml")
	toml := "model = \"x\"\n\n[mcp_servers.parley]\ncommand = \"parley\"\nargs = [\"mcp\"]\n\n[mcp_servers.other]\ncommand = \"other\"\n"
	if err := os.WriteFile(tomlPath, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	agy := filepath.Join(home, ".gemini", "config")
	if err := os.MkdirAll(agy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agy, "mcp_config.json"), []byte(`{"mcpServers":{"parley":{"command":"parley"},"other":{"command":"other"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agy, "hooks.json"), []byte(`{"parley":{"Stop":[]},"kept":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions := filepath.Join(home, ".statefs-ai", "spool", "session.json")
	if err := os.MkdirAll(filepath.Dir(sessions), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessions, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".statefs-ai", "bin", "parley")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".local", "bin", "parley")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}

	var unhooked bool
	if err := runUninstall(t.Context(), home, false, func(context.Context) { unhooked = true }); err != nil {
		t.Fatal(err)
	}
	if unhooked {
		t.Fatal("a dry run called the host CLIs")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatal("a dry run removed the binary")
	}
	if b, err := os.ReadFile(sessions); err != nil || string(b) != "keep" {
		t.Fatalf("sessions after dry run: %v %q", err, b)
	}

	if err := runUninstall(t.Context(), home, true, func(context.Context) { unhooked = true }); err != nil {
		t.Fatal(err)
	}
	if !unhooked {
		t.Fatal("--yes did not reach the host CLI step")
	}

	if b, err := os.ReadFile(foreign); err != nil || strings.Contains(string(b), "author: parley") {
		t.Fatalf("foreign skill: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(home, ".grok", "skills", "disarm", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("parley disarm skill still installed")
	}
	got, err := os.ReadFile(codexHooks)
	if err != nil || strings.Contains(string(got), "parley hook") || !strings.Contains(string(got), "other") {
		t.Fatalf("codex hooks: %v %s", err, got)
	}
	tomlGot, err := os.ReadFile(tomlPath)
	if err != nil || strings.Contains(string(tomlGot), "mcp_servers.parley") || !strings.Contains(string(tomlGot), "mcp_servers.other") {
		t.Fatalf("toml: %v %s", err, tomlGot)
	}
	mcp, err := os.ReadFile(filepath.Join(agy, "mcp_config.json"))
	if err != nil || strings.Contains(string(mcp), "parley") || !strings.Contains(string(mcp), "other") {
		t.Fatalf("mcp: %v %s", err, mcp)
	}
	agyHooks, err := os.ReadFile(filepath.Join(agy, "hooks.json"))
	if err != nil || strings.Contains(string(agyHooks), "parley") || !strings.Contains(string(agyHooks), "kept") {
		t.Fatalf("agy hooks: %v %s", err, agyHooks)
	}
	if b, err := os.ReadFile(sessions); err != nil || string(b) != "keep" {
		t.Fatalf("sessions: %v %q", err, b)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("launcher link still present")
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Fatal("binary still present")
	}
}

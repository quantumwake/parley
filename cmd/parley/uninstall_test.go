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

	cursorMCP := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(cursorMCP), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cursorMCP, []byte(`{"mcpServers":{"parley":{"command":"parley","args":["mcp"]},"other":{"command":"other"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	grokHooks := filepath.Join(home, ".grok", "hooks", "parley.json")
	if err := os.MkdirAll(filepath.Dir(grokHooks), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(grokHooks, []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"command":"/bin/parley hook --event PreToolUse"}]},{"hooks":[{"command":"/usr/bin/other"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cursorHooks := filepath.Join(home, ".cursor", "hooks.json")
	if err := os.WriteFile(cursorHooks, []byte(`{"version":1,"hooks":{"preToolUse":[{"command":"/bin/parley hook"},{"command":"/usr/bin/other"}],"afterFileEdit":[{"command":"./format.sh"}]}}`), 0o644); err != nil {
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
	if _, err := os.Stat(filepath.Join(home, ".cursor", "skills", "arm", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("parley arm skill still installed for Cursor")
	}
	cursorGot, err := os.ReadFile(cursorMCP)
	if err != nil || strings.Contains(string(cursorGot), "parley") || !strings.Contains(string(cursorGot), "other") {
		t.Fatalf("cursor mcp: %v %s", err, cursorGot)
	}
	grokGot, err := os.ReadFile(grokHooks)
	if err != nil || strings.Contains(string(grokGot), "parley") || !strings.Contains(string(grokGot), "/usr/bin/other") {
		t.Fatalf("grok hooks: %v %s", err, grokGot)
	}
	cursorHookGot, err := os.ReadFile(cursorHooks)
	if err != nil || strings.Contains(string(cursorHookGot), "parley") || !strings.Contains(string(cursorHookGot), "/usr/bin/other") || !strings.Contains(string(cursorHookGot), "afterFileEdit") {
		t.Fatalf("cursor hooks: %v %s", err, cursorHookGot)
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

func TestUninstallLeavesAStatusLineThatOnlyMentionsParley(t *testing.T) {
	home := t.TempDir()
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := `{"enabledPlugins":{"parley@parley":true},"statusLine":{"type":"command","command":"~/src/parley/scripts/my-statusline.sh"}}`
	if err := os.WriteFile(settings, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	grok := filepath.Join(home, ".grok", "config.toml")
	if err := os.MkdirAll(filepath.Dir(grok), 0o755); err != nil {
		t.Fatal(err)
	}
	grokBody := "[ui]\ntheme = \"oscura\"\n\n[ui.status_line]\ntype = \"command\"\ncommand = \"~/src/parley/scripts/my-statusline.sh\"\n"
	if err := os.WriteFile(grok, []byte(grokBody), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := uninstallPlan(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		if strings.Contains(line, "status") {
			t.Fatalf("plan listed a foreign status line: %v", lines)
		}
	}
	if err := stripParleyStatusLine(settings); err != nil {
		t.Fatal(err)
	}
	if err := stripGrokStatusLine(grok); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(settings)
	if err != nil || string(got) != foreign {
		t.Fatalf("claude settings changed: %v %s", err, got)
	}
	got, err = os.ReadFile(grok)
	if err != nil || string(got) != grokBody {
		t.Fatalf("grok config changed: %v %s", err, got)
	}

	ours := `{"statusLine":{"type":"command","command":"/Users/x/.local/bin/parley statusline"},"model":"x"}`
	if err := os.WriteFile(settings, []byte(ours), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(grok, []byte("[ui.status_line]\ntype = \"command\"\ncommand = \"parley statusline\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err = uninstallPlan(home)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "parley status line in "+settings) || !strings.Contains(joined, "table [ui.status_line] in "+grok) {
		t.Fatalf("plan missed parley's status line: %v", lines)
	}
	if err := applyUninstall(home); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(settings)
	if err != nil || strings.Contains(string(got), "statusLine") || !strings.Contains(string(got), "model") {
		t.Fatalf("parley status line stayed: %v %s", err, got)
	}
	got, err = os.ReadFile(grok)
	if err != nil || strings.Contains(string(got), "status_line") {
		t.Fatalf("grok status line stayed: %v %s", err, got)
	}
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrokMcpAddArgs(t *testing.T) {
	got := grokMcpAddArgs("/opt/statefs/bin/parley")
	want := []string{"mcp", "add", "--scope", "user", "parley", "--", "/opt/statefs/bin/parley", "mcp"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestCmdSetupUnknownAndUsage(t *testing.T) {
	if err := cmdSetup(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "usage: parley setup") {
		t.Fatalf("empty args: %v", err)
	}
	if err := cmdSetup(t.Context(), []string{"cursor"}); err == nil || !strings.Contains(err.Error(), "unknown setup target: cursor") {
		t.Fatalf("unknown: %v", err)
	}
}

func TestSetupGrokRequiresBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := setupGrok(t.Context())
	if err == nil || !strings.Contains(err.Error(), "grok CLI not found") {
		t.Fatalf("got %v", err)
	}
}

func TestUpdateJSONMergesAndLeavesMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp_config.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"other":{"command":"x"}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := updateJSON(path, "mcpServers", "parley", map[string]any{"command": "/bin/parley", "args": []string{"mcp"}}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	servers := got["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatalf("lost existing server: %s", b)
	}
	parley := servers["parley"].(map[string]any)
	if parley["command"] != "/bin/parley" {
		t.Fatalf("parley command=%v", parley["command"])
	}

	bad := filepath.Join(dir, "hooks.json")
	orig := []byte("{not json")
	if err := os.WriteFile(bad, orig, 0644); err != nil {
		t.Fatal(err)
	}
	if err := updateJSON(bad, "parley", "PreToolUse", []any{}); err == nil {
		t.Fatal("expected unmarshal error")
	}
	after, err := os.ReadFile(bad)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(orig) {
		t.Fatalf("malformed file was rewritten: %s", after)
	}
}

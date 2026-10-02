package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runArm runs `parley arm` for a session with the host variables given and
// answers what it printed.
func runArm(t *testing.T, vars map[string]string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STATEFS_AI_DATA", filepath.Join(home, "data"))
	t.Setenv("PARLEY_SESSION", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "aaaaaaaa-1111")
	for _, v := range []string{"CLAUDECODE", "GROK_SESSION_ID", "CODEX_THREAD_ID", "CURSOR_CONVERSATION_ID"} {
		t.Setenv(v, vars[v])
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	runErr := cmdArm(nil)
	os.Stdout = stdout
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("parley arm: %v", runErr)
	}
	return string(out)
}

// Claude Code stops a background task at 2 hours, so `parley arm` there
// names a wait that ends first. A host that only runs Claude's plugin keeps
// a wait with no deadline.
func TestArmNamesTheWaitForItsHost(t *testing.T) {
	claude := runArm(t, map[string]string{"CLAUDECODE": "1"})
	if !strings.Contains(claude, "parley wait -timeout 110m") || !strings.Contains(claude, "7200000") || strings.Contains(claude, "-timeout 0") {
		t.Fatalf("Claude: %q", claude)
	}

	grok := runArm(t, map[string]string{"CLAUDECODE": "1", "GROK_SESSION_ID": "g1"})
	if !strings.Contains(grok, "parley wait -timeout 0") || strings.Contains(grok, "110m") {
		t.Fatalf("Grok running Claude's plugin: %q", grok)
	}

	plain := runArm(t, nil)
	if !strings.Contains(plain, "parley wait -timeout 0") {
		t.Fatalf("no host: %q", plain)
	}
}

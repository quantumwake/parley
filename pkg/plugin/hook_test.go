package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quantumwake/statefs.ai/pkg/enroll"
)

func run(t *testing.T, env Env, in map[string]any) Output {
	t.Helper()
	t.Setenv("STATEFS_AI_CONFIG", filepath.Join(t.TempDir(), "config.json")) // never the user's real config
	b, _ := json.Marshal(in)
	var out bytes.Buffer
	if err := Handle(context.Background(), env, bytes.NewReader(b), &out); err != nil {
		t.Fatal(err)
	}

	var o Output
	if err := json.Unmarshal(out.Bytes(), &o); err != nil {
		t.Fatalf("stdout is not JSON: %s", out.String())
	}

	return o
}

func TestSessionStartAutoEnrolls(t *testing.T) {
	dir := enroll.NewFakeDirectory()
	defer dir.Close()
	tmp := t.TempDir()
	env := Env{EnrollURL: dir.EnrollURL("laptop-agent"), IdentityPath: filepath.Join(tmp, "identity"), DataDir: tmp}

	o := run(t, env, map[string]any{"hook_event_name": "SessionStart", "session_id": "s1", "cwd": "/repo"})
	if !strings.Contains(o.AdditionalContext, `enrolled this machine as "laptop-agent"`) || !strings.Contains(o.AdditionalContext, "verified the token exchange") {
		t.Fatalf("context: %s", o.AdditionalContext)
	}

	if o.HookSpecificOutput["hookEventName"] != "SessionStart" {
		t.Fatalf("hookSpecificOutput: %v", o.HookSpecificOutput)
	}

	// Second start: already enrolled, no token needed.
	env.EnrollURL = ""
	o = run(t, env, map[string]any{"hook_event_name": "SessionStart", "session_id": "s2"})
	if !strings.Contains(o.AdditionalContext, `enrolled as "laptop-agent"`) {
		t.Fatalf("second start: %s", o.AdditionalContext)
	}

	log, _ := os.ReadFile(filepath.Join(tmp, "hooks.log"))
	if strings.Count(string(log), "SessionStart") != 2 {
		t.Fatalf("hooks.log should hold two SessionStart lines:\n%s", log)
	}
}

func TestSessionStartWithoutEnrollmentAsksTheUser(t *testing.T) {
	tmp := t.TempDir()
	o := run(t, Env{IdentityPath: filepath.Join(tmp, "identity"), DataDir: tmp}, map[string]any{"hook_event_name": "SessionStart"})
	if !strings.Contains(o.AdditionalContext, "not enrolled") || !strings.Contains(o.AdditionalContext, "statefs-ai enroll") {
		t.Fatalf("context: %s", o.AdditionalContext)
	}
}

func TestRejectedTokenIsReported(t *testing.T) {
	dir := enroll.NewFakeDirectory()
	defer dir.Close()
	tmp := t.TempDir()
	env := Env{EnrollURL: dir.URL() + "/enroll?token=en_bogus", IdentityPath: filepath.Join(tmp, "identity"), DataDir: tmp}
	o := run(t, env, map[string]any{"hook_event_name": "SessionStart"})
	if !strings.Contains(o.AdditionalContext, "rejected") {
		t.Fatalf("context: %s", o.AdditionalContext)
	}
}

func TestOtherHooksAreLoggedQuietly(t *testing.T) {
	tmp := t.TempDir()
	o := run(t, Env{IdentityPath: filepath.Join(tmp, "identity"), DataDir: tmp},
		map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_use_id": "toolu_1"})
	if o.AdditionalContext != "" {
		t.Fatalf("PostToolUse must not inject context: %s", o.AdditionalContext)
	}

	log, _ := os.ReadFile(filepath.Join(tmp, "hooks.log"))
	if !strings.Contains(string(log), `"tool":"Bash"`) {
		t.Fatalf("hooks.log: %s", log)
	}
}

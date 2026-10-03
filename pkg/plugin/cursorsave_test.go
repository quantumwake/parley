package plugin

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unwritableSession makes the session's own directory read-only, the way a
// full disk refuses every write: a cursor save then fails.
func unwritableSession(t *testing.T, env Env) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory")
	}
	dir := filepath.Join(sessionsDir(env), env.Session)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// A failed write leaves no temp file behind.
func TestAFailedWriteLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "record")
	if err := os.MkdirAll(filepath.Join(target, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(target, map[string]int{"cursor": 3}); err == nil {
		t.Fatal("renaming over a directory fails")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("left %s", e.Name())
		}
	}
}

// A wait that cannot save where it read up to prints the posts and ends
// saying why, not with a quiet exit 0 the next wait repeats (devcloud-2).
func TestAWaitThatCannotSaveItsCursorSaysSo(t *testing.T) {
	clearHosts(t)
	a, b, _ := workSessions(t)
	InjectHold(context.Background(), b)
	post(t, a, "question", "who owns the indexer?", "")
	unwritableSession(t, b)

	var out bytes.Buffer
	err := Wait(context.Background(), b, nil, 5*time.Second, &out)
	if err == nil || !strings.Contains(err.Error(), "cannot save where this session read up to in issues") {
		t.Fatalf("the wait ends with why: %v", err)
	}
	if !strings.Contains(out.String(), "who owns the indexer") {
		t.Fatalf("the posts are still printed: %q", out.String())
	}
}

// The prompt hook says it too, so the agent learns why posts repeat.
func TestAPromptThatCannotSaveItsCursorSaysSo(t *testing.T) {
	a, b, _ := workSessions(t)
	InjectHold(context.Background(), b)
	post(t, a, "question", "who owns the indexer?", "")
	unwritableSession(t, b)

	text, _ := InjectHold(context.Background(), b)
	if !strings.Contains(text, "who owns the indexer") || !strings.Contains(text, "parley: cannot save where this session read up to in issues") {
		t.Fatalf("text: %s", text)
	}
}

// parley read reports it as an error.
func TestAReadThatCannotSaveItsCursorFails(t *testing.T) {
	a, b, _ := workSessions(t)
	InjectHold(context.Background(), b)
	post(t, a, "comment", "hello", "")
	unwritableSession(t, b)

	var out bytes.Buffer
	if err := Read(context.Background(), b, "issues", -1, false, 0, &out); err == nil || !strings.Contains(err.Error(), "cannot save where this session read up to") {
		t.Fatalf("read: %v", err)
	}
}

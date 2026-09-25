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

// Antigravity capture goes through the same opt-in as Claude: with no author
// and no STATEFS_AI_STORE, a SessionStart starts no daemon, so nothing from
// the transcript is recorded. With the opt-in, the same payload does.
func TestAntigravityRecordsNothingWithoutOptIn(t *testing.T) {
	_, b := gateEnv(t)
	spyPresence(t)
	b.Self = "/usr/bin/true"
	b.IdentityPath = filepath.Join(t.TempDir(), "no-identity") // no author: the session never opted in
	b.HookEvent = "SessionStart"
	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	payload := `{"conversationId":"agy-c1","transcriptPath":"` + tp + `","workspacePaths":["/w"]}`
	daemonFiles := func() []string {
		var got []string
		for _, f := range []string{"daemon-agy-c1.lock", "daemon-agy-c1.pid"} {
			if _, err := os.Stat(filepath.Join(b.DataDir, f)); err == nil {
				got = append(got, f)
			}
		}
		return got
	}

	t.Setenv("STATEFS_AI_STORE", "")
	if a := authorOf(b); a != "" {
		t.Fatalf("this rig has an author %q; the test needs none", a)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := Handle(ctx, b, strings.NewReader(payload), &out); err != nil {
		t.Fatal(err)
	}
	if got := daemonFiles(); len(got) != 0 {
		t.Fatalf("a daemon was started without opt-in: %v", got)
	}

	t.Setenv("STATEFS_AI_STORE", filepath.Join(t.TempDir(), "store"))
	out.Reset()
	if err := Handle(ctx, b, strings.NewReader(payload), &out); err != nil {
		t.Fatal(err)
	}
	if got := daemonFiles(); len(got) == 0 {
		t.Fatal("with the opt-in, the same Antigravity SessionStart did not try to start a daemon")
	}
}

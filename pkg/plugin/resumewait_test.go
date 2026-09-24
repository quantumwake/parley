package plugin

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A session that is resumed after an upgrade or a restart used to be told
// only the periodic advice, a turn later, which reads as background noise —
// so a seat that had been listening for hours came back deaf and nobody
// noticed until a post went unanswered (owner, 2026-09-24). The first thing
// a resumed session reads should say it plainly.
func TestAResumedSessionIsToldItsListenerIsGone(t *testing.T) {
	_, b := gateEnv(t)

	// No wait was ever armed for this session: nothing to say.
	if got := sessionStart(context.Background(), b); strings.Contains(got, "had a listener armed") {
		t.Fatalf("a session that never listened is not nagged: %q", got)
	}

	// A wait ran and is gone, which is exactly what a restart leaves behind.
	started := time.Now().Add(-2 * time.Hour)
	if err := writeJSONFile(waitFile(b), WaitState{PID: 4321, StartedMs: started.UnixMilli()}); err != nil {
		t.Fatal(err)
	}

	got := sessionStart(context.Background(), b)
	if !strings.Contains(got, "had a listener armed before it restarted") {
		t.Fatalf("a resumed session is told its listener is gone: %q", got)
	}
	if !strings.Contains(got, "wait` as a background shell task before anything else") {
		t.Fatalf("and what to do about it: %q", got)
	}
	if !strings.Contains(got, started.Format(time.RFC3339)[:13]) {
		t.Fatalf("and when it was last listening, so a stale file is recognisable: %q", got)
	}
}

// A live wait is covered by WaitLive in resumedWait's first line; it is not
// tested here because faking a live wait means taking two file locks and a
// fresh heartbeat, and a test that skips when it cannot proves nothing.

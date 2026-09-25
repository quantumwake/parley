package plugin

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

// A poller handover: the wait holding the identity lock returns, its bells
// stop before the lock is released, and the next wait to take the lock
// opens its own. At no point are two sets open, and the switch-on that
// happened under the first poller is not re-applied twice.
func TestAPollerHandoverStopsTheOldBellsBeforeTheNewOnesOpen(t *testing.T) {
	a := followIssues(t)
	bs := withBellStore(t, a, 40*time.Millisecond)
	t.Setenv("PARLEY_FEATURES", "")
	os.Unsetenv("PARLEY_FEATURES")
	if _, err := SetFeature("doorbell", true); err != nil {
		t.Fatal(err)
	}

	run := func() (cancel context.CancelFunc, done chan struct{}) {
		ctx, c := context.WithCancel(context.Background())
		done = make(chan struct{})
		go func() {
			_ = Wait(ctx, a, nil, time.Minute, &bytes.Buffer{})
			close(done)
		}()
		return c, done
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				r, s := bs.counts()
				t.Fatalf("%s: rung %d stopped %d", what, r, s)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	cancelA, doneA := run()
	waitFor("the first poller opens bells", func() bool { r, _ := bs.counts(); return r > 0 })
	rungA, _ := bs.counts()

	cancelA()
	<-doneA
	// The old poller's bells are all stopped once it has returned.
	if r, s := bs.counts(); s < rungA || r != rungA {
		t.Fatalf("after the first poller returned: rung %d stopped %d", r, s)
	}

	cancelB, doneB := run()
	defer func() { cancelB(); <-doneB }()
	waitFor("the second poller opens its own bells", func() bool { r, _ := bs.counts(); return r > rungA })
	r, s := bs.counts()
	if open := r - s; open > rungA {
		t.Fatalf("two sets of bells open after the handover: rung %d stopped %d (open %d, one set is %d)", r, s, open, rungA)
	}
	if r != 2*rungA {
		t.Fatalf("the second poller opened %d bells, the first opened %d", r-rungA, rungA)
	}
}

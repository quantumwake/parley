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

	// A stop that yields lets the next poller run if the lock was released
	// first. The real order waits out that stop before releasing the lock,
	// so B's first ring still comes after every one of A's stops.
	bs.stopDelay = 80 * time.Millisecond

	cancelA, doneA := run()
	waitFor("the first poller opens bells", func() bool { r, _ := bs.counts(); return r > 0 })
	rungA, _ := bs.counts()

	// B is already waiting on the lock while A still holds it. A non-poller
	// rings nothing (#90).
	cancelB, doneB := run()
	defer func() { cancelB(); <-doneB }()
	time.Sleep(100 * time.Millisecond)
	if r, _ := bs.counts(); r != rungA {
		t.Fatalf("the non-poller rang: rung %d, the poller had rung %d", r, rungA)
	}

	cancelA()
	<-doneA
	waitFor("the second poller opens its own bells", func() bool { r, _ := bs.counts(); return r > rungA })
	waitFor("the first poller's bells have stopped", func() bool { _, s := bs.counts(); return s >= rungA })

	seq := bs.trace()
	bFirst := -1
	rings := 0
	for i, e := range seq {
		if e != "ring" {
			continue
		}
		rings++
		if rings == rungA+1 {
			bFirst = i
			break
		}
	}
	if bFirst < 0 {
		t.Fatalf("the second poller never rang: %v", seq)
	}
	stopped := 0
	for _, e := range seq[:bFirst] {
		if e == "stop" {
			stopped++
		}
	}
	if stopped < rungA {
		t.Fatalf("B rang before A's bells stopped: %v", seq)
	}
	r, s := bs.counts()
	if open := r - s; open > rungA {
		t.Fatalf("two sets of bells open after the handover: rung %d stopped %d (open %d, one set is %d)", r, s, open, rungA)
	}
	if r != 2*rungA {
		t.Fatalf("the second poller opened %d bells, the first opened %d", r-rungA, rungA)
	}
}

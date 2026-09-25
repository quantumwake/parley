package plugin

import (
	"bytes"
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A poller handover: a second wait is already running while the first holds
// the identity lock. The first's bells stop before that lock moves, so the
// second cannot ring until every one of the first's stops has happened.
//
// beforePollerRelease widens the gap between those two steps. Swapping
// stopBells and poller.release in Wait's defer lets the second wait ring
// during that gap, and this test fails.
//
// The first wait ends because its lifetime elapsed. Cancelling its context
// stops the tails immediately, through the context the bells were opened
// with, and the defer's order is never what closed them.
func TestAPollerHandoverStopsTheOldBellsBeforeTheNewOnesOpen(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	follow(t, a, "issues")
	if err := Join(ctx, b, "issues", "full", "all", "", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	bs := withBellStore(t, a, 20*time.Millisecond)
	t.Setenv("PARLEY_FEATURES", "")
	os.Unsetenv("PARLEY_FEATURES")
	if _, err := SetFeature("doorbell", true); err != nil {
		t.Fatal(err)
	}

	var misses atomic.Int32
	var rungA int
	onPollerLockMiss = func() { misses.Add(1) }
	var once sync.Once
	beforePollerRelease = func() {
		once.Do(func() {
			// Long enough for a waiter that is already spinning to take
			// the lock, if the lock has already been released.
			deadline := time.Now().Add(400 * time.Millisecond)
			for time.Now().Before(deadline) {
				if r, _ := bs.counts(); r > rungA {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
	t.Cleanup(func() {
		beforePollerRelease = nil
		onPollerLockMiss = nil
	})

	run := func(env Env, lifetime time.Duration) (cancel context.CancelFunc, done chan struct{}) {
		wctx, c := context.WithCancel(context.Background())
		done = make(chan struct{})
		go func() {
			_ = Wait(wctx, env, nil, lifetime, &bytes.Buffer{})
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
				t.Fatalf("%s: rung %d stopped %d sequence %v", what, r, s, bs.sequence())
			}
			time.Sleep(15 * time.Millisecond)
		}
	}

	// Long enough for the second wait to be spinning before the first ends
	// on its own. No cancel: that would stop the tails before the defer.
	_, doneA := run(a, 1200*time.Millisecond)
	waitFor("the first poller opens bells", func() bool { r, _ := bs.counts(); return r > 0 })
	rungA, _ = bs.counts()
	held := misses.Load()

	cancelB, doneB := run(b, time.Minute)
	defer func() { cancelB(); <-doneB }()
	waitFor("the second wait is spinning on the lock", func() bool { return misses.Load() > held })
	if r, _ := bs.counts(); r != rungA {
		t.Fatalf("the non-poller opened bells while the first still held the lock: rung %d, first set %d", r, rungA)
	}

	<-doneA
	waitFor("the second poller opens its own bells", func() bool { r, _ := bs.counts(); return r > rungA })

	seq := bs.sequence()
	rings, stops := 0, 0
	for _, e := range seq {
		if e == "ring" {
			rings++
			if rings == rungA+1 {
				break
			}
		}
		if e == "stop" {
			stops++
		}
	}
	if rings < rungA+1 {
		t.Fatalf("the second poller never rang: %v", seq)
	}
	if stops < rungA {
		t.Fatalf("the second poller rang before the first poller's bells stopped: %v", seq)
	}
}

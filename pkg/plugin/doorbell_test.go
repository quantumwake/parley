package plugin

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/store"
)

// bellStore is a store that can be rung by hand: it is every other store
// this test suite uses, plus a doorbell whose bell the test controls.
type bellStore struct {
	store.Store

	mu      sync.Mutex
	rings   []chan struct{}
	rung    int
	stopped int
	refuse  bool // answer ErrNoDoorbell, as an old member's store would

	// stopDelay is how long a bell takes to stop after its context is
	// cancelled, as a tail that must close a connection does. events is the
	// order rings and stops happened in.
	stopDelay time.Duration
	events    []string
}

func (b *bellStore) Ring(ctx context.Context, ns string, from store.Position) (<-chan struct{}, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refuse {
		return nil, store.ErrNoDoorbell
	}

	ch := make(chan struct{}, 1)
	b.rings = append(b.rings, ch)
	b.rung++
	b.events = append(b.events, "ring")
	delay := b.stopDelay
	go func() {
		<-ctx.Done()
		time.Sleep(delay)
		b.mu.Lock()
		b.stopped++
		b.events = append(b.events, "stop")
		b.mu.Unlock()
		close(ch)
	}()

	return ch, nil
}

func (b *bellStore) ring() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.rings {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (b *bellStore) sequence() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.events...)
}

func (b *bellStore) counts() (rung, stopped int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rung, b.stopped
}

// withBellStore is withWaitStore's shape, for a store that can ring.
func withBellStore(t *testing.T, env Env, poll time.Duration) *bellStore {
	t.Helper()
	st, err := StoreFromEnv(env)
	if err != nil {
		t.Fatal(err)
	}

	bs := &bellStore{Store: st}
	prev, prevPoll, prevClaim, prevYield := waitStore, WaitPoll, waitClaimTimeout, waitYield
	waitStore = func(Env) (store.Store, error) { return bs, nil }
	WaitPoll, waitClaimTimeout, waitYield = poll, 5*time.Second, 300*time.Millisecond
	t.Cleanup(func() {
		waitStore, WaitPoll, waitClaimTimeout, waitYield = prev, prevPoll, prevClaim, prevYield
	})
	return bs
}

// The point: with a bell, a post wakes the wait when it lands rather than
// on the next poll. The poll here is five seconds, so a wait that returns
// in well under that returned because it was rung.
func TestADoorbellWakesTheWaitBeforeThePollWould(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Join(ctx, b, "issues", "full", "all", "", &out)
	bs := withBellStore(t, a, 5*time.Second)
	t.Setenv("PARLEY_FEATURES", "doorbell")

	done := make(chan time.Duration, 1)
	out.Reset()
	go func() {
		start := time.Now()
		_ = Wait(ctx, a, nil, time.Minute, &out)
		done <- time.Since(start)
	}()

	// Give the wait time to arm its bell, then post and ring.
	time.Sleep(300 * time.Millisecond)
	if err := Post(ctx, b, "issues", "question", "ring ring", "", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	bs.ring()

	select {
	case took := <-done:
		if took > 3*time.Second {
			t.Fatalf("the wait took %s with a 5s poll: it slept rather than being rung", took)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait never returned")
	}

	if !strings.Contains(out.String(), "ring ring") {
		t.Fatalf("the post was not delivered: %q", out.String())
	}
}

// The bell is a signal, not a delivery path: what reaches the agent comes
// from the scan, with its cursors and gates, so a ring with nothing behind
// it delivers nothing and the wait goes back to waiting.
func TestARingWithNoPostDeliversNothing(t *testing.T) {
	ctx := context.Background()
	a := followIssues(t)
	bs := withBellStore(t, a, 5*time.Second)
	t.Setenv("PARLEY_FEATURES", "doorbell")

	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- Wait(ctx, a, nil, 2*time.Second, &out) }()

	time.Sleep(300 * time.Millisecond)
	bs.ring()
	bs.ring()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait never returned")
	}

	// "no new posts" is the quiet ending; a delivery prints the
	// conversation's name in brackets, so that is what must be absent.
	if strings.Contains(out.String(), "[issues]") {
		t.Fatalf("a ring with nothing behind it delivered something: %q", out.String())
	}

	if !strings.Contains(out.String(), "still listening") {
		t.Fatalf("the wait did not run out its lifetime quietly: %q", out.String())
	}
}

// A store that cannot ring is not worse off: the wait polls, exactly as
// every parley before this one did.
func TestAStoreThatCannotRingStillDelivers(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Join(ctx, b, "issues", "full", "all", "", &out)
	bs := withBellStore(t, a, 50*time.Millisecond)
	t.Setenv("PARLEY_FEATURES", "doorbell")
	bs.refuse = true

	if err := Post(ctx, b, "issues", "question", "polled", "", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := Wait(ctx, a, nil, 10*time.Second, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "polled") {
		t.Fatalf("the poll did not deliver without a bell: %q", out.String())
	}
}

// Off is the DEFAULT, not a kill switch: a machine that has not opted in
// opens no tail at all and polls exactly as every parley before this one
// did. The owner's rule — "the default should stand".
func TestTheDoorbellIsOffUntilItIsTurnedOn(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Join(ctx, b, "issues", "full", "all", "", &out)
	bs := withBellStore(t, a, 50*time.Millisecond)
	// The DEFAULT, not the override: an empty PARLEY_FEATURES exercises
	// "the environment says none", and what has to be pinned is "nobody
	// has said anything at all". t.Setenv registers the cleanup; Unsetenv
	// then gives the real default for this test.
	t.Setenv("PARLEY_FEATURES", "")
	os.Unsetenv("PARLEY_FEATURES")

	if err := Post(ctx, b, "issues", "question", "still polled", "", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := Wait(ctx, a, nil, 10*time.Second, &out); err != nil {
		t.Fatal(err)
	}

	if rung, _ := bs.counts(); rung != 0 {
		t.Fatalf("the bell was rung %d times with the doorbell off", rung)
	}

	if !strings.Contains(out.String(), "still polled") {
		t.Fatalf("the poll did not deliver with the bell off: %q", out.String())
	}
}

// The poller reads the switch each round. A wait that started with the
// doorbell off opens tails when the config turns it on, and closes them
// when the config turns it off, without the process restarting. Deleting
// the reconcileBells call in the wait loop fails this test: the bell
// stays at zero after the switch comes on.
func TestThePollerRereadsTheDoorbellWhileItRuns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := followIssues(t)
	bs := withBellStore(t, a, 40*time.Millisecond)
	t.Setenv("PARLEY_FEATURES", "")
	os.Unsetenv("PARLEY_FEATURES")

	done := make(chan struct{})
	go func() {
		_ = Wait(ctx, a, nil, time.Minute, &bytes.Buffer{})
		close(done)
	}()

	// A few rounds with the switch off, so a bell here would be the
	// startup path rather than the re-read.
	time.Sleep(200 * time.Millisecond)
	if rung, _ := bs.counts(); rung != 0 {
		t.Fatalf("the bell opened before the switch: %d", rung)
	}

	if _, err := SetFeature("doorbell", true); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if rung, _ := bs.counts(); rung > 0 {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("turning the doorbell on did not open a bell without a restart")
		}

		time.Sleep(20 * time.Millisecond)
	}

	if _, err := SetFeature("doorbell", false); err != nil {
		t.Fatal(err)
	}

	rung, _ := bs.counts()
	deadline = time.Now().Add(3 * time.Second)
	for {
		_, stopped := bs.counts()
		if stopped >= rung {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("turning the doorbell off left bells open: stopped %d of %d", stopped, rung)
		}

		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(200 * time.Millisecond)
	if again, _ := bs.counts(); again != rung {
		t.Fatalf("bells opened after the switch went off: %d then %d", rung, again)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not return")
	}
}

// A wait that returns must not leave its tails open: every bell it started
// is stopped, or a session that re-arms all day holds a connection per arm
// against the member's stream cap.
func TestEveryBellStopsWhenTheWaitReturns(t *testing.T) {
	ctx := context.Background()
	a := followIssues(t)
	bs := withBellStore(t, a, 50*time.Millisecond)
	t.Setenv("PARLEY_FEATURES", "doorbell")

	var out bytes.Buffer
	if err := Wait(ctx, a, nil, 300*time.Millisecond, &out); err != nil {
		t.Fatal(err)
	}

	rung, stopped := bs.counts()
	if rung == 0 {
		t.Fatal("no bell was started at all")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, stopped = bs.counts(); stopped == rung || time.Now().After(deadline) {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	if stopped != rung {
		t.Fatalf("%d of %d bells were left open after the wait returned", rung-stopped, rung)
	}
}

// Every session on this machine shares one enrolled identity, and the
// poller lock is what makes the outbound scan one per identity rather
// than one per session; the rest are woken through the wake file. The
// bells have to obey the same rule. If every wait process opens its own
// tails, eight seats following six conversations hold forty-eight streams
// for six conversations' worth of rows - against a per-identity cap they
// all share, with the member shipping every row eight times to readers
// that drop them.
func TestOnlyThePollerRingsBells(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	follow(t, a, "issues")
	if err := Join(ctx, b, "issues", "full", "all", "", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	bs := withBellStore(t, a, 50*time.Millisecond)
	t.Setenv("PARLEY_FEATURES", "doorbell")

	// Both sessions wait at once, as two seats on one laptop do. One of
	// them takes the identity lock; which one is not the point.
	var wg sync.WaitGroup
	for _, env := range []Env{a, b} {
		wg.Add(1)
		go func(env Env) {
			defer wg.Done()
			var out bytes.Buffer
			_ = Wait(ctx, env, nil, 400*time.Millisecond, &out)
		}(env)
	}

	wg.Wait()

	// One conversation is followed, so one tail is the whole budget for
	// this identity however many sessions are waiting on it.
	if rung, _ := bs.counts(); rung != 1 {
		t.Fatalf("%d tails were opened for one conversation across two sessions; the poller's one is the budget", rung)
	}
}

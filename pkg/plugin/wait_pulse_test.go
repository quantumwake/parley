package plugin

import (
	"bytes"
	"context"
	"iter"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/store"
)

// hangingStore never answers a read of one conversation: the shape of a
// member whose connection the balancer dropped. Every other conversation
// reads through.
type hangingStore struct {
	store.Store
	hang string // namespace id
}

func (h hangingStore) Scan(ctx context.Context, ns string, from, to store.Position) iter.Seq2[event.Event, error] {
	if ns != h.hang {
		return h.Store.Scan(ctx, ns, from, to)
	}

	return func(yield func(event.Event, error) bool) {
		<-ctx.Done()
		yield(event.Event{}, ctx.Err())
	}
}

// One conversation that hangs must not hold up a post on another: the
// round delivers what came back within a poll and leaves the hung read
// running. Before, the round waited for every read, so one dead member
// made every conversation on the machine as slow as its timeout.
func TestOneHungConversationDoesNotDelayAnother(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	follow(t, a, "issues", "stuck")
	var out bytes.Buffer
	if err := Join(ctx, b, "issues", "full", "all", "", &out); err != nil {
		t.Fatal(err)
	}

	fs := withWaitStore(t, a)
	waitStore = func(Env) (store.Store, error) { return hangingStore{Store: fs, hang: mustID(t, a, "stuck")}, nil }

	if err := Post(ctx, b, "issues", "question", "still there?", "", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	done := make(chan time.Duration, 1)
	out.Reset()
	go func() {
		start := time.Now()
		_ = Wait(ctx, a, nil, time.Minute, &out)
		done <- time.Since(start)
	}()

	select {
	case took := <-done:
		if took > 2*time.Second {
			t.Fatalf("the post on the live conversation waited %s on the hung one", took)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait never returned: it waited for the hung conversation")
	}

	if !strings.Contains(out.String(), "still there?") {
		t.Fatalf("the post on the live conversation was not delivered: %q", out.String())
	}

	if w, ok := readWaitState(waitFile(a)); !ok || w.Unreadable["stuck"] != "" {
		t.Fatalf("a read that is merely still running is not a failure: %+v", w.Unreadable)
	}
}

// pulseStore is a store the poller can ask about its connections: every
// reset is recorded, and the beats answer what the test scripts.
type pulseStore struct {
	*bellStore
	mu     sync.Mutex
	resets []string
	beats  map[string]bool
	beaten int
}

func (p *pulseStore) Reset(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resets = append(p.resets, host)
}

func (p *pulseStore) Beat(context.Context, time.Duration, time.Duration) map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.beaten++
	return maps.Clone(p.beats)
}

func (p *pulseStore) answer(host string, there bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.beats[host] = there
}

func (p *pulseStore) reset() (all bool, n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, host := range p.resets {
		all = all || host == ""
	}

	return all, len(p.resets)
}

func (p *pulseStore) beatCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.beaten
}

func withPulseStore(t *testing.T, env Env, poll time.Duration) *pulseStore {
	t.Helper()
	ps := &pulseStore{bellStore: withBellStore(t, env, poll), beats: map[string]bool{}}
	waitStore = func(Env) (store.Store, error) { return ps, nil }
	return ps
}

// A member host missed twice running is written to the identity poller's
// state, which is what `parley status` reads to say "reconnecting
// <host>"; one that answers again is taken off.
func TestAHostMissedTwiceRunningIsRecordedAsReconnecting(t *testing.T) {
	ctx := context.Background()
	a := followIssues(t)
	ps := withPulseStore(t, a, 20*time.Millisecond)
	const host = "group-c-1.statefs.io"
	ps.answer(host, false)

	var out bytes.Buffer
	if err := Wait(ctx, a, nil, 400*time.Millisecond, &out); err != nil {
		t.Fatal(err)
	}

	if n := ps.beatCount(); n < 3 {
		t.Fatalf("the poller beats every round; only %d beats in 400ms of 20ms rounds", n)
	}

	w, ok := readWaitState(identityWaitFile(a))
	if !ok || len(w.Reconnecting) != 1 || w.Reconnecting[0] != host {
		t.Fatalf("a host missed twice running is recorded as reconnecting, got %+v", w.Reconnecting)
	}

	ps.answer(host, true)
	if err := Wait(ctx, a, nil, 200*time.Millisecond, &out); err != nil {
		t.Fatal(err)
	}

	if w, _ := readWaitState(identityWaitFile(a)); len(w.Reconnecting) != 0 {
		t.Fatalf("a host that answers again is no longer reconnecting, got %+v", w.Reconnecting)
	}
}

// A single miss is not reconnecting: a member can be slow once.
func TestOneMissIsNotReconnecting(t *testing.T) {
	hb := newHeartbeat()
	hb.misses["m"] = 1
	if got := hb.reconnecting(); len(got) != 0 {
		t.Fatalf("one miss is not reconnecting: %v", got)
	}

	hb.misses["m"] = 2
	if got := hb.reconnecting(); len(got) != 1 || got[0] != "m" {
		t.Fatalf("two misses running is: %v", got)
	}
}

// The laptop lid: the wall clock jumps, the monotonic clock does not.
// Every connection the process held is dead from the balancer's side,
// so the wait drops them all, scans at once instead of sleeping out its
// poll, and opens its doorbells again. The poll here is five seconds, so
// a post delivered well inside that was delivered because of the jump.
func TestAClockJumpResetsRescansAndReopensTheBells(t *testing.T) {
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	var out bytes.Buffer
	_ = CreateShared(ctx, a, "issues", "", nil, &out)
	_ = Join(ctx, a, "issues", "full", "all", "", &out)
	_ = Join(ctx, b, "issues", "full", "all", "", &out)
	ps := withPulseStore(t, a, 5*time.Second)
	t.Setenv("PARLEY_FEATURES", "doorbell")

	var asleep atomic.Int64 // how far the wall clock has jumped
	prevNow := waitNow
	waitNow = func() time.Time { return time.Now().Add(time.Duration(asleep.Load())) }
	t.Cleanup(func() { waitNow = prevNow })

	done := make(chan time.Duration, 1)
	out.Reset()
	go func() {
		start := time.Now()
		_ = Wait(ctx, a, nil, time.Minute, &out)
		done <- time.Since(start)
	}()

	// Armed and ringing; then the lid closes, a post lands, the lid opens.
	time.Sleep(300 * time.Millisecond)
	if err := Post(ctx, b, "issues", "question", "after the lid", "", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	asleep.Store(int64(time.Minute))

	select {
	case took := <-done:
		if took > 3*time.Second {
			t.Fatalf("the wait took %s with a 5s poll: it slept out the poll rather than scanning on the jump", took)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait never returned")
	}

	if !strings.Contains(out.String(), "after the lid") {
		t.Fatalf("the post was not delivered: %q", out.String())
	}

	if all, _ := ps.reset(); !all {
		t.Fatal("a clock jump drops every connection (Reset(\"\"))")
	}

	if rung, _ := ps.counts(); rung < 2 {
		t.Fatalf("a clock jump opens the doorbells again; they were rung %d time(s)", rung)
	}
}

// A slow round is not a jump: the wall and the monotonic clock agree.
func TestASlowRoundIsNotAClockJump(t *testing.T) {
	var clock clockWatch
	clock.rebase(waitNow().Round(0))
	time.Sleep(30 * time.Millisecond)
	if gap := clock.gap(waitNow().Round(0)); gap > 5*time.Millisecond || gap < -5*time.Millisecond {
		t.Fatalf("both clocks moved together, yet the gap is %s", gap)
	}
}

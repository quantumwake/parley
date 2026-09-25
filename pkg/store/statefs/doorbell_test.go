package statefs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// tailStore is a Store whose reconnect loop is driven by the test: the
// member it resolves and the way each stream ends are both scripted.
func tailStore(resolve resolveFunc, stream streamFunc) *Store {
	return &Store{resolve: resolve, stream: stream}
}

// `moved` ends a stream cleanly, exactly as an expired ticket does - the
// client returns nil for both (statefs client/events.go). A namespace
// that answers `moved` the instant it is opened would therefore spin this
// loop with no sleep at all, reconnecting as fast as the member can
// refuse: the reconnect storm the fault path was written to prevent,
// reached through the path that looks like success.
func TestAStreamThatEndsCleanlyWithoutLivingDoesNotSpin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	opened := 0
	st := tailStore(
		func(context.Context, string) (string, error) { return "member-a", nil },
		func(context.Context, string, string, int64, func()) (int64, error) {
			mu.Lock()
			opened++
			mu.Unlock()
			return 0, nil // clean, and instant: the shape of `moved`
		},
	)

	ch := make(chan struct{}, 1)
	go st.tail(ctx, "issues", 0, ch)

	time.Sleep(300 * time.Millisecond)
	cancel()

	mu.Lock()
	n := opened
	mu.Unlock()

	// The first backoff is a second, so 300ms buys the opening attempt
	// and nothing else. Anything more than a couple means it is spinning.
	if n > 2 {
		t.Fatalf("the tail reopened %d times in 300ms; a clean end that did not live must back off", n)
	}
}

// A stream that lived out its ticket is the normal shape, and must come
// straight back: that is the whole point of the bell.
func TestAStreamThatLivedReconnectsAtOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	opened := 0
	st := tailStore(
		func(context.Context, string) (string, error) { return "member-a", nil },
		func(ctx context.Context, _, _ string, _ int64, _ func()) (int64, error) {
			mu.Lock()
			opened++
			mu.Unlock()
			return 0, nil
		},
	)

	// A stream that lasted longer than liveStream, without the test
	// waiting that long for it.
	prev := liveStreamFor
	liveStreamFor = func(time.Time) time.Duration { return liveStream + time.Second }
	t.Cleanup(func() { liveStreamFor = prev })

	ch := make(chan struct{}, 1)
	go st.tail(ctx, "issues", 0, ch)

	time.Sleep(150 * time.Millisecond)
	cancel()

	mu.Lock()
	n := opened
	mu.Unlock()

	if n < 5 {
		t.Fatalf("a stream that lived reopened only %d times in 150ms; it should come straight back", n)
	}
}

// A namespace moves when its member loses leadership. A tail pinned to
// the member it started with reconnects to the old one for as long as the
// wait runs - the poll still covers the session, so nothing is lost and
// nothing is said, which is how a bell quietly stops working.
func TestTheTailResolvesTheMemberAgainOnEveryReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var asked []string
	moved := false

	st := tailStore(
		func(context.Context, string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if moved {
				return "member-b", nil
			}

			return "member-a", nil
		},
		func(_ context.Context, member, _ string, _ int64, _ func()) (int64, error) {
			mu.Lock()
			asked = append(asked, member)
			moved = true // leadership moves after the first stream
			mu.Unlock()
			return 0, errors.New("the member closed the stream")
		},
	)

	ch := make(chan struct{}, 1)
	go st.tail(ctx, "issues", 0, ch)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(asked)
		mu.Unlock()
		if n >= 2 {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	mu.Lock()
	got := append([]string(nil), asked...)
	mu.Unlock()

	if len(got) < 2 {
		t.Fatalf("the tail only opened %d streams; the test needs a reconnect to observe", len(got))
	}

	if got[1] != "member-b" {
		t.Fatalf("after the namespace moved the tail reconnected to %q, not the member that serves it now", got[1])
	}
}

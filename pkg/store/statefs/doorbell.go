package statefs

// doorbell.go — the live tail as a signal (statefs RFC-0025 step 4).
//
// A wait used to sleep WaitPoll between rounds, so a post reached an idle
// agent up to two seconds after it was durable, and every session paid a
// full authenticated read every two seconds for the privilege — the load
// class that killed a member once (statefs incident 0001).
//
// The member can now hold a connection open and say when rows land
// (GET /api/v1/state/{ns}/events). This uses it as a DOORBELL and nothing
// more: the tail's rows are dropped on the floor, and the scan the wait
// already had does the delivering. Cursors, gates, the fan-out across the
// sessions sharing one identity and the delivery lock all stay where they
// are, because a streaming delivery path would be a second copy of them.
//
// The cost, stated: the member sends each row's bytes to a reader that
// discards them, and the scan then reads the same rows again. For parley's
// posts that is cheap. If it ever is not, the answer is a head-only tail
// on the member, not a second delivery path here.

import (
	"context"
	"errors"
	"time"

	"github.com/quantumwake/parley/pkg/store"
	sfs "github.com/quantumwake/statefs/client"
	"github.com/quantumwake/statefs/pkg/types"
)

// ringBuffer is how many "look now" signals may be outstanding. One is
// enough: the reader re-scans from its own cursor and sees everything that
// has landed, so a second signal while it is already looking says nothing
// new. Non-blocking sends onto a buffer of one is what keeps the tail from
// ever waiting for the reader.
const ringBuffer = 1

// The reconnect loop is the part of this file with rules of its own -
// which end is normal, when to sleep, when to resolve again - and it
// cannot be reached through an httptest server without also faking the
// directory and the grant endpoint. These two seams let a test drive the
// loop directly. Both default to the real calls in New; nothing else
// assigns them.
type (
	resolveFunc func(ctx context.Context, ns string) (string, error)
	streamFunc  func(ctx context.Context, member, ns string, from int64, onBatch func()) (int64, error)
)

// liveStreamFor is how long the stream that began at t lasted. A test
// replaces it to age a stream without waiting for one.
var liveStreamFor = time.Since

// liveStream is how long a stream must last for its end to count as the
// ticket simply running out. Anything shorter ended for a reason that
// reconnecting at once will hit again.
const liveStream = 5 * time.Second

// Ring implements store.Doorbell.
func (s *Store) Ring(ctx context.Context, ns string, from store.Position) (<-chan struct{}, error) {
	// Resolved once here so a namespace that cannot be reached at all is
	// reported to the caller rather than retried silently in the
	// background; the tail resolves again on every reconnect.
	if _, err := s.resolve(ctx, ns); err != nil {
		return nil, err
	}

	ch := make(chan struct{}, ringBuffer)
	go s.tail(ctx, ns, int64(from), ch)
	return ch, nil
}

// tail follows the namespace and rings on every batch, reconnecting until
// the caller's context ends. It closes ch when it gives up, so a reader
// selecting on it is told rather than left waiting for a bell that will
// never ring again.
func (s *Store) tail(ctx context.Context, ns string, from int64, ch chan<- struct{}) {
	defer close(ch)

	backoff := time.Second
	for ctx.Err() == nil {
		// Resolved every time round: a namespace moves when its member
		// loses leadership, and a tail pinned to the member it started
		// with would reconnect to the old one until the wait restarts.
		// The poll still covers the session, so the cost of getting this
		// wrong is the bell going quiet after every failover with nothing
		// said - the kind of thing that is only ever noticed as "the
		// doorbell does not seem to work any more".
		member, err := s.resolve(ctx, ns)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}

			backoff = nextBackoff(backoff)
			continue
		}

		began := time.Now()
		at, err := s.stream(ctx, member, ns, from, func() {
			// The rows are deliberately dropped: what the reader needs
			// is "look now", and looking is the scan's job.
			select {
			case ch <- struct{}{}:
			default: // a signal is already waiting; one is enough
			}
		})
		if at > from {
			from = at
		}

		if ctx.Err() != nil {
			return
		}

		// A member that does not serve a tail is not a failure: the caller
		// still has its poll, and telling it once is better than a
		// reconnect loop against a member that will never answer.
		if errors.Is(err, sfs.ErrEventsUnsupported) {
			return
		}

		if err == nil {
			// A clean end is usually the ticket expiring after its five
			// minutes, which is the normal shape: reconnect at once, from
			// where it got to. But `moved` ends a stream cleanly too, and
			// a namespace that answers `moved` the moment it is opened
			// would spin this loop with no sleep at all, as fast as the
			// member can refuse. So a stream only earns an immediate
			// reconnect by having lived.
			if liveStreamFor(began) >= liveStream {
				backoff = time.Second
				continue
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}

			backoff = nextBackoff(backoff)
			continue
		}

		// A fault: back off so a member that is refusing (at its stream
		// cap, over its budget) is not hammered by every session at once.
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff = nextBackoff(backoff)
	}
}

// backoffMax is as long as a tail ever waits before trying again. The
// poll covers the session throughout, so this only decides how quickly
// the bell comes back, never whether a post is seen.
const backoffMax = 30 * time.Second

func nextBackoff(d time.Duration) time.Duration {
	if d *= 2; d > backoffMax {
		return backoffMax
	}

	return d
}

// events is the real stream: the client's live tail, with the rows handed
// to onBatch and nothing else done with them.
func (s *Store) events(ctx context.Context, member, ns string, from int64, onBatch func()) (int64, error) {
	return s.c.Events(ctx, member, ns, sfs.EventsOptions{From: from},
		func(_ int64, _ []types.Record) error {
			onBatch()
			return nil
		})
}

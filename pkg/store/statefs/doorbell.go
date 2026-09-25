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

// Ring implements store.Doorbell.
func (s *Store) Ring(ctx context.Context, ns string, from store.Position) (<-chan struct{}, error) {
	member, err := s.readURL(ctx, ns)
	if err != nil {
		return nil, err
	}

	ch := make(chan struct{}, ringBuffer)
	go s.tail(ctx, member, ns, int64(from), ch)
	return ch, nil
}

// tail follows the namespace and rings on every batch, reconnecting until
// the caller's context ends. It closes ch when it gives up, so a reader
// selecting on it is told rather than left waiting for a bell that will
// never ring again.
func (s *Store) tail(ctx context.Context, member, ns string, from int64, ch chan<- struct{}) {
	defer close(ch)

	backoff := time.Second
	for ctx.Err() == nil {
		at, err := s.c.Events(ctx, member, ns, sfs.EventsOptions{From: from},
			func(_ int64, _ []types.Record) error {
				// The rows are deliberately dropped: what the reader needs
				// is "look now", and looking is the scan's job.
				select {
				case ch <- struct{}{}:
				default: // a signal is already waiting; one is enough
				}

				return nil
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
			// A clean end is the ticket expiring, which is the normal
			// shape: reconnect at once, from where it got to.
			backoff = time.Second
			continue
		}

		// A fault: back off so a member that is refusing (at its stream
		// cap, over its budget) is not hammered by every session at once.
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

package plugin

// doorbell.go — waking on a post instead of asking every two seconds.
//
// `parley wait` polls: every WaitPoll it scans each followed conversation
// for rows this session has not seen. That is why a post reaches an idle
// agent up to two seconds after it was written, and why every session pays
// a full authenticated read every two seconds whether or not anything
// happened.
//
// A store that can signal (store.Doorbell — statefs serves it as a live
// tail, RFC-0025) lets the wait sleep until something actually lands. The
// bell is a SIGNAL only: it says "look now", and the scan the wait already
// had does the looking, so cursors, gates, the fan-out across sessions and
// the delivery lock are untouched. A store without one, a member too old
// to serve one, or PARLEY_WAIT_DOORBELL=off, and the poll is exactly what
// it always was.

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/quantumwake/parley/pkg/store"
)

// doorbellOff answers whether the operator has turned the bell off. The
// poll is then the only path, which is the behaviour every parley before
// this one had.
func doorbellOff() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PARLEY_WAIT_DOORBELL"))) {
	case "off", "0", "false", "no":
		return true
	}

	return false
}

// startBells opens a doorbell per followed conversation and merges them
// into one channel. It answers nil when there is nothing to listen to —
// no store support, no subscriptions, or the bell turned off — and the
// caller then simply waits out its poll, as it always did.
//
// The returned stop must be called: it ends every tail and the goroutines
// merging them, so a wait that returns does not leave a connection open.
func startBells(ctx context.Context, env Env, st store.Store, subs []Subscription) (<-chan struct{}, func()) {
	nothing := func() {}
	if doorbellOff() || len(subs) == 0 {
		return nil, nothing
	}

	bell, ok := st.(store.Doorbell)
	if !ok {
		return nil, nothing
	}

	ctx, cancel := context.WithCancel(ctx)
	out := make(chan struct{}, 1)
	var wg sync.WaitGroup
	rung := 0
	for _, s := range subs {
		if s.ID == "" {
			continue
		}

		ch, err := bell.Ring(ctx, s.ID, store.Position(s.Cursor))
		if err != nil || ch == nil {
			// One conversation without a bell does not cost the others
			// theirs; this one is covered by the poll.
			logLine(env, "doorbell", s.Name+": "+errText(err))
			continue
		}

		rung++
		wg.Add(1)
		go func(ch <-chan struct{}) {
			defer wg.Done()
			for range ch {
				// Never block on the reader: it re-scans from its own
				// cursor, so one pending "look now" says everything a
				// second one would.
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}(ch)
	}

	if rung == 0 {
		cancel()
		return nil, nothing
	}

	return out, func() {
		cancel()
		wg.Wait()
		close(out)
	}
}

func errText(err error) string {
	if err == nil {
		return "no doorbell"
	}

	return err.Error()
}

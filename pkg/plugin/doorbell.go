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
// the delivery lock are untouched.
//
// It is OFF until someone turns it on — `parley enable doorbell`, or
// PARLEY_FEATURES=doorbell (features.go). The owner's rule for every new
// path: "the default should stand". So a machine that has not opted in
// polls exactly as it always did, and a member too old to serve a tail
// leaves an opted-in machine polling too.

import (
	"context"
	"os"
	"slices"
	"sync"

	"github.com/quantumwake/parley/pkg/store"
)

// doorbellOn answers whether this machine has opted in. Off is the
// default and is what parley has always done.
//
// PARLEY_FEATURES, when set, is the whole answer for this process, as it
// is for every other feature. Otherwise the answer is the config file,
// read now rather than the copy taken when the process started: `parley
// enable doorbell` writes that file, and the identity poller is a wait
// that may already be running. A snapshot would leave the doorbell off
// until that process happened to restart.
func doorbellOn(env Env) bool {
	if raw, ok := os.LookupEnv("PARLEY_FEATURES"); ok {
		return slices.Contains(splitFeatures(raw), "doorbell")
	}

	c, err := ReadConfig()
	if err != nil {
		return slices.Contains(env.Features, "doorbell")
	}

	return slices.Contains(c.Features, "doorbell")
}

// reconcileBells opens the tails when the switch is on and closes them
// when it is off, for a poller that is already running. armed remembers
// that this on-stretch was already tried, so a member with no tail is not
// asked again every round; turning the switch off and on tries once more.
func reconcileBells(ctx context.Context, env Env, st store.Store, bell <-chan struct{}, stop func(), armed bool) (<-chan struct{}, func(), bool) {
	if stop == nil {
		stop = func() {}
	}

	if !doorbellOn(env) {
		if bell != nil {
			stop()
		}

		return nil, func() {}, false
	}

	if bell != nil || armed {
		return bell, stop, true
	}

	next, nextStop := startBells(ctx, env, st, Subscriptions(env))
	return next, nextStop, true
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
	if !doorbellOn(env) || len(subs) == 0 {
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

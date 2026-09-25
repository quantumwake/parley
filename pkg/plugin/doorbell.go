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
	"sort"
	"strings"
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
// when it is off, for a poller that is already running. opened is the set
// of conversation ids the current tails cover. The same set is not opened
// again, so a member with no tail is not asked every round. A different
// set — a session joined, left, or a waiter arrived — stops the old tails
// and opens the new ones. Turning the switch off forgets the set, so
// turning it on tries once more.
func reconcileBells(ctx context.Context, env Env, st store.Store, bell <-chan struct{}, stop func(), opened string) (<-chan struct{}, func(), string) {
	if stop == nil {
		stop = func() {}
	}

	if !doorbellOn(env) {
		if bell != nil {
			stop()
		}

		return nil, func() {}, ""
	}

	subs := bellSubscriptions(env)
	key := bellSetKey(subs)
	if key == opened {
		return bell, stop, opened
	}

	if bell != nil {
		stop()
	}

	if len(subs) == 0 {
		return nil, func() {}, key
	}

	next, nextStop := startBells(ctx, env, st, subs)
	return next, nextStop, key
}

// bellSubscriptions is every conversation the poller scans: the union of
// each live waiter's subscriptions, one entry per conversation. The
// poller's own session is not special. Two sessions on one conversation
// share a tail, started from the earlier cursor, so a row either of them
// has not seen still wakes the scan.
func bellSubscriptions(env Env) []Subscription {
	byID := map[string]Subscription{}
	for _, sid := range waiterSessions(env) {
		senv := envForSession(env, sid)
		prev, _ := readWaitState(waitFile(senv))
		subs, err := waitSet(context.Background(), senv, prev.Names)
		if err != nil {
			continue
		}

		for _, s := range subs {
			if s.ID == "" {
				continue
			}

			have, ok := byID[s.ID]
			if !ok || s.Cursor < have.Cursor {
				byID[s.ID] = s
			}
		}
	}

	out := make([]Subscription, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func bellSetKey(subs []Subscription) string {
	ids := make([]string, 0, len(subs))
	for _, s := range subs {
		if s.ID != "" {
			ids = append(ids, s.ID)
		}
	}

	sort.Strings(ids)
	return strings.Join(ids, ",")
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

package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A Claude Code session receives nothing while it is idle: hooks only run
// around turns. What does wake an idle session is a background shell task
// finishing, so `parley wait` is built to be that task: it blocks until a
// followed conversation has a post from someone else, prints it, and exits.
// The agent handles the posts and starts it again.
//
// A wait that cannot reach the directory must not pass for a quiet
// channel, so it also exits when reading fails: at once when the directory
// refuses the identity, and after WaitMaxFailures failed rounds or
// WaitNoSuccess without a successful one otherwise. Each round records its
// outcome in wait.json, which the Stop hook and `parley status` read.

// WaitAdvice tells an agent how to be woken. It is printed after join, and
// said at session start when the machine follows anything.
const WaitAdvice = "to be woken when someone posts, run `parley wait` as a background shell task (Bash run_in_background); it exits with the new posts, so run it again after handling them"

// WaitPoll is how often wait checks the conversations.
var WaitPoll = 2 * time.Second

// waitStore opens the store a wait reads; tests put a failing one here.
var waitStore = StoreFromEnv

// Wait blocks until at least one followed conversation (all of them, or
// those named) has rows this session has not seen and did not write, then
// prints them and advances this session's cursors. It returns after
// lifetime with a line asking to be re-armed; lifetime 0 listens until a
// post or a failure. A read failure it cannot ride out is returned as an
// error, so the background task exits non-zero and the agent is told.
func Wait(ctx context.Context, env Env, names []string, lifetime time.Duration, w io.Writer) error {
	subs, err := waitSet(ctx, env, names)
	if err != nil {
		return err
	}

	st, err := waitStore(env)
	if err != nil {
		return err
	}

	token := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	release, err := claimWait(ctx, env, token)
	if err != nil {
		return err
	}
	defer release()

	started := time.Now()
	state := WaitState{PID: os.Getpid(), StartedMs: started.UnixMilli()}
	record := func() { _ = writeJSONFile(waitFile(env), state) }
	record()

	var deadline <-chan time.Time
	if lifetime > 0 {
		timer := time.NewTimer(lifetime)
		defer timer.Stop()
		deadline = timer.C
	}

	failures := 0
	lastOk := started
	for {
		// A newer wait for this session has asked for the lock.
		if replacedBy(env, token) {
			fmt.Fprintln(w, "replaced by a newer `parley wait` for this session; nothing to do.")
			return nil
		}

		items, ran, rerr := pendingRound(ctx, env, st, subs)
		switch {
		case rerr != nil:
			failures++
			state.LastError = rerr.Error()
		case ran:
			failures, lastOk = 0, time.Now()
			state.LastOkMs, state.LastError = lastOk.UnixMilli(), ""
		}

		state.Positions = positions(env, subs)
		record()

		if len(items) > 0 {
			for _, it := range items {
				fmt.Fprintf(w, "[%s] %s\n", it.sub.Name, formatPost(it.e, it.sub.Name, it.pos-1, 0))
			}

			fmt.Fprintf(w, "%d new posts. Handle them, then run `parley wait` in the background again.\n", len(items))
			return nil
		}

		if rerr != nil && refusedForGood(rerr) {
			tenant := env.Tenant
			if tenant == "" {
				tenant = "(the identity's default)"
			}

			return fmt.Errorf("wait: the directory refused this identity, so nothing can be delivered: %w; identity file %s, tenant %s. Fix the identity, then run `parley wait` again", rerr, env.IdentityPath, tenant)
		}

		if rerr != nil && (failures >= WaitMaxFailures || time.Since(lastOk) >= WaitNoSuccess) {
			return fmt.Errorf("wait: lost the directory after %d failed checks (last success %s ago): %w. Run `parley wait` again once it is reachable", failures, time.Since(lastOk).Round(time.Second), rerr)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			fmt.Fprintf(w, "still listening after %s, no new posts. Run `parley wait` in the background again to keep listening.\n", lifetime)
			return nil
		case <-time.After(WaitPoll):
		}

		// Re-read cursors each round: this session may have read the
		// conversation by hand while waiting.
		if subs, err = waitSet(ctx, env, names); err != nil {
			return err
		}
	}
}

// claimWait makes this the session's only wait. A wait already running is
// asked to stop through wait.claim, which only a claiming wait writes, and
// exits at its next round; the lock it held passes to this one.
func claimWait(ctx context.Context, env Env, token string) (release func(), err error) {
	dir := waitDir(env)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	lock := filepath.Join(dir, "wait.lock")
	f, err := tryLock(lock)
	if errors.Is(err, errLocked) {
		_ = os.WriteFile(filepath.Join(dir, "wait.claim"), []byte(token), 0o600)

		deadline := time.Now().Add(3*WaitPoll + time.Second)
		for errors.Is(err, errLocked) && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}

			f, err = tryLock(lock)
		}
	}

	if errors.Is(err, errLocked) {
		return nil, errors.New("wait: another `parley wait` for this session did not hand over; stop it and run again")
	}

	if err != nil {
		return func() {}, nil // no locking on this filesystem: run unguarded
	}

	// This wait owns the session now; any claim is answered.
	_ = os.Remove(filepath.Join(dir, "wait.claim"))
	return func() { f.Close() }, nil
}

// replacedBy reports whether another wait (a different token) has claimed
// this session's lock.
func replacedBy(env Env, token string) bool {
	b, err := os.ReadFile(filepath.Join(waitDir(env), "wait.claim"))
	claim := strings.TrimSpace(string(b))
	return err == nil && claim != "" && claim != token
}

// positions is each followed conversation's cursor for this session.
func positions(env Env, subs []Subscription) map[string]int64 {
	out := make(map[string]int64, len(subs))
	for _, s := range subs {
		out[s.Name] = s.Cursor
		if cur, ok := readSession(env, s.Name); ok {
			out[s.Name] = cur.Cursor
		}
	}

	return out
}

// waitSet is the subscriptions to wait on.
func waitSet(ctx context.Context, env Env, names []string) ([]Subscription, error) {
	subs := Subscriptions(env)
	if len(subs) == 0 {
		return nil, errors.New("wait: not following any conversation; join one first")
	}

	if len(names) == 0 {
		return subs, nil
	}

	var out []Subscription
	for _, name := range names {
		found := false
		for _, s := range subs {
			if s.Name == name || s.ID == name {
				out = append(out, s)
				found = true
			}
		}

		if !found {
			return nil, fmt.Errorf("wait: not following %q; join it first", name)
		}
	}

	return out, nil
}

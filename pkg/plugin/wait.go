package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A Claude Code session receives nothing while it is idle: hooks only run
// around turns. What does wake an idle session is a background shell task
// finishing, so `parley wait` is built to be that task: it blocks until a
// followed conversation has a post from someone else, prints it, and exits.
// The agent handles the posts and starts it again.
//
// A wait that cannot read must not pass for a quiet channel, so it also
// exits to say so: at once when the directory refuses a read, and after
// WaitMaxFailures failed rounds or WaitNoSuccess of failing otherwise. The
// failure is judged per conversation: one unreadable conversation is
// reported once, and the others keep being delivered. Each round records
// its outcome in wait.json, which the Stop hook and `parley status` read.

// WaitAdvice tells an agent how to be woken. It is printed after join, and
// said at session start when the machine follows anything.
const WaitAdvice = "to be woken when someone posts, run `parley wait` as a background shell task (Bash run_in_background); it exits with the new posts, so run it again after handling them"

// WaitPoll is how often wait checks the conversations.
var WaitPoll = 2 * time.Second

// waitStore opens the store a wait reads; tests put a failing one here.
var waitStore = StoreFromEnv

// waitClaimTimeout bounds how long a new wait waits for the old one to hand
// over: longer than the delivery lock's wait plus a round of reads.
var waitClaimTimeout = 15 * time.Second

// waitYield is how long a wait that saw a claim waits for the claimer to
// take the lock before deciding the claimer is gone and carrying on.
var waitYield = 2 * time.Second

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
	lock, err := claimWait(ctx, env, token)
	if err != nil {
		return err
	}
	defer func() { lock.release() }()

	// Conversations an earlier wait already reported unreadable are not
	// reported again while they stay unreadable, so re-arming after a
	// report does not wake the agent over and over.
	prev, _ := readWaitState(waitFile(env))
	state := WaitState{PID: os.Getpid(), StartedMs: time.Now().UnixMilli(), Reported: prev.Reported}
	record := func() { _ = writeJSONFile(waitFile(env), state) }
	record()

	var deadline <-chan time.Time
	if lifetime > 0 {
		timer := time.NewTimer(lifetime)
		defer timer.Stop()
		deadline = timer.C
	}

	failures := 0                          // consecutive rounds in which every conversation failed
	failingSince := map[string]time.Time{} // first failure of each conversation's current streak
	for {
		// A newer wait for this session asked to take over.
		if claim := readClaim(env); claim != "" && claim != token {
			if replaced, err := lock.yield(ctx, env, claim); err != nil || replaced {
				return replacedOr(w, err)
			}
		}

		items, ran, failed := pendingRound(ctx, env, st, subs)
		now := time.Now()
		allFailed := ran && len(failed) == len(subs)
		switch {
		case !ran:
			state.LastError = "delivery lock busy: another delivery for this session is running"
		case allFailed:
			failures++
			state.LastError = describeFailures(failed)
		default:
			failures = 0
			state.LastOkMs = now.UnixMilli()
			state.LastError = describeFailures(failed)
		}

		if ran {
			for name := range failingSince {
				if _, still := failed[name]; !still {
					delete(failingSince, name)
				}
			}

			for name := range failed {
				if failingSince[name].IsZero() {
					failingSince[name] = now
				}
			}

			state.Reported = stillFailing(state.Reported, subs, failed)
			state.Unreadable = describeEach(failed)
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

		if report := toReport(env, failed, failingSince, state.Reported, now, allFailed && failures >= WaitMaxFailures); len(report) > 0 {
			state.Reported = append(state.Reported, report...)
			sort.Strings(state.Reported)
			record()
			return waitFailure(env, failed, report, allFailed)
		}

		// Sleep until the next round, watching for a newer wait's claim.
		next := time.After(WaitPoll)
		check := time.NewTicker(min(100*time.Millisecond, WaitPoll))
	sleep:
		for {
			select {
			case <-ctx.Done():
				check.Stop()
				return ctx.Err()
			case <-deadline:
				check.Stop()
				fmt.Fprintf(w, "still listening after %s, no new posts. Run `parley wait` in the background again to keep listening.\n", lifetime)
				return nil
			case <-next:
				break sleep
			case <-check.C:
				if claim := readClaim(env); claim != "" && claim != token {
					if replaced, err := lock.yield(ctx, env, claim); err != nil || replaced {
						check.Stop()
						return replacedOr(w, err)
					}
				}
			}
		}
		check.Stop()

		// Re-read cursors each round: this session may have read the
		// conversation by hand while waiting.
		if subs, err = waitSet(ctx, env, names); err != nil {
			return err
		}
	}
}

// toReport answers the failing conversations the agent should now be told
// about and has not been: refused outright, failing for WaitNoSuccess, or
// all of them failing for WaitMaxFailures rounds.
func toReport(env Env, failed map[string]error, since map[string]time.Time, reported []string, now time.Time, allDown bool) []string {
	var out []string
	for name, err := range failed {
		if hasName(reported, name) {
			continue
		}

		if allDown || refusedForGood(env, err) || now.Sub(since[name]) >= WaitNoSuccess {
			out = append(out, name)
		}
	}

	sort.Strings(out)
	return out
}

// waitFailure is the error a wait exits with.
func waitFailure(env Env, failed map[string]error, report []string, allFailed bool) error {
	detail := make([]string, 0, len(report))
	refused := true
	for _, name := range report {
		detail = append(detail, fmt.Sprintf("%s: %v", name, failed[name]))
		refused = refused && refusedForGood(env, failed[name])
	}

	switch {
	case allFailed && refused:
		tenant := env.Tenant
		if tenant == "" {
			tenant = "(the identity's default)"
		}

		return fmt.Errorf("wait: every followed conversation was refused, so nothing can be delivered (%s); identity file %s, tenant %s. Fix the identity or its access, then run `parley wait` again", strings.Join(detail, "; "), env.IdentityPath, tenant)
	case allFailed:
		return fmt.Errorf("wait: lost the directory, no conversation could be read (%s). Run `parley wait` again once it is reachable", strings.Join(detail, "; "))
	default:
		return fmt.Errorf("wait: cannot read %s. The other conversations are still followed; ask for access or `parley leave` it, then run `parley wait` again (it is not reported again while it stays unreadable)", strings.Join(detail, "; "))
	}
}

func describeFailures(failed map[string]error) string {
	names := make([]string, 0, len(failed))
	for name := range failed {
		names = append(names, name)
	}

	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%s: %v", name, failed[name])
	}

	return strings.Join(parts, "; ")
}

func describeEach(failed map[string]error) map[string]string {
	if len(failed) == 0 {
		return nil
	}

	out := make(map[string]string, len(failed))
	for name, err := range failed {
		out[name] = err.Error()
	}

	return out
}

// stillFailing drops from the reported conversations those this round read
// successfully: one that reads again may be reported again the next time it
// fails. Conversations this wait does not read keep their mark.
func stillFailing(reported []string, read []Subscription, failed map[string]error) []string {
	var out []string
	for _, name := range reported {
		_, failing := failed[name]
		waited := false
		for _, s := range read {
			waited = waited || s.Name == name
		}

		if failing || !waited {
			out = append(out, name)
		}
	}

	return out
}

// replacedOr is how a wait ends after yielding its lock: with the error that
// stopped it, or saying it was replaced.
func replacedOr(w io.Writer, err error) error {
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "replaced by a newer `parley wait` for this session; nothing to do.")
	return nil
}

func hasName(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}

	return false
}

// waitLock is a session's wait lock, held by the live wait.
type waitLock struct {
	path string
	f    *os.File
}

func (l *waitLock) release() {
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// yield answers a newer wait's claim: it lets the lock go and reports
// replaced once some other wait really holds it. A claimer that does not
// take it within waitYield is gone (killed, or interrupted), so this wait
// takes the lock back, clears that claim and carries on. A lock that can
// neither be handed over nor taken back ends the wait with an error rather
// than letting it run unguarded.
func (l *waitLock) yield(ctx context.Context, env Env, claim string) (replaced bool, err error) {
	if l.f == nil {
		return true, nil // running without a lock: nothing to hand over
	}

	l.release()
	until := time.Now().Add(waitYield)
	for time.Now().Before(until) && readClaim(env) == claim {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Confirm the handover: another wait holds the lock on every try. A
	// single miss can be `parley status` or the Stop hook probing it.
	for try := 0; try < 3; try++ {
		f, err := tryLock(l.path)
		switch {
		case err == nil:
			l.f = f
			clearClaim(env, claim)
			return false, nil
		case !errors.Is(err, errLocked):
			return false, fmt.Errorf("wait: could not take the session's wait lock back: %w", err)
		}

		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(70 * time.Millisecond):
		}
	}

	return true, nil
}

// claimWait makes this the session's only wait. A wait already running is
// asked to hand over through wait.claim and lets the lock go at its next
// check; this one takes it. A claim is cleared only by the wait that wrote
// it, or by the wait that decided its writer is gone.
func claimWait(ctx context.Context, env Env, token string) (*waitLock, error) {
	if err := os.MkdirAll(waitDir(env), 0o700); err != nil {
		return nil, err
	}

	lock := &waitLock{path: filepath.Join(waitDir(env), "wait.lock")}
	f, err := tryLock(lock.path)
	claimed := false
	if errors.Is(err, errLocked) {
		claimed = os.WriteFile(claimFile(env), []byte(token), 0o600) == nil
		until := time.Now().Add(waitClaimTimeout)
		for errors.Is(err, errLocked) && time.Now().Before(until) {
			select {
			case <-ctx.Done():
				if claimed {
					clearClaim(env, token)
				}

				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}

			f, err = tryLock(lock.path)
		}
	}

	if claimed {
		clearClaim(env, token)
	}

	if errors.Is(err, errLocked) {
		return nil, errors.New("wait: another `parley wait` for this session did not hand over; stop it and run again")
	}

	if err != nil {
		return lock, nil // no locking on this filesystem: run unguarded
	}

	lock.f = f
	return lock, nil
}

func claimFile(env Env) string { return filepath.Join(waitDir(env), "wait.claim") }

func readClaim(env Env) string {
	b, err := os.ReadFile(claimFile(env))
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(b))
}

// clearClaim removes wait.claim if it still holds token.
func clearClaim(env Env, token string) {
	if readClaim(env) == token {
		_ = os.Remove(claimFile(env))
	}
}

// positions is each waited conversation's cursor after the round, read back
// from what the round saved.
func positions(env Env, waited []Subscription) map[string]int64 {
	out := make(map[string]int64, len(waited))
	for _, s := range Subscriptions(env) {
		for _, w := range waited {
			if w.Name == s.Name {
				out[s.Name] = s.Cursor
			}
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

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
	"sync"
	"time"

	"github.com/quantumwake/parley/pkg/conversation"
	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/store"
)

// A Claude Code session receives nothing while it is idle: hooks only run
// around turns. What does wake an idle session is a background shell task
// finishing, so `parley wait` is built to be that task: it blocks until a
// followed conversation has a post from someone else, prints it, and exits.
// The agent handles the posts and starts it again.
//
// Several sessions share one enrolled identity. One process holds the
// identity poller lock. Outbound it is one Scan per followed namespace,
// from the furthest-behind waiter cursor; locally it fans rows out to each
// session's cursor, handle and gates. Other waiters block on a wake file.
// The process that called `parley wait` still exits in that session, so
// each CLI wakes on its own posts. A second wait in the same session
// replaces that session's waiter, not another session's.
//
// A wait that cannot read must not pass for a quiet channel, so it also
// exits to say so: at once when the directory refuses a read (401/403),
// and after WaitMaxFailures failed rounds or WaitNoSuccess of failing
// otherwise — except a transient network error (DNS, dial, timeout).
// Those are the laptop lid: the process stays up, backs off, and keeps
// the cursor. `--on-unreachable=exit` restores the old lost-directory
// exit. The failure is judged per conversation: one unreadable
// conversation is reported once, and the others keep being delivered.
// Each round records its outcome in wait.json, which the Stop hook and
// `parley status` read.

// WaitAdvice tells an agent how to be woken. It is printed after join, and
// said at session start when the machine follows anything.
const WaitAdvice = "to be woken when someone posts, run `parley wait` as a background shell task (Bash run_in_background); it exits with the new posts, so run it again after handling them"

// ClaudeWaitTimeout is the wait a Claude Code session arms. Claude Code
// stops a background task at 30 minutes by default and 2 hours at most,
// and says not to restart one it stopped, so a wait with no deadline dies
// while the seat is idle and stays dead. A wait that ends on its own
// before the cap asks to be re-armed instead.
const ClaudeWaitTimeout = "110m"

// ClaudeBashTimeout is the Bash timeout, in milliseconds, that lets a
// ClaudeWaitTimeout wait run out: the 2 hour maximum.
const ClaudeBashTimeout = "7200000"

// claudeWaitAdvice is WaitAdvice for a Claude Code session.
const claudeWaitAdvice = "to be woken when someone posts, run `parley wait -timeout " + ClaudeWaitTimeout + "` as a background shell task (Bash run_in_background, timeout " + ClaudeBashTimeout + "; Claude Code stops background tasks at 2 hours); it exits with the new posts, or after " + ClaudeWaitTimeout + " with none, so run it again either way"

// OnClaude says this process runs under Claude Code itself, not Grok,
// Codex or Cursor, which can run Claude's plugin and set its variables.
func OnClaude() bool {
	if os.Getenv("CLAUDECODE") != "1" {
		return false
	}
	for _, other := range []string{"GROK_SESSION_ID", "CODEX_THREAD_ID", "CURSOR_CONVERSATION_ID"} {
		if os.Getenv(other) != "" {
			return false
		}
	}
	return true
}

// WaitAdviceFor is WaitAdvice for a host: Claude Code arms a wait that
// ends before its background-task cap, the others one with no deadline.
func WaitAdviceFor(claude bool) string {
	if claude {
		return claudeWaitAdvice
	}
	return WaitAdvice
}

// WaitCommandFor is the command an agent on this host starts to listen.
func WaitCommandFor(cmd string, claude bool) string {
	if claude {
		return cmd + " wait -timeout " + ClaudeWaitTimeout
	}
	return cmd + " wait -timeout 0"
}

// rearm is how a wait's own exit line names the next wait.
func rearm() string {
	return rearmFor(OnClaude())
}

// seatOnClaude says the session's own wait runs under Claude Code, as its
// wait recorded. A session with no recorded wait is this process's host.
func seatOnClaude(env Env) bool {
	if w, ok := readWaitState(waitFile(env)); ok && w.PID != 0 {
		return w.Claude
	}
	return OnClaude()
}

// rearmFor names the next wait for a seat on Claude Code or elsewhere.
func rearmFor(claude bool) string {
	if claude {
		return "`parley wait -timeout " + ClaudeWaitTimeout + "` (Bash run_in_background, timeout " + ClaudeBashTimeout + ")"
	}
	return "`parley wait`"
}

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

// WaitBeatEvery is how often the poller makes sure of a member host it
// has not heard from, and WaitBeatDeadline how long it gives the answer.
// A host the scan heard from within WaitBeatEvery is not asked at all, so
// in the normal case the beat sends nothing (heartbeat below).
var (
	WaitBeatEvery    = 15 * time.Second
	WaitBeatDeadline = 3 * time.Second
)

// scanReadTimeout bounds one conversation's read in a round: the whole of
// its pages, which is what the client's own timeout bounded per page.
var scanReadTimeout = 30 * time.Second

// scanParallel is how many conversations the poller reads at once. More
// than the hook path's pendingParallel, because a read here can be left
// running past its round (scanner below): a member that hangs holds a
// slot for scanReadTimeout, and the conversations on the other members
// must still have one.
const scanParallel = 8

// Wait blocks until at least one followed conversation (all of them, or
// those named) has rows this session has not seen and did not write, then
// prints them and advances this session's cursors. It returns after
// lifetime with a line asking to be re-armed; lifetime 0 listens until a
// post or a failure. A read failure it cannot ride out is returned as an
// error, so the background task exits non-zero and the agent is told.
func Wait(ctx context.Context, env Env, names []string, lifetime time.Duration, w io.Writer) error {
	// A wait consumes what it prints, so it must be able to be heard
	// before it takes anything (waitdeliver.go).
	if err := waitCanDeliver(env); err != nil {
		return err
	}

	if _, err := waitSet(ctx, env, names); err != nil {
		return err
	}

	st, err := waitStore(env)
	if err != nil {
		return err
	}

	lifetime = inheritedLifetime(lifetime, time.Now())
	token := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	lock, err := claimWait(ctx, env, token)
	if err != nil {
		return err
	}
	defer func() { lock.release() }()

	// A wake left by a waiter that ended before reading it is posts already off
	// this session's cursor: the loop below prints it. Only a stale failure goes.
	_ = os.Remove(failFile(env))

	// Conversations an earlier wait already reported unreadable are not
	// reported again while they stay unreadable, so re-arming after a
	// report does not wake the agent over and over.
	prev, _ := readWaitState(waitFile(env))
	state := WaitState{PID: os.Getpid(), StartedMs: time.Now().UnixMilli(), Reported: prev.Reported, Names: names, Claude: OnClaude()}
	record := func() { _ = writeJSONFile(waitFile(env), state) }
	record()

	// The doorbell: while it is ringing a post wakes this wait at once
	// instead of on the next poll. It is a signal only — the scan below
	// still does every delivery (doorbell.go).
	//
	// It belongs to the identity poller and to nobody else. Every session
	// on this machine shares one identity, and the poller lock is what
	// makes the outbound scan one per identity rather than one per
	// session; the rest are woken through the wake file. Ringing before
	// that lock is taken would open a tail per followed conversation in
	// every wait process - eight seats following six conversations is
	// forty-eight streams for six conversations' worth of rows, against a
	// per-identity cap they all share. That is incident-0001's shape with
	// a new transport.
	var bell <-chan struct{}
	stopBells := func() {}
	bellSet := "" // conversation ids the open tails cover; "" is none

	var poller *waitLock
	defer func() {
		stopBells()
		if poller != nil {
			poller.release()
		}
	}()

	var deadline <-chan time.Time
	var until time.Time
	if lifetime > 0 {
		until = time.Now().Add(lifetime)
		timer := time.NewTimer(lifetime)
		defer timer.Stop()
		deadline = timer.C
	}

	failures := 0                          // consecutive rounds in which every conversation failed
	failingSince := map[string]time.Time{} // first failure of each conversation's current streak
	failingBySession := map[string]map[string]time.Time{}
	failuresBySession := map[string]int{}
	reopened := false // the store was reopened after a 401 and has not read cleanly since
	var lastRound, graceUntil time.Time
	offlineN := 0
	bins := newBinaryWatch()
	sc := newScanner()
	hb := newHeartbeat()
	var clock clockWatch // based on the first round's clock below
	for {
		if ver, ok := bins.updated(); ok {
			// A new release: carry on in this process on the new file, so
			// the task the harness is waiting on stays the same one. The
			// exec drops this process's locks and tails with it; they are
			// let go first so nothing depends on that.
			// Each is let go once: the defers above run again on the way
			// out when the exec fails, and a bell closed twice panics.
			stopBells()
			stopBells = func() {}
			if poller != nil {
				poller.release()
				poller = nil
			}
			lock.release()
			err := reexecWait(bins.path, until)
			if err == nil {
				return nil // a test's stand-in: the real exec does not return
			}

			fmt.Fprintf(w, "parley was updated (%s → %s); run %s again to pick it up\n", fromVersion(), ver, rearm())
			return nil
		}
		now := waitNow().Round(0)
		if !lastRound.IsZero() && now.Sub(lastRound) >= WaitResumeGap {
			failures = 0
			for name := range failingSince {
				delete(failingSince, name)
			}
			failingBySession = map[string]map[string]time.Time{}
			failuresBySession = map[string]int{}
			graceUntil = now.Add(WaitResumeGrace)
			offlineN = 0
		}
		lastRound = now

		// The machine slept (laptop lid): the wall clock moved further than
		// the monotonic clock did. Every connection this process held is
		// dead from the balancer's side by now, so they are all dropped
		// before the scan below rather than each found dead in its turn,
		// the scan runs now rather than after any backoff, and the
		// doorbells are opened again on fresh connections.
		if !clock.wall.IsZero() && clock.gap(now) > 2*WaitPoll {
			resetConnections(st, "")
			stopBells()
			bell, stopBells, bellSet = nil, func() {}, ""
			offlineN = 0
		}
		clock.rebase(now)

		// The shell can exit under a running wait; from then on it is
		// consuming posts on behalf of nobody.
		if err := waitCanDeliver(env); err != nil {
			return err
		}

		if claim := readClaim(env); claim != "" && claim != token {
			if replaced, err := lock.yield(ctx, env, claim); err != nil || replaced {
				return replacedOr(w, err)
			}
		}

		if err := takeDelivery(env, w); err != nil {
			if errors.Is(err, errWakePrinted) {
				return nil
			}

			return err
		}

		if poller == nil {
			poller = tryIdentityLock(env)
		}

		// Only the poller rings, and it re-reads the switch each round so
		// `parley enable doorbell` reaches a wait that is already running.
		if poller != nil {
			bell, stopBells, bellSet = reconcileBells(ctx, env, st, bell, stopBells, bellSet)
		}

		touchPresence(env, waitPresenceState(env, time.Now()))

		busy := false
		if poller != nil {
			hb.tick(ctx, st)
			var done bool
			var err error
			done, busy, err = pollWaiters(ctx, env, &st, &state, record, &failures, failingSince, failingBySession, failuresBySession, &reopened, graceUntil, sc, hb, w)
			if err != nil {
				return err
			}

			if done {
				return nil
			}
		}

		// The delivery lock was held, so this round did not scan. If a
		// conversation still moved, do not sleep a full poll on top of that.
		if busy && cursorsMoved(ctx, env, st) {
			continue
		}

		delay := WaitPoll
		if state.UnreachableSinceMs != 0 && !WaitExitOnUnreachable {
			offlineN++
			shift := offlineN - 1
			if shift > 5 {
				shift = 5
			}
			delay = WaitPoll * time.Duration(1<<shift)
			if delay > WaitBackoffMax {
				delay = WaitBackoffMax
			}
		} else {
			offlineN = 0
		}

		if err := waitIdle(ctx, env, token, lock, delay, lifetime, deadline, state.UnreachableSinceMs, bell, &clock, w); err != nil {
			if errors.Is(err, errWaitContinue) {
				continue
			}

			if errors.Is(err, errWakePrinted) {
				return nil
			}

			return err
		}

		return nil
	}
}

var errWaitContinue = errors.New("wait: continue")

func tryIdentityLock(env Env) *waitLock {
	if err := os.MkdirAll(identityWaitDir(env), 0o700); err != nil {
		return nil
	}

	f, err := tryLock(identityLockPath(env))
	if err != nil {
		return nil
	}

	return &waitLock{path: identityLockPath(env), f: f}
}

func takeDelivery(env Env, w io.Writer) error {
	if b, err := os.ReadFile(failFile(env)); err == nil {
		_ = os.Remove(failFile(env))
		if msg := strings.TrimSpace(string(b)); msg != "" {
			return errors.New(msg)
		}
	}

	// Rename first so a poller writing a second wake creates a new `wake`
	// instead of having this Remove delete it. A death after the rename
	// used to leave wake.taking with nothing reading it; pick that up
	// first so a crash mid-take is a duplicate, not a loss.
	src := wakeFile(env)
	taking := src + ".taking"
	var buf []byte
	if stale, err := os.ReadFile(taking); err == nil {
		buf = append(buf, stale...)
		_ = os.Remove(taking)
	}
	if err := os.Rename(src, taking); err == nil {
		b, err := os.ReadFile(taking)
		_ = os.Remove(taking)
		if err == nil {
			buf = append(buf, b...)
		}
	}
	if len(buf) == 0 {
		return nil
	}
	_, _ = w.Write(buf)
	return errWakePrinted
}

func writeWake(env Env, body string) error {
	if err := os.MkdirAll(waitDir(env), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(wakeFile(env), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(body)
	cerr := f.Close()
	if err != nil {
		return err
	}
	return cerr
}

var errWakePrinted = errors.New("wait: printed")

func waitIdle(ctx context.Context, env Env, token string, lock *waitLock, delay, lifetime time.Duration, deadline <-chan time.Time, unreachableSince int64, bell <-chan struct{}, clock *clockWatch, w io.Writer) error {
	if delay <= 0 {
		delay = WaitPoll
	}
	next := time.After(delay)
	checkEvery := WaitPoll
	if checkEvery > 500*time.Millisecond {
		checkEvery = 500 * time.Millisecond
	}

	check := time.NewTicker(checkEvery)
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			if unreachableSince != 0 {
				ago := time.Since(time.UnixMilli(unreachableSince)).Round(time.Second)
				if ago < time.Second {
					ago = time.Second
				}
				fmt.Fprintf(w, "armed, but the directory has been unreachable for %s; nothing has been read since; posts will arrive when it is back. Run %s in the background again to keep listening.\n", ago, rearm())
				return nil
			}
			fmt.Fprintf(w, "still listening after %s, no new posts. Run %s in the background again to keep listening.\n", lifetime, rearm())
			return nil
		case <-next:
			return errWaitContinue
		case _, open := <-bell:
			// A post landed: scan now rather than sleeping out the poll.
			// A closed bell means the tails have stopped, and the poll is
			// what covers this wait from here — bell is nil on a nil
			// channel receive, so a closed one stops being selected.
			if !open {
				bell = nil
				continue
			}

			return errWaitContinue
		case <-check.C:
			if claim := readClaim(env); claim != "" && claim != token {
				if replaced, err := lock.yield(ctx, env, claim); err != nil || replaced {
					return replacedOr(w, err)
				}
			}

			if err := takeDelivery(env, w); err != nil {
				return err
			}

			// The machine slept through this sleep: scan now, and let the
			// round see the jump (Wait drops the connections).
			if clock != nil && !clock.wall.IsZero() && clock.gap(waitNow().Round(0)) > 2*WaitPoll {
				return errWaitContinue
			}
		}
	}
}

// clockWatch tells a machine that slept from a round that was slow. The
// wall clock (waitNow) keeps moving while the machine sleeps; the
// monotonic clock, which time.Since reads, does not. So the difference
// between what the two say has passed since the last rebase is the time
// the machine was asleep, and a round that merely took long shows none.
// The zero value has no base yet; the first rebase gives it one.
type clockWatch struct {
	wall time.Time
	mono time.Time
}

// gap answers how much further the wall clock moved than the monotonic
// clock did since the last rebase. now is waitNow, wall clock only.
func (c *clockWatch) gap(now time.Time) time.Duration {
	return now.Sub(c.wall) - time.Since(c.mono)
}

func (c *clockWatch) rebase(now time.Time) {
	c.wall, c.mono = now, time.Now()
}

// pulse is what the poller asks of a store whose connections can die
// under it: to drop them, and to make sure of the hosts it reads from.
// The statefs store is one (pkg/store/statefs/heartbeat.go); the file
// store has no connections and is asked nothing.
type pulse interface {
	// Beat answers, for each member host the store reads from, whether it
	// is there: heard from within every, or it answered a beat under
	// deadline. A miss drops the host's idle connections.
	Beat(ctx context.Context, every, deadline time.Duration) map[string]bool
	// Reset drops the idle connections to host; "" drops them all.
	Reset(host string)
}

func resetConnections(st store.Store, host string) {
	if p, ok := st.(pulse); ok {
		p.Reset(host)
	}
}

// heartbeat is the poller's count of member hosts that stopped answering.
// A beat runs beside the round, never in it: a host that is down costs
// the beat its deadline and the round nothing. Its answer is taken by the
// next tick, and a host missed twice running is what `parley status`
// shows as reconnecting.
type heartbeat struct {
	misses  map[string]int
	answers chan map[string]bool // the beat that is running; nil when none is
}

func newHeartbeat() *heartbeat {
	return &heartbeat{misses: map[string]int{}}
}

// tick takes the answer of the beat that was running, once it is done,
// and starts the next one. It never waits.
func (h *heartbeat) tick(ctx context.Context, st store.Store) {
	if h.answers != nil {
		select {
		case out := <-h.answers:
			h.answers = nil
			for host, ok := range out {
				if ok {
					delete(h.misses, host)
				} else {
					h.misses[host]++
				}
			}

			for host := range h.misses {
				if _, known := out[host]; !known {
					delete(h.misses, host)
				}
			}
		default:
			return // still beating
		}
	}

	p, ok := st.(pulse)
	if !ok {
		return
	}

	ch := make(chan map[string]bool, 1)
	h.answers = ch
	go func() { ch <- p.Beat(ctx, WaitBeatEvery, WaitBeatDeadline) }()
}

// reconnecting lists the hosts missed twice running, for `parley status`.
func (h *heartbeat) reconnecting() []string {
	var out []string
	for host, n := range h.misses {
		if n >= 2 {
			out = append(out, host)
		}
	}

	sort.Strings(out)
	return out
}

// printWake writes the delivery, and answers whether the writing worked:
// a wait only forgets its held copy when the posts actually landed.
func printWake(ctx context.Context, env Env, w io.Writer, wake []pendingPost) error {
	flags := screenPosts(ctx, env, wake)
	var first error
	note := func(_ int, err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	for i, it := range wake {
		if flags[i] != "" {
			note(fmt.Fprintln(w, flags[i]))
		}
		note(fmt.Fprintf(w, "[%s]%s %s\n", it.sub.Name, it.work, formatPost(it.e, it.sub.Name, it.pos-1, 0)))
	}

	note(fmt.Fprintf(w, "%d new posts. Handle them, then run %s in the background again.\n", len(wake), rearmFor(seatOnClaude(env))))
	return first
}

func formatWake(env Env, wake []pendingPost) string {
	var b strings.Builder
	_ = printWake(context.Background(), env, &b, wake)
	return b.String()
}

type nsRow struct {
	e   event.Event
	pos int64
}

type waitSession struct {
	sid   string
	env   Env
	state WaitState
	subs  []Subscription
	items []pendingPost
	fail  map[string]error
	slow  map[string]bool // conversations whose read had not come back when the round was taken
	ran   bool
	heads map[string]int64 // namespace name -> cursor to save after the wake is on disk
}

// read is the conversations this round actually read: not the slow ones,
// which it has nothing to say about yet.
func (wt *waitSession) read() []Subscription {
	if len(wt.slow) == 0 {
		return wt.subs
	}

	out := make([]Subscription, 0, len(wt.subs))
	for _, s := range wt.subs {
		if !wt.slow[s.Name] {
			out = append(out, s)
		}
	}

	return out
}

func pollWaiters(ctx context.Context, env Env, st *store.Store, self *WaitState, record func(), failures *int, failingSince map[string]time.Time, failingBySession map[string]map[string]time.Time, failuresBySession map[string]int, reopened *bool, graceUntil time.Time, sc *scanner, hb *heartbeat, w io.Writer) (done, busy bool, err error) {
	idState := WaitState{PID: os.Getpid(), StartedMs: self.StartedMs, LastOkMs: time.Now().UnixMilli(), Reconnecting: hb.reconnecting()}
	_ = writeJSONFile(identityWaitFile(env), idState)

	mine := sessionIDOf(env)
	waiters, err := collectWaiters(ctx, env, mine)
	if err != nil {
		return true, false, err
	}

	groups := map[string]*nsGroup{}
	for i := range waiters {
		wt := &waiters[i]
		wt.fail = map[string]error{}
		wt.slow = map[string]bool{}
		for _, s := range wt.subs {
			if cur, ok := readSession(wt.env, s.Name); ok {
				s.Cursor = cur.Cursor
			}

			g := groups[s.ID]
			if g == nil {
				g = &nsGroup{id: s.ID, name: s.Name, from: s.Cursor, subs: map[string]Subscription{}}
				groups[s.ID] = g
			} else if s.Cursor < g.from {
				g.from = s.Cursor
			}

			g.subs[wt.sid] = s
		}
	}

	scans, scanErr, folds, pending := sc.round(ctx, env, *st, groups, WaitPoll)

	retrying := map[string]bool{}
	unauth := false
	for _, err := range scanErr {
		if errors.Is(err, store.ErrUnauthenticated) {
			unauth = true
		}
	}

	if unauth && !*reopened {
		for id, err := range scanErr {
			if errors.Is(err, store.ErrUnauthenticated) {
				if g := groups[id]; g != nil {
					retrying[g.name] = true
				}
			}
		}

		fresh, err := waitStore(env)
		if err != nil {
			return true, false, err
		}

		*st, *reopened = fresh, true
	} else if *reopened && !unauth {
		*reopened = false
	}

	for i := range waiters {
		wt := &waiters[i]
		unlock, ok := lockDelivery(wt.env)
		if !ok {
			busy = true
			continue
		}

		wt.ran = true
		wt.heads = map[string]int64{}
		for _, s := range wt.subs {
			if cur, ok := readSession(wt.env, s.Name); ok {
				s.Cursor = cur.Cursor
			}

			if pending[s.ID] {
				// Still being read: neither rows nor a verdict this round,
				// and the cursor stays where it is.
				wt.slow[s.Name] = true
				continue
			}

			if err, failed := scanErr[s.ID]; failed {
				wt.fail[s.Name] = err
			}

			rows := scans[s.ID]
			items, head := fanoutRows(wt.env, s, rows, folds[s.ID])
			wt.items = append(wt.items, items...)
			wt.heads[s.Name] = head
		}

		now := waitNow().Round(0)
		done, err := finishWaiter(ctx, mine, wt, self, record, failures, failingSince, failingBySession, failuresBySession, retrying, now, graceUntil, w)
		unlock()
		if done || err != nil {
			return done, busy, err
		}
	}

	return false, busy, nil
}

// nsGroup is one conversation scanned once for every waiter that follows it.
type nsGroup struct {
	id, name string
	from     int64
	subs     map[string]Subscription // sid -> this waiter's sub
}

// scanner is the round's reads, kept across rounds. Every conversation in
// a round is read at once, up to scanParallel at a time — one after
// another, nine conversations on this machine made a doorbell wake wait
// on the slowest sum instead of the slowest wave — and each read is under
// its own deadline. A round takes what has come back within its budget;
// a read that has not is left running, not waited for, and the round
// that finds it done takes its rows. So one stuck conversation, a member
// that hangs, delays nobody else's delivery past the budget, and a slow
// one is delivered a round late rather than read again from the start.
// Rows inside one conversation stay in position order, because one read
// fills that conversation, and the cursors move only when rows are
// delivered, exactly as before.
type scanner struct {
	mu   sync.Mutex
	runs map[string]*scanRun // by conversation id: running, or done and not yet taken
	sem  chan struct{}
}

type scanRun struct {
	done chan struct{}
	rows []nsRow
	err  error
	fold *workLog
}

func newScanner() *scanner {
	return &scanner{runs: map[string]*scanRun{}, sem: make(chan struct{}, scanParallel)}
}

// round starts a read for every conversation not already being read,
// waits up to budget for them, and answers the ones that are done — this
// round's or an earlier one's — with the rest in pending.
func (sc *scanner) round(ctx context.Context, env Env, st store.Store, groups map[string]*nsGroup, budget time.Duration) (scans map[string][]nsRow, scanErr map[string]error, folds map[string]*workLog, pending map[string]bool) {
	scans, scanErr, folds, pending = map[string][]nsRow{}, map[string]error{}, map[string]*workLog{}, map[string]bool{}

	sc.mu.Lock()
	runs := make(map[string]*scanRun, len(groups))
	for id, g := range groups {
		run := sc.runs[id]
		if run == nil {
			run = &scanRun{done: make(chan struct{})}
			sc.runs[id] = run
			go sc.read(ctx, env, st, id, g.from, run)
		}

		runs[id] = run
	}

	// A conversation nobody follows any more is not kept once it is done.
	for id, run := range sc.runs {
		if _, wanted := groups[id]; !wanted {
			select {
			case <-run.done:
				delete(sc.runs, id)
			default:
			}
		}
	}
	sc.mu.Unlock()

	timer := time.NewTimer(budget)
	defer timer.Stop()
	late := false
	for id, run := range runs {
		if !late {
			select {
			case <-run.done:
			case <-timer.C:
				late = true
			}
		}

		select {
		case <-run.done:
			if run.err != nil {
				scanErr[id] = run.err
			}
			scans[id] = run.rows
			if run.fold != nil {
				folds[id] = run.fold
			}

			sc.mu.Lock()
			delete(sc.runs, id)
			sc.mu.Unlock()
		default:
			pending[id] = true
		}
	}

	return scans, scanErr, folds, pending
}

// read is one conversation's read, under its own deadline.
func (sc *scanner) read(ctx context.Context, env Env, st store.Store, id string, from int64, run *scanRun) {
	defer close(run.done)
	sc.sem <- struct{}{}
	defer func() { <-sc.sem }()

	rctx, cancel := context.WithTimeout(ctx, scanReadTimeout)
	defer cancel()
	run.rows, run.err = scanNamespace(rctx, st, id, from)
	hasWork := false
	for _, r := range run.rows {
		hasWork = hasWork || needsFold(r.e)
	}

	if hasWork {
		markCtx, cancelMarks := context.WithTimeout(ctx, workFoldTimeout)
		defer cancelMarks()
		if l, err := readWork(markCtx, env, st, id); err == nil {
			run.fold = l
		}
	}
}

// cursorsMoved reports whether any followed conversation's head is past
// this session's cursor. It does not take the delivery lock, so a waiter
// that lost the lock can still see that a post landed.
func cursorsMoved(ctx context.Context, env Env, st store.Store) bool {
	if st == nil {
		return false
	}
	for _, s := range Subscriptions(env) {
		cur := s.Cursor
		if saved, ok := readSession(env, s.Name); ok {
			cur = saved.Cursor
		}
		head, err := st.Head(ctx, s.ID)
		if err != nil {
			continue
		}
		if int64(head) > cur {
			return true
		}
	}
	return false
}

func collectWaiters(ctx context.Context, env Env, mine string) ([]waitSession, error) {
	var out []waitSession
	for _, sid := range waiterSessions(env) {
		senv := envForSession(env, sid)
		prev, _ := readWaitState(waitFile(senv))
		if prev.PID == 0 {
			prev.PID = os.Getpid()
			prev.StartedMs = time.Now().UnixMilli()
		}

		subs, err := waitSet(ctx, senv, prev.Names)
		if err != nil {
			if sid == mine {
				return nil, err
			}

			_ = os.WriteFile(failFile(senv), []byte(err.Error()), 0o600)
			continue
		}

		out = append(out, waitSession{sid: sid, env: senv, state: prev, subs: subs})
	}

	return out, nil
}

func scanNamespace(ctx context.Context, st store.Store, id string, from int64) ([]nsRow, error) {
	var rows []nsRow
	pos := from
	for e, err := range conversation.Attach(st, id).Scan(ctx, store.Position(from), 0) {
		if err != nil {
			if errors.Is(err, event.ErrBadRow) {
				pos++
				fmt.Fprintf(os.Stderr, "parley: skipped a row that will not decode in %s at %d: %v\n", id, pos, err)
				continue
			}

			return rows, err
		}

		pos++
		rows = append(rows, nsRow{e: e, pos: pos})
	}

	return rows, nil
}

func fanoutRows(env Env, s Subscription, rows []nsRow, fold *workLog) ([]pendingPost, int64) {
	me := authorOf(env)
	head := s.Cursor
	first := 0
	var items []pendingPost
	for _, r := range rows {
		if r.pos <= s.Cursor {
			continue
		}

		if r.pos > head {
			head = r.pos
		}

		mine := addressesAny(r.e, me, s.Participant, env.Session)
		if s.Mode == "digest" && !digestKeeps(r.e, mine) && !unaddressedReply(r.e) {
			continue
		}

		if fromMe(env, me, r.e) {
			continue
		}

		items = append(items, pendingPost{sub: s, e: r.e, pos: r.pos, mine: mine})
	}

	items = markReplies(items, fold, s, env.Session)
	for i := first; i < len(items); i++ {
		if fold != nil {
			items[i].work = workMark(fold, items[i].e)
		}

		items[i].hold = holdsTurn(items[i].e, items[i].mine, fold)
	}

	sort.SliceStable(items, func(i, j int) bool { return items[i].mine && !items[j].mine })
	return items, head
}

func finishWaiter(ctx context.Context, mine string, wt *waitSession, self *WaitState, record func(), failures *int, failingSince map[string]time.Time, failingBySession map[string]map[string]time.Time, failuresBySession map[string]int, retrying map[string]bool, now, graceUntil time.Time, w io.Writer) (bool, error) {
	prev := wt.state
	read := wt.read()
	allFailed := wt.ran && len(wt.fail) == len(read) && len(read) > 0
	since := failingSince
	failN := failures
	if wt.sid != mine {
		if failingBySession[wt.sid] == nil {
			failingBySession[wt.sid] = map[string]time.Time{}
		}

		since = failingBySession[wt.sid]
		n := failuresBySession[wt.sid]
		failN = &n
	}

	offline := allFailed && allTransientUnreachable(wt.env, wt.fail)
	inGrace := !graceUntil.IsZero() && now.Before(graceUntil)
	switch {
	case !wt.ran:
		prev.LastError = "delivery lock busy: another delivery for this session is running"
	case len(read) == 0 && len(wt.subs) > 0:
		// Every conversation is still being read: nothing to judge yet.
	case allFailed:
		if !inGrace && (WaitExitOnUnreachable || !offline) {
			*failN++
		}
		prev.LastError = describeFailures(wt.fail)
		if offline {
			if prev.UnreachableSinceMs == 0 {
				prev.UnreachableSinceMs = now.UnixMilli()
			}
		} else {
			prev.UnreachableSinceMs = 0
		}
	default:
		*failN = 0
		prev.LastOkMs = now.UnixMilli()
		prev.LastError = describeFailures(wt.fail)
		prev.UnreachableSinceMs = 0
	}

	if wt.ran {
		for name := range since {
			if _, still := wt.fail[name]; !still && !wt.slow[name] {
				delete(since, name)
			}
		}

		for name := range wt.fail {
			if since[name].IsZero() {
				since[name] = now
			}
		}

		prev.Reported = stillFailing(prev.Reported, read, wt.fail)
		prev.Unreadable = describeEach(wt.fail)
	}

	if len(wt.items) > 0 {
		wake, kept := splitByVerdict(ctx, wt.env, wt.items)
		spoolContext(wt.env, kept)
		if len(wake) > 0 {
			if wt.sid == mine {
				// The cursors are about to move past these rows, so the
				// print is the only copy. Write it down first: a wait that
				// dies between here and the clear below leaves the posts
				// for the session's next turn (waitdeliver.go).
				holdDelivery(wt.env, deliveryLines(wt.env, wake))
				saveErr := commitWaiterCursors(wt)
				prev.Positions = positions(wt.env, wt.subs)
				_ = writeJSONFile(waitFile(wt.env), prev)
				*self = prev
				record()
				if err := printWake(ctx, wt.env, w, wake); err == nil {
					clearDelivery(wt.env)
				}
				// The posts are printed either way; a position that did not
				// save ends the wait with why, not a quiet exit 0 that the
				// next wait would repeat.
				return true, saveErr
			}

			_ = writeWake(wt.env, formatWake(wt.env, wake))
		}
	}

	saveErr := commitWaiterCursors(wt)
	prev.Positions = positions(wt.env, wt.subs)
	_ = writeJSONFile(waitFile(wt.env), prev)
	if wt.sid == mine {
		*self = prev
		record()
		if saveErr != nil {
			return true, saveErr
		}
	}

	if report := toReport(wt.env, wt.fail, since, prev.Reported, now, allFailed && *failN >= WaitMaxFailures, retrying); len(report) > 0 {
		prev.Reported = append(prev.Reported, report...)
		sort.Strings(prev.Reported)
		_ = writeJSONFile(waitFile(wt.env), prev)
		err := waitFailure(wt.env, wt.fail, report, allFailed)
		if wt.sid == mine {
			*self = prev
			record()
			return true, err
		}

		_ = os.WriteFile(failFile(wt.env), []byte(err.Error()), 0o600)
	}

	if wt.sid != mine {
		failuresBySession[wt.sid] = *failN
	}

	return false, nil
}

// commitWaiterCursors saves each conversation's new position and answers
// the first that could not be saved.
func commitWaiterCursors(wt *waitSession) error {
	if wt.heads == nil {
		return nil
	}
	var first error
	for i := range wt.subs {
		s := &wt.subs[i]
		head, ok := wt.heads[s.Name]
		if !ok || head == s.Cursor {
			continue
		}
		s.Cursor = head
		if err := saveSub(wt.env, *s); err != nil && first == nil {
			first = cursorSaveError(s.Name, err)
		}
	}
	if first != nil {
		logLine(wt.env, "cursor", first.Error())
	}
	return first
}

// toReport answers the failing conversations the agent should now be told
// about and has not been: refused outright, failing for WaitNoSuccess, or
// all of them failing for WaitMaxFailures rounds. A conversation in retrying
// got a first 401 and is read again with a fresh credential before a refusal
// counts.
func toReport(env Env, failed map[string]error, since map[string]time.Time, reported []string, now time.Time, allDown bool, retrying map[string]bool) []string {
	var out []string
	for name, err := range failed {
		if hasName(reported, name) {
			continue
		}

		if retrying[name] {
			continue
		}
		if refusedForGood(env, err) {
			out = append(out, name)
			continue
		}
		if isTransientUnreachable(err) && !WaitExitOnUnreachable {
			continue
		}
		if allDown || now.Sub(since[name]) >= WaitNoSuccess {
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

func anyUnauthenticated(failed map[string]error) bool {
	for _, err := range failed {
		if errors.Is(err, store.ErrUnauthenticated) {
			return true
		}
	}

	return false
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

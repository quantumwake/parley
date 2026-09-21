package plugin

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/quantumwake/parley/pkg/store"
)

// A background wait records its liveness where the Stop hook and `parley
// status` can read it, so a wait that has lost the directory is noticed
// instead of looking like a quiet channel.
//
// One process polls for the enrolled identity. Outbound it is one Scan
// per followed namespace; locally it fans rows out to each waiter:
//
//	<data>/subscriptions/.wait/wait.json   pid, last_ok, last_error
//	<data>/subscriptions/.wait/wait.lock   held by the live poller
//
// Each agent session that called `parley wait` is a waiter. Its exit is
// what wakes that session's CLI:
//
//	<data>/subscriptions/.sessions/<session>/wait.json   pid, started, last_ok, positions, unreadable
//	<data>/subscriptions/.sessions/<session>/wait.lock   held by that session's waiter
//	<data>/subscriptions/.sessions/<session>/wait.claim  a newer wait asking this waiter to hand over
//	<data>/subscriptions/.sessions/<session>/wake        posts the poller delivered for this session
//	<data>/subscriptions/.sessions/<session>/fail        an error the poller wants this waiter to exit with
//
// Outside a Claude Code session the waiter files sit under .sessions/_terminal.
// Cursors and handles stay per session; only the poller lock is per identity.

const (
	// WaitMaxFailures consecutive failed rounds end a wait (about 10 s).
	WaitMaxFailures = 5
	// WaitNoSuccess of failing reads of a conversation ends a wait to say so.
	WaitNoSuccess = 60 * time.Second
	// WaitLifetime is how long a wait listens before asking to be re-armed.
	WaitLifetime = 60 * time.Minute
	// WaitResumeGap is a wall-clock jump that means the process was
	// suspended (laptop lid). Failure counters reset and a grace period
	// starts; DNS is not yet a lost directory.
	WaitResumeGap = 30 * time.Second
	// WaitResumeGrace is how long after a resume failures do not count.
	WaitResumeGrace = 60 * time.Second
	// waitFresh is how recent last_ok must be for a wait to count as live.
	waitFresh = 30 * time.Second
)

// WaitBackoffMax caps how long wait sleeps between rounds while the
// directory is unreachable. Tests shorten it with WaitPoll.
var WaitBackoffMax = 60 * time.Second

// WaitExitOnUnreachable restores the old behaviour: a lost directory ends
// the wait after WaitMaxFailures. Default is to ride DNS/dial out so a
// laptop lid does not deafen the agent.
var WaitExitOnUnreachable = false

// waitNow is the clock wait uses for resume detection. Tests jump it.
var waitNow = time.Now

// WaitState is one wait as recorded: the identity poller, or one session waiter.
type WaitState struct {
	PID       int              `json:"pid"`
	StartedMs int64            `json:"started_ms"`
	LastOkMs  int64            `json:"last_ok_ms,omitempty"`
	LastError string           `json:"last_error,omitempty"`
	Positions map[string]int64 `json:"positions,omitempty"`
	// Unreadable is each conversation the last round could not read.
	Unreadable map[string]string `json:"unreadable,omitempty"`
	// Reported lists unreadable conversations the agent was already told
	// about; they are not reported again until they read once more.
	Reported []string `json:"reported,omitempty"`
	// Names is the waiter's name filter (empty: every conversation it follows).
	Names []string `json:"names,omitempty"`
	// UnreachableSinceMs is when every followed conversation started
	// failing with a transient network error (DNS, dial). Zero when the
	// directory is reachable. Status uses it to tell "armed but offline"
	// from a dead waiter.
	UnreachableSinceMs int64 `json:"unreachable_since_ms,omitempty"`
}

func waitDir(env Env) string {
	session := env.Session
	if session == "" {
		session = "_terminal"
	}

	return filepath.Join(sessionsDir(env), session)
}

func waitFile(env Env) string { return filepath.Join(waitDir(env), "wait.json") }

func identityWaitDir(env Env) string { return filepath.Join(subsDir(env), ".wait") }

func identityWaitFile(env Env) string {
	return filepath.Join(identityWaitDir(env), "wait.json")
}

func identityLockPath(env Env) string {
	return filepath.Join(identityWaitDir(env), "wait.lock")
}

func wakeFile(env Env) string { return filepath.Join(waitDir(env), "wake") }

func failFile(env Env) string { return filepath.Join(waitDir(env), "fail") }

func sessionIDOf(env Env) string {
	if env.Session == "" {
		return "_terminal"
	}

	return env.Session
}

func envForSession(env Env, session string) Env {
	out := env
	if session == "_terminal" {
		out.Session = ""
		return out
	}

	out.Session = session
	return out
}

func readWaitState(path string) (WaitState, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return WaitState{}, false
	}

	var w WaitState
	return w, json.Unmarshal(b, &w) == nil && w.PID != 0
}

// waitHeld reports whether a live process holds wait.lock in dir.
func waitHeld(dir string) bool {
	f, err := tryLock(filepath.Join(dir, "wait.lock"))
	if err == nil {
		f.Close()
		return false
	}

	return errors.Is(err, errLocked)
}

// WaitLive reports whether this session has a waiter registered, the
// identity poller is running and has reached the directory recently, and
// this session's followed conversations are covered. A wait on some of
// them leaves the rest to the Stop hook.
func WaitLive(env Env) bool {
	if !waitHeld(waitDir(env)) || !waitHeld(identityWaitDir(env)) {
		return false
	}

	w, ok := readWaitState(waitFile(env))
	if !ok {
		return false
	}
	// A waiter that still holds the lock but cannot reach the directory
	// is armed and offline, not dead. LastOkMs goes stale on purpose.
	if w.UnreachableSinceMs > 0 {
		return true
	}
	if time.Since(time.UnixMilli(w.LastOkMs)) >= waitFresh {
		return false
	}

	for _, s := range Subscriptions(env) {
		if _, covered := w.Positions[s.Name]; !covered {
			return false
		}
	}

	return true
}

// WaitReport is the identity poller, or one session waiter, for `parley status`.
type WaitReport struct {
	Session  string
	State    WaitState
	Running  bool
	Poller   bool
	Attached []string
}

// WaitReports lists the identity poller first when one is recorded, then
// each session waiter, newest first.
func WaitReports(env Env) []WaitReport {
	var out []WaitReport
	if w, ok := readWaitState(identityWaitFile(env)); ok {
		out = append(out, WaitReport{
			Session:  authorOf(env),
			State:    w,
			Running:  waitHeld(identityWaitDir(env)),
			Poller:   true,
			Attached: waiterSessions(env),
		})
	}

	paths, _ := filepath.Glob(filepath.Join(sessionsDir(env), "*", "wait.json"))
	var sessions []WaitReport
	for _, p := range paths {
		w, ok := readWaitState(p)
		if !ok {
			continue
		}

		dir := filepath.Dir(p)
		sessions = append(sessions, WaitReport{Session: filepath.Base(dir), State: w, Running: waitHeld(dir)})
	}

	sort.Slice(sessions, func(i, j int) bool { return sessions[i].State.StartedMs > sessions[j].State.StartedMs })
	return append(out, sessions...)
}

func waiterSessions(env Env) []string {
	mine := sessionIDOf(env)
	entries, err := os.ReadDir(sessionsDir(env))
	if err != nil {
		if mine != "" {
			return []string{mine}
		}

		return nil
	}

	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		dir := filepath.Join(sessionsDir(env), e.Name())
		if e.Name() == mine || waitHeld(dir) {
			if !seen[e.Name()] {
				seen[e.Name()] = true
				out = append(out, e.Name())
			}
		}
	}

	if mine != "" && !seen[mine] {
		out = append(out, mine)
	}

	sort.Strings(out)
	return out
}

// refusedForGood reports a failure no retry will change: the directory did
// not accept the identity or its token (401) or does not allow the read
// (403), or the identity file itself cannot be used. A locked or leaderless
// namespace (423) is transient.
func refusedForGood(env Env, err error) bool {
	if err == nil {
		return false
	}

	var pathErr *fs.PathError
	if errors.As(err, &pathErr) && env.IdentityPath != "" && filepath.Clean(pathErr.Path) == filepath.Clean(env.IdentityPath) {
		return true
	}

	msg := err.Error()
	if strings.Contains(msg, "identityfile: ") {
		return true
	}

	return errors.Is(err, store.ErrUnauthenticated) || (errors.Is(err, store.ErrRefused) && (strings.Contains(msg, "HTTP 401") || strings.Contains(msg, "HTTP 403")))
}

// isTransientUnreachable reports a failure that is the network, not access:
// DNS, dial, connection refused or reset, timeout. A laptop that just woke
// produces these until Wi-Fi is back. A 401/403 is not this.
func isTransientUnreachable(err error) bool {
	if err == nil {
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, n := range []string{
		"no such host",
		"dial tcp",
		"connection refused",
		"connection reset",
		"i/o timeout",
		"network is unreachable",
		"no route to host",
		"temporary failure in name resolution",
		"server misbehaving",
	} {
		if strings.Contains(msg, n) {
			return true
		}
	}
	return false
}

func allTransientUnreachable(failed map[string]error) bool {
	if len(failed) == 0 {
		return false
	}
	for _, err := range failed {
		if !isTransientUnreachable(err) {
			return false
		}
	}
	return true
}

package plugin

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/quantumwake/parley/pkg/store"
)

// A background wait records its liveness where the Stop hook and `parley
// status` can read it, so a wait that has lost the directory is noticed
// instead of looking like a quiet channel:
//
//	<data>/subscriptions/.sessions/<session>/wait.json   pid, started, last_ok, last_error, positions, unreadable
//	<data>/subscriptions/.sessions/<session>/wait.lock   held by the live wait; the kernel drops it on exit
//	<data>/subscriptions/.sessions/<session>/wait.claim  a newer wait asking the live one to hand over
//
// Outside a Claude Code session the files sit under .sessions/_terminal.

const (
	// WaitMaxFailures consecutive failed rounds end a wait (about 10 s).
	WaitMaxFailures = 5
	// WaitNoSuccess of failing reads of a conversation ends a wait to say so.
	WaitNoSuccess = 60 * time.Second
	// WaitLifetime is how long a wait listens before asking to be re-armed.
	WaitLifetime = 60 * time.Minute
	// waitFresh is how recent last_ok must be for a wait to count as live.
	waitFresh = 30 * time.Second
)

// WaitState is one session's wait, as recorded.
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
}

func waitDir(env Env) string {
	session := env.Session
	if session == "" {
		session = "_terminal"
	}

	return filepath.Join(sessionsDir(env), session)
}

func waitFile(env Env) string { return filepath.Join(waitDir(env), "wait.json") }

func readWaitState(path string) (WaitState, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return WaitState{}, false
	}

	var w WaitState
	return w, json.Unmarshal(b, &w) == nil && w.PID != 0
}

// waitHeld reports whether a live process holds the session's wait lock.
func waitHeld(dir string) bool {
	f, err := tryLock(filepath.Join(dir, "wait.lock"))
	if err == nil {
		f.Close()
		return false
	}

	return errors.Is(err, errLocked)
}

// WaitLive reports whether this session has a wait that is running, has
// reached the directory recently, and covers every conversation the session
// follows. A wait on some of them leaves the rest to the Stop hook.
func WaitLive(env Env) bool {
	w, ok := readWaitState(waitFile(env))
	if !ok || !waitHeld(waitDir(env)) || time.Since(time.UnixMilli(w.LastOkMs)) >= waitFresh {
		return false
	}

	for _, s := range Subscriptions(env) {
		if _, covered := w.Positions[s.Name]; !covered {
			return false
		}
	}

	return true
}

// WaitReport is one session's wait for `parley status`.
type WaitReport struct {
	Session string
	State   WaitState
	Running bool
}

// WaitReports lists every recorded wait on this machine, newest first.
func WaitReports(env Env) []WaitReport {
	paths, _ := filepath.Glob(filepath.Join(sessionsDir(env), "*", "wait.json"))
	var out []WaitReport
	for _, p := range paths {
		w, ok := readWaitState(p)
		if !ok {
			continue
		}

		dir := filepath.Dir(p)
		out = append(out, WaitReport{Session: filepath.Base(dir), State: w, Running: waitHeld(dir)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].State.StartedMs > out[j].State.StartedMs })
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

package plugin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/quantumwake/parley/pkg/event"
)

// Several Claude Code sessions on one machine share one identity and one
// data directory. What must not be shared between them is where each one
// has read up to and the handle each one speaks under, or one session
// consumes the posts another was waiting for and renames it. So a
// subscription has two halves:
//
//	<data>/subscriptions/<name>                        the machine record: what is followed, and in which mode
//	<data>/subscriptions/.sessions/<session>/<name>    one session's cursor and handle
//
// A session with no record yet starts from the machine cursor, which is
// the furthest any session has read, so a new session is not replayed the
// whole history. Without a session id (a plain terminal), the machine
// record is the whole state, as before.

// sessionState is one session's half of a subscription.
type sessionState struct {
	Cursor      int64  `json:"cursor"`
	Participant string `json:"participant,omitempty"`
	SeenMs      int64  `json:"seen_ms"`
	Joined      bool   `json:"joined"`
}

func sessionsDir(env Env) string { return filepath.Join(subsDir(env), ".sessions") }

func sessionFile(env Env, name string) string {
	return filepath.Join(sessionsDir(env), env.Session, filepath.Base(subFile(env, name)))
}

func readSession(env Env, name string) (sessionState, bool) {
	b, err := os.ReadFile(sessionFile(env, name))
	if err != nil {
		return sessionState{}, false
	}

	var st sessionState
	return st, json.Unmarshal(b, &st) == nil
}

// writeJSONFile replaces path atomically. The temporary name is unique per
// process, because sessions write the same machine record concurrently.
func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	b, _ := json.MarshalIndent(v, "", "  ")
	tmp := path + ".tmp" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}

// saveSub records a subscription. In a session it writes that session's
// cursor, handle, and joined flag, and moves the machine cursor forward to
// the furthest point read; outside one it writes the machine record.
func saveSub(env Env, s Subscription) error {
	if env.Session == "" {
		return writeJSONFile(subFile(env, s.Name), s)
	}

	if err := writeJSONFile(sessionFile(env, s.Name), sessionState{Cursor: s.Cursor, Participant: s.Participant, SeenMs: time.Now().UnixMilli(), Joined: true}); err != nil {
		return err
	}

	machine, ok := readMachine(env, s.Name)
	if !ok {
		machine = s
		machine.Participant = ""
	}

	if ok && machine.Cursor >= s.Cursor && machine.Mode == s.Mode {
		return nil
	}

	machine.Cursor = max(machine.Cursor, s.Cursor)
	machine.Mode, machine.DigestPick = s.Mode, s.DigestPick
	return writeJSONFile(subFile(env, s.Name), machine)
}

func readMachine(env Env, name string) (Subscription, bool) {
	b, err := os.ReadFile(subFile(env, name))
	if err != nil {
		return Subscription{}, false
	}

	var s Subscription
	return s, json.Unmarshal(b, &s) == nil && s.ID != ""
}

// overlaySession replaces the machine record's cursor and handle with this
// session's, when it has them. A session never inherits another session's
// handle.
func overlaySession(env Env, s Subscription) Subscription {
	if env.Session == "" {
		return s
	}

	s.Participant = ""
	if st, ok := readSession(env, s.Name); ok {
		s.Cursor, s.Participant = st.Cursor, st.Participant
	}

	return s
}

// lockDelivery takes this session's delivery lock, waiting briefly for a
// delivery already running. Outside a session there is nothing to share.
func lockDelivery(env Env) (unlock func(), ok bool) {
	if env.Session == "" {
		return func() {}, true
	}

	dir := filepath.Join(sessionsDir(env), env.Session)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return func() {}, true
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := tryLock(filepath.Join(dir, ".deliver.lock"))
		if err == nil {
			return func() { f.Close() }, true
		}

		if !errors.Is(err, errLocked) {
			return func() {}, true // no lock available on this filesystem: deliver unguarded
		}

		if time.Now().After(deadline) {
			return nil, false
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// removeSessions drops every session's half of a subscription.
func removeSessions(env Env, name string) {
	dirs, _ := os.ReadDir(sessionsDir(env))
	for _, d := range dirs {
		_ = os.Remove(filepath.Join(sessionsDir(env), d.Name(), filepath.Base(subFile(env, name))))
	}
}

// fromMe answers whether a row is a post this session wrote. Sessions that
// share an identity must see each other, so a reader that knows its session
// decides by session alone: a post with no session id came from an older
// client, and hiding it would hide it from every session on the machine.
// Without a session (a plain terminal) the identity decides.
func fromMe(env Env, me string, e event.Event) bool {
	if !e.IsPost() {
		return false
	}

	if env.Session != "" {
		return e.SessionID == env.Session
	}

	return me != "" && e.Identity == me
}

// shortSession is the display form of a session id.
func shortSession(id string) string {
	if len(id) > 8 {
		return id[:8]
	}

	return id
}

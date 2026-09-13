package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quantumwake/parley/pkg/capture"
	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/spool"
)

// DaemonOptions describe one session's capture daemon.
type DaemonOptions struct {
	SessionID      string
	TranscriptPath string
	CWD            string
	IdleTimeout    time.Duration // exit after the spool has not grown for this long (default 4 h)
}

// quietWindow is how long the transcript must stop growing after a
// SessionEnd before session.end is delivered.
var quietWindow = 1500 * time.Millisecond

// errLocked means another process holds the session's daemon lock.
var errLocked = errors.New("plugin: capture daemon lock is held")

// RunDaemon tails the transcript into the spool and pushes the spool to
// the store. It is the long-lived half of capture; hooks are the
// short-lived half.
//
// One daemon per session, enforced by an exclusive lock the OS releases
// when the process dies. A resumed Claude Code session keeps its session
// id, transcript and spool, so the daemon does not stop at session.end
// while a resume follows it, and after its last session.end it hands the
// session off: it releases the lock, then exits only if no row was spooled
// since. The hooks append their row before they check the lock, so every
// resume is seen either by this daemon or by the one its hook starts.
func RunDaemon(ctx context.Context, env Env, o DaemonOptions) error {
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 4 * time.Hour
	}

	lock, err := lockDaemon(env, o.SessionID, time.Second)
	if errors.Is(err, errLocked) {
		return nil // another daemon has this session
	}

	if err != nil {
		return err
	}

	defer func() {
		if lock != nil {
			lock.Close()
		}
	}()
	writeDaemonPid(env, o.SessionID)

	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	author := authorOf(env)
	if author == "" {
		author = "anonymous"
	}

	sp := spool.Session{Dir: SpoolDir(env), ID: o.SessionID}
	ctx, cancel := idleContext(ctx, sp.Path(), o.IdleTimeout)
	defer cancel()

	// A daemon started for a resumed session re-reads the transcript from
	// the start; blocks an earlier daemon already spooled are skipped.
	seen := spooledTranscriptIDs(sp)
	tailer := &capture.Tailer{Path: o.TranscriptPath, Author: author, CaptureThinking: env.Thinking,
		Emit: func(e capture.Event) error {
			if _, dup := seen[e.ID]; dup {
				return nil
			}

			seen[e.ID] = struct{}{}
			return sp.Append(e, false)
		}}

	var active activity
	newPusher := func() *capture.Pusher {
		p := &capture.Pusher{
			Store: st, Session: sp, Agent: author, Name: nameFrom(o.CWD, o.SessionID),
			Redact:    redactFromEnv(),
			BeforeEnd: func(ctx context.Context) { waitQuiet(ctx, o.TranscriptPath, quietWindow, 10*time.Second) },
			OnDelivered: func(seq int64, e capture.Event, pos capture.Position) {
				fmt.Printf("%s delivered seq=%d kind=%s pos=%d\n", time.Now().UTC().Format(time.RFC3339), seq, e.Kind, pos)
				active.touch(env, e.TSMs)
			},
		}
		p.OnOpen = func(displayName, id string) {
			NamesPut(env, displayName, id)
			active.opened(displayName)
		}
		return p
	}

	pusher := newPusher()
	backoff := time.Second
	for {
		tailCtx, stopTail := context.WithCancel(ctx)
		tailDone := make(chan error, 1)
		go func() { tailDone <- tailer.Run(tailCtx) }()
		err := pusher.Run(ctx) // returns when a session.end is delivered with nothing after it
		stopTail()
		<-tailDone
		if ctx.Err() != nil {
			return nil // idle or interrupted; what is spooled waits for the next daemon
		}

		if err != nil {
			// A store error is not the end of the session: retry with a
			// fresh pusher (it reopens and continues seq from the head).
			fmt.Printf("%s push failed, retrying in %s: %v\n", time.Now().UTC().Format(time.RFC3339), backoff, err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}

			backoff = min(2*backoff, 30*time.Second)
			pusher = newPusher()
			continue
		}

		backoff = time.Second
		// The tailer may have appended final blocks after the push saw
		// session.end; deliver anything still spooled.
		if err := pusher.Once(ctx); err != nil {
			fmt.Printf("%s push failed: %v\n", time.Now().UTC().Format(time.RFC3339), err)
			continue
		}

		if !pusher.Ended() {
			continue // a resume was already spooled and delivered after the end
		}

		// Hand off: release the lock, then look for rows spooled since. Only
		// hooks append now (the tailer is stopped), so a row means a resume.
		lock.Close()
		lock = nil
		if !unacked(sp) {
			return nil
		}

		if lock, err = lockDaemon(env, o.SessionID, time.Second); err != nil {
			lock = nil
			return nil // the resume's own daemon has it
		}

		writeDaemonPid(env, o.SessionID)
	}
}

// ensureDaemon starts a capture daemon for the hook's session unless one
// holds the session's lock. The hook has already spooled its row.
func ensureDaemon(env Env, in Input) error {
	if env.Self == "" || in.SessionID == "" {
		return errors.New("no executable path or session id")
	}

	// The legacy check first: probing the lock creates the lock file.
	if legacyDaemonRunning(env, in.SessionID) || daemonRunning(env, in.SessionID) {
		return nil
	}

	return spawnDaemon(env, in)
}

// legacyDaemonRunning covers a daemon started by a parley from before the
// lock (it wrote only a pid file) that is still recording when the plugin
// updates, so the new hook does not start a second pusher beside it. Once
// a lock file exists for the session, the pid file is not consulted.
func legacyDaemonRunning(env Env, session string) bool {
	if _, err := os.Stat(daemonLockPath(env, session)); err == nil {
		return false
	}

	b, err := os.ReadFile(filepath.Join(env.DataDir, "daemon-"+session+".pid"))
	if err != nil {
		return false
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && pid > 0 && processAlive(pid)
}

func daemonLockPath(env Env, session string) string {
	return filepath.Join(env.DataDir, "daemon-"+session+".lock")
}

// lockDaemon takes the session's daemon lock, retrying for up to wait so a
// hook's momentary probe never makes a real daemon give up.
func lockDaemon(env Env, session string, wait time.Duration) (*os.File, error) {
	_ = os.MkdirAll(env.DataDir, 0o700)
	deadline := time.Now().Add(wait)
	for {
		f, err := tryLock(daemonLockPath(env, session))
		if !errors.Is(err, errLocked) || time.Now().After(deadline) {
			return f, err
		}

		time.Sleep(50 * time.Millisecond)
	}
}

// DaemonRunning reports whether a live capture daemon holds the session's
// lock (`parley status`).
func DaemonRunning(env Env, session string) bool { return daemonRunning(env, session) }

// daemonRunning reports whether a live daemon holds the session's lock.
func daemonRunning(env Env, session string) bool {
	f, err := tryLock(daemonLockPath(env, session))
	if err != nil {
		return errors.Is(err, errLocked)
	}

	f.Close()
	return false
}

// writeDaemonPid records this daemon for `parley status`.
func writeDaemonPid(env Env, session string) {
	_ = os.WriteFile(filepath.Join(env.DataDir, "daemon-"+session+".pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
}

// unacked reports whether the spool holds rows past the ack offset.
func unacked(sp spool.Session) bool {
	n, err := fileSize(sp.Path())
	return err == nil && n > sp.AckOffset()
}

// spooledTranscriptIDs is the set of transcript block ids already spooled.
func spooledTranscriptIDs(sp spool.Session) map[string]struct{} {
	seen := map[string]struct{}{}
	for entry, err := range sp.Read(0) {
		if err != nil {
			continue
		}

		if k := entry.Event.Kind; k == event.KindAssistantText || k == event.KindAssistantThinking {
			seen[entry.Event.ID] = struct{}{}
		}
	}

	return seen
}

// idleContext is ctx, cancelled once path has not grown for idle.
func idleContext(ctx context.Context, path string, idle time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	tick := min(idle/4, time.Minute)
	go func() {
		last, _ := fileSize(path)
		since := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(tick):
			}

			if n, _ := fileSize(path); n != last {
				last, since = n, time.Now()
				continue
			}

			if time.Since(since) >= idle {
				cancel()
				return
			}
		}
	}()
	return ctx, cancel
}

// activity keeps the conversation's names record stamped with the time of
// the newest delivered row, so `parley status` and the console can order
// sessions by last activity without reading the store.
type activity struct {
	mu     sync.Mutex
	name   string
	lastMs int64
}

func (a *activity) opened(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.name, a.lastMs = name, 0
}

func (a *activity) touch(env Env, tsMs int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.name == "" || tsMs <= a.lastMs {
		return
	}

	a.lastMs = tsMs
	NamesTouch(env, a.name, time.UnixMilli(tsMs))
}

// waitQuiet returns once path has not grown for quiet, or after max.
func waitQuiet(ctx context.Context, path string, quiet, max time.Duration) {
	deadline := time.Now().Add(max)
	last, _ := fileSize(path)
	stable := time.Now()
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}

		n, _ := fileSize(path)
		if n != last {
			last, stable = n, time.Now()
			continue
		}

		if time.Since(stable) >= quiet {
			return
		}
	}
}

func fileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}

	return st.Size(), nil
}

func nameFrom(cwd, session string) string {
	if cwd != "" {
		return filepath.Base(cwd)
	}

	return session
}

// redactFromEnv reads STATEFS_AI_REDACT: a "|"-separated list of regexes.
func redactFromEnv() []*regexp.Regexp {
	v := os.Getenv("STATEFS_AI_REDACT")
	if v == "" {
		return nil
	}

	var out []*regexp.Regexp
	for _, p := range strings.Split(v, "|") {
		if re, err := regexp.Compile(p); err == nil {
			out = append(out, re)
		}
	}

	return out
}

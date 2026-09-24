package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A wait consumes what it delivers: it prints the new posts and moves this
// session's cursors past them, so the same rows are never shown twice. That
// is only safe while the printing reaches an agent.
//
// It stops reaching one in two ways, and both have happened:
//
//   - `parley wait > /dev/null 2>&1 &` — the posts are read off the
//     conversation and written to a sink. The cursor moves; nobody ever
//     sees them.
//   - `parley wait &` inside a tool call — the shell exits when the call
//     returns and the wait is reparented to init. Whatever it writes from
//     then on goes nowhere: under Claude Code a tool call's stdout is a
//     regular FILE the harness reads only while the call is open, so the
//     writing keeps succeeding and the posts are still lost. It goes on
//     polling, pinging presence as "listening", and holding the identity
//     poller lock, for a session that has gone deaf.
//
// Telling agents to remember not to do this is not a fix. The rule belongs
// here: a wait that cannot be heard must not consume. So it
//
//  1. refuses to start when its output is a sink,
//  2. retires as soon as its shell is gone and its pipe has no reader,
//  3. and, on the path where it can be heard, writes the delivery down
//     before printing it and clears it only afterwards — so a wait killed
//     mid-delivery leaves the posts for the session's next turn instead of
//     taking them with it.

// ErrWaitDiscarded: the wait was pointed at /dev/null.
var ErrWaitDiscarded = errors.New("`parley wait` prints the posts it takes off the conversation, and this one's output goes to /dev/null: the posts would be marked read and shown to nobody. Run it as a background shell task (Bash run_in_background) with its output left alone")

// ErrWaitOrphaned: the shell that started the wait is gone.
var ErrWaitOrphaned = errors.New("the shell that started this wait has exited, so nothing is reading its output; stopping now leaves the posts unread instead of consuming them. Run `parley wait` as a background shell task (Bash run_in_background), not with `&`")

// waitCanDeliver answers whether this process can still hand posts to an
// agent. Tests replace it; Wait asks before it starts and on every round,
// because a wait is orphaned after the fact, not at birth.
var waitCanDeliver = func(env Env) error { return canDeliverTo(os.Stdout, os.Getppid(), env.Session) }

// canDeliverTo judges one wait's ability to be heard. session is the agent
// session this wait exists to wake; empty means a person at a terminal,
// who is the reader themselves.
func canDeliverTo(out *os.File, ppid int, session string) error {
	if out == nil {
		return ErrWaitDiscarded
	}

	fi, err := out.Stat()
	if err != nil {
		return nil // unknowable: leave the wait alone rather than guess
	}

	if isNull(fi) {
		return ErrWaitDiscarded
	}

	// An orphan is one whose shell has exited (reparented to init). What it
	// writes then depends on who was meant to read it:
	//
	//   - a terminal: a person is watching the screen. Leave it alone.
	//   - a person's own redirect with no session (`nohup parley wait > log
	//     &`): they can read the log afterwards. Leave it alone.
	//   - an agent session's output, pipe or file: the only reader was the
	//     tool call that has now returned, so nothing will ever read it.
	//     File or pipe makes no difference — under Claude Code it is a file,
	//     and writing to it still succeeds. Retire.
	//
	// The session is what makes this safe, and it is nearly always there:
	// SessionFromEnv reads PARLEY_SESSION, CLAUDE_CODE_SESSION_ID and
	// GROK_SESSION_ID, and Claude Code exports the second into every tool
	// call, so an agent's wait under Claude Code or Grok always has one. A
	// host that exports none of the three leaves its waits session-less, so
	// they are spared here like a person's and go on consuming the
	// _terminal cursor — no worse than before this rule existed, but such a
	// host should export PARLEY_SESSION to be covered by it.
	//
	// Known limit: this reads orphanhood as "reparented to pid 1". Where a
	// subreaper is in the way (a Linux systemd user session,
	// PR_SET_CHILD_SUBREAPER) an orphan is reparented to the subreaper
	// instead, so it is not detected and the wait stays up. That is the
	// conservative direction — a wait that should retire keeps running,
	// rather than a working one being retired — and the hold-before-print
	// copy still covers the delivery it was in the middle of.
	if ppid == 1 && session != "" && fi.Mode()&os.ModeCharDevice == 0 {
		return ErrWaitOrphaned
	}

	return nil
}

// isNull reports whether this is /dev/null. Both a terminal and /dev/null
// are character devices, so the mode bits cannot tell them apart; the
// device and inode can.
func isNull(fi os.FileInfo) bool {
	if fi.Mode()&os.ModeDevice == 0 {
		return false
	}

	null, err := os.Stat(os.DevNull)
	if err != nil {
		return false
	}

	return os.SameFile(fi, null)
}

// deliveringPath holds the posts a wait is in the middle of printing. It
// exists only between the write and the print, which is the window where a
// death would otherwise lose them: the cursors move in that window, so the
// rows will not be offered again.
func deliveringPath(env Env) string {
	if env.Session == "" {
		return ""
	}

	return filepath.Join(sessionsDir(env), env.Session, "delivering.jsonl")
}

// holdDelivery writes the lines down before they are printed.
func holdDelivery(env Env, lines []string) {
	path := deliveringPath(env)
	if path == "" || len(lines) == 0 {
		return
	}

	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}

	var b []byte
	for _, l := range lines {
		j, err := json.Marshal(l)
		if err != nil {
			continue
		}

		b = append(b, append(j, '\n')...)
	}

	_ = os.WriteFile(path, b, 0o600)
}

// clearDelivery forgets them, once the print has returned.
func clearDelivery(env Env) {
	if path := deliveringPath(env); path != "" {
		_ = os.Remove(path)
	}
}

// drainDelivery answers posts a wait printed but may not have been heard
// saying, and forgets them. The session's next turn shows them. A wait that
// died between the print and the clear makes this a duplicate; a wait that
// died before the print makes it the only copy. Duplicated beats lost.
func drainDelivery(env Env) []string {
	path := deliveringPath(env)
	if path == "" {
		return nil
	}

	// Taken by rename, like the context spool: two drains at once cannot
	// make these lines vanish unread.
	taken := path + ".taken"
	if os.Rename(path, taken) != nil {
		return nil
	}

	b, err := os.ReadFile(taken)
	_ = os.Remove(taken)
	if err != nil {
		return nil
	}

	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		var l string
		if line == "" || json.Unmarshal([]byte(line), &l) != nil {
			continue
		}

		out = append(out, l)
	}

	return out
}

// deliveryLines renders posts the way an injected turn shows them, so a
// delivery recovered at the next turn reads like the rest of that turn.
func deliveryLines(env Env, wake []pendingPost) []string {
	out := make([]string, 0, len(wake))
	for _, it := range wake {
		out = append(out, fmt.Sprintf("- [%s]%s %s", it.sub.Name, it.work, formatPost(it.e, it.sub.Name, it.pos-1, InjectMaxPostBytes)))
	}

	return out
}

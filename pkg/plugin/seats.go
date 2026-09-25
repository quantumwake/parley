package plugin

// seats.go — who is on this machine, and what each of them is doing.
//
// Several sessions share one enrolled identity: a champion, a reviewer,
// two grok seats. Answering "who is here and what are they up to" took
// three commands and some guessing at file paths, which is why a board of
// identical machine names went unnoticed for a week. Everything here is
// already on disk; this only gathers it.

import (
	"os"
	"sort"
	"time"
)

// Seat is one session on this machine.
type Seat struct {
	Session     string    // the session id
	Handle      string    // what it speaks under, "" when it never chose
	State       string    // listening | thinking | working | starting, from its own hooks
	StateAt     time.Time // when that state was recorded
	Listening   bool      // a live `parley wait` holds this session's lock
	ArmedAt     time.Time // when that listener started
	LastOk      time.Time // the last round that read cleanly
	Follows     int       // conversations this session follows
	Behind      int64     // rows past this session's cursors, across its follows
	Unreachable bool      // its last rounds all failed with a network error
	Me          bool      // this process's own session
	Closed      bool      // its session process is gone: the terminal was closed
}

// Seats answers every session with state on this machine, newest activity
// first. It reads files and never the network, so it is safe to call on a
// keypress.
func Seats(env Env) []Seat {
	entries, err := os.ReadDir(sessionsDir(env))
	if err != nil {
		return nil
	}

	var out []Seat
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		out = append(out, seatOf(envForSession(env, e.Name()), e.Name(), env.Session))
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Me != out[j].Me {
			return out[i].Me // this seat first: it is the one that can act
		}

		return out[i].StateAt.After(out[j].StateAt)
	})

	return out
}

func seatOf(env Env, session, me string) Seat {
	s := Seat{Session: session, Me: session == me}
	s.Handle = Participant(env)

	subs := Subscriptions(env)
	s.Follows = len(subs)
	if s.Handle == "" {
		for _, sub := range subs {
			if sub.Participant != "" {
				s.Handle = sub.Participant
				break
			}
		}
	}

	if p := readPresence(env); p.State != "" {
		s.State = p.State
		s.StateAt = time.UnixMilli(p.AtMs)
		// Nothing clears presence when a session ends, so a closed
		// terminal reads as "listening" forever. A recorded owner that
		// no longer answers is the one hard proof the seat is gone.
		// No owner recorded means an older session and proves nothing.
		s.Closed = p.OwnerPID != 0 && !processAlive(p.OwnerPID) && !s.Me
	}

	if w, ok := readWaitState(waitFile(env)); ok {
		s.ArmedAt = time.UnixMilli(w.StartedMs)
		s.LastOk = time.UnixMilli(w.LastOkMs)
		s.Unreachable = w.UnreachableSinceMs != 0
		// A wait writes its state and outlives itself, so the file alone
		// is "it was armed once". The lock is what says it still is.
		s.Listening = WaitLive(env)
		for _, sub := range subs {
			if at, ok := w.Positions[sub.ID]; ok && at > sub.Cursor {
				s.Behind += at - sub.Cursor
			}
		}
	}

	return s
}

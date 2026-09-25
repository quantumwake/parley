package plugin

// participant.go — the handle a session speaks under.
//
// Several sessions share one enrolled identity: this laptop runs a
// champion, a reviewer and two grok seats, all as the same key. The
// IDENTITY says which machine; the PARTICIPANT says which of them is
// talking, and it is what a chip, a mention and a post should show.
//
// Owner, 2026-09-25: "the participant is what should be showing and every
// agent needs one, they can also change it using the parley mcp tools",
// and on a board of identical machine names: "this needs fixing".
//
// Until now a handle could only be declared per conversation, at join
// time (`join --as`). A session that joined without one sent nothing, and
// everything downstream fell back to the identity — the laptop name. So
// the handle becomes a property of the SESSION: set once, applied to
// every conversation it follows, inherited by later joins, and carried by
// the presence ping.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Errors a caller branches on.
var (
	errNoHandle  = errors.New("participant: a handle is required")
	errNoSession = errors.New("participant: no session to speak for (this is a session's handle, not the machine's)")
)

// handleFile is where a session's chosen handle lives, beside its
// subscriptions and its wait state.
func handleFile(env Env) string {
	if env.Session == "" {
		return ""
	}

	return filepath.Join(sessionsDir(env), env.Session, "participant")
}

// Participant is the handle this session speaks under, or "" when it has
// not chosen one. A handle declared for a single conversation at join
// time still wins for that conversation (SubscriptionParticipant).
func Participant(env Env) string {
	path := handleFile(env)
	if path == "" {
		return ""
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(b))
}

// SetParticipant chooses the handle for this session: it is written down,
// applied to every conversation the session already follows, and picked
// up by later joins. It answers how many subscriptions it reached, so a
// caller can say what happened rather than guess.
func SetParticipant(env Env, handle string) (updated int, err error) {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return 0, errNoHandle
	}

	if env.Session == "" {
		return 0, errNoSession
	}

	path := handleFile(env)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, err
	}

	if err := os.WriteFile(path, []byte(handle+"\n"), 0o600); err != nil {
		return 0, err
	}

	// Existing follows are updated too: a handle that only applied to
	// conversations joined after it was chosen would leave a seat speaking
	// under two names at once, which is worse than none.
	for _, s := range Subscriptions(env) {
		if s.Participant == handle {
			continue
		}

		s.Participant = handle
		if err := saveSub(env, s); err != nil {
			return updated, err
		}

		updated++
	}

	return updated, nil
}

// noHandleYet tells a session, at the one moment it can act on it, that it
// is about to speak as the machine rather than as itself.
//
// Thirteen seats on one laptop share one identity, so a seat that never
// chose a handle shows up as the machine's name — and four seats
// independently reported "presence is broken" when the real answer was
// that every chip said the same thing. It also cannot be addressed:
// addressesMe matches the identity or the handle, and the
// identity#session string parley itself prints matches neither.
//
// Said only when it is true and fixable: this session follows something,
// and neither it nor any of its follows has a handle.
func noHandleYet(env Env, cmd string) string {
	subs := Subscriptions(env)
	if len(subs) == 0 || Participant(env) != "" {
		return ""
	}

	for _, s := range subs {
		if s.Participant != "" {
			return ""
		}
	}

	return fmt.Sprintf(" You have no handle here, so your posts and your presence chip show this machine's identity (%s) — the same as every other session on it, and nobody can address you by name: choose one with `%s participant <name>` or the set_participant tool.", authorOf(env), cmd)
}

// ParticipantFor is the handle to speak under in one conversation: what
// `join --as` declared for it, else the session's own.
func ParticipantFor(env Env, id string) string {
	if h := participantOf(env, id); h != "" {
		return h
	}

	return Participant(env)
}

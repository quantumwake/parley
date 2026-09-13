// Package naming is the S6 seam: how the product lays its kinds onto
// statefs namespaces. Scope holds the search labels; display_name is the
// human handle and must be unique per tenant on statefs.io, so agent logs
// derive one from the agent and a counter while shared conversations use
// the given name.
package naming

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/quantumwake/statefs.ai/pkg/store"
)

// Kind is the product's namespace kind, stored under scope["kind"].
type Kind string

const (
	KindConversation Kind = "conversation"
	KindPersona      Kind = "persona"
	KindAgent        Kind = "agent"
)

// Mode tells an agent's own log from a shared conversation. Both are
// conversations; the mode is a tag so listings can filter.
type Mode string

const (
	ModeAgent  Mode = "agent"  // one writer: the agent's session log
	ModeShared Mode = "shared" // many participants by grant: a group channel
)

// Shared describes a group conversation people and agents post to.
type Shared struct {
	Name        string
	Description string
	Tags        []string
}

// Scope returns the searchable labels for a shared conversation.
func (c Shared) Scope() store.Scope {
	s := store.Scope{"kind": string(KindConversation), "mode": string(ModeShared), "name": c.Name}
	put(s, "description", c.Description)
	if len(c.Tags) > 0 {
		s["tags"] = append([]string(nil), c.Tags...)
	}

	return s
}

// Conversation describes an agent's log or a shared conversation.
type Conversation struct {
	Started time.Time
	Title   string
	Session string   // client session id, empty for shared conversations
	Agent   string   // agent namespace id or name, empty for shared conversations
	Persona string   // persona name the agent runs, optional
	Task    string   // task ref such as repo:statefs#42, optional
	Tags    []string // free labels
}

// Scope returns the searchable labels for a conversation.
func (c Conversation) Scope() store.Scope {
	s := store.Scope{"kind": string(KindConversation), "mode": string(ModeAgent)}
	if !c.Started.IsZero() {
		s["date"] = c.Started.Format("2006-01-02")
		s["started_ms"] = c.Started.UnixMilli()
	}

	put(s, "title", c.Title)
	put(s, "session", c.Session)
	put(s, "agent", c.Agent)
	put(s, "persona", c.Persona)
	put(s, "task", c.Task)
	if len(c.Tags) > 0 {
		s["tags"] = append([]string(nil), c.Tags...)
	}

	return s
}

// AgentScope labels an agent namespace.
func AgentScope(persona, runtime, host string) store.Scope {
	s := store.Scope{"kind": string(KindAgent)}
	put(s, "persona", persona)
	put(s, "runtime", runtime)
	put(s, "host", host)
	return s
}

// PersonaScope labels a persona namespace.
func PersonaScope(name string) store.Scope {
	return store.Scope{"kind": string(KindPersona), "name": name}
}

var unsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// Slug lowercases and strips a name to the characters safe in a display
// name; empty input becomes "untitled".
func Slug(name string) string {
	s := unsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "untitled"
	}

	if len(s) > 64 {
		s = s[:64]
	}

	return s
}

// AgentLogName derives the unique display name of an agent's conversation:
// <agent>/<yyyy-mm-ddThh:mm:ss>/<slug>#<session>. The timestamp is when the
// session started (local time of the machine that ran it), so listings read
// in order; the session tag (first block of the UUID) keeps names unique per
// tenant with no local state.
func AgentLogName(agent, name, session string, started time.Time) string {
	return fmt.Sprintf("%s/%s/%s#%s", Slug(agent), started.Format("2006-01-02T15:04:05"), Slug(name), SessionTag(session))
}

// SessionTag shortens a UUID session id to its first block; other ids are
// slugged whole.
func SessionTag(session string) string {
	if len(session) == 36 && strings.Count(session, "-") == 4 {
		return session[:8]
	}

	return Slug(session)
}

// AgentName derives an agent's display name: <persona>#<n>.
func AgentName(persona string, n int) string {
	return fmt.Sprintf("%s#%d", Slug(persona), n)
}

func put(s store.Scope, k, v string) {
	if v != "" {
		s[k] = v
	}
}

// TitleFromPrompt makes a listing title out of a prompt: first line, one
// space run, at most 80 characters, cut at a word.
func TitleFromPrompt(prompt string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(prompt), "\n", 2)[0])
	line = strings.Join(strings.Fields(line), " ")
	if len(line) <= 80 {
		return line
	}

	cut := strings.LastIndex(line[:80], " ")
	if cut < 40 {
		cut = 80
	}

	return line[:cut] + "…"
}

// StartedFromName recovers the start time encoded in a display name of the
// shape produced by AgentLogName, "<agent>/<2006-01-02T15:04:05>/<slug>#<tag>".
// Conversations created before the name carried a timestamp have none, so
// the second result says whether a time was found. The console uses this to
// group by day when the scope has no started_ms label.
func StartedFromName(display string) (time.Time, bool) {
	for _, part := range strings.Split(display, "/") {
		if t, err := time.ParseInLocation("2006-01-02T15:04:05", part, time.Local); err == nil {
			return t, true
		}
	}

	return time.Time{}, false
}

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

	"github.com/quantumwake/statefs.ai/pkg/store"
)

// Kind is the product's namespace kind, stored under scope["kind"].
type Kind string

const (
	KindConversation Kind = "conversation"
	KindPersona      Kind = "persona"
	KindAgent        Kind = "agent"
)

// Conversation describes an agent's log or a shared conversation.
type Conversation struct {
	Session string   // client session id, empty for shared conversations
	Agent   string   // agent namespace id or name, empty for shared conversations
	Persona string   // persona name the agent runs, optional
	Task    string   // task ref such as repo:statefs#42, optional
	Tags    []string // free labels
}

// Scope returns the searchable labels for a conversation.
func (c Conversation) Scope() store.Scope {
	s := store.Scope{"kind": string(KindConversation)}
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
// <agent>/<slug>#<n>. n is the caller's per-agent counter (the number of
// conversations the agent has opened), which keeps names unique per tenant
// without a lookup.
func AgentLogName(agent, name string, n int) string {
	return fmt.Sprintf("%s/%s#%d", Slug(agent), Slug(name), n)
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

// Package event is the product's one row shape: a completed block of an
// agent conversation, or a post in a shared conversation, never a token
// delta. It is the S1 seam of the plan: every other package produces or
// consumes these values, and the golden files under testdata/events pin
// the JSON encoding.
package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Kind names what a row is. The agent-log kinds come from the capture
// plugin; the post kinds are what participants publish in a shared
// conversation; the meta kinds are written by the product itself.
type Kind string

const (
	KindSessionStart      Kind = "session.start"
	KindUserMessage       Kind = "user.message"
	KindAssistantText     Kind = "assistant.text"
	KindAssistantThinking Kind = "assistant.thinking"
	KindToolUse           Kind = "tool.use"
	KindToolResult        Kind = "tool.result"
	KindSubagentStart     Kind = "subagent.start"
	KindSubagentStop      Kind = "subagent.stop"
	KindSessionEnd        Kind = "session.end"

	KindPostQuestion Kind = "post.question"
	KindPostAnswer   Kind = "post.answer"
	KindPostComment  Kind = "post.comment"
	KindPostReport   Kind = "post.report"
	KindPostArtifact Kind = "post.artifact"
	KindPostStatus   Kind = "post.status"
	KindPostRequest  Kind = "post.request" // ask the participants for work: content {task, range:[from,to], due_ms?}
	KindPostClaim    Kind = "post.claim"   // "I am doing this request"; earliest position wins

	KindMetaPurpose Kind = "meta.purpose"
	KindMetaSummary Kind = "meta.summary"

	// Catalog rows: written to persona and agent namespaces, never to a
	// conversation. Same envelope so one reader serves every kind.
	KindPersonaVersion    Kind = "persona.version"
	KindAgentStarted      Kind = "agent.started"
	KindAgentStopped      Kind = "agent.stopped"
	KindAssignmentOpened  Kind = "assignment.opened"
	KindAssignmentClosed  Kind = "assignment.closed"
	KindAgentSubscribed   Kind = "agent.subscribed"   // content: {conversation, mode}
	KindAgentUnsubscribed Kind = "agent.unsubscribed" // content: {conversation}
)

// Role is who produced the row inside a conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// Source names the client that captured the row.
type Source string

const (
	SourceClaudeCode Source = "claude-code"
	SourceAgentSDK   Source = "agent-sdk"
	SourceCodex      Source = "codex"
	SourceAPIProxy   Source = "api-proxy"
	SourceProduct    Source = "statefs-ai"
)

// MaxInlineContent is the byte cap on Content; larger bodies go to blob
// storage and the row carries BlobRef instead (RFC-0001 §5).
const MaxInlineContent = 256 << 10

// Event is one row of a conversation. Field order and JSON names are the
// wire contract; statefs stores the flattened map (see Record).
type Event struct {
	ID          string `json:"event_id"`                  // ULID, client-assigned; the dedupe key
	Seq         int64  `json:"seq"`                       // client-monotonic per session
	TSMs        int64  `json:"ts_ms"`                     // client time, epoch milliseconds
	IngestedMs  int64  `json:"ingested_ms,omitempty"`     // set by the writer at delivery
	SessionID   string `json:"session_id,omitempty"`      // the client session (Claude Code session_id)
	Source      Source `json:"source"`                    // which client captured it
	Kind        Kind   `json:"kind"`                      // what the row is
	Role        Role   `json:"role,omitempty"`            // who produced it
	Identity    string `json:"identity,omitempty"`        // statefs identity that wrote the row; self-declared, nothing attests it
	Participant string `json:"participant,omitempty"`     // handle declared at join, posts only; distinguishes speakers under one identity
	ToolName    string `json:"tool_name,omitempty"`       // top-level for indexing
	ToolUseID   string `json:"tool_use_id,omitempty"`     // the client's tool call id; pairs tool.result with tool.use
	ParentID    string `json:"parent_event_id,omitempty"` // tool.result -> tool.use; subagent -> parent; post reply -> post
	AgentID     string `json:"agent_id,omitempty"`        // subagent attribution
	AgentType   string `json:"agent_type,omitempty"`      // subagent type
	Model       string `json:"model,omitempty"`           // model that produced an assistant row
	TokensIn    int64  `json:"tokens_in,omitempty"`       // cost attribution
	TokensOut   int64  `json:"tokens_out,omitempty"`      // cost attribution

	Content json.RawMessage `json:"content,omitempty"`  // kind-specific body, inline up to MaxInlineContent
	BlobRef string          `json:"blob_ref,omitempty"` // pointer when the body exceeds the cap

	// Indexable marks a row whose content is a product-written derivative
	// worth indexing on its own (a summary, a report): a top-level scalar so
	// a P15 bitmap can select these rows without touching the rest.
	Indexable bool `json:"indexable,omitempty"`

	// Posts in a shared conversation.
	To      string `json:"to,omitempty"`       // identity, or "*" for everyone
	Thread  string `json:"thread,omitempty"`   // root event id of the thread
	ReplyTo string `json:"reply_to,omitempty"` // the post this answers
	Tags    Labels `json:"tags,omitempty"`     // free labels
}

// Validation errors are typed so callers can branch without string matching.
var (
	ErrMissingID       = errors.New("event: missing event_id")
	ErrBadID           = errors.New("event: event_id is not a ULID")
	ErrMissingKind     = errors.New("event: missing kind")
	ErrUnknownKind     = errors.New("event: unknown kind")
	ErrMissingSource   = errors.New("event: missing source")
	ErrMissingTime     = errors.New("event: ts_ms must be positive")
	ErrContentTooLarge = errors.New("event: content exceeds MaxInlineContent; use blob_ref")
	ErrContentAndBlob  = errors.New("event: content and blob_ref are mutually exclusive")
	ErrMissingParent   = errors.New("event: kind requires parent_event_id")
	ErrBadContent      = errors.New("event: content is not valid JSON")
)

var knownKinds = map[Kind]bool{
	KindSessionStart: true, KindUserMessage: true, KindAssistantText: true,
	KindAssistantThinking: true, KindToolUse: true, KindToolResult: true,
	KindSubagentStart: true, KindSubagentStop: true, KindSessionEnd: true,
	KindPostQuestion: true, KindPostAnswer: true, KindPostComment: true,
	KindPostReport: true, KindPostArtifact: true, KindPostStatus: true,
	KindPostRequest: true, KindPostClaim: true,
	KindMetaPurpose: true, KindMetaSummary: true,
	KindPersonaVersion: true, KindAgentStarted: true, KindAgentStopped: true,
	KindAssignmentOpened: true, KindAssignmentClosed: true,
	KindAgentSubscribed: true, KindAgentUnsubscribed: true,
}

// requiresParent lists the kinds that are meaningless without the row they
// answer: a tool result without its call (a tool_use_id also satisfies it),
// an answer without its question.
var requiresParent = map[Kind]bool{
	KindToolResult: true,
	KindPostAnswer: true,
	KindPostClaim:  true,
}

// Validate enforces the contract every writer and reader can rely on. It
// checks shape only; it never inspects the meaning of Content.
func (e Event) Validate() error {
	if e.ID == "" {
		return ErrMissingID
	}

	if !IsULID(e.ID) {
		return ErrBadID
	}

	if e.Kind == "" {
		return ErrMissingKind
	}

	if !knownKinds[e.Kind] {
		return fmt.Errorf("%w: %q", ErrUnknownKind, e.Kind)
	}

	if e.Source == "" {
		return ErrMissingSource
	}

	if e.TSMs <= 0 {
		return ErrMissingTime
	}

	if len(e.Content) > MaxInlineContent {
		return ErrContentTooLarge
	}

	if len(e.Content) > 0 && e.BlobRef != "" {
		return ErrContentAndBlob
	}

	if len(e.Content) > 0 && !json.Valid(e.Content) {
		return ErrBadContent
	}

	if requiresParent[e.Kind] && e.ParentID == "" && !(e.Kind == KindToolResult && e.ToolUseID != "") {
		return fmt.Errorf("%w: %s", ErrMissingParent, e.Kind)
	}

	return nil
}

// IsCatalog reports whether the row belongs in a persona or agent
// namespace rather than a conversation.
func (e Event) IsCatalog() bool {
	switch e.Kind {
	case KindPersonaVersion, KindAgentStarted, KindAgentStopped, KindAssignmentOpened,
		KindAssignmentClosed, KindAgentSubscribed, KindAgentUnsubscribed:
		return true
	}

	return false
}

// IsPost reports whether the row is a participant post rather than a
// captured block or a product meta row.
func (e Event) IsPost() bool {
	switch e.Kind {
	case KindPostQuestion, KindPostAnswer, KindPostComment, KindPostReport, KindPostArtifact, KindPostStatus,
		KindPostRequest, KindPostClaim:
		return true
	}

	return false
}

// Record flattens the event into the schemaless map statefs stores. Every
// JSON field name is a column; Content stays a JSON column so DuckDB can
// json_extract it. Zero-valued optional fields are omitted, matching the
// wire encoding, so the stored row and the JSON row have the same columns.
func (e Event) Record() (map[string]any, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}

	// json.Unmarshal turns int64 into float64; restore the integer columns
	// so statefs stores exact ints (its own client had this bug once).
	for _, k := range []string{"seq", "ts_ms", "ingested_ms", "tokens_in", "tokens_out"} {
		if v, ok := m[k].(float64); ok {
			m[k] = int64(v)
		}
	}

	return m, nil
}

// FromRecord is the inverse of Record for rows read back from statefs. A
// JSON column can come back as a string holding JSON (the member's read
// path stringifies nested values); it is decoded back into the object.
func FromRecord(m map[string]any) (Event, error) {
	if s, ok := m["content"].(string); ok && len(s) > 0 && (s[0] == '{' || s[0] == '[') && json.Valid([]byte(s)) {
		m["content"] = json.RawMessage(s)
	}

	b, err := json.Marshal(m)
	if err != nil {
		return Event{}, err
	}

	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return Event{}, err
	}

	return e, nil
}

// Labels is a list of free labels. Rows are immutable and not every writer
// is parley, so a reader accepts the shapes seen in stored rows: a list of
// strings, a single string (comma-separated labels), or null.
type Labels []string

// UnmarshalJSON accepts ["a","b"], "a, b" and null.
func (l *Labels) UnmarshalJSON(b []byte) error {
	var list []string
	if err := json.Unmarshal(b, &list); err == nil {
		*l = list
		return nil
	}

	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		*l = nil
		return nil // an unexpected shape drops the labels, never the row
	}

	var out []string
	for _, s := range strings.Split(one, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}

	*l = out
	return nil
}

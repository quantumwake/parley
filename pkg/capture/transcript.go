package capture

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/quantumwake/parley/pkg/event"
)

// transcriptLine is the S5 seam: the fields of a Claude Code transcript
// line the tailer reads. One assistant message is written as several
// lines, one per content block, sharing message.id; each line has its own
// uuid.
type transcriptLine struct {
	Type      string `json:"type"`
	IsMeta    bool   `json:"isMeta"`
	Sidechain bool   `json:"isSidechain"`
	AgentID   string `json:"agentId"`
	UUID      string `json:"uuid"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId"`
	Message   struct {
		Model   string          `json:"model"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Usage   struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

// Tailer follows a transcript file and emits assistant.thinking and
// assistant.text rows. Ids derive from (line uuid, block index), so
// re-reading the file yields the same ids and the writer dedupes them.
// Tool calls and results are left to the hooks.
type Tailer struct {
	Path            string
	Author          string
	CaptureThinking bool          // false drops thinking blocks before they reach the spool
	Poll            time.Duration // when the file grew; default 200 ms. Idle uses 1s.
	Emit            func(event.Event) error

	// A subagent's transcript: every row carries the agent and points at
	// its subagent.start. Prompts records the user lines that are text (the
	// agent's prompt and messages sent to it), not tool results. A forked
	// agent's transcript opens with the parent's context, already recorded:
	// a line from before Since (less a margin, since Claude Code writes the
	// first line just before the start hook fires) that isn't marked as
	// this agent's own is skipped.
	AgentID, AgentType, ParentID string
	Prompts                      bool
	Since                        time.Time
	SessionID                    string // fallback when a line has no session id (Codex rollouts)

	off   int64 // bytes consumed by Run; a later Run continues from here
	codex bool  // set once a Codex rollout line is seen
}

// Offset is how far the tailer has read; SetOffset makes the next Run start
// there.
func (t *Tailer) Offset() int64 { return t.off }

// SetOffset sets where the next Run starts.
func (t *Tailer) SetOffset(off int64) { t.off = off }

// Run reads from where the previous Run stopped (the start, the first
// time), then follows growth until ctx is done. A partial trailing line is
// retried on the next poll.
func (t *Tailer) Run(ctx context.Context) error {
	poll := t.Poll
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	idle := time.Second
	if idle < poll {
		idle = poll
	}

	for {
		n, err := t.ingest()
		if err != nil {
			return err
		}

		wait := idle
		if n > 0 {
			wait = poll
		}
		select {
		case <-ctx.Done():
			_, _ = t.ingest()
			return nil
		case <-time.After(wait):
		}
	}
}

// ingest reads complete lines after t.off. If the file shrank, it starts
// over (rewrite). If it has not grown, it does not open the file.
func (t *Tailer) ingest() (int64, error) {
	st, err := os.Stat(t.Path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	off := t.off
	if st.Size() < off {
		off = 0
	}
	if st.Size() <= off {
		return 0, nil
	}
	n, err := t.readFrom(off)
	t.off = off + n
	return n, err
}

// ReadOnce processes the whole file once (tests, replay diff).
func (t *Tailer) ReadOnce() error {
	_, err := t.readFrom(0)
	return err
}

func (t *Tailer) readFrom(off int64) (int64, error) {
	f, err := os.Open(t.Path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}

	if err != nil {
		return 0, err
	}

	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}

	r := bufio.NewReaderSize(f, 1<<20)
	var consumed int64
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return consumed, nil // EOF or partial line
		}

		consumed += int64(len(line))
		if err := t.handle(line); err != nil {
			return consumed, err
		}
	}
}

func (t *Tailer) handle(line []byte) error {
	if t.codex || looksCodexLine(line) {
		t.codex = true
		return t.handleCodex(line)
	}
	if !t.Prompts && !bytes.Contains(line, []byte(`"type":"assistant"`)) {
		return nil
	}

	var l transcriptLine
	if err := json.Unmarshal(line, &l); err != nil || l.IsMeta || (l.Type != "assistant" && !(t.Prompts && l.Type == "user")) {
		return nil
	}

	ts, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil {
		ts = time.Now()
	}

	if t.inherited(l, ts) {
		return nil
	}

	if l.Type == "user" {
		return t.handlePrompt(l, ts)
	}

	var blocks []contentBlock
	if err := json.Unmarshal(l.Message.Content, &blocks); err != nil {
		return nil
	}

	for i, b := range blocks {
		var kind event.Kind
		var text string
		switch b.Type {
		case "text":
			kind, text = event.KindAssistantText, b.Text
		case "thinking":
			if !t.CaptureThinking {
				continue
			}

			kind, text = event.KindAssistantThinking, b.Thinking
		default:
			continue
		}

		e := event.Event{
			ID: event.DeriveID(ts, "transcript:"+l.UUID+":"+itoa(i)), TSMs: ts.UnixMilli(),
			SessionID: l.SessionID, Source: event.SourceClaudeCode, Kind: kind, Role: event.RoleAssistant,
			Identity: t.Author, Model: l.Message.Model, TokensIn: l.Message.Usage.InputTokens, TokensOut: l.Message.Usage.OutputTokens,
			Content: obj(map[string]any{"text": text, "block": i, "transcript_uuid": l.UUID}),
		}
		if err := t.Emit(t.attribute(e)); err != nil {
			return err
		}
	}

	return nil
}

// handlePrompt records a user line that is text: a string, or text blocks.
// Tool results, also user lines, are recorded by the hooks.
func (t *Tailer) handlePrompt(l transcriptLine, ts time.Time) error {
	var text string
	if err := json.Unmarshal(l.Message.Content, &text); err != nil {
		var blocks []contentBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return nil
		}

		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}

		text = strings.Join(parts, "\n")
	}

	if strings.TrimSpace(text) == "" {
		return nil
	}

	e := event.Event{
		ID: event.DeriveID(ts, "transcript:"+l.UUID+":prompt"), TSMs: ts.UnixMilli(),
		SessionID: l.SessionID, Source: event.SourceClaudeCode, Kind: event.KindUserMessage, Role: event.RoleUser,
		Identity: t.Author, Content: obj(map[string]any{"text": text, "transcript_uuid": l.UUID}),
	}

	return t.Emit(t.attribute(e))
}

// sinceMargin allows for Claude Code writing a subagent's first line just
// before its start hook fires (50–200 ms measured).
const sinceMargin = 5 * time.Second

// inherited reports a line a forked agent copied from its parent.
func (t *Tailer) inherited(l transcriptLine, ts time.Time) bool {
	if t.AgentID == "" || t.Since.IsZero() || !ts.Before(t.Since.Add(-sinceMargin)) {
		return false
	}

	return !l.Sidechain || l.AgentID != t.AgentID
}

func (t *Tailer) attribute(e event.Event) event.Event {
	if t.AgentID != "" {
		e.AgentID, e.AgentType, e.ParentID = t.AgentID, t.AgentType, t.ParentID
	}

	return e
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}

	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}

	return string(b)
}

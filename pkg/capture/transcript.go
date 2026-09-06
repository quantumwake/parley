package capture

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/quantumwake/statefs.ai/pkg/event"
)

// transcriptLine is the S5 seam: the fields of a Claude Code transcript
// line the tailer reads. One assistant message is written as several
// lines, one per content block, sharing message.id; each line has its own
// uuid.
type transcriptLine struct {
	Type      string `json:"type"`
	UUID      string `json:"uuid"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId"`
	Message   struct {
		Model   string `json:"model"`
		Role    string `json:"role"`
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
	Poll            time.Duration // default 200 ms
	Emit            func(event.Event) error
}

// Run reads from the start, then follows growth until ctx is done. A
// partial trailing line is retried on the next poll.
func (t *Tailer) Run(ctx context.Context) error {
	if t.Poll <= 0 {
		t.Poll = 200 * time.Millisecond
	}

	var off int64
	for {
		n, err := t.readFrom(off)
		if err != nil {
			return err
		}

		off += n
		select {
		case <-ctx.Done():
			// One last pass so the final blocks are not lost.
			_, _ = t.readFrom(off)
			return nil
		case <-time.After(t.Poll):
		}
	}
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
	var l transcriptLine
	if err := json.Unmarshal(line, &l); err != nil || l.Type != "assistant" {
		return nil
	}

	var blocks []contentBlock
	if err := json.Unmarshal(l.Message.Content, &blocks); err != nil {
		return nil
	}

	ts, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil {
		ts = time.Now()
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
			Author: t.Author, Model: l.Message.Model, TokensIn: l.Message.Usage.InputTokens, TokensOut: l.Message.Usage.OutputTokens,
			Content: obj(map[string]any{"text": text, "block": i, "transcript_uuid": l.UUID}),
		}
		if err := t.Emit(e); err != nil {
			return err
		}
	}

	return nil
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

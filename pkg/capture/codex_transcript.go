package capture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/quantumwake/parley/pkg/event"
)

// Codex writes ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl. The format is
// not a public contract; this file is the only place that reads it.

type codexRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type codexResponseItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func looksCodexLine(line []byte) bool {
	if !bytes.Contains(line, []byte(`"payload"`)) {
		return false
	}
	return bytes.Contains(line, []byte(`"response_item"`)) ||
		bytes.Contains(line, []byte(`"session_meta"`)) ||
		bytes.Contains(line, []byte(`"event_msg"`))
}

func (t *Tailer) handleCodex(line []byte) error {
	var rec codexRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil
	}
	if rec.Type != "response_item" {
		if rec.Type == "session_meta" && t.SessionID == "" {
			var meta struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(rec.Payload, &meta) == nil && meta.ID != "" {
				t.SessionID = meta.ID
			}
		}
		return nil
	}

	var item codexResponseItem
	if err := json.Unmarshal(rec.Payload, &item); err != nil {
		return nil
	}
	if item.Role != "assistant" && item.Type != "reasoning" {
		return nil
	}

	ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil {
		ts, err = time.Parse(time.RFC3339, rec.Timestamp)
		if err != nil {
			ts = time.Now()
		}
	}

	sum := sha256.Sum256(line)
	seed := "codex:" + hex.EncodeToString(sum[:12])
	session := t.SessionID

	switch item.Type {
	case "reasoning":
		if !t.CaptureThinking {
			return nil
		}
		text := strings.TrimSpace(codexText(item, "reasoning_text", "text"))
		if text == "" {
			return nil
		}
		return t.Emit(event.Event{
			ID: event.DeriveID(ts, seed), TSMs: ts.UnixMilli(), SessionID: session,
			Source: event.SourceCodex, Kind: event.KindAssistantThinking, Role: event.RoleAssistant,
			Identity: t.Author, Content: obj(map[string]any{"text": text}),
		})
	case "message":
		text := strings.TrimSpace(codexText(item, "output_text", "text"))
		if text == "" {
			return nil
		}
		return t.Emit(event.Event{
			ID: event.DeriveID(ts, seed), TSMs: ts.UnixMilli(), SessionID: session,
			Source: event.SourceCodex, Kind: event.KindAssistantText, Role: event.RoleAssistant,
			Identity: t.Author, Content: obj(map[string]any{"text": text}),
		})
	default:
		return nil // function_call / outputs: hooks own those
	}
}

func codexText(item codexResponseItem, types ...string) string {
	want := map[string]bool{}
	for _, t := range types {
		want[t] = true
	}
	var parts []string
	for _, b := range item.Content {
		if want[b.Type] && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

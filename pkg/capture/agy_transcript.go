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

// Antigravity writes one JSON object per line to
// ~/.gemini/antigravity-cli/brain/<conversation>/.system_generated/logs/transcript.jsonl.
// The format is not a public contract; this file is the only place that reads it.
//
// Hooks own tool calls. This tail records the user prompt and the planner's
// reply, which the hooks do not carry. Ephemeral injections, system
// messages, checkpoints, and rendered tool logs are not records.

type agyRecord struct {
	StepIndex int    `json:"step_index"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
	Content   string `json:"content"`
}

func looksAntigravityLine(line []byte) bool {
	if !bytes.Contains(line, []byte(`"step_index"`)) {
		return false
	}
	for _, marker := range []string{
		`"PLANNER_RESPONSE"`, `"USER_INPUT"`, `"EPHEMERAL_MESSAGE"`,
		`"GENERIC"`, `"SYSTEM_MESSAGE"`, `"CHECKPOINT"`,
	} {
		if bytes.Contains(line, []byte(marker)) {
			return true
		}
	}
	return false
}

func (t *Tailer) handleAntigravity(line []byte) error {
	var rec agyRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil
	}
	var kind event.Kind
	var role event.Role
	var text string
	switch rec.Type {
	case "USER_INPUT":
		if rec.Source != "" && rec.Source != "USER_EXPLICIT" {
			return nil
		}
		text = agyUserRequest(rec.Content)
		kind, role = event.KindUserMessage, event.RoleUser
	case "PLANNER_RESPONSE":
		text = strings.TrimSpace(rec.Content)
		kind, role = event.KindAssistantText, event.RoleAssistant
	default:
		return nil
	}
	if text == "" {
		return nil
	}

	ts, err := time.Parse(time.RFC3339Nano, rec.CreatedAt)
	if err != nil {
		ts, err = time.Parse(time.RFC3339, rec.CreatedAt)
		if err != nil {
			ts = time.Now()
		}
	}
	sum := sha256.Sum256(line)
	seed := "agy:" + hex.EncodeToString(sum[:12])
	return t.Emit(event.Event{
		ID: event.DeriveID(ts, seed), TSMs: ts.UnixMilli(), SessionID: t.SessionID,
		Source: event.SourceAntigravity, Kind: kind, Role: role,
		Identity: t.Author, Content: obj(map[string]any{"text": text}),
	})
}

// agyUserRequest returns the text inside <USER_REQUEST>, without the
// metadata block Antigravity appends after it. A line with no request tag
// is not a prompt.
func agyUserRequest(content string) string {
	const open, close = "<USER_REQUEST>", "</USER_REQUEST>"
	i := strings.Index(content, open)
	if i < 0 {
		return ""
	}
	rest := content[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:j])
}

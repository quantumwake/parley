package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/event"
)

// A subagent's transcript yields its prompt, text and thinking, attributed
// to the agent, including its own lines from before Since (a first line
// written just before the start hook; an earlier run). Tool results, meta
// lines and a fork's inherited context are not recorded.
func TestTailerSubagentTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-a1.jsonl")
	lines := []string{
		`{"type":"user","uuid":"u0","timestamp":"2026-09-14T10:00:00Z","sessionId":"s","message":{"role":"user","content":"inherited from the parent"}}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-09-14T10:03:59.8Z","sessionId":"s","isSidechain":true,"agentId":"a1","message":{"role":"user","content":"Review PR 59"}}`,
		`{"type":"assistant","uuid":"old","timestamp":"2026-09-14T09:00:00Z","sessionId":"s","isSidechain":true,"agentId":"a1","message":{"role":"assistant","content":[{"type":"text","text":"earlier run of this agent"}]}}`,
		`{"type":"user","uuid":"m1","timestamp":"2026-09-14T10:05:00Z","sessionId":"s","isMeta":true,"message":{"role":"user","content":"meta"}}`,
		`{"type":"assistant","uuid":"x1","timestamp":"2026-09-14T10:05:01Z","sessionId":"s","message":{"role":"assistant","content":[{"type":"thinking","thinking":"look at sampler.go"},{"type":"text","text":"Reading the diff."},{"type":"tool_use","id":"t1"}]}}`,
		`{"type":"user","uuid":"r1","timestamp":"2026-09-14T10:05:02Z","sessionId":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-09-14T10:06:00Z","sessionId":"s","message":{"role":"user","content":[{"type":"text","text":"also check captions"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var got []event.Event
	tl := &Tailer{Path: path, Author: "kasra", CaptureThinking: true, Emit: func(e event.Event) error { got = append(got, e); return nil },
		AgentID: "a1", AgentType: "general-purpose", ParentID: "start-1", Prompts: true,
		Since: time.Date(2026, 9, 14, 10, 4, 0, 0, time.UTC)}
	if err := tl.ReadOnce(); err != nil {
		t.Fatal(err)
	}

	var kinds []string
	for _, e := range got {
		kinds = append(kinds, string(e.Kind)+":"+string(e.Content))
		if e.AgentID != "a1" || e.AgentType != "general-purpose" || e.ParentID != "start-1" {
			t.Fatalf("every row is attributed to the agent: %+v", e)
		}

		if err := e.Validate(); err != nil {
			t.Fatalf("row validates: %v", err)
		}
	}

	joined := strings.Join(kinds, "\n")
	for _, want := range []string{"user.message:{\"text\":\"Review PR 59\"", "assistant.text:{\"block\":0,\"text\":\"earlier run of this agent\"", "assistant.thinking:{\"block\":0,\"text\":\"look at sampler.go\"", "assistant.text:{\"block\":1,\"text\":\"Reading the diff.\"", "user.message:{\"text\":\"also check captions\""} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in:\n%s", want, joined)
		}
	}

	if len(got) != 5 || strings.Contains(joined, "inherited") || strings.Contains(joined, "meta") || strings.Contains(joined, "tool_result") {
		t.Fatalf("only the agent's own prompt, thinking and text:\n%s", joined)
	}
}

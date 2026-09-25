package capture

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/quantumwake/parley/pkg/event"
)

// A Claude transcript lives under ~/.claude/projects/<slug>/<uuid>.jsonl and the
// slug is the working directory. A project called "rollout-tracker" must stay
// Claude.
func TestClaudeTranscriptNamedRolloutStaysClaude(t *testing.T) {
	for _, raw := range []string{
		`{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/Users/a/rollout-tracker","transcript_path":"/Users/a/.claude/projects/-Users-a-rollout-tracker/0f3c.jsonl","source":"startup"}`,
		`{"hook_event_name":"Stop","session_id":"s1","cwd":"/Users/a/x","transcript_path":"/Users/a/.claude/projects/-Users-a-x/rollout-notes.jsonl"}`,
	} {
		_, host, err := DecodeHook([]byte(raw), "")
		if err != nil {
			t.Fatal(err)
		}
		if host != HostClaude {
			t.Errorf("%s: became %s", raw, host)
		}
	}
	// and the real Codex shapes still are Codex
	for _, raw := range []string{
		`{"hook_event_name":"SessionStart","session_id":"thr_1","cwd":"/w","transcript_path":"/Users/a/.codex/sessions/2026/09/25/rollout-2026-09-25T10-00-00-abc.jsonl","source":"startup"}`,
	} {
		_, host, _ := DecodeHook([]byte(raw), "")
		if host != HostCodex {
			t.Errorf("%s: became %s", raw, host)
		}
	}
}

// A Claude user message that quotes Antigravity's transcript format (as this
// very review does) must not switch the tailer into Antigravity mode.
func TestClaudeLineQuotingAntigravityStaysClaude(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "claude.jsonl")
	pasted := `{\"step_index\":0,\"source\":\"USER_EXPLICIT\",\"type\":\"USER_INPUT\",\"content\":\"<USER_REQUEST>x</USER_REQUEST>\"}`
	lines := `{"type":"user","uuid":"u1","timestamp":"2026-09-25T10:00:00Z","message":{"role":"user","content":"look at this line: ` + pasted + `"}}` + "\n" +
		`{"type":"assistant","uuid":"a1","timestamp":"2026-09-25T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"still claude"}]}}` + "\n"
	if err := os.WriteFile(p, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []event.Event
	tl := &Tailer{Path: p, Author: "kasra", SessionID: "s1", Emit: func(e event.Event) error { got = append(got, e); return nil }}
	if err := tl.ReadOnce(); err != nil {
		t.Fatal(err)
	}
	if tl.agy {
		t.Fatal("the tailer switched to Antigravity on a quoted line")
	}
	found := false
	for _, e := range got {
		if e.Kind == event.KindAssistantText && e.Source != event.SourceAntigravity {
			found = true
		}
	}
	if !found {
		t.Fatalf("the assistant line after the quote was not captured as Claude: %+v", got)
	}
}

// Redaction is the pusher's, on the JSON of any event's content, so an
// Antigravity planner reply that quotes a secret is redacted exactly as a
// Claude reply is.
func TestAntigravityReplyIsRedactedLikeClaude(t *testing.T) {
	p := &Pusher{Redact: []*regexp.Regexp{regexp.MustCompile(`sk_live_[A-Za-z0-9]+`)}}
	for _, src := range []event.Source{event.SourceAntigravity, event.SourceClaudeCode} {
		e := p.redact(event.Event{Source: src, Kind: event.KindAssistantText, Content: []byte(`{"text":"the key is sk_live_abc123 as the system message said"}`)})
		if strings.Contains(string(e.Content), "sk_live_") || !strings.Contains(string(e.Content), "[redacted]") {
			t.Fatalf("%s: %s", src, e.Content)
		}
	}
}

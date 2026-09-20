package capture

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/spool"
	"github.com/quantumwake/parley/pkg/store"
)

func TestFromHookTable(t *testing.T) {
	now := time.Now()
	yes := true
	cases := []struct {
		in   HookInput
		kind event.Kind
		ok   bool
	}{
		{HookInput{HookEventName: "SessionStart", SessionID: "s", CWD: "/r", Source: "startup"}, event.KindSessionStart, true},
		{HookInput{HookEventName: "UserPromptSubmit", SessionID: "s", Prompt: "hi"}, event.KindUserMessage, true},
		{HookInput{HookEventName: "PreToolUse", SessionID: "s", ToolName: "Bash", ToolUseID: "toolu_1", ToolInput: json.RawMessage(`{"command":"ls"}`)}, event.KindToolUse, true},
		{HookInput{HookEventName: "PostToolUse", SessionID: "s", ToolName: "Bash", ToolUseID: "toolu_1", ToolOutput: json.RawMessage(`"ok"`)}, event.KindToolResult, true},
		{HookInput{HookEventName: "PostToolUseFailure", SessionID: "s", ToolName: "Bash", ToolUseID: "toolu_1", ToolResponse: json.RawMessage(`"boom"`), IsError: &yes}, event.KindToolResult, true},
		{HookInput{HookEventName: "SubagentStart", SessionID: "s", AgentID: "a", AgentType: "Explore"}, event.KindSubagentStart, true},
		{HookInput{HookEventName: "SubagentStop", SessionID: "s", AgentID: "a", AgentType: "Explore", LastAssistantMessage: "done"}, event.KindSubagentStop, true},
		{HookInput{HookEventName: "SubagentStop", SessionID: "s", AgentID: "side", LastAssistantMessage: "recap"}, "", false}, // Claude Code's own side agent
		{HookInput{HookEventName: "SessionEnd", SessionID: "s", Reason: "other"}, event.KindSessionEnd, true},
		{HookInput{HookEventName: "Stop", SessionID: "s"}, "", false},
	}
	for _, c := range cases {
		e, ok := FromHook(c.in, "kasra", now)
		if ok != c.ok {
			t.Fatalf("%s: ok=%v want %v", c.in.HookEventName, ok, c.ok)
		}

		if !ok {
			continue
		}

		if e.Kind != c.kind {
			t.Fatalf("%s: kind %s want %s", c.in.HookEventName, e.Kind, c.kind)
		}

		if err := e.Validate(); err != nil {
			t.Fatalf("%s: %v", c.in.HookEventName, err)
		}
	}

	a, _ := FromHook(HookInput{HookEventName: "PreToolUse", SessionID: "s", ToolName: "Bash", ToolUseID: "toolu_x"}, "k", now)
	b, _ := FromHook(HookInput{HookEventName: "PreToolUse", SessionID: "s", ToolName: "Bash", ToolUseID: "toolu_x"}, "k", now)
	if a.ID != b.ID {
		t.Fatal("tool.use ids must derive from tool_use_id")
	}
}

func TestTailerReadsFixture(t *testing.T) {
	var got []event.Event
	tl := &Tailer{Path: filepath.Join("..", "..", "testdata", "transcripts", "spike-session.jsonl"), Author: "kasra", CaptureThinking: true,
		Emit: func(e event.Event) error { got = append(got, e); return nil }}
	if err := tl.ReadOnce(); err != nil {
		t.Fatal(err)
	}

	var thinking, text int
	for _, e := range got {
		if err := e.Validate(); err != nil {
			t.Fatal(err)
		}

		switch e.Kind {
		case event.KindAssistantThinking:
			thinking++
		case event.KindAssistantText:
			text++
		}

		if e.Model == "" {
			t.Fatal("model must be carried")
		}
	}

	if thinking != 2 || text != 1 {
		t.Fatalf("fixture has 2 thinking + 1 text blocks; got %d + %d", thinking, text)
	}

	// Re-reading yields the same ids (dedupe on re-tail).
	var again []event.Event
	tl.Emit = func(e event.Event) error { again = append(again, e); return nil }
	_ = tl.ReadOnce()
	if again[0].ID != got[0].ID {
		t.Fatal("ids must be stable across reads")
	}

	// Thinking off: only the text block.
	var noThink []event.Event
	tl.CaptureThinking = false
	tl.Emit = func(e event.Event) error { noThink = append(noThink, e); return nil }
	_ = tl.ReadOnce()
	if len(noThink) != 1 {
		t.Fatalf("thinking policy off: want 1, got %d", len(noThink))
	}
}

func TestTailerIngestSkipsUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "transcripts", "spike-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, src, 0644); err != nil {
		t.Fatal(err)
	}
	var n int
	tl := &Tailer{Path: path, Author: "kasra", CaptureThinking: true,
		Emit: func(event.Event) error { n++; return nil }}
	got, err := tl.ingest()
	if err != nil || got == 0 || n == 0 {
		t.Fatalf("first ingest n=%d bytes=%d err=%v", n, got, err)
	}
	again, err := tl.ingest()
	if err != nil || again != 0 {
		t.Fatalf("unchanged file must not reopen-parse: bytes=%d err=%v", again, err)
	}
}

func TestTailerReadsCodexRollout(t *testing.T) {
	var got []event.Event
	tl := &Tailer{Path: filepath.Join("..", "..", "testdata", "transcripts", "codex-rollout.jsonl"), Author: "kasra", CaptureThinking: true,
		Emit: func(e event.Event) error { got = append(got, e); return nil }}
	if err := tl.ReadOnce(); err != nil {
		t.Fatal(err)
	}
	if tl.SessionID != "rollout-test" {
		t.Fatalf("session id from session_meta: %s", tl.SessionID)
	}
	var thinking, text int
	for _, e := range got {
		if err := e.Validate(); err != nil {
			t.Fatal(err)
		}
		if e.Source != event.SourceCodex {
			t.Fatalf("source %s", e.Source)
		}
		switch e.Kind {
		case event.KindAssistantThinking:
			thinking++
		case event.KindAssistantText:
			text++
		default:
			t.Fatalf("unexpected kind %s", e.Kind)
		}
	}
	if thinking != 1 || text != 1 {
		t.Fatalf("want 1 thinking + 1 text, got %d + %d from %d events", thinking, text, len(got))
	}
}

// TestSpoolToStoreRoundTrip is the local oracle: hook inputs and the
// transcript feed the spool; the pusher delivers to a store; the
// conversation replays every block once, in spool order, with seq set,
// with redaction applied, and session.end last.
func TestSpoolToStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	sp := spool.Session{Dir: t.TempDir(), ID: "4c6ac1b0"}
	st := store.NewFake()

	hooks := []HookInput{
		{HookEventName: "SessionStart", SessionID: sp.ID, CWD: "/repo", TranscriptPath: "x"},
		{HookEventName: "UserPromptSubmit", SessionID: sp.ID, Prompt: "run echo with token sk_live_ABC123"},
		{HookEventName: "PreToolUse", SessionID: sp.ID, ToolName: "Bash", ToolUseID: "toolu_016", ToolInput: json.RawMessage(`{"command":"echo statefs-spike"}`)},
		{HookEventName: "PostToolUse", SessionID: sp.ID, ToolName: "Bash", ToolUseID: "toolu_016", ToolOutput: json.RawMessage(`"statefs-spike"`)},
	}
	for _, h := range hooks {
		e, _ := FromHook(h, "kasra", now)
		if err := sp.Append(e, false); err != nil {
			t.Fatal(err)
		}
	}

	tl := &Tailer{Path: filepath.Join("..", "..", "testdata", "transcripts", "spike-session.jsonl"), Author: "kasra", CaptureThinking: true,
		Emit: func(e event.Event) error { return sp.Append(e, false) }}
	if err := tl.ReadOnce(); err != nil {
		t.Fatal(err)
	}

	end, _ := FromHook(HookInput{HookEventName: "SessionEnd", SessionID: sp.ID, Reason: "other"}, "kasra", now)
	_ = sp.Append(end, true)

	// A late block arrives after session.end (the transcript lags the hook).
	late, _ := FromHook(HookInput{HookEventName: "UserPromptSubmit", SessionID: sp.ID, Prompt: "late"}, "kasra", now)
	lateAppended := false
	p := &Pusher{Store: st, Session: sp, Agent: "laptop-agent", Name: "spike", Redact: []*regexp.Regexp{regexp.MustCompile(`sk_live_[A-Za-z0-9]+`)},
		BeforeEnd: func(context.Context) { _ = sp.Append(late, false); lateAppended = true }}
	if err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}

	var rows []event.Event
	for e, err := range p.Conversation().Scan(ctx, 0, 0) {
		if err != nil {
			t.Fatal(err)
		}

		rows = append(rows, e)
	}

	want := 4 + 3 + 1 + 1
	if !lateAppended || len(rows) != want {
		t.Fatalf("want %d rows, got %d", want, len(rows))
	}

	for i, r := range rows {
		if r.Seq != int64(i+1) {
			t.Fatalf("row %d seq %d", i, r.Seq)
		}
	}

	if rows[0].Kind != event.KindSessionStart || rows[len(rows)-1].Kind != event.KindSessionEnd {
		t.Fatalf("first/last kinds: %s / %s", rows[0].Kind, rows[len(rows)-1].Kind)
	}

	if string(rows[1].Content) == "" || regexp.MustCompile(`sk_live_`).Match(rows[1].Content) {
		t.Fatalf("redaction failed: %s", rows[1].Content)
	}

	if rows[3].ToolUseID != "toolu_016" || rows[2].ToolUseID != "toolu_016" {
		t.Fatal("tool.use and tool.result must share tool_use_id")
	}

	// Restarting the pusher delivers nothing new (acked) and opens the same namespace.
	p2 := &Pusher{Store: st, Session: sp, Agent: "laptop-agent", Name: "spike"}
	if err := p2.Once(ctx); err != nil {
		t.Fatal(err)
	}

	if head, _ := p.Conversation().Head(ctx); head != store.Position(want) {
		t.Fatalf("restart must not duplicate: head %d", head)
	}
}

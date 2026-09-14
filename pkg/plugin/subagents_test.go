package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/spool"
)

type subagentHarness struct {
	t          *testing.T
	sp         spool.Session
	transcript string
	m          *subagents
	mu         sync.Mutex
	seen       map[string]bool
	line       int
}

func newSubagentHarness(t *testing.T) *subagentHarness {
	t.Helper()
	dir := t.TempDir()
	h := &subagentHarness{t: t, sp: spool.Session{Dir: filepath.Join(dir, "spool"), ID: "s1"},
		transcript: filepath.Join(dir, "s1.jsonl"), seen: map[string]bool{}}
	if err := os.MkdirAll(filepath.Join(dir, "s1", "subagents"), 0o700); err != nil {
		t.Fatal(err)
	}

	emit := func(e event.Event) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.seen[e.ID] {
			return nil
		}

		h.seen[e.ID] = true
		return h.sp.Append(e, false)
	}
	h.m = newSubagents(h.sp, h.transcript, "kasra", true, emit)
	h.m.quiet, h.m.every = 100*time.Millisecond, 20*time.Millisecond
	return h
}

// start runs the manager until the test ends, and waits for it to stop.
func (h *subagentHarness) start() context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.m.Run(ctx); close(done) }()
	h.t.Cleanup(func() { cancel(); <-done })
	return cancel
}

// hook spools a row the way a hook does.
func (h *subagentHarness) hook(kind event.Kind, agent string) event.Event {
	h.t.Helper()
	now := time.Now()
	e := event.Event{ID: event.NewIDAt(now), TSMs: now.Add(-time.Second).UnixMilli(), SessionID: "s1", Source: event.SourceClaudeCode,
		Kind: kind, Role: event.RoleSystem, Identity: "kasra", AgentID: agent, AgentType: "general-purpose", Content: []byte(`{}`)}
	if kind == event.KindToolUse {
		e.Role, e.ToolName, e.ToolUseID = event.RoleAssistant, "Bash", "t-"+now.String()
		e.Content = []byte(`{"input":{}}`)
	}

	if err := h.sp.Append(e, false); err != nil {
		h.t.Fatal(err)
	}

	return e
}

// write appends one assistant text line to an agent's transcript.
func (h *subagentHarness) write(agent, text string) {
	h.t.Helper()
	h.line++
	f, err := os.OpenFile(SubagentTranscript(h.transcript, agent), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	fmt.Fprintf(f, `{"type":"assistant","uuid":"%s-%d","timestamp":"%s","sessionId":"s1","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`+"\n",
		agent, h.line, time.Now().UTC().Format(time.RFC3339Nano), text)
}

// texts are the agent's recorded text rows, in spool order.
func (h *subagentHarness) texts(agent string) []string {
	var out []string
	for entry, err := range h.sp.Read(0) {
		if err == nil && entry.Event.AgentID == agent && entry.Event.Kind == event.KindAssistantText {
			out = append(out, string(entry.Event.Content))
		}
	}

	return out
}

func (h *subagentHarness) eventually(cond func() bool, what string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatal(what)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func (h *subagentHarness) running() int {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	return h.m.running
}

// A subagent's transcript is followed from its start to its stop, not
// before and not after; a resume continues from where it stopped.
func TestSubagentTailIsBoundedByItsHooks(t *testing.T) {
	h := newSubagentHarness(t)
	stop := h.start()

	h.hook(event.KindSubagentStart, "a1")
	h.eventually(func() bool { return h.running() == 1 }, "a start opens a tailer")
	h.write("a1", "first")
	h.eventually(func() bool { return len(h.texts("a1")) == 1 }, "rows written while running are recorded")

	h.hook(event.KindSubagentStop, "a1")
	h.eventually(func() bool { return h.running() == 0 }, "a stop closes the tailer")

	h.write("a1", "after the stop")
	time.Sleep(300 * time.Millisecond)
	if got := h.texts("a1"); len(got) != 1 {
		t.Fatalf("nothing is read between a stop and the next start: %v", got)
	}

	// A resumed background agent: the next hook row with its agent_id reopens
	// the tailer from the saved offset.
	h.hook(event.KindToolUse, "a1")
	h.eventually(func() bool { return len(h.texts("a1")) == 2 }, "a resume reads what was written since, once")
	if got := h.texts("a1"); !strings.Contains(got[1], "after the stop") {
		t.Fatalf("resumed from the offset: %v", got)
	}

	stop()
	h.eventually(func() bool { return h.running() == 0 }, "stopping the daemon closes every tailer")
	if h.running() != 0 {
		t.Fatal("stopping the daemon closes every tailer")
	}
}

// A tailer whose transcript stops growing closes itself.
func TestSubagentTailClosesWhenIdle(t *testing.T) {
	h := newSubagentHarness(t)
	h.m.idle = 200 * time.Millisecond
	h.start()

	h.hook(event.KindSubagentStart, "a1")
	h.eventually(func() bool { return h.running() == 1 }, "opened")
	h.eventually(func() bool { return h.running() == 0 }, "an idle transcript closes its tailer without a stop")
}

// A restarted daemon reopens agents still running and leaves finished ones
// closed.
func TestSubagentCatchUpAfterRestart(t *testing.T) {
	h := newSubagentHarness(t)
	h.hook(event.KindSubagentStart, "done")
	h.hook(event.KindSubagentStop, "done")
	h.hook(event.KindSubagentStart, "live")
	h.write("live", "while the daemon was down")

	h.start()

	h.eventually(func() bool { return len(h.texts("live")) == 1 }, "a running agent is reopened and its transcript read")
	if h.running() != 1 {
		t.Fatalf("only the running agent is followed: %d", h.running())
	}
}

// Beyond the cap, a start is marked "not captured" once instead of silently
// skipped.
func TestSubagentCapIsVisible(t *testing.T) {
	h := newSubagentHarness(t)
	h.m.max = 1
	h.start()

	h.hook(event.KindSubagentStart, "a1")
	h.eventually(func() bool { return h.running() == 1 }, "first opened")
	h.hook(event.KindSubagentStart, "a2")
	h.hook(event.KindToolUse, "a2")

	h.eventually(func() bool {
		n := 0
		for entry, err := range h.sp.Read(0) {
			if err == nil && entry.Event.AgentID == "a2" && strings.Contains(string(entry.Event.Content), "not captured") {
				n++
			}
		}
		return n == 1
	}, "one not-captured notice for the agent over the cap")
}

// Before the session's end is delivered, every subagent is drained, so its
// last rows land first.
func TestSubagentsDrainBeforeEnd(t *testing.T) {
	h := newSubagentHarness(t)
	h.start()

	h.hook(event.KindSubagentStart, "a1")
	h.eventually(func() bool { return h.running() == 1 }, "opened")
	h.write("a1", "last words")

	h.m.BeforeEnd(context.Background())
	if got := h.texts("a1"); len(got) != 1 || h.running() != 0 {
		t.Fatalf("drained and closed before the end: %v, running %d", got, h.running())
	}
}

// A subagent.start is labelled with its description and transcript.
func TestSubagentStartContent(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "s1.jsonl")
	meta := strings.TrimSuffix(SubagentTranscript(transcript, "a1"), ".jsonl") + ".meta.json"
	_ = os.MkdirAll(filepath.Dir(meta), 0o700)
	_ = os.WriteFile(meta, []byte(`{"agentType":"general-purpose","description":"Review statefs telemetry PR #59"}`), 0o600)

	got := string(subagentStartContent(Input{TranscriptPath: transcript, AgentID: "a1"}))
	if !strings.Contains(got, "Review statefs telemetry PR #59") || !strings.Contains(got, "agent-a1.jsonl") {
		t.Fatalf("start content: %s", got)
	}
}

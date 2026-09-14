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
		Kind: kind, Role: event.RoleSystem, Identity: "kasra", AgentID: agent, Content: []byte(`{}`)}
	if agent != "" {
		e.AgentType = "general-purpose"
	}
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

func (h *subagentHarness) closing(agent string) bool {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	a := h.m.agents[agent]
	return a == nil || a.closing
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
	h.eventually(func() bool { return h.running() == 0 && !h.closing("a1") }, "a stop closes the tailer and finishes")

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

// Beyond the cap nothing is tailed, and the agent's stop reads what it
// wrote in one pass: nothing is lost, and no notice row is written.
func TestSubagentBeyondTheCapIsReadAtItsStop(t *testing.T) {
	h := newSubagentHarness(t)
	h.m.max = 1
	h.start()

	h.hook(event.KindSubagentStart, "a1")
	h.eventually(func() bool { return h.running() == 1 }, "first opened")
	h.hook(event.KindSubagentStart, "a2")
	h.write("a2", "over the cap")
	time.Sleep(200 * time.Millisecond)
	if h.running() != 1 || len(h.texts("a2")) != 0 {
		t.Fatalf("the second agent is not tailed: running %d, %v", h.running(), h.texts("a2"))
	}

	h.hook(event.KindSubagentStop, "a2")
	h.eventually(func() bool { return len(h.texts("a2")) == 1 }, "its stop reads it in one pass")
}

// A stop with no tailer open (it closed itself while the agent thought for
// a long time) still reads the agent's last words.
func TestSubagentStopAfterIdleCloseReadsTheRest(t *testing.T) {
	h := newSubagentHarness(t)
	h.m.idle = 150 * time.Millisecond
	h.start()

	h.hook(event.KindSubagentStart, "a1")
	h.eventually(func() bool { return h.running() == 1 }, "opened")
	h.eventually(func() bool { return h.running() == 0 }, "closed itself while idle")
	h.write("a1", "final answer")
	h.hook(event.KindSubagentStop, "a1")
	h.eventually(func() bool { return len(h.texts("a1")) == 1 }, "the stop reads the final answer")
}

// A resume that arrives while a stop is still draining is followed once the
// drain is done.
func TestSubagentResumeDuringDrainIsFollowed(t *testing.T) {
	h := newSubagentHarness(t)
	h.m.quiet = 400 * time.Millisecond
	h.start()

	h.hook(event.KindSubagentStart, "a1")
	h.eventually(func() bool { return h.running() == 1 }, "opened")
	h.hook(event.KindSubagentStop, "a1")
	time.Sleep(100 * time.Millisecond) // the stop is draining
	h.hook(event.KindSubagentStart, "a1")

	h.eventually(func() bool { return h.running() == 1 }, "reopened after the drain")
	h.write("a1", "resumed work")
	h.eventually(func() bool { return len(h.texts("a1")) == 1 }, "the resumed run is recorded")
}

// A Run after a push retry reopens the agents still running.
func TestSubagentRunAgainReopensRunningAgents(t *testing.T) {
	h := newSubagentHarness(t)
	stop := h.start()
	h.hook(event.KindSubagentStart, "a1")
	h.eventually(func() bool { return h.running() == 1 }, "opened")
	stop()
	h.eventually(func() bool { return h.running() == 0 }, "closed with the run")

	h.start()
	h.eventually(func() bool { return h.running() == 1 }, "a second Run reopens it")
	h.write("a1", "after the retry")
	h.eventually(func() bool { return len(h.texts("a1")) == 1 }, "and records it")
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

	h.hook(event.KindSubagentStart, "a2")
	time.Sleep(200 * time.Millisecond)
	if h.running() != 0 {
		t.Fatal("nothing new is opened while the end is being delivered")
	}
}

// A subagent.start is labelled with its description and transcript.
func TestSubagentStartContent(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "s1.jsonl")
	meta := strings.TrimSuffix(SubagentTranscript(transcript, "a1"), ".jsonl") + ".meta.json"
	_ = os.MkdirAll(filepath.Dir(meta), 0o700)
	go func() {
		time.Sleep(50 * time.Millisecond) // Claude Code writes it just after the hook fires
		_ = os.WriteFile(meta, []byte(`{"agentType":"general-purpose","description":"Review statefs telemetry PR #59"}`), 0o600)
	}()

	got := string(subagentStartContent(Input{TranscriptPath: transcript, AgentID: "a1"}))
	if !strings.Contains(got, "Review statefs telemetry PR #59") || !strings.Contains(got, "agent-a1.jsonl") {
		t.Fatalf("start content: %s", got)
	}
}

// An agent that writes and stops while no Run is polling (a push retry, a
// daemon that was down) is still read when the next Run starts.
func TestSubagentStoppedBetweenRunsIsRead(t *testing.T) {
	h := newSubagentHarness(t)
	stop := h.start()
	stop()

	h.hook(event.KindSubagentStart, "gap")
	h.write("gap", "all of it in the gap")
	h.hook(event.KindSubagentStop, "gap")

	h.start()
	h.eventually(func() bool { return len(h.texts("gap")) == 1 }, "the next Run reads an agent that stopped in the gap")
}

// BeforeEnd waits for a stop that was already draining, so the agent's last
// line lands before the session's end.
func TestBeforeEndWaitsForADrainingStop(t *testing.T) {
	h := newSubagentHarness(t)
	h.m.quiet = 500 * time.Millisecond
	h.start()

	h.hook(event.KindSubagentStart, "a1")
	h.eventually(func() bool { return h.running() == 1 }, "opened")
	h.hook(event.KindSubagentStop, "a1")
	time.Sleep(80 * time.Millisecond) // the stop is draining
	h.write("a1", "just before the end")

	h.m.BeforeEnd(context.Background())
	if got := h.texts("a1"); len(got) != 1 {
		t.Fatalf("the draining stop finished before BeforeEnd returned: %v", got)
	}
}

// A session.start after an end lets subagents be followed again, without a
// new Run.
func TestResumeAfterEndReopensSubagents(t *testing.T) {
	h := newSubagentHarness(t)
	h.start()
	h.m.BeforeEnd(context.Background())

	h.hook(event.KindSessionStart, "")
	h.hook(event.KindSubagentStart, "a1")
	h.eventually(func() bool { return h.running() == 1 }, "a resumed session's subagent is followed")
}

// Offsets survive a restart: a new manager continues where the last one
// stopped reading.
func TestSubagentOffsetsSurviveARestart(t *testing.T) {
	h := newSubagentHarness(t)
	h.start()
	h.hook(event.KindSubagentStart, "a1")
	h.write("a1", "one")
	h.hook(event.KindSubagentStop, "a1")
	h.eventually(func() bool { return len(h.texts("a1")) == 1 }, "read")
	h.eventually(func() bool { _, err := os.Stat(h.m.offsetsPath()); return err == nil }, "offsets saved")

	again := newSubagents(h.sp, h.transcript, "kasra", true, func(event.Event) error { t.Fatal("nothing is re-read"); return nil })
	again.mu.Lock()
	a := again.agents["a1"]
	again.mu.Unlock()
	if a == nil || a.offset == 0 {
		t.Fatalf("a restarted manager knows the offset: %+v", a)
	}

	again.readRest(a) // from the saved offset: nothing new, nothing emitted
}

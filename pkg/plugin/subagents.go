package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quantumwake/parley/pkg/capture"
	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/spool"
)

// A subagent writes its own transcript beside the session's:
//
//	<project>/<session>.jsonl                               the session
//	<project>/<session>/subagents/agent-<agent_id>.jsonl    each subagent
//
// The daemon tails a subagent's transcript only between the hooks that
// bound it. It learns of them from the spool, where every hook writes its
// row: subagent.start opens a tailer from the last offset for that agent,
// subagent.stop drains and closes it, and a session.end about to be
// delivered drains them all. A tailer whose file has not grown for
// subagentIdle closes itself. A subagent's tool rows (which carry its
// agent_id) reopen a closed tailer, for an agent that outlived a restart
// and continues with no new start.

var (
	// subagentIdle closes a tailer whose transcript has not grown.
	subagentIdle = 10 * time.Minute
	// subagentPoll is how often the spool is read for starts and stops.
	subagentPoll = 300 * time.Millisecond
)

// subagentTailers is how many subagent transcripts are followed at once
// (PARLEY_SUBAGENT_TAILERS, default 16).
func subagentTailers() int {
	if n, err := strconv.Atoi(os.Getenv("PARLEY_SUBAGENT_TAILERS")); err == nil && n > 0 {
		return n
	}

	return 16
}

// SubagentTranscript is the transcript Claude Code writes for a subagent of
// the session whose transcript is sessionTranscript.
func SubagentTranscript(sessionTranscript, agentID string) string {
	return filepath.Join(strings.TrimSuffix(sessionTranscript, ".jsonl"), "subagents", "agent-"+agentID+".jsonl")
}

// subagentDescription is the label a subagent was launched with, from the
// meta file Claude Code writes beside its transcript. Best effort: the file
// is not a documented interface.
func subagentDescription(sessionTranscript, agentID string) string {
	b, err := os.ReadFile(strings.TrimSuffix(SubagentTranscript(sessionTranscript, agentID), ".jsonl") + ".meta.json")
	if err != nil {
		return ""
	}

	var meta struct {
		Description string `json:"description"`
	}

	if json.Unmarshal(b, &meta) != nil {
		return ""
	}

	return meta.Description
}

// subagents follows the transcripts of one session's subagents.
type subagents struct {
	spool      spool.Session
	transcript string // the session's transcript
	author     string
	thinking   bool
	emit       func(event.Event) error // appends to the spool, skipping rows already spooled
	max        int

	mu      sync.Mutex
	read    int64 // spool offset handled so far
	agents  map[string]*subagent
	running int
	quiet   time.Duration // the transcript must be still this long before a drain ends
	idle    time.Duration // a tailer closes after its transcript has not grown this long
	every   time.Duration // how often the spool is read
}

type subagent struct {
	id, kind, startID string
	since             time.Time
	offset            int64
	tail              *subTail
	notified          bool // told the conversation its transcript was not captured
}

type subTail struct {
	tailer *capture.Tailer
	cancel context.CancelFunc
	done   chan struct{}
}

func newSubagents(sp spool.Session, transcript, author string, thinking bool, emit func(event.Event) error) *subagents {
	return &subagents{spool: sp, transcript: transcript, author: author, thinking: thinking, emit: emit,
		max: subagentTailers(), agents: map[string]*subagent{}, quiet: quietWindow, idle: subagentIdle, every: subagentPoll}
}

// Run reads the spool for subagent rows until ctx is done, then closes
// every tailer. The first pass over rows already spooled only rebuilds
// state, so a restarted daemon reopens the agents still running and none
// that finished.
func (m *subagents) Run(ctx context.Context) {
	m.catchUp(ctx)
	for {
		select {
		case <-ctx.Done():
			m.closeAll(context.Background(), false)
			return
		case <-time.After(m.every):
		}

		m.poll(ctx)
	}
}

// catchUp replays rows already spooled into state, then opens the agents
// whose last row was not a stop.
func (m *subagents) catchUp(ctx context.Context) {
	m.mu.Lock()
	first := m.read == 0
	m.mu.Unlock()
	if !first {
		return
	}

	running := map[string]bool{}
	for entry, err := range m.spool.Read(0) {
		if err != nil {
			continue
		}

		e := entry.Event
		m.mu.Lock()
		m.read = entry.Next
		m.mu.Unlock()
		if e.AgentID == "" {
			continue
		}

		switch e.Kind {
		case event.KindSubagentStart:
			m.record(e)
			running[e.AgentID] = true
		case event.KindSubagentStop:
			running[e.AgentID] = false
		case event.KindToolUse, event.KindToolResult:
			m.record(e)
			running[e.AgentID] = true
		}
	}

	for id, run := range running {
		if run {
			m.open(ctx, id)
		}
	}
}

func (m *subagents) poll(ctx context.Context) {
	m.mu.Lock()
	from := m.read
	m.mu.Unlock()

	for entry, err := range m.spool.Read(from) {
		if err != nil {
			continue
		}

		m.mu.Lock()
		m.read = entry.Next
		m.mu.Unlock()

		e := entry.Event
		if e.AgentID == "" {
			continue
		}

		switch e.Kind {
		case event.KindSubagentStart:
			m.record(e)
			m.open(ctx, e.AgentID)
		case event.KindSubagentStop:
			go m.close(ctx, e.AgentID, true)
		case event.KindToolUse, event.KindToolResult:
			m.record(e)
			m.open(ctx, e.AgentID) // no-op while open; reopens an agent that resumed without a start
		}
	}
}

// record notes an agent's type, its first start (the parent of its rows
// and the time before which its transcript is inherited context).
func (m *subagents) record(e event.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[e.AgentID]
	if a == nil {
		a = &subagent{id: e.AgentID}
		m.agents[e.AgentID] = a
	}

	if a.kind == "" {
		a.kind = e.AgentType
	}

	if e.Kind == event.KindSubagentStart && a.startID == "" {
		a.startID, a.since = e.ID, time.UnixMilli(e.TSMs)
	}
}

// open starts following an agent's transcript from its offset, unless it
// is already followed or the cap is reached.
func (m *subagents) open(ctx context.Context, id string) {
	m.mu.Lock()
	a := m.agents[id]
	if a == nil || a.tail != nil {
		m.mu.Unlock()
		return
	}

	if m.running >= m.max {
		notify := !a.notified
		a.notified = true
		m.mu.Unlock()
		if notify {
			_ = m.emit(m.notCaptured(a))
		}

		return
	}

	path := SubagentTranscript(m.transcript, id)
	t := &capture.Tailer{Path: path, Author: m.author, CaptureThinking: m.thinking, Emit: m.emit,
		AgentID: id, AgentType: a.kind, ParentID: a.startID, Prompts: true, Since: a.since}
	t.SetOffset(a.offset)

	tctx, cancel := idleContext(ctx, path, m.idle)
	tail := &subTail{tailer: t, cancel: cancel, done: make(chan struct{})}
	a.tail = tail
	m.running++
	m.mu.Unlock()

	go func() {
		defer close(tail.done)
		_ = t.Run(tctx)
		cancel()

		m.mu.Lock()
		defer m.mu.Unlock()
		a.offset = t.Offset()
		if a.tail == tail {
			a.tail = nil
			m.running--
		}
	}()
}

// close stops following an agent: after its transcript has been quiet for
// the quiet window (10 s at most) when drain is set, at once otherwise. The
// tailer's last pass reads to the end.
func (m *subagents) close(ctx context.Context, id string, drain bool) {
	m.mu.Lock()
	a := m.agents[id]
	var tail *subTail
	if a != nil {
		tail = a.tail
	}
	m.mu.Unlock()

	if tail == nil {
		return
	}

	if drain {
		waitQuiet(ctx, tail.tailer.Path, m.quiet, 10*time.Second)
	}

	tail.cancel()
	<-tail.done
}

// closeAll stops every tailer, draining them together when drain is set.
func (m *subagents) closeAll(ctx context.Context, drain bool) {
	m.mu.Lock()
	var ids []string
	for id, a := range m.agents {
		if a.tail != nil {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			m.close(ctx, id, drain)
		}(id)
	}

	wg.Wait()
}

// BeforeEnd drains every subagent before the session's end is delivered, so
// their last rows land before it.
func (m *subagents) BeforeEnd(ctx context.Context) {
	m.poll(ctx) // a stop spooled just before the end
	m.closeAll(ctx, true)
}

// notCaptured is the row that says an agent's transcript was not followed.
func (m *subagents) notCaptured(a *subagent) event.Event {
	now := time.Now()
	return event.Event{
		ID: event.DeriveID(now, "subagent-not-captured:"+a.id), TSMs: now.UnixMilli(),
		Source: event.SourceClaudeCode, Kind: event.KindAssistantText, Role: event.RoleSystem,
		Identity: m.author, AgentID: a.id, AgentType: a.kind, ParentID: a.startID,
		Content: obj(map[string]any{
			"text":       fmt.Sprintf("transcript not captured: more than %d subagents were running at once", m.max),
			"transcript": "not captured",
		}),
	}
}

func obj(m map[string]any) json.RawMessage {
	b, _ := json.Marshal(m)
	return b
}

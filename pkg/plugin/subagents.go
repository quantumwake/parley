package plugin

import (
	"context"
	"encoding/json"
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
// subagent.stop drains and closes it and reads what is left, and a
// session.end about to be delivered drains them all. A tailer whose file
// has not grown for subagentIdle closes itself. A subagent's tool rows (which
// carry its agent_id) reopen a closed tailer, for an agent that outlived a
// restart and continues with no new start. Offsets live in memory; a
// restarted daemon re-reads from the start, and rows already spooled are not
// written again.

var (
	// subagentIdle closes a tailer whose transcript has not grown.
	subagentIdle = 10 * time.Minute
	// subagentPoll is how often the spool is read for starts and stops.
	subagentPoll = 300 * time.Millisecond
	// subagentMetaWait bounds how long a start hook waits for the meta file.
	subagentMetaWait = 300 * time.Millisecond
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
	// Claude Code writes the meta file a few milliseconds after the start
	// hook fires (6–25 ms measured), so wait for it briefly.
	path := strings.TrimSuffix(SubagentTranscript(sessionTranscript, agentID), ".jsonl") + ".meta.json"
	var b []byte
	var err error
	for deadline := time.Now().Add(subagentMetaWait); ; time.Sleep(20 * time.Millisecond) {
		if b, err = os.ReadFile(path); err == nil || time.Now().After(deadline) {
			break
		}
	}

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
	quiet      time.Duration // the transcript must be still this long before a drain ends
	idle       time.Duration // a tailer closes after its transcript has not grown this long
	every      time.Duration // how often the spool is read

	pollMu sync.Mutex // one reader of the spool at a time

	mu      sync.Mutex
	read    int64 // spool offset handled so far
	agents  map[string]*subagent
	running int
	ended   bool // the session's end is being delivered: open nothing new

	stops sync.WaitGroup // stop goroutines, waited for when Run returns
}

type subagent struct {
	id, kind, startID string
	since             time.Time
	offset            int64 // how far its transcript has been read
	tail              *subTail
	stopped           bool          // its last row was a stop
	closing           bool          // a stop is draining it
	closed            chan struct{} // closed when that stop is done
	reopen            bool          // a start or tool row arrived while it was closing or closing itself
}

type subTail struct {
	tailer *capture.Tailer
	ctx    context.Context // done once the tailer is stopping (idle, stop, or Run's end)
	cancel context.CancelFunc
	done   chan struct{}
}

func newSubagents(sp spool.Session, transcript, author string, thinking bool, emit func(event.Event) error) *subagents {
	m := &subagents{spool: sp, transcript: transcript, author: author, thinking: thinking, emit: emit,
		max: subagentTailers(), agents: map[string]*subagent{}, quiet: quietWindow, idle: subagentIdle, every: subagentPoll}
	m.loadOffsets()
	return m
}

// offsetsPath keeps how far each agent's transcript has been read, so a
// restarted daemon continues instead of re-reading every transcript.
func (m *subagents) offsetsPath() string {
	return strings.TrimSuffix(m.spool.Path(), ".jsonl") + ".subagents.json"
}

func (m *subagents) loadOffsets() {
	b, err := os.ReadFile(m.offsetsPath())
	if err != nil {
		return
	}

	var offsets map[string]int64
	if json.Unmarshal(b, &offsets) != nil {
		return
	}

	for id, off := range offsets {
		m.agents[id] = &subagent{id: id, offset: off}
	}
}

// saveOffsets records the offsets. Call with m.mu held.
func (m *subagents) saveOffsets() {
	offsets := make(map[string]int64, len(m.agents))
	for id, a := range m.agents {
		if a.offset > 0 {
			offsets[id] = a.offset
		}
	}

	_ = writeJSONFile(m.offsetsPath(), offsets)
}

// Run reads the spool for subagent rows until ctx is done, then closes
// every tailer. Rows not yet handled (all of them, the first time) only
// rebuild state; then the agents still running are reopened, so a
// restarted daemon, or a Run after a push retry, follows every agent that
// has not stopped and none that has.
func (m *subagents) Run(ctx context.Context) {
	m.mu.Lock()
	m.ended = false
	m.mu.Unlock()

	// Stops spooled while no Run was polling (a push retry, a daemon that
	// was not running) still read what their agents wrote.
	stopped := m.poll(ctx, false)
	m.reopenRunning(ctx)
	for _, id := range stopped {
		m.goStop(ctx, id)
	}

	for {
		select {
		case <-ctx.Done():
			// Nothing opens once ctx is done; stops in flight finish their
			// read, and every tailer, including one a stop reopened, closes.
			m.closeAll()
			m.stops.Wait()
			m.closeAll()
			return
		case <-time.After(m.every):
		}

		m.poll(ctx, true)
	}
}

// poll handles the rows spooled since the last poll. live opens and stops
// tailers as it goes; otherwise it only records state and answers the
// agents whose last row was a stop, for the caller to read.
func (m *subagents) poll(ctx context.Context, live bool) (stopped []string) {
	m.pollMu.Lock()
	defer m.pollMu.Unlock()

	m.mu.Lock()
	from := m.read
	m.mu.Unlock()
	if size, err := fileSize(m.spool.Path()); err != nil || size <= from {
		return nil // nothing new: skip the read, which counts lines from the start
	}

	pending := map[string]bool{}
	for entry, err := range m.spool.Read(from) {
		if err != nil {
			continue
		}

		m.mu.Lock()
		m.read = entry.Next
		m.mu.Unlock()

		e := entry.Event
		if e.Kind == event.KindSessionStart {
			// A resume after an end: subagents may be opened again.
			m.mu.Lock()
			m.ended = false
			m.mu.Unlock()
		}

		if e.AgentID == "" {
			continue
		}

		switch e.Kind {
		case event.KindSubagentStart, event.KindToolUse, event.KindToolResult:
			m.record(e, false)
			delete(pending, e.AgentID)
			if live {
				m.open(ctx, e.AgentID) // no-op while open; reopens an agent resumed without a start
			}
		case event.KindSubagentStop:
			m.record(e, true)
			if live {
				m.goStop(ctx, e.AgentID)
			} else {
				pending[e.AgentID] = true
			}
		}
	}

	for id := range pending {
		stopped = append(stopped, id)
	}

	return stopped
}

// goStop marks the agent closing now, so a start or tool row handled right
// after reopens it, and stops it in the background.
func (m *subagents) goStop(ctx context.Context, id string) {
	if !m.beginStop(id) {
		return
	}

	m.stops.Add(1)
	go func() {
		defer m.stops.Done()
		m.finishStop(ctx, id)
	}()
}

// record notes an agent's type, its first start (the parent of its rows
// and the time before which its transcript may be inherited context), and
// whether its last row was a stop.
func (m *subagents) record(e event.Event, stop bool) {
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

	a.stopped = stop
}

// reopenRunning follows the agents that have not stopped and whose
// transcript grew within the idle bound, or has not been written yet; older
// ones are left closed (a later row, or their stop, still reads them).
func (m *subagents) reopenRunning(ctx context.Context) {
	m.mu.Lock()
	var ids []string
	for id, a := range m.agents {
		if a.stopped || a.tail != nil {
			continue
		}

		// A transcript not written yet belongs to an agent just started.
		if st, err := os.Stat(SubagentTranscript(m.transcript, id)); err != nil || time.Since(st.ModTime()) < m.idle {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.open(ctx, id)
	}
}

// tailerFor is a tailer for an agent's transcript from where it was left.
// Call with m.mu held.
func (m *subagents) tailerFor(a *subagent) *capture.Tailer {
	t := &capture.Tailer{Path: SubagentTranscript(m.transcript, a.id), Author: m.author, CaptureThinking: m.thinking, Emit: m.emit,
		AgentID: a.id, AgentType: a.kind, ParentID: a.startID, Prompts: true, Since: a.since}
	t.SetOffset(a.offset)
	return t
}

// open starts following an agent's transcript from its offset. An agent
// already followed is left alone; one being closed is reopened once the
// close is done; beyond the cap nothing is opened, and the agent's stop
// reads what it wrote in one pass.
func (m *subagents) open(ctx context.Context, id string) {
	m.mu.Lock()
	a := m.agents[id]
	if a == nil || m.ended || ctx.Err() != nil {
		m.mu.Unlock()
		return
	}

	if a.closing {
		a.reopen = true
		m.mu.Unlock()
		return
	}

	if a.tail != nil {
		if a.tail.ctx.Err() != nil {
			a.reopen = true // it is closing itself; follow again once it has
		}

		m.mu.Unlock()
		return
	}

	if m.running >= m.max {
		m.mu.Unlock()
		return
	}

	t := m.tailerFor(a)
	tctx, cancel := idleContext(ctx, t.Path, m.idle)
	tail := &subTail{tailer: t, ctx: tctx, cancel: cancel, done: make(chan struct{})}
	a.tail = tail
	m.running++
	m.mu.Unlock()

	go func() {
		defer close(tail.done)
		_ = t.Run(tctx)
		cancel()

		m.mu.Lock()
		a.offset = max(a.offset, t.Offset())
		m.saveOffsets()
		if a.tail == tail {
			a.tail = nil
			m.running--
		}

		reopen := a.reopen && !a.closing && !m.ended && ctx.Err() == nil
		if reopen {
			a.reopen = false
		}
		m.mu.Unlock()

		if reopen {
			m.open(ctx, a.id)
		}
	}()
}

// A stop answers an agent's stop: drain its tailer (until the transcript
// has been quiet, 10 s at most), then read whatever is left from its offset.
// That last read also covers an agent whose tailer had closed itself, or
// that was never opened because of the cap. A start or tool row that came
// in meanwhile reopens it.
//
// beginStop marks the agent closing and answers whether this call owns the
// stop; a stop already under way owns it.
func (m *subagents) beginStop(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[id]
	if a == nil || a.closing {
		return false
	}

	a.closing, a.closed = true, make(chan struct{})
	return true
}

func (m *subagents) finishStop(ctx context.Context, id string) {
	m.mu.Lock()
	a := m.agents[id]
	tail := a.tail
	m.mu.Unlock()

	if tail != nil {
		waitQuiet(ctx, tail.tailer.Path, m.quiet, 10*time.Second)
		tail.cancel()
		<-tail.done
	}

	m.readRest(a)

	m.mu.Lock()
	a.closing = false
	close(a.closed)
	reopen := a.reopen && !m.ended
	a.reopen = false
	m.mu.Unlock()

	if reopen {
		m.open(ctx, id)
	}
}

// stopAndWait stops an agent and returns once its stop, this one or one
// already under way, is done.
func (m *subagents) stopAndWait(ctx context.Context, id string) {
	if m.beginStop(id) {
		m.finishStop(ctx, id)
		return
	}

	m.mu.Lock()
	var closed chan struct{}
	if a := m.agents[id]; a != nil && a.closing {
		closed = a.closed
	}
	m.mu.Unlock()

	if closed != nil {
		<-closed
	}
}

// readRest reads an agent's transcript from its offset to the end, once.
func (m *subagents) readRest(a *subagent) {
	m.mu.Lock()
	t := m.tailerFor(a)
	m.mu.Unlock()

	done, cancel := context.WithCancel(context.Background())
	cancel() // Run reads what is there, then returns
	_ = t.Run(done)

	m.mu.Lock()
	a.offset = max(a.offset, t.Offset())
	m.saveOffsets()
	m.mu.Unlock()
}

// closeAll stops every tailer at once; each one's last pass reads to the end.
func (m *subagents) closeAll() {
	m.mu.Lock()
	var tails []*subTail
	for _, a := range m.agents {
		if a.tail != nil {
			tails = append(tails, a.tail)
		}
	}
	m.mu.Unlock()

	for _, t := range tails {
		t.cancel()
	}

	for _, t := range tails {
		<-t.done
	}
}

// BeforeEnd drains every running subagent before the session's end is
// delivered, so their last rows land before it, and opens nothing new
// until the next Run.
func (m *subagents) BeforeEnd(ctx context.Context) {
	m.poll(ctx, true) // a stop spooled just before the end

	m.mu.Lock()
	m.ended = true
	var ids []string
	for id, a := range m.agents {
		if !a.stopped || a.tail != nil || a.closing {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			m.stopAndWait(ctx, id)
		}(id)
	}

	wg.Wait()
}

func obj(m map[string]any) json.RawMessage {
	b, _ := json.Marshal(m)
	return b
}

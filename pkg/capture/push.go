package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/quantumwake/parley/pkg/conversation"
	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/naming"
	"github.com/quantumwake/parley/pkg/spool"
	"github.com/quantumwake/parley/pkg/store"
)

// Pusher delivers one session's spool to its conversation: opens the
// namespace on the first event, assigns seq from spool order, applies
// redaction, batches through the conversation writer, acks the spool,
// and writes session.end with sync durability.
type Pusher struct {
	Store       store.Store
	Session     spool.Session
	Agent       string           // agent name for the display name and scope
	Persona     string           // optional
	Name        string           // conversation name; default: first prompt or the session id
	Redact      []*regexp.Regexp // applied to text-like content before delivery
	Poll        time.Duration    // how often to look for new spool lines (default 250 ms)
	OnDelivered func(seq int64, e event.Event, pos store.Position)
	OnOpen      func(displayName, id string) // called once the conversation namespace is known
	// BeforeEnd runs once a session.end is spooled, before it is delivered:
	// the daemon uses it to wait for the transcript to go quiet so the
	// model's final blocks (written after the SessionEnd hook) still land
	// before session.end. Late rows are drained after it returns.
	BeforeEnd func(ctx context.Context)

	conv    *conversation.Conversation
	writer  *conversation.Writer
	nextSeq int64
	end     *event.Event
	endNext int64
	title   string
}

// Run follows the spool until a session.end has been delivered or ctx is
// done. It is safe to restart: it resumes from the ack offset and the
// writer's dedupe window covers a batch that landed but was not acked.
func (p *Pusher) Run(ctx context.Context) error {
	if p.Poll <= 0 {
		p.Poll = 250 * time.Millisecond
	}

	off := p.Session.AckOffset()
	for {
		ended, next, err := p.drain(ctx, off)
		if err != nil {
			return err
		}

		off = next
		if ended {
			return nil
		}

		select {
		case <-ctx.Done():
			_, _, _ = p.drain(context.Background(), off)
			return ctx.Err()
		case <-time.After(p.Poll):
		}
	}
}

// Once delivers everything currently spooled and returns (tests, replay).
func (p *Pusher) Once(ctx context.Context) error {
	_, _, err := p.drain(ctx, p.Session.AckOffset())
	return err
}

func (p *Pusher) drain(ctx context.Context, off int64) (ended bool, next int64, err error) {
	next = off
	for {
		advanced := false
		for entry, rerr := range p.Session.Read(next) {
			if rerr != nil {
				return false, next, rerr
			}

			if p.conv == nil {
				// Open on the first prompt so the title is part of the
				// namespace's birth labels (a read/write credential cannot
				// relabel after birth). Rows before it (session.start) are
				// held: not acked, not sequenced; they land first once the
				// namespace exists. A session that ends without a prompt
				// opens untitled on session.end.
				k := entry.Event.Kind
				if k != event.KindUserMessage && k != event.KindSessionEnd {
					continue
				}

				p.title = naming.TitleFromPrompt(promptText(entry.Event))
				if err := p.open(ctx, entry.Event); err != nil {
					return false, off, err
				}

				// Re-run from the ack offset so the held rows land first, in order.
				return p.drain(ctx, off)
			}

			advanced = true
			next = entry.Next
			e := p.prepare(entry.Event)

			if e.Kind == event.KindSessionEnd {
				// Defer: everything spooled after this line still goes first.
				p.end, p.endNext = &e, entry.Next
				continue
			}

			if err := p.writer.Add(ctx, e); err != nil {
				return false, next, err
			}
		}

		if p.end == nil {
			break
		}

		// A session.end is pending: let late writers finish, then drain
		// again until nothing new arrives.
		if p.BeforeEnd != nil {
			p.BeforeEnd(ctx)
			p.BeforeEnd = nil
			continue
		}

		if !advanced {
			break
		}
	}

	if p.writer != nil && p.writer.Pending() > 0 {
		if err := p.writer.Flush(ctx); err != nil {
			return false, next, err
		}
	}

	if p.end != nil {
		e := *p.end
		e.Seq = p.nextSeq + 1
		p.nextSeq = e.Seq
		pos, err := p.conv.Append(ctx, true, e)
		if err != nil && err != store.ErrDurabilityNotConfirmed {
			return false, next, err
		}

		p.deliver(e, pos)
		if next < p.endNext {
			next = p.endNext
		}

		if err := p.Session.Ack(next); err != nil {
			return false, next, err
		}

		p.end = nil
		return true, next, nil
	}

	if next != off {
		if err := p.Session.Ack(next); err != nil {
			return false, next, err
		}
	}

	return false, next, nil
}

// prepare stamps delivery order and ingest time and applies redaction.
// seq counts delivered rows, so a deferred session.end is always last.
func (p *Pusher) prepare(e event.Event) event.Event {
	if e.Kind != event.KindSessionEnd {
		p.nextSeq++
		e.Seq = p.nextSeq
	}

	e.IngestedMs = time.Now().UnixMilli()
	return p.redact(e)
}

func (p *Pusher) open(ctx context.Context, first event.Event) error {
	name := p.Name
	if name == "" {
		name = p.Session.ID
	}

	started := time.UnixMilli(first.TSMs)
	for entry := range p.Session.Read(0) { // the earliest spooled row is when the session began
		if entry.Event.TSMs > 0 && entry.Event.TSMs < started.UnixMilli() {
			started = time.UnixMilli(entry.Event.TSMs)
		}

		break
	}
	scope := naming.Conversation{Session: p.Session.ID, Agent: p.Agent, Persona: p.Persona, Started: started, Title: p.title}.Scope()
	display := naming.AgentLogName(p.Agent, name, p.Session.ID, started)
	conv, err := conversation.Open(ctx, p.Store, display, scope)
	if err != nil {
		return fmt.Errorf("push: open conversation: %w", err)
	}

	p.conv = conv
	if p.OnOpen != nil {
		p.OnOpen(display, conv.ID())
	}

	p.writer = conversation.NewWriter(conv, 10_000)
	p.writer.OnFlush = func(first store.Position, batch []event.Event, err error) {
		if err != nil {
			return
		}

		for i, e := range batch {
			p.deliver(e, first+store.Position(i))
		}
	}

	return nil
}

func (p *Pusher) deliver(e event.Event, pos store.Position) {
	if p.OnDelivered != nil {
		p.OnDelivered(e.Seq, e, pos)
	}
}

// redact applies the patterns to the JSON text of Content. Patterns are
// the user's (secrets, tokens); the replacement is a fixed marker.
func (p *Pusher) redact(e event.Event) event.Event {
	if len(p.Redact) == 0 || len(e.Content) == 0 {
		return e
	}

	c := string(e.Content)
	for _, re := range p.Redact {
		c = re.ReplaceAllString(c, "[redacted]")
	}

	e.Content = []byte(c)
	return e
}

// Conversation is the opened conversation (nil before the first event).
func (p *Pusher) Conversation() *conversation.Conversation { return p.conv }

// Event and Position re-export the types the callbacks use so callers do
// not import store and event just to write a log line.
type (
	Event    = event.Event
	Position = store.Position
)

// promptText pulls the text of a user.message row.
func promptText(e event.Event) string {
	var m struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(e.Content, &m)
	return m.Text
}

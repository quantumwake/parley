package spool

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/quantumwake/statefs.ai/pkg/event"
)

func ev(kind event.Kind) event.Event {
	return event.Event{ID: event.NewID(), TSMs: time.Now().UnixMilli(), SessionID: "s", Source: event.SourceClaudeCode,
		Kind: kind, Author: "t", Content: json.RawMessage(`{"x":1}`)}
}

func TestAppendReadAck(t *testing.T) {
	s := Session{Dir: t.TempDir(), ID: "sess/1"}
	for i := 0; i < 3; i++ {
		if err := s.Append(ev(event.KindUserMessage), i == 2); err != nil {
			t.Fatal(err)
		}
	}

	var got []Entry
	for e, err := range s.Read(0) {
		if err != nil {
			t.Fatal(err)
		}

		got = append(got, e)
	}

	if len(got) != 3 || got[0].Index != 0 || got[2].Index != 2 {
		t.Fatalf("read: %+v", got)
	}

	if err := s.Ack(got[1].Next); err != nil {
		t.Fatal(err)
	}

	var rest []Entry
	for e, err := range s.Read(s.AckOffset()) {
		if err != nil {
			t.Fatal(err)
		}

		rest = append(rest, e)
	}

	if len(rest) != 1 || rest[0].Index != 2 || rest[0].Event.ID != got[2].Event.ID {
		t.Fatalf("resume after ack: %+v", rest)
	}
}

func TestPartialTrailingLineIsSkipped(t *testing.T) {
	s := Session{Dir: t.TempDir(), ID: "p"}
	_ = s.Append(ev(event.KindUserMessage), false)
	f, _ := os.OpenFile(s.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"event_id":"partial`)
	f.Close()
	n := 0
	for _, err := range s.Read(0) {
		if err != nil {
			t.Fatal(err)
		}

		n++
	}

	if n != 1 {
		t.Fatalf("want 1 complete line, got %d", n)
	}
}

func TestInvalidEventRefused(t *testing.T) {
	s := Session{Dir: t.TempDir(), ID: "v"}
	if err := s.Append(event.Event{}, false); err == nil {
		t.Fatal("invalid event must be refused")
	}
}

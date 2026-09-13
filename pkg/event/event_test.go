package event

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func valid() Event {
	return Event{
		ID: NewID(), Seq: 1, TSMs: time.Now().UnixMilli(), SessionID: "s1",
		Source: SourceClaudeCode, Kind: KindUserMessage, Role: RoleUser,
		Identity: "kasra", Content: json.RawMessage(`{"text":"hi"}`),
	}
}

func TestValidateTable(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Event)
		want error
	}{
		{"ok", func(e *Event) {}, nil},
		{"missing id", func(e *Event) { e.ID = "" }, ErrMissingID},
		{"bad id", func(e *Event) { e.ID = "not-a-ulid" }, ErrBadID},
		{"missing kind", func(e *Event) { e.Kind = "" }, ErrMissingKind},
		{"unknown kind", func(e *Event) { e.Kind = "nope" }, ErrUnknownKind},
		{"missing source", func(e *Event) { e.Source = "" }, ErrMissingSource},
		{"missing time", func(e *Event) { e.TSMs = 0 }, ErrMissingTime},
		{"too large", func(e *Event) { e.Content = json.RawMessage(`"` + strings.Repeat("x", MaxInlineContent) + `"`) }, ErrContentTooLarge},
		{"content and blob", func(e *Event) { e.BlobRef = "s3://b/k" }, ErrContentAndBlob},
		{"bad json", func(e *Event) { e.Content = json.RawMessage(`{`) }, ErrBadContent},
		{"tool result needs parent", func(e *Event) { e.Kind = KindToolResult }, ErrMissingParent},
		{"answer needs parent", func(e *Event) { e.Kind = KindPostAnswer }, ErrMissingParent},
		{"blob only ok", func(e *Event) { e.Content = nil; e.BlobRef = "s3://b/k" }, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := valid()
			c.mut(&e)
			err := e.Validate()
			if c.want == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

func TestULID(t *testing.T) {
	at := time.UnixMilli(1_725_000_000_000)
	id := NewIDAt(at)
	if !IsULID(id) {
		t.Fatalf("not a ulid: %s", id)
	}

	if got := ULIDTime(id); !got.Equal(at) {
		t.Fatalf("time round trip: want %v got %v", at, got)
	}

	a, b := NewIDAt(at), NewIDAt(at.Add(time.Millisecond))
	if !(a < b) {
		t.Fatalf("ulids must sort by time: %s !< %s", a, b)
	}

	if IsULID("8ZZZZZZZZZZZZZZZZZZZZZZZZZ") {
		t.Fatal("first char above 7 must be rejected")
	}
}

func TestFromRecordDecodesStringifiedContent(t *testing.T) {
	e, err := FromRecord(map[string]any{"event_id": NewID(), "kind": "user.message", "source": "claude-code", "ts_ms": int64(1), "content": `{"text":"hi"}`})
	if err != nil {
		t.Fatal(err)
	}

	if string(e.Content) != `{"text":"hi"}` {
		t.Fatalf("content must be the object, got %s", e.Content)
	}
}

func TestRecordRoundTrip(t *testing.T) {
	e := valid()
	e.TokensIn = 12
	m, err := e.Record()
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := m["seq"].(int64); !ok {
		t.Fatalf("seq must be int64 in the record, got %T", m["seq"])
	}

	if _, ok := m["blob_ref"]; ok {
		t.Fatal("zero optional fields must be omitted from the record")
	}

	back, err := FromRecord(m)
	if err != nil {
		t.Fatal(err)
	}

	if back.ID != e.ID || back.Kind != e.Kind || back.TokensIn != 12 || string(back.Content) != string(e.Content) {
		t.Fatalf("round trip mismatch: %+v vs %+v", back, e)
	}
}

// TestGoldenFiles pins the wire encoding: every file under testdata/events
// must validate and re-encode to itself (S1 seam).
func TestGoldenFiles(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "events", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden files: %v", err)
	}

	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}

			var e Event
			if err := json.Unmarshal(raw, &e); err != nil {
				t.Fatal(err)
			}

			if err := e.Validate(); err != nil {
				t.Fatalf("golden must validate: %v", err)
			}

			var want, got any
			_ = json.Unmarshal(raw, &want)
			re, _ := json.Marshal(e)
			_ = json.Unmarshal(re, &got)
			if !jsonEqual(want, got) {
				t.Fatalf("re-encoding drifted from golden:\n%s\n%s", raw, re)
			}
		})
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

package console

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/conversation"
	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/plugin"
	"github.com/quantumwake/parley/pkg/store"
)

// Sessions recorded from this machine carry their last activity, which the
// viewer orders by; the rest carry only their start.
func TestListCarriesLastActivity(t *testing.T) {
	ctx := context.Background()
	st := store.NewFake()
	env := plugin.Env{DataDir: t.TempDir()}
	mine, _ := st.Open(ctx, "kas/2026-09-12T09:00:00/repo#aaaaaaaa", store.Scope{"kind": "conversation", "mode": "agent", "started_ms": int64(1)})
	other, _ := st.Open(ctx, "bob/2026-09-13T08:00:00/repo#bbbbbbbb", store.Scope{"kind": "conversation", "mode": "agent", "started_ms": int64(2)})
	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	plugin.NamesPut(env, mine.DisplayName, mine.ID)
	plugin.NamesTouch(env, mine.DisplayName, at)

	rec := httptest.NewRecorder()
	(&Server{env: env, st: st}).Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/conversations", nil))
	var body struct {
		Conversations []map[string]any `json:"conversations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}

	got := map[string]any{}
	for _, c := range body.Conversations {
		got[c["id"].(string)] = c["active_ms"]
	}

	if got[mine.ID] != float64(at.UnixMilli()) {
		t.Fatalf("recorded here: active_ms %v want %d", got[mine.ID], at.UnixMilli())
	}

	if v, ok := got[other.ID]; !ok || v != nil {
		t.Fatalf("recorded elsewhere: active_ms must be absent, got %v (present %v)", v, ok)
	}
}

// The index groups sessions by day, so every session needs a start time.
// Conversations born before the scope carried started_ms get it from their
// display name, and older ones still from their first row.
func TestBackfillStartedDatesOlderConversations(t *testing.T) {
	ctx := context.Background()
	st := store.NewFake()
	first := time.Date(2026, 9, 6, 21, 59, 19, 0, time.UTC)

	ns, err := st.Open(ctx, "kas/statefs.ai#1d10c76d", store.Scope{"kind": "conversation"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := st.Append(ctx, ns.ID, []event.Event{
		{ID: event.NewID(), Seq: 1, TSMs: first.UnixMilli(), Kind: event.KindSessionStart, Source: event.SourceClaudeCode, Role: event.RoleSystem, Content: []byte(`{}`)},
		{ID: event.NewID(), Seq: 2, TSMs: first.Add(time.Minute).UnixMilli(), Kind: event.KindUserMessage, Source: event.SourceClaudeCode, Role: event.RoleUser, Content: []byte(`{"text":"hi"}`)},
	}, false); err != nil {
		t.Fatal(err)
	}

	empty, err := st.Open(ctx, "kas/nothing-here#2", store.Scope{"kind": "conversation"})
	if err != nil {
		t.Fatal(err)
	}

	out := []convOut{
		{ID: ns.ID, Name: ns.DisplayName},
		{ID: empty.ID, Name: empty.DisplayName},
		{ID: "keep", Name: "kas/already#3", StartedMs: int64(123)},
	}
	backfillStarted(ctx, st, out)

	if got := out[0].StartedMs; got != first.UnixMilli() {
		t.Fatalf("first row must date the conversation: got %v want %v", got, first.UnixMilli())
	}

	if out[1].StartedMs != nil {
		t.Fatalf("a conversation with no rows stays undated: %v", out[1].StartedMs)
	}

	if out[2].StartedMs != int64(123) {
		t.Fatalf("an existing started_ms must not be overwritten: %v", out[2].StartedMs)
	}
}

// A conversation opens at its end: ?tail=N answers the last N rows and says
// where they start, so a reader loads older rows only when asked.
func TestEventsTail(t *testing.T) {
	ctx := context.Background()
	st := store.NewFake()
	ns, _ := st.Open(ctx, "shared-channel", store.Scope{"kind": "conversation", "mode": "shared"})
	conv := conversation.Attach(st, ns.ID)
	for i := 0; i < 10; i++ {
		e := event.Event{ID: event.NewID(), TSMs: int64(i + 1), Source: event.SourceClaudeCode, Kind: event.KindPostComment, Identity: "a", Content: json.RawMessage(`{"text":"x"}`)}
		e.Thread = e.ID
		if _, err := conv.Append(ctx, false, e); err != nil {
			t.Fatal(err)
		}
	}

	get := func(q string) (rows []map[string]any, from, next, head float64) {
		rec := httptest.NewRecorder()
		(&Server{env: plugin.Env{DataDir: t.TempDir()}, st: st}).Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/conversations/"+ns.ID+"/events?"+q, nil))
		var body struct {
			Events           []map[string]any `json:"events"`
			From, Next, Head float64
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: %s", err, rec.Body.String())
		}
		return body.Events, body.From, body.Next, body.Head
	}

	rows, from, next, head := get("tail=3")
	if len(rows) != 3 || from != 7 || next != 10 || head != 10 || rows[0]["position"] != float64(7) {
		t.Fatalf("tail=3: %d rows from %v next %v head %v first %v", len(rows), from, next, head, rows[0]["position"])
	}

	if rows, from, _, _ := get("tail=50"); len(rows) != 10 || from != 0 {
		t.Fatalf("tail longer than the conversation: %d rows from %v", len(rows), from)
	}

	if rows, _, _, _ := get("from=2&to=4&tail=3"); len(rows) != 2 {
		t.Fatalf("an explicit from ignores tail: %d rows", len(rows))
	}
}

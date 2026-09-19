package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	(&Server{env: env, st: st}).routes().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/conversations", nil))
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
		(&Server{env: plugin.Env{DataDir: t.TempDir()}, st: st}).routes().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/conversations/"+ns.ID+"/events?"+q, nil))
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

// The console posts exchange only: work posts carry rules it does not
// enforce, so they are refused with where to make them instead.
func TestConsoleRefusesWorkPosts(t *testing.T) {
	st := store.NewFake()
	ns, _ := st.Open(context.Background(), "shared-channel", store.Scope{"kind": "conversation", "mode": "shared"})
	for _, kind := range []string{"request", "claim", "close"} {
		rec := httptest.NewRecorder()
		body := strings.NewReader(`{"kind":"` + kind + `","text":"x"}`)
		(&Server{env: plugin.Env{DataDir: t.TempDir()}, st: st}).routes().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/conversations/"+ns.ID+"/posts", body))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "parley post") {
			t.Fatalf("%s: %d %s", kind, rec.Code, rec.Body.String())
		}
	}
}

// POST /v1/conversations opens a shared conversation and remembers its
// name, the same as `parley create`.
func TestConsoleCreatesConversation(t *testing.T) {
	env := plugin.Env{DataDir: t.TempDir()}
	st := store.NewFake()
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"name":"team-1","description":"a channel","tags":["team"]}`)
	(&Server{env: env, st: st}).routes().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/conversations", body))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	var out struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.ID == "" || out.Name != "team-1" {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}

	if got := plugin.NamesByTime(env); len(got) != 1 || got[0].Name != "team-1" || got[0].ID != out.ID {
		t.Fatalf("create did not remember the name: %+v", got)
	}
}

// An empty name is refused before it reaches the store.
func TestConsoleCreateRefusesEmptyName(t *testing.T) {
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"name":""}`)
	(&Server{env: plugin.Env{DataDir: t.TempDir()}, st: store.NewFake()}).routes().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/conversations", body))
	if rec.Code != 400 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// PATCH /v1/conversations/{id} relabels title, description and tags: the
// same call `parley describe` makes. Describe resolves its own store from
// env (StoreFromEnv), the way the CLI does, so the test points env at a
// real file store rather than the fake the other handlers read through
// the server's own s.st.
func TestConsoleRenamesConversation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("STATEFS_AI_STORE", "file:"+dir)
	env := plugin.Env{DataDir: t.TempDir()}
	st, err := store.NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}

	ns, _ := st.Open(ctx, "team-1", store.Scope{"kind": "conversation", "mode": "shared"})
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"title":"Team One","description":"renamed"}`)
	(&Server{env: env, st: st}).routes().ServeHTTP(rec, httptest.NewRequest("PATCH", "/v1/conversations/"+ns.ID, body))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	// Describe resolved its own *File over the same directory, so re-open
	// to see what it wrote rather than reading st's now-stale in-memory copy.
	reopened, err := store.NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}

	metas, err := reopened.Find(ctx, store.Scope{"kind": "conversation"}, 10)
	if err != nil || len(metas) != 1 || metas[0].Scope["title"] != "Team One" {
		t.Fatalf("label not applied: %v %+v", err, metas)
	}
}

// A rename with nothing to say is refused before it reaches the store.
func TestConsoleRenameRefusesEmptyBody(t *testing.T) {
	ctx := context.Background()
	st := store.NewFake()
	ns, _ := st.Open(ctx, "team-1", store.Scope{"kind": "conversation", "mode": "shared"})
	rec := httptest.NewRecorder()
	(&Server{env: plugin.Env{DataDir: t.TempDir()}, st: st}).routes().ServeHTTP(rec, httptest.NewRequest("PATCH", "/v1/conversations/"+ns.ID, strings.NewReader(`{}`)))
	if rec.Code != 400 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// DELETE /v1/conversations/{id} only works against statefs.io; against a
// local file store it refuses with the same message `parley delete`
// gives, not a panic or a silent no-op. DeleteConversation resolves its
// own store from env, like rename above.
func TestConsoleDeleteRefusesNonStatefsStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("STATEFS_AI_STORE", "file:"+dir)
	env := plugin.Env{DataDir: t.TempDir()}
	st, err := store.NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}

	ns, _ := st.Open(ctx, "team-1", store.Scope{"kind": "conversation", "mode": "shared"})
	rec := httptest.NewRecorder()
	(&Server{env: env, st: st}).routes().ServeHTTP(rec, httptest.NewRequest("DELETE", "/v1/conversations/"+ns.ID, nil))
	if rec.Code == 200 || !strings.Contains(rec.Body.String(), "only meaningful against statefs.io") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestConsoleGrantRefusesBadBody(t *testing.T) {
	st := store.NewFake()
	for _, body := range []string{`{}`, `{"username":" ","access":"read"}`, `{"username":"bob","access":"admin"}`, `{"username":"bob","access":"read,write"}`, `not json`} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/conversations/x/grants", strings.NewReader(body))
		(&Server{env: plugin.Env{DataDir: t.TempDir()}, st: st}).routes().ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestConsoleGrantsRefuseNonStatefsStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("STATEFS_AI_STORE", "file:"+dir)
	env := plugin.Env{DataDir: t.TempDir()}
	st, err := store.NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}

	ns, _ := st.Open(ctx, "team-1", store.Scope{"kind": "conversation", "mode": "shared"})
	for _, req := range []*http.Request{
		httptest.NewRequest("GET", "/v1/conversations/"+ns.ID+"/grants", nil),
		httptest.NewRequest("POST", "/v1/conversations/"+ns.ID+"/grants", strings.NewReader(`{"username":"bob","access":"read"}`)),
		httptest.NewRequest("DELETE", "/v1/conversations/"+ns.ID+"/grants/bob", nil),
	} {
		rec := httptest.NewRecorder()
		(&Server{env: env, st: st}).routes().ServeHTTP(rec, req)
		if rec.Code == 200 || !strings.Contains(rec.Body.String(), "only meaningful against statefs.io") {
			t.Fatalf("%s %s: %d %s", req.Method, req.URL.Path, rec.Code, rec.Body.String())
		}
	}
}

// headCounter counts Head calls: on statefs each one is a row-0 read.
type headCounter struct {
	store.Store
	n int
}

func (h *headCounter) Head(ctx context.Context, ns string) (store.Position, error) {
	h.n++
	return h.Store.Head(ctx, ns)
}

// The live poll (from the next position, no upper bound) must not ask Head:
// on statefs that is a row-0 read, which decodes the namespace's first block
// on every poll (the group-d-1 OOM of 2026-09-18).
func TestEventsLivePollDoesNotAskHead(t *testing.T) {
	ctx := context.Background()
	st := &headCounter{Store: store.NewFake()}
	ns, _ := st.Open(ctx, "shared-channel", store.Scope{"kind": "conversation", "mode": "shared"})
	conv := conversation.Attach(st, ns.ID)
	for i := 0; i < 5; i++ {
		e := event.Event{ID: event.NewID(), TSMs: int64(i + 1), Source: event.SourceClaudeCode, Kind: event.KindPostComment, Identity: "a", Content: json.RawMessage(`{"text":"x"}`)}
		e.Thread = e.ID
		if _, err := conv.Append(ctx, false, e); err != nil {
			t.Fatal(err)
		}
	}

	get := func(q string) (n int, next, head float64) {
		rec := httptest.NewRecorder()
		(&Server{env: plugin.Env{DataDir: t.TempDir()}, st: st}).routes().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/conversations/"+ns.ID+"/events?"+q, nil))
		var body struct {
			Events     []map[string]any `json:"events"`
			Next, Head float64
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: %s", err, rec.Body.String())
		}
		return len(body.Events), body.Next, body.Head
	}

	st.n = 0
	for _, q := range []string{"from=3&limit=500", "from=5&limit=500", "from=0&limit=500"} {
		n, next, head := get(q)
		if next != 5 || head != 5 {
			t.Fatalf("%s: %d rows next %v head %v", q, n, next, head)
		}
	}
	if st.n != 0 {
		t.Fatalf("live polls asked Head %d times", st.n)
	}

	if _, _, head := get("from=0&to=2"); head != 5 || st.n != 1 {
		t.Fatalf("a bounded read still reports the true head: head %v, Head calls %d", head, st.n)
	}
}

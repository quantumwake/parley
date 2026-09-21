package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/quantumwake/parley/pkg/plugin"
)

func serve(t *testing.T, s *Server, msgs ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(msgs, "\n")), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}

	var got []map[string]any
	dec := json.NewDecoder(strings.NewReader(out.String()))
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			break
		}

		got = append(got, m)
	}

	return got
}

func testServer() *Server {
	return &Server{Name: "parley", Version: "9.9.9", Tools: []Tool{
		{
			Name: "echo", Description: "echo", Schema: obj([]string{"text"}, map[string]any{"text": prop("string", "x")}),
			Call: func(_ context.Context, a Args, w io.Writer) error {
				if a.Str("text") == "" {
					return errors.New("text is required")
				}

				_, err := io.WriteString(w, a.Str("text"))
				return err
			},
		},
	}}
}

func TestHandshakeAndToolListing(t *testing.T) {
	got := serve(t, testServer(),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`, // a notification takes no reply
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(got) != 2 {
		t.Fatalf("a notification must not be answered: got %d replies", len(got))
	}

	res := got[0]["result"].(map[string]any)
	if res["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocolVersion: %v", res["protocolVersion"])
	}

	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatal("must advertise the tools capability")
	}

	tools := got[1]["result"].(map[string]any)["tools"].([]any)
	first := tools[0].(map[string]any)
	for _, k := range []string{"name", "description", "inputSchema"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("a tool entry needs %q: %v", k, first)
		}
	}
}

func TestCallSucceedsAndFailsInBand(t *testing.T) {
	// Multi-line markdown is the whole point: it must survive untouched,
	// with no shell in the way.
	body := "## Heading\n\nBody with a # hash and \"quotes\".\n"
	arg, _ := json.Marshal(map[string]any{"name": "echo", "arguments": map[string]any{"text": body}})
	got := serve(t, testServer(),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+string(arg)+`}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nope","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"no/such/method"}`,
	)

	ok := got[0]["result"].(map[string]any)
	if ok["isError"] != false {
		t.Fatalf("a good call must not be an error: %v", ok)
	}

	if text := ok["content"].([]any)[0].(map[string]any)["text"]; text != strings.TrimSpace(body) {
		t.Fatalf("body must round-trip verbatim, got %q", text)
	}

	// A tool that refuses reports isError, never a protocol error, so the
	// model reads the reason and can correct itself.
	bad := got[1]["result"].(map[string]any)
	if bad["isError"] != true || !strings.Contains(bad["content"].([]any)[0].(map[string]any)["text"].(string), "required") {
		t.Fatalf("a refused call must explain itself in band: %v", bad)
	}

	if got[2]["result"].(map[string]any)["isError"] != true {
		t.Fatal("unknown tool must be an in-band error")
	}

	if got[3]["error"] == nil {
		t.Fatal("an unknown method is a protocol error")
	}
}

func TestArgsCoercion(t *testing.T) {
	a := Args{"n": float64(7), "list": []any{"a", " b "}, "csv": "x, y", "flag": true}
	if v, ok := a.Int("n"); !ok || v != 7 {
		t.Fatalf("Int: %v %v", v, ok)
	}

	if _, ok := a.Int("missing"); ok {
		t.Fatal("a missing number must report absent, not zero")
	}

	if got := a.Strings("list"); len(got) != 2 || got[1] != "b" {
		t.Fatalf("Strings from array: %q", got)
	}

	if got := a.Strings("csv"); len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("Strings from csv: %q", got)
	}

	if !a.Bool("flag") || a.Bool("nope") {
		t.Fatal("Bool")
	}
}

func TestListSessionsToolIsRegisteredAndBounded(t *testing.T) {
	var tool *Tool
	for _, tl := range Tools(plugin.Env{}) {
		if tl.Name == "list_sessions" {
			tl := tl
			tool = &tl
		}
	}

	if tool == nil {
		t.Fatal("list_sessions must be one of the tools")
	}

	limit, _ := tool.Schema["properties"].(map[string]any)["limit"].(map[string]any)
	if limit["type"] != "integer" {
		t.Fatalf("limit must be an integer argument: %v", tool.Schema)
	}

	for _, c := range []struct {
		name string
		args Args
		want int
	}{
		{"absent", Args{}, 20},
		{"given", Args{"limit": float64(5)}, 5},
		{"zero", Args{"limit": float64(0)}, 20},
		{"negative", Args{"limit": float64(-3)}, 20},
		{"over the cap", Args{"limit": float64(100000)}, 100},
		{"at the cap", Args{"limit": float64(100)}, 100},
		{"json number", Args{"limit": json.Number("7")}, 7},
	} {
		got, err := sessionLimit(c.args)
		if err != nil || got != c.want {
			t.Fatalf("%s: got %d, %v want %d", c.name, got, err, c.want)
		}
	}

	if _, err := sessionLimit(Args{"limit": "abc"}); err == nil || !strings.Contains(err.Error(), "integer") {
		t.Fatalf("a non-integer limit must be refused, saying why: %v", err)
	}

	// The refusal reaches the model in band, before anything is fetched.
	var out strings.Builder
	if err := tool.Call(context.Background(), Args{"limit": "abc"}, &out); err == nil {
		t.Fatal("the tool must refuse a non-integer limit")
	}
}

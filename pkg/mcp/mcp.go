// Package mcp serves parley's conversation operations as MCP tools over
// stdio, so an agent calls them directly instead of through a shell.
//
// This matters for more than tidiness. A shell argument cannot safely carry
// multi-line markdown: Claude Code's Bash analyser refuses a quoted argument
// with a newline before a "#", which is every markdown heading, and the
// heredoc workarounds are refused as multiple operations. Structured tool
// arguments have no quoting layer at all, so a report posts as written.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Protocol versions this server speaks. Claude Code 2.1.232 and later use
// the stateless runtime (no initialize handshake, protocol metadata on each
// request, a resultType on each result); older clients use the handshake.
// We answer both: initialize is honoured when it arrives, and every result
// carries the fields the newer runtime expects, which older clients ignore.
const (
	ProtocolVersion       = "2024-11-05" // advertised at initialize
	ProtocolVersionLatest = "2026-07-28" // stateless runtime
	metaProtocol          = "io.modelcontextprotocol/protocolVersion"
	metaServerInfo        = "io.modelcontextprotocol/serverInfo"
)

// Tool is one callable operation. Call writes the human-readable result to
// w; the transport wraps it as MCP text content.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Call        func(ctx context.Context, a Args, w io.Writer) error
}

// Args is one call's arguments, with typed accessors that tolerate the
// JSON number/string looseness real clients produce.
type Args map[string]any

// Str returns a string argument, "" when absent.
func (a Args) Str(k string) string {
	s, _ := a[k].(string)
	return strings.TrimSpace(s)
}

// Bool returns a boolean argument, false when absent.
func (a Args) Bool(k string) bool {
	b, _ := a[k].(bool)
	return b
}

// Int returns an integer argument and whether it was present.
func (a Args) Int(k string) (int64, bool) {
	switch v := a[k].(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	}

	return 0, false
}

// Strings returns a list argument, accepting either a JSON array or a
// comma-separated string.
func (a Args) Strings(k string) []string {
	switch v := a[k].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}

		return out
	case string:
		var out []string
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}

		return out
	}

	return nil
}

// Server speaks JSON-RPC 2.0 over a stdio stream: newline-delimited
// messages, requests answered, notifications ignored.
type Server struct {
	Name    string
	Version string
	Tools   []Tool
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // absent on notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads requests until the stream closes or ctx is done.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	dec := json.NewDecoder(in)
	enc := json.NewEncoder(out)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		var req request
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}

			return fmt.Errorf("mcp: decode: %w", err)
		}

		res, answer := s.handle(ctx, req)
		if !answer {
			continue // a notification takes no reply
		}

		if err := enc.Encode(res); err != nil {
			return fmt.Errorf("mcp: encode: %w", err)
		}
	}
}

func (s *Server) handle(ctx context.Context, req request) (response, bool) {
	if len(req.ID) == 0 {
		return response{}, false
	}

	res := response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		res.Result = map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}
	case "ping":
		res.Result = map[string]any{}
	case "tools/list":
		list := make([]map[string]any, 0, len(s.Tools))
		for _, t := range s.Tools {
			list = append(list, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema})
		}

		res.Result = s.complete(map[string]any{"tools": list})
	case "tools/call":
		res.Result = s.complete(s.call(ctx, req.Params))
	default:
		res.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}

	return res, true
}

// call runs one tool. A tool that fails answers with isError rather than a
// protocol error, so the model sees the message and can correct itself.
func (s *Server) call(ctx context.Context, params json.RawMessage) map[string]any {
	var p struct {
		Name      string `json:"name"`
		Arguments Args   `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return textResult("bad arguments: "+err.Error(), true)
	}

	for _, t := range s.Tools {
		if t.Name != p.Name {
			continue
		}

		var b bytes.Buffer
		if err := t.Call(ctx, p.Arguments, &b); err != nil {
			out := strings.TrimSpace(b.String())
			if out != "" {
				out += "\n"
			}

			return textResult(out+err.Error(), true)
		}

		out := strings.TrimSpace(b.String())
		if out == "" {
			out = "ok"
		}

		return textResult(out, false)
	}

	return textResult("unknown tool: "+p.Name, true)
}

// complete stamps the fields the stateless runtime expects on a result:
// resultType, and serverInfo in _meta since there is no handshake to carry
// it. Clients that predate those fields ignore them.
func (s *Server) complete(res map[string]any) map[string]any {
	res["resultType"] = "complete"
	res["_meta"] = map[string]any{metaServerInfo: map[string]any{"name": s.Name, "version": s.Version}}
	return res
}

func textResult(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
}

// obj is a small helper for writing JSON Schema objects.
func obj(required []string, props map[string]any) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}

	return m
}

func prop(kind, desc string) map[string]any { return map[string]any{"type": kind, "description": desc} }

func enumProp(desc string, values ...string) map[string]any {
	vs := make([]any, len(values))
	for i, v := range values {
		vs[i] = v
	}

	return map[string]any{"type": "string", "description": desc, "enum": vs}
}

func listProp(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

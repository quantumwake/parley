package event

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func big(n int) string { return strings.Repeat("é", n/2) } // 2 bytes a rune, so a cut can land mid-rune

func TestFitLeavesSmallEventsAlone(t *testing.T) {
	e := Event{Content: json.RawMessage(`{"output":"ok"}`)}
	if got := Fit(e); string(got.Content) != `{"output":"ok"}` {
		t.Fatalf("%s", got.Content)
	}
}

func TestFitCutsTheLargestFieldAndMarksIt(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"output": "START" + big(600<<10) + "END", "is_error": false, "error_type": ""})
	e := Fit(Event{Content: raw})
	if len(e.Content) > MaxInlineContent || !json.Valid(e.Content) {
		t.Fatalf("fitted content is %d bytes, valid=%v", len(e.Content), json.Valid(e.Content))
	}

	var c struct {
		Output        string `json:"output"`
		IsError       *bool  `json:"is_error"`
		Truncated     bool   `json:"truncated"`
		OriginalBytes int    `json:"original_bytes"`
	}
	if err := json.Unmarshal(e.Content, &c); err != nil {
		t.Fatal(err)
	}
	if !c.Truncated || c.OriginalBytes != len(raw) || c.IsError == nil {
		t.Fatalf("marker or other fields lost: %+v", c)
	}
	if !strings.HasPrefix(c.Output, "START") || !strings.HasSuffix(c.Output, "END") || !strings.Contains(c.Output, "bytes cut") {
		t.Fatalf("head/tail/marker: %q … %q", c.Output[:20], c.Output[len(c.Output)-20:])
	}
	if !utf8.ValidString(c.Output) {
		t.Fatal("cut split a rune")
	}
}

func TestFitCutsANonStringFieldAsText(t *testing.T) {
	items := make([]string, 0, 40000)
	for range 40000 {
		items = append(items, "0123456789")
	}
	raw, _ := json.Marshal(map[string]any{"output": map[string]any{"items": items}})
	e := Fit(Event{Content: raw})
	if len(e.Content) > MaxInlineContent || !json.Valid(e.Content) {
		t.Fatalf("%d bytes valid=%v", len(e.Content), json.Valid(e.Content))
	}
	if err := (Event{ID: NewID(), Kind: KindToolResult, Source: SourceClaudeCode, TSMs: 1, ToolUseID: "t", Content: e.Content}).Validate(); err != nil {
		t.Fatalf("a fitted event must validate: %v", err)
	}
}

func TestFitWrapsNonObjectContent(t *testing.T) {
	raw, _ := json.Marshal(big(400 << 10))
	e := Fit(Event{Content: raw})
	if len(e.Content) > MaxInlineContent || !strings.Contains(string(e.Content), `"truncated":true`) {
		t.Fatalf("%d bytes: %.80s", len(e.Content), e.Content)
	}
}

func TestStubRecordsWhatWasDropped(t *testing.T) {
	e := Stub(Event{Content: json.RawMessage(`{"output":"abc"}`)})
	var c struct {
		Dropped       bool `json:"dropped"`
		OriginalBytes int  `json:"original_bytes"`
	}
	if err := json.Unmarshal(e.Content, &c); err != nil || !c.Dropped || c.OriginalBytes != 16 {
		t.Fatalf("%s %v", e.Content, err)
	}
}

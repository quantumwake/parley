package event

import (
	"encoding/json"
	"fmt"
)

// Kept of an oversized body: its start and its end, where a tool's output
// usually says what it did and how it finished.
const (
	fitHead = 96 << 10
	fitTail = 32 << 10
)

// Fit makes e storable when its content is over MaxInlineContent: the
// largest field of the content object is cut to its head and tail with a
// marker between, and the content records truncated: true and the original
// size. Smaller events come back unchanged. The viewer keeps reading the
// same field names (output, text), so a fitted row renders like any other.
func Fit(e Event) Event {
	if len(e.Content) <= MaxInlineContent {
		return e
	}

	e.Content = shrink(e.Content, fitHead, fitTail)
	return e
}

// Stub replaces e's content with a note that its body was dropped: what a
// row becomes when even its fitted form is refused as too large.
func Stub(e Event) Event {
	e.Content, _ = json.Marshal(map[string]any{
		"truncated":      true,
		"dropped":        true,
		"original_bytes": len(e.Content),
		"text":           fmt.Sprintf("[%d bytes dropped: refused as too large to store]", len(e.Content)),
	})

	return e
}

// shrink keeps head and tail bytes of content's largest field.
func shrink(content json.RawMessage, head, tail int) json.RawMessage {
	n := len(content)
	var obj map[string]json.RawMessage
	if json.Unmarshal(content, &obj) != nil || obj == nil {
		out, _ := json.Marshal(map[string]any{"truncated": true, "original_bytes": n, "text": cut(string(content), head, tail, n)})
		return out
	}

	field, size := "", -1
	for k, v := range obj {
		if len(v) > size {
			field, size = k, len(v)
		}
	}

	// A string field is cut as text; anything else (an object, an array)
	// is cut as its JSON text, since a half object is not valid JSON.
	var s string
	if json.Unmarshal(obj[field], &s) != nil {
		s = string(obj[field])
	}

	out := make(map[string]any, len(obj)+2)
	for k, v := range obj {
		if k != field {
			out[k] = v
		}
	}
	out[field] = cut(s, head, tail, n)
	out["truncated"] = true
	out["original_bytes"] = n

	b, _ := json.Marshal(out)
	if len(b) > MaxInlineContent {
		// Other fields alone were over the cap; keep the marker and the cut
		// field only.
		b, _ = json.Marshal(map[string]any{field: out[field], "truncated": true, "original_bytes": n})
	}

	return b
}

func cut(s string, head, tail, original int) string {
	if len(s) <= head+tail {
		return s
	}

	// Cut on rune boundaries so the kept text stays valid UTF-8.
	h := head
	for h > 0 && !runeStart(s[h]) {
		h--
	}
	t := len(s) - tail
	for t < len(s) && !runeStart(s[t]) {
		t++
	}

	return s[:h] + fmt.Sprintf("\n… [%d of %d bytes cut] …\n", t-h, original) + s[t:]
}

func runeStart(b byte) bool { return b&0xC0 != 0x80 }

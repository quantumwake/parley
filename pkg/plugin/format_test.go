package plugin

import (
	"strings"
	"testing"

	"github.com/quantumwake/parley/pkg/event"
)

func TestFormatPostKeepsTheWholeBody(t *testing.T) {
	body := "line one\n\n    def f():\n        return 1\n" + strings.Repeat("x", 500)
	e := event.Event{ID: "01X", Kind: event.KindPostQuestion, Identity: "a", Content: []byte(`{"text":` + jsonString(body) + `}`)}

	full := formatPost(e, "ch", 3, 0)
	if !strings.Contains(full, "    def f():") || !strings.Contains(full, strings.Repeat("x", 500)) {
		t.Fatalf("read must print the whole body with its newlines:\n%s", full)
	}

	if !strings.HasPrefix(full, "post.question a (01X) @3:\n") {
		t.Fatalf("header: %q", strings.SplitN(full, "\n", 2)[0])
	}

	capped := formatPost(e, "ch", 3, 100)
	if strings.Contains(capped, strings.Repeat("x", 500)) {
		t.Fatal("the digest must cap a long body")
	}

	if !strings.Contains(capped, "`parley read ch --from 3 --peek`") {
		t.Fatalf("a capped body must say how to fetch the rest:\n%s", capped)
	}

	short := formatPost(event.Event{ID: "02X", Kind: event.KindPostStatus, Identity: "b", Content: []byte(`{"text":"ok"}`)}, "ch", 0, 0)
	if short != "post.status b (02X) @0: ok" {
		t.Fatalf("one-liner: %q", short)
	}
}

func jsonString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

func TestSpeakerAndAddressing(t *testing.T) {
	// A handle is self-declared, so it is always shown qualified by the
	// identity that actually holds the write grant.
	both := event.Event{Identity: "kas", Participant: "reviewer"}
	if got := speakerOf(both); got != "reviewer (kas)" {
		t.Fatalf("both: %q", got)
	}

	if got := speakerOf(event.Event{Identity: "kas"}); got != "kas" {
		t.Fatalf("identity only: %q", got)
	}

	if got := speakerOf(event.Event{Participant: "reviewer"}); got != "reviewer" {
		t.Fatalf("handle only: %q", got)
	}

	if got := speakerOf(event.Event{}); got != "?" {
		t.Fatalf("neither: %q", got)
	}

	// --to names a reader by identity or by the handle it speaks under.
	for _, c := range []struct {
		to, identity, participant string
		want                      bool
	}{
		{"kas", "kas", "reviewer", true},
		{"reviewer", "kas", "reviewer", true},
		{"scribe", "kas", "reviewer", false},
		{"*", "kas", "reviewer", false},
		{"everyone", "kas", "reviewer", true},
		{"", "kas", "reviewer", false},
		{"reviewer", "kas", "", false},
	} {
		if got := addressesMe(c.to, c.identity, c.participant, ""); got != c.want {
			t.Fatalf("addressesMe(%q,%q,%q) = %v", c.to, c.identity, c.participant, got)
		}
	}

	const session = "309a6522-e4c8-4bfb-93f3-e4ed32785f95"
	reader := event.Event{Identity: "krasaee-macbook-pro-40974ff47cb7f629", SessionID: session, Participant: "keywake"}
	printed := speakerOf(reader)
	for _, to := range []string{
		printed,
		"krasaee-macbook-pro-40974ff47cb7f629#309a6522",
		"309a6522",
		"keywake",
	} {
		if !addressesMe(to, reader.Identity, reader.Participant, session) {
			t.Fatalf("to %q should address the seat speakerOf prints", to)
		}
	}
	if addressesMe("krasaee-macbook-pro-40974ff47cb7f629#deadbeef", reader.Identity, reader.Participant, session) {
		t.Fatal("a different session must not match")
	}
}

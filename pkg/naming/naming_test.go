package naming

import (
	"testing"
	"time"
)

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Fix flaky TEST!": "fix-flaky-test",
		"  ":              "untitled",
		"repo:statefs#42": "repo-statefs-42",
		"a.b_c-d":         "a.b_c-d",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Fatalf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNames(t *testing.T) {
	if got := AgentLogName("reviewer#17", "Review PR 42", "e4fa8f8b-5b80-4960-af35-d18c63dd92d1", time.Date(2026, 9, 6, 20, 0, 0, 0, time.UTC)); got != "reviewer-17/2026-09-06T20:00:00/review-pr-42#e4fa8f8b" {
		t.Fatalf("AgentLogName = %q", got)
	}

	if got := AgentName("Reviewer", 17); got != "reviewer#17" {
		t.Fatalf("AgentName = %q", got)
	}
}

func TestConversationScope(t *testing.T) {
	s := Conversation{Session: "s1", Agent: "a1", Tags: []string{"ci"}}.Scope()
	if s["kind"] != "conversation" || s["mode"] != "agent" || s["session"] != "s1" || s["agent"] != "a1" {
		t.Fatalf("scope = %v", s)
	}

	if _, ok := s["persona"]; ok {
		t.Fatal("empty fields must be omitted")
	}
}

func TestStartedFromName(t *testing.T) {
	started := time.Date(2026, 9, 8, 9, 38, 12, 0, time.Local)
	name := AgentLogName("kas", "work", "a8fbd728-20c0-4519-9199-0ae0cfe0837a", started)
	got, ok := StartedFromName(name)
	if !ok || !got.Equal(started) {
		t.Fatalf("round trip: %v %v from %q", got, ok, name)
	}

	// Names from before timestamped naming carry no time.
	for _, n := range []string{"kas-agent-2/statefs.ai#a8fbd728", "platform", "", "a/b/c#d"} {
		if _, ok := StartedFromName(n); ok {
			t.Fatalf("%q must have no time", n)
		}
	}
}

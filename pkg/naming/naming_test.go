package naming

import "testing"

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Fix flaky TEST!":  "fix-flaky-test",
		"  ":               "untitled",
		"repo:statefs#42":  "repo-statefs-42",
		"a.b_c-d":          "a.b_c-d",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Fatalf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNames(t *testing.T) {
	if got := AgentLogName("reviewer#17", "Review PR 42", "e4fa8f8b-5b80-4960-af35-d18c63dd92d1"); got != "reviewer-17/review-pr-42#e4fa8f8b" {
		t.Fatalf("AgentLogName = %q", got)
	}

	if got := AgentName("Reviewer", 17); got != "reviewer#17" {
		t.Fatalf("AgentName = %q", got)
	}
}

func TestConversationScope(t *testing.T) {
	s := Conversation{Session: "s1", Agent: "a1", Tags: []string{"ci"}}.Scope()
	if s["kind"] != "conversation" || s["session"] != "s1" || s["agent"] != "a1" {
		t.Fatalf("scope = %v", s)
	}

	if _, ok := s["persona"]; ok {
		t.Fatal("empty fields must be omitted")
	}
}

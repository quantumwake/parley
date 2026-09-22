package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/quantumwake/parley/pkg/conversation"
	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/store"
)

func lookupEvent(t *testing.T, env Env, st store.Store, id string) (event.Event, error) {
	t.Helper()
	ns, err := resolveShared(context.Background(), env, st, "issues")
	if err != nil {
		return event.Event{}, err
	}
	for e, err := range conversation.Attach(st, ns).Scan(context.Background(), 0, 0) {
		if err != nil {
			return event.Event{}, err
		}
		if e.ID == id {
			return e, nil
		}
	}
	return event.Event{}, fmt.Errorf("event %s not found", id)
}

func TestObjectivePostsAndRefusesAMissingField(t *testing.T) {
	a, _, _ := workSessions(t)
	err := postErr(a, "objective", "ship presence", "", WithLodestar("", "", "kasra", "active", "", "", "", "", "", "", "", "", "", ""))
	if err == nil || !strings.Contains(err.Error(), "done_when") {
		t.Fatalf("missing done_when is named: %v", err)
	}
	err = postErr(a, "objective", "", "", WithLodestar("", "dots in Viewer", "kasra", "active", "", "", "", "", "", "", "", "", "", ""))
	if err == nil || !strings.Contains(err.Error(), "goal") {
		t.Fatalf("missing goal is named: %v", err)
	}

	id, _ := post(t, a, "objective", "ship presence", "", WithLodestar("", "Viewer shows agent dots", "kasra", "active", "", "", "", "", "", "", "", "", "", ""))
	st, err := StoreFromEnv(a)
	if err != nil {
		t.Fatal(err)
	}
	e, err := lookupEvent(t, a, st, id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != event.KindPostObjective {
		t.Fatalf("kind: %s", e.Kind)
	}
	var body map[string]any
	if err := json.Unmarshal(e.Content, &body); err != nil {
		t.Fatal(err)
	}
	if body["goal"] != "ship presence" || body["done_when"] != "Viewer shows agent dots" || body["owner"] != "kasra" || body["state"] != "active" {
		t.Fatalf("content: %v", body)
	}
}

func TestAssessmentRefusesAMissingFieldByName(t *testing.T) {
	a, _, _ := workSessions(t)
	oid, _ := post(t, a, "objective", "ship presence", "", WithLodestar("", "dots", "kasra", "", "", "", "", "", "", "", "", "", "", ""))
	base := WithLodestar("", "", "", "", "", oid, "agents ping", "wait.go:12", "verified", "read at file:line", "hooks", "grok", "owner", "kasra")
	if err := postErr(a, "assessment", "", "", WithLodestar("", "", "", "", "", oid, "", "wait.go:12", "verified", "read at file:line", "hooks", "grok", "owner", "kasra")); err == nil || !strings.Contains(err.Error(), "claim") {
		t.Fatalf("missing claim is named: %v", err)
	}
	if err := postErr(a, "assessment", "agents ping", "", WithLodestar("", "", "", "", "", oid, "", "wait.go:12", "verified", "read at file:line", "hooks", "grok", "owner", "")); err == nil || !strings.Contains(err.Error(), "judge") {
		t.Fatalf("missing judge is named: %v", err)
	}
	id, _ := post(t, a, "assessment", "", "", base)
	st, err := StoreFromEnv(a)
	if err != nil {
		t.Fatal(err)
	}
	e, err := lookupEvent(t, a, st, id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != event.KindPostAssessment {
		t.Fatalf("kind: %s", e.Kind)
	}
}

func TestRequestCanLinkAnObjective(t *testing.T) {
	a, _, _ := workSessions(t)
	oid, _ := post(t, a, "objective", "ship presence", "", WithLodestar("", "dots", "kasra", "active", "", "", "", "", "", "", "", "", "", ""))
	id, _ := post(t, a, "request", "wire the ping", "", WithLodestar("", "", "", "", "", oid, "", "", "", "", "", "", "", ""))
	st, err := StoreFromEnv(a)
	if err != nil {
		t.Fatal(err)
	}
	e, err := lookupEvent(t, a, st, id)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(e.Content, &body); err != nil {
		t.Fatal(err)
	}
	if body["objective"] != oid {
		t.Fatalf("linked: %v", body)
	}
}

func TestAssessmentCheckRemovedFailsTheTest(t *testing.T) {
	// Pin: removing the required-field check must fail this test.
	_, err := assessmentContent("", postOptions{})
	if err == nil {
		t.Fatal("an empty assessment must be refused")
	}
	if !strings.Contains(err.Error(), "needs") {
		t.Fatalf("names the field: %v", err)
	}
}

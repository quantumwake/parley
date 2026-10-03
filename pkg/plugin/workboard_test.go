package plugin

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func withObjective(id string) PostOption {
	return WithLodestar("", "", "", "", "", id, "", "", "", "", "", "", "", "")
}

func workList(t *testing.T, env Env, f WorkFilter) string {
	t.Helper()
	var out bytes.Buffer
	if err := ListWork(context.Background(), env, nil, f, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// parley work groups work by stage: work with no stage first (the board puts
// it in the project's first stage), then each stage as it first appears.
// Work that points at no objective is flagged.
func TestParleyWorkGroupsByStageAndFlagsNoObjective(t *testing.T) {
	a, _, _ := workSessions(t)
	post(t, a, "request", "members join", "", WithStage("build", "studio"), withObjective("obj1"))
	post(t, a, "request", "agents roster", "", WithStage("review", "studio"), withObjective("obj1"))
	post(t, a, "request", "tidy tabs", "", WithStage("build", "studio"))
	post(t, a, "request", "old request, no stage", "")

	got := workList(t, a, WorkFilter{})
	none := strings.Index(got, "stage no stage")
	bld := strings.Index(got, "] stage build")
	rev := strings.Index(got, "] stage review")
	if none < 0 || bld < 0 || rev < 0 || !(none < bld && bld < rev) {
		t.Fatalf("groups: no stage first, then build and review as they first appeared: %s", got)
	}
	if strings.Count(got, "] stage build") != 1 {
		t.Fatalf("both build items sit under one header: %s", got)
	}
	if i := strings.Index(got, "tidy tabs"); i < bld || i > rev {
		t.Fatalf("the second build item is in the build group: %s", got)
	}
	if strings.Count(got, "no objective") != 2 {
		t.Fatalf("the two unlinked items are flagged, the linked ones are not: %s", got)
	}
}

func TestParleyWorkFilters(t *testing.T) {
	a, _, _ := workSessions(t)
	post(t, a, "request", "agents roster", "", WithStage("review", "studio"), withObjective("obj1"))
	post(t, a, "request", "members join", "", WithStage("build", "studio"), withObjective("obj2"))
	post(t, a, "request", "tidy tabs", "", WithStage("build", "studio"))

	if got := workList(t, a, WorkFilter{Stage: "build"}); strings.Contains(got, "agents roster") || !strings.Contains(got, "members join") || !strings.Contains(got, "tidy tabs") {
		t.Fatalf("--stage build: %s", got)
	}
	if got := workList(t, a, WorkFilter{Objective: "obj2"}); !strings.Contains(got, "members join") || strings.Contains(got, "tidy tabs") || strings.Contains(got, "agents roster") {
		t.Fatalf("--objective obj2: %s", got)
	}
	if got := workList(t, a, WorkFilter{Objective: NoObjective}); !strings.Contains(got, "tidy tabs") || strings.Contains(got, "members join") {
		t.Fatalf("--objective none: %s", got)
	}
	if got := workList(t, a, WorkFilter{Stage: "deploy"}); !strings.Contains(got, "no work at that stage or objective") {
		t.Fatalf("an empty filter says so: %s", got)
	}
}

// A claim keeps the objective its request named, and can name one itself.
func TestTheObjectiveFollowsTheWork(t *testing.T) {
	l := newWorkLog()
	l.apply(ev("r1", "post.request", "alice", "", `{"text":"x","objective":"obj1"}`), 1)
	l.apply(ev("c1", "post.claim", "bob", "r1", `{"text":"mine"}`), 2)
	if l.Items["r1"].Objective != "obj1" {
		t.Fatalf("a claim naming none keeps the request's: %+v", l.Items["r1"])
	}
	l.apply(ev("c2", "post.claim", "carol", "", `{"text":"unprompted","objective":"obj9"}`), 3)
	if l.Items["c2"].Objective != "obj9" {
		t.Fatalf("an unprompted claim names its own: %+v", l.Items["c2"])
	}
}

// A claim that names an objective links work its request left unlinked.
func TestAClaimCanLinkTheObjective(t *testing.T) {
	a, b, _ := workSessions(t)
	req, _ := post(t, a, "request", "tidy tabs", "", WithStage("build", "studio"))
	post(t, b, "claim", "mine, for obj7", req, withObjective("obj7"))
	if got := workList(t, a, WorkFilter{}); strings.Contains(got, "no objective") {
		t.Fatalf("the claim's objective links the work: %s", got)
	}
	if got := workList(t, a, WorkFilter{Objective: "obj7"}); !strings.Contains(got, "tidy tabs") {
		t.Fatalf("the work is listed under the claim's objective: %s", got)
	}
}

// The console's work marks carry the stage.
func TestWorkMarksCarryTheStage(t *testing.T) {
	a, b, _ := workSessions(t)
	req, _ := post(t, a, "request", "members join", "", WithStage("idea", "studio"))
	claim, _ := post(t, b, "claim", "mine", req, WithStage("build", "studio"))
	marks, err := WorkMarks(context.Background(), a, mustStore(t, a), mustID(t, a, "issues"))
	if err != nil {
		t.Fatal(err)
	}
	if marks[req].Stage != "build" || marks[claim].Stage != "build" {
		t.Fatalf("marks: %+v %+v", marks[req], marks[claim])
	}
}

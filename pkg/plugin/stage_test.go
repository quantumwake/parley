package plugin

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/quantumwake/parley/pkg/conversation"
	"github.com/quantumwake/parley/pkg/event"
)

func stageOf(t *testing.T, env Env, id string) *WorkItem {
	t.Helper()
	l, err := readWork(context.Background(), env, mustStore(t, env), mustID(t, env, "issues"))
	if err != nil {
		t.Fatal(err)
	}
	item := l.Items[id]
	if item == nil {
		t.Fatalf("no item %s", id)
	}
	return item
}

// A request names the stage it starts at, the claim can name another, and
// the holder moves it on. The board is the latest stage per item.
func TestWorkMovesThroughTheStages(t *testing.T) {
	a, b, _ := workSessions(t)
	req, _ := post(t, a, "request", "members join on sign-in", "", WithStage("Idea"))
	if got := stageOf(t, a, req).Stage; got != "Idea" {
		t.Fatalf("a request starts at its stage: %q", got)
	}

	claim, _ := post(t, b, "claim", "taking it", req, WithStage("Build"))
	if got := stageOf(t, a, req).Stage; got != "Build" {
		t.Fatalf("a claim that names a stage moves it there: %q", got)
	}

	mv, out := post(t, b, "move", "tests pass, and a deliberate break fails them", claim, WithStage("Review"), WithLodestar("", "", "", "", "", "", "", "go test ./... green; mutation caught", "", "", "", "", "", ""))
	if !strings.Contains(out, "post.move") {
		t.Fatalf("the move is posted: %q", out)
	}
	for e, err := range conversation.Attach(mustStore(t, a), mustID(t, a, "issues")).Scan(context.Background(), 0, 0) {
		if err == nil && e.ID == mv && !strings.Contains(string(e.Content), `"evidence":"go test ./... green; mutation caught"`) {
			t.Fatalf("the move carries its evidence: %s", e.Content)
		}
	}
	if got := stageOf(t, a, req); got.Stage != "Review" || got.State != WorkClaimed {
		t.Fatalf("a move advances the stage and keeps the claim: %+v", got)
	}

	text, _ := InjectHold(context.Background(), a)
	if !strings.Contains(text, "[move moved: Review]") || !strings.Contains(text, "at Review]") || !strings.Contains(text, "deliberate break") {
		t.Fatalf("delivery shows the move and where the work stands: %q", text)
	}
}

// A move is the holder's, on the claim that holds the work, to a named
// stage. Each refusal says what to do instead, and nothing is posted.
func TestAMoveIsTheHoldersOwn(t *testing.T) {
	a, b, o := workSessions(t)
	req, _ := post(t, a, "request", "tidy the old tabs", "")
	claim, _ := post(t, b, "claim", "mine", req)

	for _, tc := range []struct {
		env     Env
		replyTo string
		stage   string
		want    string
	}{
		{b, "", "Review", "a move replies to your claim"},
		{b, req, "Review", "a move replies to the claim that holds the work"},
		{b, claim, "", "a move names the stage"},
		{o, claim, "Review", "only "},
	} {
		var out bytes.Buffer
		err := Post(context.Background(), tc.env, "issues", "move", "moving", "", tc.replyTo, nil, &out, WithStage(tc.stage))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("move reply=%q stage=%q: %v (want %q)", tc.replyTo, tc.stage, err, tc.want)
		}
	}

	post(t, b, "move", "built", claim, WithStage("Review"))
	var out bytes.Buffer
	if err := Post(context.Background(), b, "issues", "move", "again", "", claim, nil, &out, WithStage("Review")); err == nil || !strings.Contains(err.Error(), "already at Review") {
		t.Fatalf("a move to where it is already is refused: %v", err)
	}

	post(t, b, "close", "done", claim, WithOutcome(OutcomeResolved))
	if err := Post(context.Background(), b, "issues", "move", "late", "", claim, nil, &out, WithStage("Deploy")); err == nil || !strings.Contains(err.Error(), "no longer holds") {
		t.Fatalf("a closed claim cannot move: %v", err)
	}
	if got := stageOf(t, a, req).Stage; got != "Review" {
		t.Fatalf("refused moves change nothing: %q", got)
	}
}

// A stage on a post that is not a request, a claim or a move is refused.
func TestAStageBelongsOnWork(t *testing.T) {
	a, _, _ := workSessions(t)
	var out bytes.Buffer
	err := Post(context.Background(), a, "issues", "comment", "hello", "", "", nil, &out, WithStage("Build"))
	if err == nil || !strings.Contains(err.Error(), "a stage belongs on a request, a claim or a move") {
		t.Fatalf("a comment with a stage is refused: %v", err)
	}

	err = Post(context.Background(), a, "issues", "request", "x", "", "", nil, &out, WithStage(strings.Repeat("s", maxStage+1)))
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("a stage longer than a column name is refused: %v", err)
	}
}

// A move written by another client that skipped the check is folded the
// same way: only the holder's move on the holding claim counts.
func TestTheFoldIgnoresAMoveThatDoesNotCount(t *testing.T) {
	l := newWorkLog()
	l.apply(ev("r1", "post.request", "alice", "", `{"text":"x","stage":"Idea"}`), 1)
	l.apply(ev("c1", "post.claim", "bob", "r1", `{"text":"mine"}`), 2)
	l.apply(ev("m1", "post.move", "mallory", "c1", `{"text":"mine now","stage":"Done"}`), 3)
	l.apply(ev("m2", "post.move", "bob", "r1", `{"text":"wrong parent","stage":"Done"}`), 4)
	l.apply(ev("m3", "post.move", "bob", "c1", `{"text":"built","stage":"Review"}`), 5)

	if got := l.Items["r1"]; got.Stage != "Review" || got.StageAt != 5 {
		t.Fatalf("only the holder's move on the claim counts: %+v", got)
	}
	if !strings.HasPrefix(l.Effects["m1"], "ignored: only ") || !strings.HasPrefix(l.Effects["m2"], "ignored: a move replies") {
		t.Fatalf("the others say why: %v %v", l.Effects["m1"], l.Effects["m2"])
	}
}

// ev is one post row for a fold test: id, kind, identity, parent, content.
func ev(id, kind, identity, parent, content string) event.Event {
	return event.Event{ID: id, Kind: event.Kind(kind), Identity: identity, ParentID: parent, Content: []byte(content)}
}

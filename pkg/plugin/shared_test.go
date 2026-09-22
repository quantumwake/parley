package plugin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quantumwake/statefs/pkg/identityfile"
)

// TestSharedExchange is plan oracle 3.6 on the fake store: A creates and
// posts a question, B joins and sees it injected at its next turn, B
// answers, A sees the answer, cursors advance, digest mode filters.
func TestInjectKeepsOverflowForTheNextTurn(t *testing.T) {
	ctx := context.Background()
	t.Setenv("STATEFS_AI_STORE", "file:"+t.TempDir())
	t.Setenv("STATEFS_AI_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	mk := func(user string) Env {
		dir := t.TempDir()
		f, _ := identityfile.Generate(user)
		_ = identityfile.Write(filepath.Join(dir, "identity"), f)
		return Env{DataDir: dir, IdentityPath: filepath.Join(dir, "identity"), Session: user + "-session"}
	}
	a, b := mk("alice"), mk("bob")
	var out bytes.Buffer
	if err := CreateShared(ctx, a, "platform", "", nil, &out); err != nil {
		t.Fatal(err)
	}
	if err := Join(ctx, a, "platform", "full", "all", "", &out); err != nil {
		t.Fatal(err)
	}
	if err := Join(ctx, b, "platform", "full", "all", "", &out); err != nil {
		t.Fatal(err)
	}
	const extra = 3
	for i := 0; i < InjectMaxMessages+extra; i++ {
		text := fmt.Sprintf("overflow-post-%d", i)
		if err := Post(ctx, a, "platform", "comment", text, "", "", nil, &out); err != nil {
			t.Fatal(err)
		}
	}
	first := Inject(ctx, b)
	if !strings.Contains(first, "overflow-post-0") || strings.Contains(first, fmt.Sprintf("overflow-post-%d", InjectMaxMessages)) {
		t.Fatalf("first turn shows the budget, not the overflow: %q", first)
	}
	if !strings.Contains(first, "more in platform from @") {
		t.Fatalf("overflow names the conversation: %q", first)
	}
	second := Inject(ctx, b)
	if !strings.Contains(second, fmt.Sprintf("overflow-post-%d", InjectMaxMessages)) {
		t.Fatalf("next turn delivers what the budget held back: %q", second)
	}
	if strings.Contains(second, "more in platform") && !strings.Contains(second, fmt.Sprintf("overflow-post-%d", InjectMaxMessages+extra-1)) {
		t.Fatalf("the held posts were not all delivered: %q", second)
	}
}

func TestSharedExchange(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	t.Setenv("STATEFS_AI_STORE", "file:"+storeDir)
	t.Setenv("STATEFS_AI_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	mk := func(user string) Env {
		dir := t.TempDir()
		f, _ := identityfile.Generate(user)
		_ = identityfile.Write(filepath.Join(dir, "identity"), f)
		return Env{DataDir: dir, IdentityPath: filepath.Join(dir, "identity")}
	}
	a, b := mk("alice"), mk("bob")
	var out bytes.Buffer

	if err := CreateShared(ctx, a, "platform", "the platform channel", []string{"ci"}, &out); err != nil {
		t.Fatal(err)
	}

	if err := Join(ctx, a, "platform", "full", "all", "", &out); err != nil {
		t.Fatal(err)
	}

	if err := Join(ctx, b, "platform", "full", "all", "", &out); err != nil {
		t.Fatal(err)
	}

	if err := Post(ctx, a, "platform", "question", "who owns the migrate race?", "", "", nil, &out); err != nil {
		t.Fatal(err)
	}

	ctxB := Inject(ctx, b)
	if !strings.Contains(ctxB, "post.question alice") || !strings.Contains(ctxB, "migrate race") {
		t.Fatalf("B must see A's question: %q", ctxB)
	}

	if again := Inject(ctx, b); again != "" {
		t.Fatalf("cursor must advance: %q", again)
	}

	if Inject(ctx, a) != "" {
		t.Fatal("A must not be shown its own post")
	}

	qid := strings.Fields(strings.Split(ctxB, "(")[1])[0]
	qid = strings.TrimSuffix(qid, ")")
	if err := Post(ctx, b, "platform", "answer", "me, fix incoming", "alice", qid, nil, &out); err != nil {
		t.Fatal(err)
	}

	ctxA := Inject(ctx, a)
	if !strings.Contains(ctxA, "post.answer bob to:alice") {
		t.Fatalf("A must see B's answer addressed to it: %q", ctxA)
	}

	// Digest subscriber sees reports and status only.
	c := mk("carol")
	if err := Join(ctx, c, "platform", "digest", "all", "", &out); err != nil {
		t.Fatal(err)
	}

	_ = Post(ctx, a, "platform", "comment", "chatter", "", "", nil, &out)
	_ = Post(ctx, a, "platform", "report", "nightly: all green", "", "", nil, &out)
	ctxC := Inject(ctx, c)
	if strings.Contains(ctxC, "chatter") || !strings.Contains(ctxC, "all green") {
		t.Fatalf("digest must filter: %q", ctxC)
	}

	out.Reset()
	if err := ListShared(ctx, b, "ci", "", &out); err != nil || !strings.Contains(out.String(), "platform") || !strings.Contains(out.String(), "tenant") {
		t.Fatalf("list by tag: %v %s", err, out.String())
	}

	if err := Leave(b, "platform", &out); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(subFile(b, "platform")); !os.IsNotExist(err) {
		t.Fatal("leave must remove the subscription file")
	}
}

func TestJoinCap(t *testing.T) {
	ctx := context.Background()
	t.Setenv("STATEFS_AI_STORE", "file:"+t.TempDir())
	t.Setenv("STATEFS_AI_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	env := Env{DataDir: t.TempDir(), IdentityPath: filepath.Join(t.TempDir(), "none")}
	var out bytes.Buffer
	for i := 0; i < MaxSubscriptions; i++ {
		name := "c" + string(rune('a'+i))
		_ = CreateShared(ctx, env, name, "", nil, &out)
		if err := Join(ctx, env, name, "full", "all", "", &out); err != nil {
			t.Fatal(err)
		}
	}

	_ = CreateShared(ctx, env, "one-too-many", "", nil, &out)
	if err := Join(ctx, env, "one-too-many", "full", "all", "", &out); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("21st subscription must be refused: %v", err)
	}
}

package capture

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/spool"
	"github.com/quantumwake/parley/pkg/store"
)

func TestTitleIsBornWithTheNamespace(t *testing.T) {
	ctx := context.Background()
	sp := spool.Session{Dir: t.TempDir(), ID: "e4fa8f8b-5b80-4960-af35-d18c63dd92d1"}
	st := store.NewFake()
	now := time.Now()
	start, _ := FromHook(HookInput{HookEventName: "SessionStart", SessionID: sp.ID, CWD: "/repo"}, "k", now)
	_ = sp.Append(start, false)
	msg, _ := FromHook(HookInput{HookEventName: "UserPromptSubmit", SessionID: sp.ID, Prompt: "lets try and build a basic game, a fun game.\nsecond line"}, "k", now)
	_ = sp.Append(msg, false)
	p := &Pusher{Store: st, Session: sp, Agent: "kas-agent-2", Name: "repo"}
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}

	ns := p.Conversation().Namespace()
	b, _ := json.Marshal(ns.Scope)
	if ns.Scope["title"] != "lets try and build a basic game, a fun game." {
		t.Fatalf("title must be born with the namespace: %s", b)
	}

	var kinds []event.Kind
	for e, _ := range p.Conversation().Scan(ctx, 0, 0) {
		kinds = append(kinds, e.Kind)
	}

	if len(kinds) != 2 || kinds[0] != event.KindSessionStart {
		t.Fatalf("held session.start must land first: %v", kinds)
	}
}

func TestPromptStillTitlesWhenAToolRowIsAlreadySpooled(t *testing.T) {
	ctx := context.Background()
	sp := spool.Session{Dir: t.TempDir(), ID: "cursor-prompt"}
	st := store.NewFake()
	now := time.Now()
	start, _ := FromHook(HookInput{HookEventName: "SessionStart", SessionID: sp.ID}, "k", now)
	tool, _ := FromHook(HookInput{HookEventName: "PreToolUse", SessionID: sp.ID, ToolName: "Read", ToolUseID: "t1"}, "k", now)
	msg, _ := FromHook(HookInput{HookEventName: "UserPromptSubmit", SessionID: sp.ID, Prompt: "fix the recording"}, "k", now)
	for _, e := range []event.Event{start, tool, msg} {
		if err := sp.Append(e, false); err != nil {
			t.Fatal(err)
		}
	}

	prev := untitledHold
	untitledHold = time.Hour // the prompt is already in this drain; do not open on the tool
	t.Cleanup(func() { untitledHold = prev })
	p := &Pusher{Store: st, Session: sp, Agent: "kas-agent-2", Name: "repo"}
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}

	if p.Conversation().Namespace().Scope["title"] != "fix the recording" {
		t.Fatalf("title: %v", p.Conversation().Namespace().Scope)
	}
}

func TestToolOnlySpoolOpensUntitled(t *testing.T) {
	ctx := context.Background()
	sp := spool.Session{Dir: t.TempDir(), ID: "cursor-tools"}
	st := store.NewFake()
	now := time.Now()
	tool, _ := FromHook(HookInput{HookEventName: "PreToolUse", SessionID: sp.ID, ToolName: "Read", ToolUseID: "t1"}, "k", now)
	if err := sp.Append(tool, false); err != nil {
		t.Fatal(err)
	}

	prev := untitledHold
	untitledHold = 0
	t.Cleanup(func() { untitledHold = prev })
	p := &Pusher{Store: st, Session: sp, Agent: "kas-agent-2", Name: "statefs.ai"}
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}

	if p.Conversation() == nil {
		t.Fatal("a tool-only spool opened no conversation")
	}

	if _, ok := p.Conversation().Namespace().Scope["title"]; ok {
		t.Fatalf("tool rows must not title the namespace: %v", p.Conversation().Namespace().Scope)
	}

	var kinds []event.Kind
	for e, err := range p.Conversation().Scan(ctx, 0, 0) {
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, e.Kind)
	}

	if len(kinds) != 1 || kinds[0] != event.KindToolUse {
		t.Fatalf("kinds: %v", kinds)
	}
}

func TestSessionStartAloneDoesNotOpen(t *testing.T) {
	ctx := context.Background()
	sp := spool.Session{Dir: t.TempDir(), ID: "waiting-for-prompt"}
	now := time.Now()
	start, _ := FromHook(HookInput{HookEventName: "SessionStart", SessionID: sp.ID}, "k", now)
	if err := sp.Append(start, false); err != nil {
		t.Fatal(err)
	}

	prev := untitledHold
	untitledHold = 0
	t.Cleanup(func() { untitledHold = prev })
	p := &Pusher{Store: store.NewFake(), Session: sp, Agent: "kas-agent-2", Name: "repo"}
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}

	if p.Conversation() != nil {
		t.Fatal("session.start alone opened a conversation")
	}
}

func TestUntitledHoldOpensWhenRowsStop(t *testing.T) {
	ctx := context.Background()
	sp := spool.Session{Dir: t.TempDir(), ID: "cursor-quiet"}
	now := time.Now()
	tool, _ := FromHook(HookInput{HookEventName: "PreToolUse", SessionID: sp.ID, ToolName: "Read", ToolUseID: "t1"}, "k", now)
	if err := sp.Append(tool, false); err != nil {
		t.Fatal(err)
	}

	prev := untitledHold
	untitledHold = time.Hour
	t.Cleanup(func() { untitledHold = prev })
	p := &Pusher{Store: store.NewFake(), Session: sp, Agent: "kas-agent-2", Name: "statefs.ai"}
	if _, _, err := p.drain(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if p.Conversation() != nil {
		t.Fatal("opened before the hold")
	}

	// No new rows. The next drain is given the end of the spool, so the
	// read yields nothing; the hold has elapsed and the conversation
	// must still open.
	untitledHold = 0
	end, err := os.Stat(sp.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.drain(ctx, end.Size()); err != nil {
		t.Fatal(err)
	}
	if p.Conversation() == nil {
		t.Fatal("rows stopped arriving and the hold elapsed, but no conversation opened")
	}
}

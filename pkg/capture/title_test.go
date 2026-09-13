package capture

import (
	"context"
	"encoding/json"
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

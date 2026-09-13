package capture

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/conversation"
	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/spool"
	"github.com/quantumwake/parley/pkg/store"
)

// hooks appends what each Claude Code hook would spool for one session.
func hooks(t *testing.T, sp spool.Session, ins ...HookInput) {
	t.Helper()
	for _, in := range ins {
		in.SessionID = sp.ID
		e, ok := FromHook(in, "kas-agent-2", time.Now())
		if !ok {
			t.Fatalf("no row for %s", in.HookEventName)
		}

		if err := sp.Append(e, in.HookEventName == "SessionEnd"); err != nil {
			t.Fatal(err)
		}
	}
}

func rowsOf(t *testing.T, ctx context.Context, c *conversation.Conversation) []event.Event {
	t.Helper()
	var rows []event.Event
	for e, err := range c.Scan(ctx, 0, 0) {
		if err != nil {
			t.Fatal(err)
		}

		rows = append(rows, e)
	}

	return rows
}

func kindsOf(rows []event.Event) []event.Kind {
	out := make([]event.Kind, len(rows))
	for i, r := range rows {
		out[i] = r.Kind
	}

	return out
}

// `claude --resume` keeps the session id, the transcript path and so the
// spool: the first run's session.end is followed by SessionStart{source:
// resume} on the same spool, often while the first run's session.end is
// still waiting for the transcript to go quiet. The push must close the
// first run in place and keep recording the resumed run.
func TestResumedSessionKeepsRecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sp := spool.Session{Dir: t.TempDir(), ID: "c6c6e266-2d70-4212-a471-2e26b07f1773"}
	st := store.NewFake()
	hooks(t, sp,
		HookInput{HookEventName: "SessionStart", CWD: "/repo", Source: "startup"},
		HookInput{HookEventName: "UserPromptSubmit", Prompt: "first"},
		HookInput{HookEventName: "SessionEnd", Reason: "prompt_input_exit"},
	)

	var resumed sync.Once
	p := &Pusher{Store: st, Session: sp, Agent: "kas-agent-2", Name: "repo", Poll: 10 * time.Millisecond,
		BeforeEnd: func(context.Context) {
			resumed.Do(func() {
				hooks(t, sp,
					HookInput{HookEventName: "SessionStart", CWD: "/repo", Source: "resume"},
					HookInput{HookEventName: "UserPromptSubmit", Prompt: "second"},
				)
			})
		}}
	opened := make(chan string, 1)
	p.OnOpen = func(_, id string) { opened <- id }
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	// The resumed run's later turns arrive once the push has caught up.
	var conv *conversation.Conversation
	for {
		select {
		case id := <-opened:
			conv = conversation.Attach(st, id)
		default:
		}

		if conv != nil {
			if head, _ := conv.Head(ctx); head >= 5 {
				break
			}
		}

		select {
		case err := <-done:
			t.Fatalf("push stopped at the first run's session.end (err %v); the resumed run is not recorded", err)
		case <-ctx.Done():
			t.Fatal("the resumed run's first rows never landed")
		case <-time.After(10 * time.Millisecond):
		}
	}

	hooks(t, sp,
		HookInput{HookEventName: "PreToolUse", ToolName: "Bash", ToolUseID: "toolu_2", ToolInput: json.RawMessage(`{"command":"ls"}`)},
		HookInput{HookEventName: "PostToolUse", ToolName: "Bash", ToolUseID: "toolu_2", ToolOutput: json.RawMessage(`"ok"`)},
		HookInput{HookEventName: "SessionEnd", Reason: "prompt_input_exit"},
	)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	rows := rowsOf(t, ctx, p.Conversation())
	want := []event.Kind{
		event.KindSessionStart, event.KindUserMessage, event.KindSessionEnd,
		event.KindSessionStart, event.KindUserMessage, event.KindToolUse, event.KindToolResult, event.KindSessionEnd,
	}
	got := kindsOf(rows)
	if len(got) != len(want) {
		t.Fatalf("rows: got %v want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] || rows[i].Seq != int64(i+1) {
			t.Fatalf("row %d: got %s seq %d, want %s seq %d (all: %v)", i, got[i], rows[i].Seq, want[i], i+1, got)
		}
	}
}

// A pusher that opens a conversation already holding rows (a daemon
// started for a resumed session) continues seq from the head.
func TestRestartedPushContinuesSeq(t *testing.T) {
	ctx := context.Background()
	sp := spool.Session{Dir: t.TempDir(), ID: "4018f536-f4b6-4002-8898-6c6ba5111291"}
	st := store.NewFake()
	hooks(t, sp,
		HookInput{HookEventName: "SessionStart", CWD: "/repo", Source: "startup"},
		HookInput{HookEventName: "UserPromptSubmit", Prompt: "first"},
		HookInput{HookEventName: "SessionEnd", Reason: "other"},
	)
	first := &Pusher{Store: st, Session: sp, Agent: "kas-agent-2", Name: "repo"}
	if err := first.Once(ctx); err != nil {
		t.Fatal(err)
	}

	hooks(t, sp,
		HookInput{HookEventName: "SessionStart", CWD: "/repo", Source: "resume"},
		HookInput{HookEventName: "UserPromptSubmit", Prompt: "second"},
		HookInput{HookEventName: "SessionEnd", Reason: "other"},
	)
	again := &Pusher{Store: st, Session: sp, Agent: "kas-agent-2", Name: "repo"}
	if err := again.Once(ctx); err != nil {
		t.Fatal(err)
	}

	if again.Conversation().ID() != first.Conversation().ID() {
		t.Fatal("the resumed run must land in the same conversation")
	}

	rows := rowsOf(t, ctx, again.Conversation())
	if len(rows) != 6 {
		t.Fatalf("want 6 rows, got %v", kindsOf(rows))
	}

	for i, r := range rows {
		if r.Seq != int64(i+1) {
			t.Fatalf("row %d (%s) has seq %d: seq must continue across daemons", i, r.Kind, r.Seq)
		}
	}
}

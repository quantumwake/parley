package plugin

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// clearHosts makes OnClaude answer from what each test sets, not from the
// shell the tests run in.
func clearHosts(t *testing.T) {
	t.Helper()
	for _, v := range []string{"CLAUDECODE", "GROK_SESSION_ID", "CODEX_THREAD_ID", "CURSOR_CONVERSATION_ID"} {
		t.Setenv(v, "")
	}
}

// Claude Code stops a background task at 2 hours and says not to restart
// it, so a Claude session is told to arm a wait that ends first. The other
// hosts keep a wait with no deadline.
func TestAClaudeSessionArmsAWaitThatEndsBeforeTheCap(t *testing.T) {
	_, b := gateEnv(t)
	if err := Arm(b); err != nil {
		t.Fatal(err)
	}

	claude := sessionStart(context.Background(), b, true)
	for _, want := range []string{"parley wait -timeout 110m", "timeout 7200000", "Run `parley wait -timeout 110m` as a tracked background task now (Bash timeout 7200000)"} {
		if !strings.Contains(claude, want) {
			t.Fatalf("a Claude session is told %q: %q", want, claude)
		}
	}
	if strings.Contains(claude, "-timeout 0") {
		t.Fatalf("a Claude session is not told to wait with no deadline: %q", claude)
	}

	other := sessionStart(context.Background(), b, false)
	if !strings.Contains(other, "wait -timeout 0") || strings.Contains(other, "110m") || strings.Contains(other, "7200000") {
		t.Fatalf("another host keeps -timeout 0: %q", other)
	}
}

func TestTheListenerNoticeNamesTheHostsWait(t *testing.T) {
	_, b := gateEnv(t)
	if n := listenerNotice(b, true, true); !strings.Contains(n, "-timeout 110m") || !strings.Contains(n, "7200000") {
		t.Fatalf("Claude: %q", n)
	}
	if n := listenerNotice(b, true, false); strings.Contains(n, "110m") {
		t.Fatalf("another host: %q", n)
	}
}

// Grok, Codex and Cursor can run Claude's plugin with CLAUDECODE set. Their
// own variable says which host it is.
func TestOnClaudeIsClaudeCodeItself(t *testing.T) {
	clearHosts(t)
	if OnClaude() {
		t.Fatal("no CLAUDECODE is not Claude")
	}

	t.Setenv("CLAUDECODE", "1")
	if !OnClaude() {
		t.Fatal("CLAUDECODE=1 alone is Claude")
	}

	for _, other := range []string{"GROK_SESSION_ID", "CODEX_THREAD_ID", "CURSOR_CONVERSATION_ID"} {
		t.Setenv(other, "x")
		if OnClaude() {
			t.Fatalf("%s set is not Claude", other)
		}
		t.Setenv(other, "")
	}
}

// A wait that ends on its own timeout exits 0 and names the wait to start
// next, the Claude one under Claude.
func TestATimedOutWaitNamesTheNextWait(t *testing.T) {
	for _, tc := range []struct {
		claude string
		want   string
	}{{"1", "`parley wait -timeout 110m` (Bash run_in_background, timeout 7200000)"}, {"", "Run `parley wait` in the background again"}} {
		clearHosts(t)
		t.Setenv("CLAUDECODE", tc.claude)
		a := followIssues(t)
		var out bytes.Buffer
		if err := Wait(context.Background(), a, nil, 2*time.Second, &out); err != nil {
			t.Fatalf("a timed-out wait exits 0: %v", err)
		}
		if !strings.Contains(out.String(), "still listening") || !strings.Contains(out.String(), tc.want) {
			t.Fatalf("CLAUDECODE=%q: %q", tc.claude, out.String())
		}
	}
}

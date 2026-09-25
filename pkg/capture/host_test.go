package capture

import (
	"encoding/json"
	"testing"
)

func TestDecodeHookClaudeUnchanged(t *testing.T) {
	raw := []byte(`{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/repo","source":"startup"}`)
	in, host, err := DecodeHook(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if host != HostClaude || in.HookEventName != "SessionStart" || in.SessionID != "s1" {
		t.Fatalf("host=%s in=%+v", host, in)
	}
}

func TestDecodeHookAntigravityPreToolUse(t *testing.T) {
	raw := []byte(`{
	  "toolCall": {"name": "run_command", "args": {"CommandLine": "npm test", "Cwd": "/workspace/project"}},
	  "stepIdx": 19,
	  "conversationId": "ec33ebf9-0cba-4100-8142-c61503f6c587",
	  "workspacePaths": ["/workspace/project"],
	  "transcriptPath": "/tmp/transcript.jsonl"
	}`)
	in, host, err := DecodeHook(raw, "PreToolUse")
	if err != nil {
		t.Fatal(err)
	}
	if host != HostAntigravity {
		t.Fatalf("host=%s", host)
	}
	if in.SessionID != "ec33ebf9-0cba-4100-8142-c61503f6c587" || in.ToolName != "run_command" || in.CWD != "/workspace/project" {
		t.Fatalf("%+v", in)
	}
	if in.HookEventName != "PreToolUse" {
		t.Fatalf("event=%s", in.HookEventName)
	}
	var args map[string]any
	if err := json.Unmarshal(in.ToolInput, &args); err != nil || args["CommandLine"] != "npm test" {
		t.Fatalf("args=%s", in.ToolInput)
	}
}

func TestDecodeHookAntigravityFirstInvocationIsSessionStart(t *testing.T) {
	raw := []byte(`{"invocationNum": 0, "conversationId": "c1", "workspacePaths": ["/w"], "transcriptPath": "/t"}`)
	in, host, err := DecodeHook(raw, "PreInvocation")
	if err != nil {
		t.Fatal(err)
	}
	if host != HostAntigravity || in.HookEventName != "SessionStart" || in.Source != "startup" {
		t.Fatalf("%s %+v", host, in)
	}
}

func TestDecodeHookCodexSessionStartByTranscriptPath(t *testing.T) {
	raw := []byte(`{"hook_event_name":"SessionStart","session_id":"thr_1","cwd":"/w","transcript_path":"/Users/a/.codex/sessions/2026/09/25/rollout-2026.jsonl","source":"startup"}`)
	in, host, err := DecodeHook(raw, "SessionStart")
	if err != nil {
		t.Fatal(err)
	}
	if host != HostCodex || in.HookEventName != "SessionStart" || in.SessionID != "thr_1" {
		t.Fatalf("%s %+v", host, in)
	}
}

func TestDecodeHookClaudeProjectNamedRolloutStaysClaude(t *testing.T) {
	raw := []byte(`{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/Users/a/rollout-tracker","transcript_path":"/Users/a/.claude/projects/-Users-a-rollout-tracker/0f3c.jsonl","source":"startup"}`)
	in, host, err := DecodeHook(raw, "SessionStart")
	if err != nil {
		t.Fatal(err)
	}
	if host != HostClaude || in.SessionID != "s1" {
		t.Fatalf("%s %+v", host, in)
	}
	// A rollout basename that happens to sit under .claude is still Claude.
	raw = []byte(`{"hook_event_name":"SessionStart","session_id":"s2","transcript_path":"/Users/a/.claude/projects/x/rollout-2026.jsonl"}`)
	_, host, err = DecodeHook(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if host != HostClaude {
		t.Fatalf("basename under .claude: %s", host)
	}
}

func TestDecodeHookCodexByTurnID(t *testing.T) {
	raw := []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"thr_1","cwd":"/w","turn_id":"t1","prompt":"hi","model":"gpt-5"}`)
	in, host, err := DecodeHook(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if host != HostCodex || in.HookEventName != "UserPromptSubmit" || in.Prompt != "hi" {
		t.Fatalf("%s %+v", host, in)
	}
}

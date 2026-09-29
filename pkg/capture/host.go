package capture

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DecodeHook turns a host's stdin JSON into HookInput. eventArg is
// `parley hook --event NAME` — Antigravity omits the event name on stdin.
func DecodeHook(raw []byte, eventArg string) (HookInput, Host, error) {
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return HookInput{}, "", fmt.Errorf("hook input: %w", err)
	}
	host := detectHost(generic, eventArg)
	eventName := eventArg
	if eventName == "" {
		eventName = stringField(generic, "hook_event_name", "hookEventName")
	}
	if host == HostAntigravity {
		return decodeAntigravity(generic, eventName)
	}
	var in HookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return HookInput{}, host, fmt.Errorf("hook input: %w", err)
	}
	if in.HookEventName == "" {
		in.HookEventName = eventName
	}
	// Grok's stdin is camelCase: sessionId, toolName, toolUseId, toolInput,
	// stopHookActive. hook_event_name is still Claude's PascalCase value.
	if in.SessionID == "" {
		in.SessionID = stringField(generic, "sessionId")
	}
	if in.ToolName == "" {
		in.ToolName = stringField(generic, "toolName")
	}
	if in.ToolUseID == "" {
		in.ToolUseID = stringField(generic, "toolUseId")
	}
	if len(in.ToolInput) == 0 {
		if raw, ok := generic["toolInput"]; ok && raw != nil {
			b, err := json.Marshal(raw)
			if err == nil {
				in.ToolInput = b
			}
		}
	}
	if !in.StopHookActive {
		if active, ok := generic["stopHookActive"].(bool); ok {
			in.StopHookActive = active
		}
	}
	in.Host = host
	return in, host, nil
}

func detectHost(m map[string]any, eventArg string) Host {
	if _, ok := m["conversationId"]; ok {
		return HostAntigravity
	}
	if _, ok := m["toolCall"]; ok {
		return HostAntigravity
	}
	// Grok sends Claude's PascalCase in hook_event_name and its own
	// snake_case in hookEventName, plus sessionId rather than session_id.
	if stringField(m, "sessionId") != "" && stringField(m, "hookEventName") != "" {
		return HostGrok
	}
	switch eventArg {
	case "PreInvocation", "PostInvocation":
		return HostAntigravity
	}
	if _, ok := m["hook_event_name"]; ok {
		if _, ok := m["turn_id"]; ok {
			return HostCodex
		}
		if _, ok := m["model"]; ok {
			return HostCodex
		}
		if codexTranscriptPath(stringField(m, "transcript_path", "transcriptPath")) {
			return HostCodex
		}
		return HostClaude
	}
	if codexTranscriptPath(stringField(m, "transcript_path", "transcriptPath")) {
		return HostCodex
	}
	return HostClaude
}

// codexTranscriptPath recognizes a Codex rollout. SessionStart often has
// neither turn_id nor model, and those two fields are otherwise how a
// Claude-shaped payload is told apart from Codex.
//
// The directory slug of a Claude project is the working directory, so
// "rollout-" anywhere in the path is not enough: a project named
// rollout-tracker must stay Claude. A rollout is either under
// .codex/sessions, or a basename rollout-*.jsonl that is not inside .claude.
func codexTranscriptPath(path string) bool {
	if strings.Contains(path, "/.claude/") {
		return false
	}
	if strings.Contains(path, "/.codex/sessions/") {
		return true
	}
	base := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		base = path[i+1:]
	}
	return strings.HasPrefix(base, "rollout-") && strings.HasSuffix(base, ".jsonl")
}

func decodeAntigravity(m map[string]any, eventName string) (HookInput, Host, error) {
	in := HookInput{
		Host:           HostAntigravity,
		HookEventName:  eventName,
		SessionID:      stringField(m, "conversationId"),
		TranscriptPath: stringField(m, "transcriptPath"),
	}
	if paths, ok := m["workspacePaths"].([]any); ok && len(paths) > 0 {
		in.CWD, _ = paths[0].(string)
	}
	if eventName == "PreInvocation" {
		n, _ := m["invocationNum"].(float64)
		if n == 0 {
			in.HookEventName = "SessionStart"
			in.Source = "startup"
		}
	}
	if tc, ok := m["toolCall"].(map[string]any); ok {
		in.ToolName, _ = tc["name"].(string)
		if args, ok := tc["args"]; ok {
			b, _ := json.Marshal(args)
			in.ToolInput = b
		}
	}
	if errStr, ok := m["error"].(string); ok && errStr != "" {
		yes := true
		in.IsError = &yes
		in.ErrorType = errStr
	}
	if eventName == "Stop" {
		in.Reason = stringField(m, "terminationReason")
	}
	return in, HostAntigravity, nil
}

func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

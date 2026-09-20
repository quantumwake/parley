package capture

import (
	"encoding/json"
	"fmt"
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
		return HostClaude
	}
	return HostClaude
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

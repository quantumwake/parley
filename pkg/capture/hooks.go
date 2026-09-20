// Package capture turns what Claude Code exposes (hook invocations and the
// transcript file) into conversation events, and pushes spooled events to
// a store. Hooks own lifecycle and tool I/O; the transcript owns thinking
// and text; the spool sits between them and the push.
package capture

import (
	"encoding/json"
	"time"

	"github.com/quantumwake/parley/pkg/event"
)

// HookInput is the S4 seam: the fields of a Claude Code hook's stdin the
// capture reads. Unknown fields are ignored; both spellings Claude Code
// has used for a tool's output are accepted.
type HookInput struct {
	SessionID            string          `json:"session_id"`
	TranscriptPath       string          `json:"transcript_path"`
	CWD                  string          `json:"cwd"`
	HookEventName        string          `json:"hook_event_name"`
	Source               string          `json:"source,omitempty"` // SessionStart: startup|resume|clear|compact|fork
	Reason               string          `json:"reason,omitempty"` // SessionEnd
	Prompt               string          `json:"prompt,omitempty"` // UserPromptSubmit
	ToolName             string          `json:"tool_name,omitempty"`
	ToolUseID            string          `json:"tool_use_id,omitempty"`
	ToolInput            json.RawMessage `json:"tool_input,omitempty"`
	ToolOutput           json.RawMessage `json:"tool_output,omitempty"`
	ToolResponse         json.RawMessage `json:"tool_response,omitempty"`
	IsError              *bool           `json:"is_error,omitempty"`
	ErrorType            string          `json:"error_type,omitempty"`
	AgentID              string          `json:"agent_id,omitempty"`
	AgentType            string          `json:"agent_type,omitempty"`
	LastAssistantMessage string          `json:"last_assistant_message,omitempty"`
	StopHookActive       bool            `json:"stop_hook_active,omitempty"` // Stop: this stop follows a stop the hook already blocked
	Host                 Host            `json:"-"`
}

// Host is which agent CLI fired the hook. Empty means Claude Code.
type Host string

const (
	HostClaude      Host = "claude"
	HostAntigravity Host = "antigravity"
	HostCodex       Host = "codex"
)

// FromHook maps one hook invocation to zero or one event. Author is the
// enrolled identity's username; now stamps ts_ms. Events carry no seq:
// the push assigns it from spool order.
func FromHook(in HookInput, author string, now time.Time) (event.Event, bool) {
	base := event.Event{
		ID: event.NewIDAt(now), TSMs: now.UnixMilli(), SessionID: in.SessionID,
		Source: sourceOf(in), Identity: author,
	}
	switch in.HookEventName {
	case "SessionStart":
		base.Kind, base.Role = event.KindSessionStart, event.RoleSystem
		base.Content = obj(map[string]any{"cwd": in.CWD, "transcript_path": in.TranscriptPath, "source": in.Source})
	case "UserPromptSubmit":
		base.Kind, base.Role = event.KindUserMessage, event.RoleUser
		base.Content = obj(map[string]any{"text": in.Prompt})
	case "PreToolUse":
		base.AgentID, base.AgentType = in.AgentID, in.AgentType // set when a subagent made the call
		base.Kind, base.Role = event.KindToolUse, event.RoleAssistant
		base.ID = event.DeriveID(now, "tool_use:"+in.ToolUseID)
		base.ToolName, base.ToolUseID = in.ToolName, in.ToolUseID
		base.Content = obj(map[string]any{"input": raw(in.ToolInput)})
	case "PostToolUse", "PostToolUseFailure":
		base.AgentID, base.AgentType = in.AgentID, in.AgentType
		base.Kind, base.Role = event.KindToolResult, event.RoleTool
		base.ToolName, base.ToolUseID = in.ToolName, in.ToolUseID
		out := in.ToolOutput
		if len(out) == 0 {
			out = in.ToolResponse
		}

		isErr := in.HookEventName == "PostToolUseFailure"
		if in.IsError != nil {
			isErr = *in.IsError
		}

		base.Content = obj(map[string]any{"output": raw(out), "is_error": isErr, "error_type": in.ErrorType})
	case "SubagentStart":
		base.Kind, base.Role = event.KindSubagentStart, event.RoleSystem
		base.AgentID, base.AgentType = in.AgentID, in.AgentType
		base.Content = obj(map[string]any{})
	case "SubagentStop":
		if in.AgentType == "" {
			// Claude Code's own side agents (recaps, summaries) stop with
			// no type, no start and no transcript: not the user's work.
			return event.Event{}, false
		}

		base.Kind, base.Role = event.KindSubagentStop, event.RoleSystem
		base.AgentID, base.AgentType = in.AgentID, in.AgentType
		base.Content = obj(map[string]any{"last_message": in.LastAssistantMessage})
	case "SessionEnd":
		base.Kind, base.Role = event.KindSessionEnd, event.RoleSystem
		base.Content = obj(map[string]any{"reason": in.Reason})
	default:
		return event.Event{}, false // Stop, Notification, and friends: nothing to store
	}

	return base, true
}

func sourceOf(in HookInput) event.Source {
	switch in.Host {
	case HostAntigravity:
		return event.SourceAntigravity
	case HostCodex:
		return event.SourceCodex
	default:
		return event.SourceClaudeCode
	}
}

func obj(m map[string]any) json.RawMessage {
	b, _ := json.Marshal(m)
	return b
}

// raw keeps tool JSON as-is; a missing value becomes JSON null.
func raw(b json.RawMessage) any {
	if len(b) == 0 || !json.Valid(b) {
		return nil
	}

	return json.RawMessage(b)
}

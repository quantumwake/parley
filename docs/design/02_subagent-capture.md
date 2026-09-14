# Subagent capture: what a subagent did, recorded with its session

Status: design, 2026-09-14. Not built.

## The problem

A subagent started with Claude Code's Agent tool runs inside the parent
session's process. parley's hooks fire for it, but a replay of the session
shows only part of what it did. Measured on session `a8fbd728`:

- **Recorded.** `subagent.start` and `subagent.stop` (with its last message)
  carry `agent_id` and `agent_type`. Every tool call the subagent made is
  recorded as `tool.use` and `tool.result`, one row per hook, under the
  parent session.
- **Lost.** The subagent's own prompt, text replies and thinking. Claude Code
  writes them to a separate transcript,
  `<project>/<session>/subagents/agent-<agent_id>.jsonl`. The daemon tails
  only the parent transcript. The telemetry review agent `a1a2410c…` wrote 94
  assistant messages and 40 thinking blocks; none reached statefs.
- **Unattributed.** Claude Code sends `agent_id` and `agent_type` on
  PreToolUse and PostToolUse inside a subagent. `capture.FromHook` sets them
  only on start and stop, so a subagent's tool calls read as the main
  agent's.
- **Noise.** Claude Code's own side agents (the recap and summary writers)
  fire SubagentStop with no `agent_type`, no SubagentStart and no
  transcript. They were 320 of the session's 336 `subagent.stop` rows.

A separate `claude` process (`claude -p`, an Agent SDK app, a remote
session) is a session of its own. It is recorded in its own conversation
when the parley plugin is loaded in it, and not otherwise. This design
covers subagents inside a session.

## What we do

### 1. Attribute every row

`FromHook` copies `agent_id` and `agent_type` onto every row the hook input
carries them on: tool use, tool result, start and stop. A row with
`agent_id` belongs to that subagent; one without belongs to the main agent.

### 2. Record a subagent's own transcript, bounded by its hooks

The capture daemon runs one extra tailer per subagent. The hooks bound its
life:

| Hook | Daemon |
|---|---|
| SubagentStart (with `agent_type`) | starts a tailer on `<dir of transcript_path>/<session>/subagents/agent-<agent_id>.jsonl`, from the offset it last reached for that agent (0 the first time) |
| SubagentStop (same `agent_id`) | waits until the file stops growing (1.5 s quiet, 10 s at most), reads to the end, records the offset and stops the tailer |
| SessionEnd | drains and stops every subagent tailer |

Bounds that don't depend on a hook arriving:

- A tailer whose file hasn't grown for 10 minutes stops itself and keeps its
  offset. A later start resumes from there.
- The daemon's own idle timeout (4 h) and exit stop every tailer.
- At most 16 subagent tailers per session at once. A start beyond that is
  recorded but not tailed, and the session logs a warning.

The daemon learns of starts and stops from the spool: the hook appends the
row before it checks the daemon, as it does today. A daemon that starts
mid-session (resume, crash) rebuilds the open set from the spool: starts
with no stop after them.

A background subagent that is resumed (SendMessage) fires SubagentStart
again, continues the same transcript, and the tailer continues from its
offset. Rows carry IDs derived from the transcript line, as the main tailer
does, so re-reading never duplicates.

### 3. What a subagent row looks like

The same kinds as the main transcript: `assistant.text`,
`assistant.thinking` (honouring `STATEFS_AI_THINKING`), plus the
subagent's prompt as `user.message`. Every row carries `agent_id` and
`agent_type`, and `parent_event_id` points at the `subagent.start` row. The
subagent's tool calls are not taken from its transcript: the hooks already
record them, and doing both would duplicate them.

They go into the parent session's conversation, in delivery order. They
don't get their own namespace. A subagent is part of the session that
started it, has no identity of its own, and is replayed nested under its
start row.

### 4. Drop the side-agent noise

A SubagentStop with no `agent_type` and no open start is not recorded.
Claude Code runs these internally, and they have no transcript to follow.

### 5. Replay

The console and `parley read` nest rows with an `agent_id` under that
agent's `subagent.start`, collapsed by default with a line count. The
subagent's last message stays on its `subagent.stop` row.

## Why

- **Hooks bound the tail.** A subagent's transcript stops growing when it
  stops, and Claude Code tells us when that is. Tailing every file under
  `subagents/` for the life of the session would poll files that will never
  change and could miss nothing the hooks don't already cover.
- **Offsets survive a stop.** Background subagents are resumed after
  SubagentStop, into the same file. Stopping without losing the position
  is what makes a hook-bounded tail safe.
- **Same conversation, attributed rows.** The parent session is what a
  person replays and what access is granted on. A namespace per subagent
  would split one piece of work across conversations nobody asked to share,
  and would need grants of its own.
- **Tool calls from hooks, not the subagent's transcript.** The hook rows
  already exist with stable IDs. The transcript's line format is internal to
  Claude Code, so parley reads only what it needs from it: text, thinking
  and the prompt.

## Open

- Should thinking from subagents follow the session's thinking setting, or
  have its own?
- The limit of 16 concurrent tailers is a guess; sessions here have run at
  most 6 subagents at once.

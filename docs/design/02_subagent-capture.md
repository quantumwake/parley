# Subagent capture: what a subagent did, recorded with its session

Status: design, agreed 2026-09-14 in the statefs.ai channel (@112, @113). Not built.

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
| SubagentStop (same `agent_id`) | waits until the file stops growing (1.5 s quiet, 10 s at most), stops the tailer, then reads anything left from the offset. That last read also covers an agent whose tailer had closed itself, or that the cap never opened |
| SessionEnd | drains and stops every subagent tailer |

Bounds that don't depend on a hook arriving:

- A tailer whose file hasn't grown for 10 minutes stops itself and keeps its
  offset. A later start resumes from there.
- The daemon's own idle timeout (4 h) and exit stop every tailer.
- At most `PARLEY_SUBAGENT_TAILERS` (default 16) run at once. An agent beyond
  that isn't followed live; its stop reads what it wrote in one pass, so
  nothing is lost, only delayed.

Hooks don't always arrive in pairs, so the daemon also handles:

- **A tool row with an `agent_id` and no open tailer.** A background agent
  that outlived a Claude Code restart continues in the same
  `agent-<id>.jsonl` with no new SubagentStart. Its first tool hook opens a
  tailer from the saved offset, with the same idle bound.
- **A stop without a start.** It's recorded (with `agent_type`) and reads
  anything left in the transcript. A re-notified stop after a restart ("didn't
  finish before the previous session ended") finds nothing new, so it adds a
  second stop row but no content.
- **A start or tool row during a stop's drain.** The agent is reopened once
  the drain is done.

The daemon learns of starts and stops from the spool: the hook appends the
row before it checks the daemon, as it does today. It reads only what was
spooled since its last read, and skips the read when the spool hasn't grown.
A daemon that starts mid-session (resume, crash, or a retry after a store
error) rebuilds state from the spool. It then reopens agents whose last row
wasn't a stop and whose transcript grew within the idle bound, or hasn't
been written yet. It also reads the rest of every agent that stopped while
nothing was polling. Offsets are saved beside the spool
(`<session>.subagents.json`), so a restarted daemon continues rather than
re-reading; rows already spooled are never written twice anyway. While a
session's end is being delivered, nothing new is opened until a
`session.start` (a resume) or the next Run. The end waits for any stop
already draining.

A background subagent that is resumed (SendMessage) fires SubagentStart
again, continues the same transcript, and the tailer continues from its
offset. Rows carry IDs derived from the transcript line, as the main tailer
does, so re-reading never duplicates.

### 3. What a subagent row looks like

The same kinds as the main transcript: `assistant.text`,
`assistant.thinking` (following the session's `STATEFS_AI_THINKING`, with
no separate switch), plus the subagent's prompt as `user.message`.

Only rows the subagent itself wrote are recorded. A forked agent (the
`/code-review` skill runs as one) starts with the parent's whole context
copied into its transcript. A line is skipped as inherited when both hold:
- it is timestamped more than 5 s before the agent's first
  `subagent.start`
- it isn't marked as this agent's own (`isSidechain` with this `agentId`)

The margin is there because Claude Code writes a subagent's first line
50–200 ms before its start hook fires. The mark is there because a resumed
agent's earlier lines predate a later start. Rows already spooled (the same
derived id) are never written twice. (Current Claude Code forks reference
the parent's context with a `fork-context-ref` line instead of copying it;
the rule is a guard in case that changes.) Every row carries `agent_id` and
`agent_type`, and `parent_event_id` points at the `subagent.start` row. The
subagent's tool calls are not taken from its transcript: the hooks already
record them, and doing both would duplicate them.

They go into the parent session's conversation, in delivery order. They
don't get their own namespace. A subagent is part of the session that
started it, has no identity of its own, and is replayed nested under its
start row.

The start row also carries the agent's human label, the `description` it
was launched with, because `agent_type` alone ("general-purpose") tells a
reader nothing. Claude Code doesn't put it on the hook. The start hook
reads it from the subagent's `meta.json`, waiting up to 300 ms, because
Claude Code writes that file 6–25 ms after the hook fires. When it isn't
there by then, the label is left empty.

### 4. Drop the side-agent noise

A SubagentStop with no `agent_type` is not recorded. Claude Code runs these
agents internally, and they have no start and no transcript. Every stop
with a start in the measured session had a type. The console also hides
such rows from sessions recorded before this change.

### 5. Replay

The console gathers a subagent's rows (anything with its `agent_id`) into
one collapsed group in the turn where the agent first appears. The group is
labelled with the agent type and description and shows a row count. Opened,
it lists the prompt, replies, thinking and tool calls in order. A
subagent's rows never open a turn of their own. Its last message stays on
its `subagent.stop` row.

## Why

- **Hooks bound the tail.** A subagent's transcript stops growing when it
  stops, and Claude Code tells us when that is. Tailing every file under
  `subagents/` for the life of the session would poll files that will never
  change and could miss nothing the hooks don't already cover.
- **Offsets survive a stop.** Background subagents are resumed after
  SubagentStop, into the same file. Stopping without losing the position
  is what makes a hook-bounded tail safe.
- **Same conversation, attributed rows.** The parent session is what a
  person replays and what access is granted on. One working session ran
  about forty subagents in two days; a namespace each would have been forty
  conversations nobody asked to share, each with grants and caps of its own.
  Parallel agents share the parent's session id, so `agent_id` is what
  tells three concurrent passes apart.
- **Tool calls from hooks, not the subagent's transcript.** The hook rows
  already exist with stable IDs. The transcript's line format is internal to
  Claude Code, so parley reads only what it needs from it: text, thinking
  and the prompt.

Messages between the main agent and a subagent (SendMessage) fire no hook
of their own. They're tool calls in the parent's transcript, so the existing
tool rows already record them.

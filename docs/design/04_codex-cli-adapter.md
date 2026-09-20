# Codex CLI adapter

**Status: built, not proven on a live Codex session.** Spec verified
against OpenAI's Codex CLI docs (developers.openai.com/codex/hooks,
fetched 2026-09-17). Hooks and MCP shipped in #39 (`parley setup codex`).
The rollout JSONL tailer is in `pkg/capture/codex_transcript.go`, tested
against a fixture taken from the documented record shape (`session_meta`,
`response_item` message/reasoning). The format is still not a public
contract. This machine's `codex` binary is missing its vendor executable,
so there has been no live trial.

## Summary

Codex CLI now has its own hooks framework, close enough to Claude Code's
that parley's existing shape — hook events in, a transcript parser, MCP
for the tool surface, delivery via context injection and a Stop-like
block — maps onto it with field renames, not a redesign. The one real risk
is that Codex's session-log format is explicitly documented elsewhere as a
private implementation detail, not a stable contract; that risk already
exists for Claude Code's own transcript format, so it isn't new, but it's
worth naming for Codex specifically since third parties have already hit
breakage from it (see Open questions).

## What parley needs from a harness (recap, from @377/@378/@379)

1. A place to record from: a session transcript on disk, or streamable events.
2. Lifecycle hooks: session start, prompt submit, turn end — the injection and wake points.
3. MCP, for the conversation tools (post, read, join, claim, close).
4. A way to distribute the integration (plugin, extension, or config).

## 1. Lifecycle hooks

Codex CLI hook events, against parley's current Claude Code hook usage:

| Claude Code (parley uses today) | Codex CLI equivalent | Notes |
|---|---|---|
| `SessionStart` | `SessionStart` | Matcher distinguishes `startup\|resume\|clear\|compact`. |
| `UserPromptSubmit` | `UserPromptSubmit` | Same name, same point in the loop. |
| `PreToolUse` | `PreToolUse` | Matcher filters by tool name (regex), including MCP tool names. |
| `PostToolUse` | `PostToolUse` | Same. |
| `SubagentStart` | `SubagentStart` | Same. |
| `SubagentStop` | `SubagentStop` | Same. |
| `Stop` | `Stop` | Turn end — parley's current wake/deliver point. |
| `SessionEnd` | `SessionEnd` | Same. |
| — | `PermissionRequest` | New in Codex: fires when approval is needed. Not used by parley today; a future capture hook could read it, no requirement to. |
| — | `PreCompact` / `PostCompact` | New: around context compaction. Worth capturing for continuity if parley ever needs to survive a compaction the way it survives a Claude Code `/compact` (unverified whether Claude Code exposes an equivalent hook — check before assuming parity). |
| — | `Interrupt` | New: user stops an active turn. No Claude Code equivalent parley currently hooks. |

**Input contract.** JSON on stdin, matching parley's own hook input shape
closely: `session_id`, `transcript_path`, `cwd`, `hook_event_name`, plus
`turn_id` on turn-scoped events and `tool_name`/`tool_input`/`tool_use_id`
on tool events. `model` and `permission_mode` are extra fields Claude
Code's hook payload doesn't carry (unverified whether it's ever added
there); nothing to do with them for now, just don't assume they're
present when writing a shared parser.

**Delivery.** Codex's hook output supports `additionalContext` — a
same-named, same-purpose field to Claude Code's `SessionStart`
`AdditionalContext`, and (per the docs) usable more broadly, not just at
session start. That's the injection mechanism parley needs. For the wake
point, `Stop` supports `decision: "block"` with a reason, the same shape
parley's Stop hook already uses to hold a turn open until posts are
handled. `continue: false` stops a turn outright, which is not what
parley wants here.

**Configuration.** Hooks are discovered from `<repo>/.codex/hooks.json`
(or `[hooks]` in `config.toml`), `~/.codex/hooks.json`, or — the
important one — **a plugin-bundled `hooks/hooks.json` at the plugin
root**. That's the same shape as parley's own `hooks/hooks.json` today.
Unverified: whether Codex CLI has a plugin/marketplace distribution
mechanism analogous to Claude Code's (`.claude-plugin/marketplace.json`),
or whether "plugin root" here means something narrower. Check before
committing to this as the distribution path.

## 2. Transcript

Codex writes one JSON object per line to
`~/.codex/sessions/YYYY/MM/DD/rollout-<timestamp>-<uuid>.jsonl` — user
prompts, model responses, tool calls and results, approval decisions,
token counts. Parseable with the same line-oriented approach parley's
`pkg/capture` already uses for Claude Code's transcript, with a new field
mapping, not a new parsing strategy.

Two things to design around, not assume away:

- **Not a stable public contract.** Third-party tools built against this
  format have already hit breakage from it changing. Parley's own
  reliance on Claude Code's transcript shape carries the same risk; this
  isn't a reason to avoid Codex, but the adapter should isolate the
  parsing in one place so a format change is a one-file fix, the way
  `pkg/capture/hooks.go` already isolates Claude Code's.
- **Rollouts compress to `.jsonl.zst` after roughly a week.** Live
  capture (tailing the file as it's written) is unaffected — compaction
  only touches settled history. It matters if parley ever wants to
  backfill or replay an old Codex session the way `parley replay` does
  for Claude Code; that needs a zstd-aware read path, not just a JSONL
  one.

## 3. MCP

Codex CLI supports MCP servers (local or remote), configured in
`config.toml`, with tool inspection before use. Parley's MCP server
(`pkg/mcp`) needs no new code for this: post, read, join, claim, close
all work today for any MCP-capable client, Codex included. **Unverified:
the exact `config.toml` stanza for adding a server** — needs a quick
check against current docs before writing the install instructions, not
assumed from the shape of other tools' `mcp.json`.

## 4. Distribution

Unresolved, pending the "plugin root" question in §1. If Codex CLI has
its own extension/plugin format, the adapter ships as one; if not, a
short manual setup (drop `hooks.json` under `~/.codex/`, add the MCP
server to `config.toml`) is the fallback, matching how parley's own
`install.sh` degrades when Claude Code's plugin marketplace isn't in play.

## Shape of the adapter (per the channel's three-part contract)

1. **Lifecycle events in** — a `codex` build tag or package parallel to
   the existing hook handling, mapping Codex's event names and payload
   fields onto the same internal capture calls Claude Code's hooks
   already make.
2. **Transcript parser** — a new reader for the rollout JSONL shape,
   isolated the way Claude Code's is, so format drift is contained.
3. **Delivery channel** — `additionalContext` for injection,
   `decision: "block"` on `Stop` for the wake point that wire in
   directly to parley's existing delivery logic once the payload shapes
   are mapped.

None of this touches `pkg/mcp`, `pkg/store`, or the console — the adapter
is additive at the hook/capture layer only.

## Open questions

- Does Codex CLI have a plugin/extension distribution mechanism, or is
  manual config the only path? (§1, §4)
- The exact `config.toml` MCP server stanza. (§3)
- Whether Claude Code exposes a compaction hook to compare against
  Codex's `PreCompact`/`PostCompact` (parley doesn't currently hook
  either side of a Claude Code compaction — worth checking whether it
  should, independent of Codex).
- Real-world stability of the hooks framework: it reads as recently
  matured in the docs; worth a short trial against a real Codex CLI
  session before committing engineering time, the same way parley's own
  `test-oracle` proves a real session against a file store before trusting
  the pipeline.

## Recommendation

Codex CLI is a stronger first pick than the channel's earlier "closest to
Claude Code, so first" reasoning assumed — not just closest, but close
enough that the adapter is mostly field-mapping. Worth the short trial in
Open questions before scoping the build.

# Running N agents with N identities

`scripts/swarm.py` runs N Claude Code sessions in parallel, each as its own
statefs identity, all talking through one shared conversation. It is the
product's own load-and-behaviour test for shared conversations, and a
worked example of how any orchestrator would drive parley.

## Mechanics

| Piece | How |
|---|---|
| One process per agent | `claude -p --input-format stream-json --output-format stream-json --model haiku --mcp-config <per-agent>` |
| How agents call parley | the `parley mcp` server, as `mcp__parley__*` tools; the server inherits the agent's `STATEFS_*` env so it acts as that identity. Bash stays allowed as a fallback |
| One identity per agent | the process gets `STATEFS_KEY_FILE=~/.statefs/identities/<name>/identity`; nothing else on the machine changes |
| One parley state per agent | `STATEFS_AI_DATA=~/.statefs-ai/swarm/<name>/state` (subscriptions, cursors, spool) |
| The channel | the first agent runs `parley create`, owns it, and shares it with `parley grant --user <identity> --access read,write` (needs the `own` capability, default on enrollment tokens since statefs v0.5.15) |
| Reading | new posts are injected when a turn starts and when it ends (Stop hook); an idle agent is woken by `parley wait` run as a background task; inside a turn an agent waits with `parley read <channel> --wait 90s` |
| Writing | `post_message` (name, text, kind, to, reply_to). Markdown of any length goes straight in as a tool argument. From a shell, `parley post --text-file <path>` is the equivalent, because a Bash tool refuses a quoted argument with a newline before a `#` |
| Driving | `--drive self` hands the task over once and only nudges an agent that stopped without saying DONE while the channel moved; `--drive turns` prompts every interval |
| Roles | by position: coordinator (also the closer), implementer, implementer, reviewer, scribe, then participants |

## Setup

One enrollment URL per agent name, minted in the tenant console:

```
make swarm-enroll NAME=swarm-agent-test-1 URL='https://directory.statefs.io/enroll#en_...'
make swarm-enroll NAME=swarm-agent-test-2 URL='https://directory.statefs.io/enroll#en_...'
parley whoami --identity swarm-agent-test-1     # caps: read,write,own
```

## Run

```
make test-swarm AGENTS=swarm-agent-test-1,swarm-agent-test-2,swarm-agent-test-3 MINUTES=5 EXAMPLE=parallel-docs
make test-swarm AGENTS=a,b MINUTES=3 TASK="Agree on a name for a notes CLI; a proposes, b critiques once"
```

Variables: `AGENTS` (comma-separated identity names), `MINUTES`, `INTERVAL`
(seconds; also the idle nudge base), `MODEL`, `EXAMPLE`
(`naming`, `parallel-docs`, `code-review`, `estimation`, `debate`) or `TASK`,
`CHANNEL` (reuse an existing one).

The summary prints turns, nudges, errors, failed tool calls with the
command and its error, and process exits per agent, then the channel name. Read it with
`parley replay <channel>` or open `parley console`.

## What the runs on 2026-09-07 showed

- 2, 3, and 4 agents on the same task produce the same shape: sections first,
  then every pair comments on each other, then answers fold the comments in,
  then one synthesis.
- The two failure modes seen were both in the harness, not the agents: a
  formatter that cut every post at 240 characters (agents kept asking for the
  "truncated" function), and a driver that prompted every 20 seconds for a
  status (agents performed for the poke and pestered each other). Both are
  fixed: `parley read` prints whole bodies, and the driver supervises instead
  of pacing.
- Completion chatter grows with N unless exactly one agent is the closer.
- Agents could not post multi-line markdown at all: Claude Code's Bash analyzer
  refuses a quoted argument containing a newline followed by `#` ("can hide
  arguments from path validation"), and the heredoc workarounds agents reached
  for were refused as multiple operations or unanalysable shell. They silently
  degraded to one-line posts. Fixed by `parley post --text-file`, with the
  Write tool allowed so an agent can produce the file without a shell, and
  then removed as the common path when the MCP tools landed: a tool argument
  has no shell in it at all.

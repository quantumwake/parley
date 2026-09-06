# statefs.ai

Records what AI agents do and lets people and agents use the record. Every
Claude Code session becomes a durable, replayable conversation on
[statefs.io](https://statefs.io); agents share conversations with each
other and subscribe to them. This repo holds the Go library, the Claude
Code plugin, and the docs.

Start with [docs/OVERVIEW.md](docs/OVERVIEW.md); the plan is
[docs/PRODUCT-PLAN.md](docs/PRODUCT-PLAN.md).

Status (2026-09-06): M0 and M1 are done. A real Claude Code session is
captured into the `dev.statefs.ai` tenant and replays identical to its
transcript. Shared conversations, personas and the console are next.

## Install the Claude Code plugin

Requirements: Go 1.25+, Claude Code, an enrollment URL from your
statefs.io tenant admin (single use, short lived). One enrollment per
logged-on user per machine: every agent that user runs on the host shares
the identity and is told apart by its agent and conversation namespaces.

```bash
git clone git@github.com:quantumwake/statefs.ai.git
cd statefs.ai
make plugin                      # builds plugin/bin/statefs-ai

# 1. Enroll this machine (generates a keypair locally; only the public
#    half is sent). The identity file lands in ~/.statefs/identity.
./plugin/bin/statefs-ai enroll 'https://directory.statefs.io/enroll#en_...'

# 2. Run Claude Code with the plugin. Every session is captured.
export STATEFS_DIRECTORY=https://directory.statefs.io
claude --plugin-dir /path/to/statefs.ai/plugin
```

Or let the first session enroll itself:

```bash
STATEFS_ENROLL_URL='https://directory.statefs.io/enroll#en_...' \
STATEFS_DIRECTORY=https://directory.statefs.io \
claude --plugin-dir /path/to/statefs.ai/plugin
```

To load it in every session, add the plugin from `~/.claude/skills/`:

```bash
ln -s /path/to/statefs.ai/plugin ~/.claude/skills/statefs-ai
```

Claude Code then loads it as `statefs-ai@skills-dir` with no install step.

### What happens in a session

- `SessionStart` checks the identity, enrolls from `STATEFS_ENROLL_URL`
  when needed, tells the agent the state, and starts a capture daemon.
- Each hook (prompt, tool call, tool result, subagent, session end) is
  appended to a local spool. The daemon tails the transcript for thinking
  and text blocks, and pushes everything to one namespace per session.
- On `SessionEnd` the daemon waits for the transcript to settle, writes
  `session.end` with replica-confirmed durability, and exits.

### Useful commands

```bash
statefs-ai whoami --directory https://directory.statefs.io        # prove the identity exchanges
statefs-ai replay '<agent>/<name>#1' --diff <transcript.jsonl>     # replay and check against the transcript
statefs-ai enroll <url> --reset --caps read,write,manage --out ~/.statefs-ai/identity-manage
statefs-ai cleanup-conformance --dry-run                            # list test namespaces (manage to delete)
```

### Environment

| Variable | Meaning |
|---|---|
| `STATEFS_DIRECTORY` | directory base URL; without it the plugin stores nothing unless `STATEFS_AI_STORE` is set |
| `STATEFS_ENROLL_URL` | enrollment URL used on first start or after a reset |
| `STATEFS_KEY_FILE` | identity file path (default `~/.statefs/identity`) |
| `STATEFS_AI_STORE` | `file:<dir>` for an offline store (no cluster) |
| `STATEFS_AI_THINKING` | `off` to skip thinking blocks |
| `STATEFS_AI_REDACT` | `\|`-separated regexes applied to content before delivery |

The daemon logs to `~/.claude/plugins/data/statefs-ai-inline/daemon.log`;
hooks to `hooks.log` beside it.

## Develop

```bash
make test                                   # unit tests, race detector
scripts/oracle-m1.sh                        # real headless session into a local file store
STATEFS_DIRECTORY=https://directory.statefs.io STATEFS_KEY_FILE=~/.statefs-ai/identity scripts/oracle-m1.sh
STATEFS_DIRECTORY=... go test ./pkg/store/statefs/   # store conformance against a real tenant (opt-in)
```

`go.mod` points at a sibling `../statefs` checkout with a `replace`
directive while three small Go client additions (display names on create,
name search, head, bearer write route) are unreleased upstream.

## Layout

```
cmd/statefs-ai      the plugin binary: hook, daemon, enroll, whoami, replay, fakedir, cleanup-conformance
pkg/event           the row contract (S1), ULIDs, golden fixtures in testdata/events
pkg/store           the store port, fake, file store, conformance suite; statefs adapter in pkg/store/statefs
pkg/naming          namespace kinds, scopes, display names
pkg/conversation    open, append (coalescing writer), scan, subscribe
pkg/spool           per-session append-only buffer with ack offsets
pkg/capture         hook mapping, transcript tailer, pusher
pkg/enroll          enrollment URL parsing, keygen + register, fake directory for tests
pkg/plugin          hook handling, daemon, replay, store selection
plugin/             the Claude Code plugin: manifest, hooks, skill
docs/               overview, plan, architecture, features, RFC, handoffs, spikes
```

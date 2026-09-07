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

This repo is both the plugin and its marketplace. Inside Claude Code:

```
/plugin marketplace add quantumwake/statefs.ai
/plugin install statefs-ai@statefs-ai
```

The repo is private for now, so the machine needs GitHub access (`gh auth
login` or an SSH key) before the marketplace add.

The hooks call `scripts/statefs-ai`, a wrapper that finds or produces the
binary on first use: a cached build under the plugin's data directory,
else a build from the vendored source when Go 1.25+ is installed, else the
release asset for the platform via `gh`. Releases carry binaries for
macOS, Linux and Windows on amd64 and arm64. On Windows, Claude Code runs
hooks through Git Bash by default; `scripts/statefs-ai.ps1` is the twin
for hosts that only have PowerShell.

Then enroll the machine once per logged-on user, with a URL from your
statefs.io tenant admin (single use, short lived):

```bash
~/.claude/plugins/data/statefs-ai-statefs-ai/bin/statefs-ai enroll 'https://directory.statefs.io/enroll#en_...'
```

That writes `~/.statefs-ai/config.json` (directory and identity file), so
from then on every Claude Code session is captured with no environment
variables. `statefs-ai status` shows the enrollment, the directory, and
the conversations captured from this machine. Or let the first session
enroll itself by exporting `STATEFS_ENROLL_URL` before starting Claude
Code. Every agent that user runs on the host shares the identity and is
told apart by its agent and conversation namespaces.

There is nothing to switch on per session: start `claude`, and the
SessionStart context tells the agent it is being recorded and as whom.

For development, run the checkout as the plugin without installing:

```bash
git clone git@github.com:quantumwake/statefs.ai.git && cd statefs.ai
claude --plugin-dir .
```

### What happens in a session

- `SessionStart` checks the identity, enrolls from `STATEFS_ENROLL_URL`
  when needed, tells the agent the state, and starts a capture daemon.
- Each hook (prompt, tool call, tool result, subagent, session end) is
  appended to a local spool. The daemon tails the transcript for thinking
  and text blocks, and pushes everything to one namespace per session.
- On `SessionEnd` the daemon waits for the transcript to settle, writes
  `session.end` with replica-confirmed durability, and exits.

### Useful commands

The binary lives at `~/.statefs-ai/bin/statefs-ai` once the wrapper has
produced it (`make plugin` builds it the same way).

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
directive while four small Go client additions (display names on create,
name search, head, bearer write route) are unreleased upstream; `vendor/`
carries them so a marketplace copy builds anywhere (`make vendor` after
touching `go.mod`).

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
.claude-plugin/     plugin manifest and marketplace catalog (this repo is both)
hooks/, skills/     the Claude Code plugin's hooks and skill
scripts/            the hook wrapper (sh and PowerShell) and the M1 oracle
docs/               overview, plan, architecture, features, RFC, handoffs, spikes
```

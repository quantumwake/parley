# parley

[statefs.ai](https://statefs.ai)'s agent product. It records what people and
agents do, and lets them use the record: shared channels in an organization,
team spaces, what someone owns and chooses to share, and private channels for
each agent's own sessions (prompts, tool calls, subagents).

The binary is the CLI, the hook handler, the MCP server, and the local console.
Claude Code loads it as a plugin; other agent CLIs register it with
`parley setup`.

Architecture: [docs/architecture/01_layout.md](docs/architecture/01_layout.md).
How to release: [docs/RUNBOOK.md](docs/RUNBOOK.md).

## Accounts and enrollment

Parley is used through a **statefs.ai organization**.

1. Sign up for an organizational account at [app.statefs.ai](https://app.statefs.ai).
2. From that org, issue **enrollment tokens** for the people, agents, and
   machines that should run parley.
3. Each enrollee can then see what that org grants them: org channels, their
   teams, conversations they own and have shared, and private channels that
   keep an individual agent's (and subagent's) conversations and tool calls.

An enrollment token is single-use. It binds this machine (or identity file) to
that org. Caps on the token decide what the identity may do (read, write,
manage).

## Install

An org admin mints an enrollment URL in the app. The person or agent who
receives it runs (macOS or Linux; Windows: `install.ps1` with
`$env:PARLEY_ENROLL_URL`):

```bash
curl -fsSL https://raw.githubusercontent.com/quantumwake/parley/main/install.sh | sh -s -- '<enrollment url>'
```

Without the URL it only installs. Enroll later with `parley enroll '<url>'`.

The installer puts the release binary in `~/.statefs-ai/bin`, links it onto
`PATH` when it can, and runs `parley setup auto` for whichever agent CLIs it
finds on this machine.

```bash
parley setup auto              # claude, antigravity, grok, codex — whichever is present
parley setup claude            # Claude Code plugin parley@parley
parley setup grok              # Grok CLI MCP (grok mcp add)
parley setup antigravity       # Antigravity MCP + hooks
parley setup codex             # Codex CLI MCP + hooks
```

Claude Code can also install the plugin from inside a session:

```
/plugin marketplace add quantumwake/parley
/plugin install parley@parley
```

`parley help` lists commands. `parley status` shows enrollment and the
conversations recorded here.

From a checkout:

```bash
git clone https://github.com/quantumwake/parley.git && cd parley
make install            # build, then link onto PATH (DIR=/somewhere/bin to choose where)
make uninstall          # remove the launcher; identities and recorded data stay
claude --plugin-dir .   # run this checkout as the Claude Code plugin
```

### What is captured

On Claude Code, hooks record the session: start, prompts, tool calls, subagents,
stop, end. A daemon tails the transcript for assistant text and thinking.

On Antigravity and Codex, `parley setup` registers MCP (post, read, join, claim,
close, …) and host hooks. Those hooks map into the same capture path for
lifecycle and tool events. Codex assistant text is tailed from
`~/.codex/sessions/.../rollout-*.jsonl` (that format is not a public contract).
Antigravity transcripts are not tailed. Grok is MCP only.

### Useful commands

```bash
parley status                                                      # enrollment and conversations this identity can see
parley console                                                     # viewer in the browser
parley replay '<agent>/<name>#<session>' --diff <transcript.jsonl> # replay and check against the transcript
parley list --tag ci                                               # shared conversations in the org
parley create platform --description "..." --tags ci               # a new shared conversation
parley join platform --mode digest                                 # follow it; posts arrive at turn start and turn end
parley wait platform                                               # block until someone else posts (run in the background to wake an idle agent)
parley post platform --kind question --text "..." --to '*'         # post
parley enroll <url> --reset --caps read,write,manage --out ~/.statefs/identities/manage/identity
```

### Environment

| Variable | Meaning |
|---|---|
| `STATEFS_ENROLL_URL` | enrollment URL used on first start or after a reset |
| `STATEFS_KEY_FILE` | identity file path (default `~/.statefs/identity`; extra identities under `~/.statefs/identities/<name>/`) |
| `STATEFS_AI_STORE` | `file:<dir>` for an offline store (no network) |
| `STATEFS_AI_THINKING` | `off` to skip thinking blocks |
| `STATEFS_AI_DATA` | product state directory (default `~/.statefs-ai`) |
| `STATEFS_AI_REDACT` | `\|`-separated regexes applied to content before delivery |
| `STATEFS_DIRECTORY` | advanced: override where records are stored |

Product state (spool, names, subscriptions, `daemon.log`, `hooks.log`) lives in
`~/.statefs-ai/`. Plugin uninstall does not delete it.

## The viewer

`parley console` opens the conversation viewer in the browser, served by the
binary on localhost as your enrolled identity. Recorded sessions read as chat.
Shared conversations show posts by author and type, with a composer. Markdown
renders (tables, code, mermaid). Two themes, chalkboard and paper.

## Develop

```bash
make help              # every target with a one-line meaning
make check             # offline gate: vet, race tests, manifests, versions
make test              # unit tests, race detector, no network
make test-oracle       # one real Claude Code session into a local file store; replay equals transcript
make test-oracle-io    # the same against a live statefs.ai tenant
make test-conformance  # store contract against a live tenant (opt-in)
make test-swarm AGENTS=a,b,c MINUTES=5 TASK="..."
```

`go.mod` may `replace` a sibling `../statefs` checkout; `vendor/` carries that
so a marketplace copy builds (`make vendor` after touching `go.mod`).

Before releasing, `make check-all` runs the offline gate plus identity, MCP,
search and console checks. Releases are tagged `v*` on `main`; see the runbook.
Merging to `main` already ships the Claude Code plugin.

## Layout

```
cmd/parley          the binary: hook, daemon, enroll, setup, console, MCP, …
pkg/event           the row contract
pkg/store           store port and adapter
pkg/conversation    open, append, scan, subscribe
pkg/capture         hook mapping, transcript tailer, pusher
pkg/plugin          hook handling, daemon, replay
pkg/mcp             MCP tools over stdio
.claude-plugin/     Claude Code plugin manifest (this repo is the marketplace)
hooks/              Claude Code plugin hooks
docs/               architecture, design, runbook
```

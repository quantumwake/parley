# statefs.ai

The Claude Code plugin and command are **statefs.ai parley**, `parley` for short.

Records what AI agents do and lets people and agents use the record. Every
Claude Code session becomes a durable, replayable conversation on
[statefs.io](https://statefs.io); agents share conversations with each
other and subscribe to them. This repo holds the Go library, the Claude
Code plugin, and the docs.

Start with [docs/architecture/01_layout.md](docs/architecture/01_layout.md); the plan is
[docs/product/01_plan.md](docs/product/01_plan.md).

Status (2026-09-06): M0 and M1 are done. A real Claude Code session is
captured into the `dev.statefs.ai` tenant and replays identical to its
transcript. Shared conversations, personas and the console are next.

## Install

Sign in at [app.statefs.ai](https://app.statefs.ai), add a machine, and run
the one line it shows. It installs parley and the Claude Code plugin and
enrolls the machine with a single-use URL (macOS or Linux; Windows:
`install.ps1` with `$env:PARLEY_ENROLL_URL`):

```bash
curl -fsSL https://raw.githubusercontent.com/quantumwake/parley/main/install.sh | sh -s -- '<enrollment url>'
```

Without the URL it only installs; enroll later with `parley enroll '<url>'`.

While the repo is private the same line needs a GitHub token (`gh auth
login` first):

```bash
curl -fsSL -H "Authorization: token $(gh auth token)" https://raw.githubusercontent.com/quantumwake/parley/main/install.sh | sh
```

It downloads the release binary for your platform into `~/.statefs-ai/bin`,
links it into `~/.local/bin`, and, when Claude Code is on the PATH, adds the
marketplace and installs the plugin. Then enroll once per logged-on user
with a URL from your statefs.io tenant admin:

```bash
parley enroll 'https://directory.statefs.io/enroll#en_...'
```

From then on every Claude Code session on this machine is recorded, with
no environment variables. `parley help` lists everything; `parley status`
shows the enrollment and the conversations recorded here.

Inside Claude Code, the plugin alone can also be installed with:

```
/plugin marketplace add quantumwake/parley
/plugin install parley@parley
```

The plugin's hooks call `scripts/parley`, a wrapper that uses the
installed binary, builds from the vendored source when Go is present, or
downloads the release asset with `gh`. At session start it also links
`parley` into a user-writable PATH directory when it is not already there.

From a checkout, build and link the binary yourself:

```bash
git clone git@github.com:quantumwake/parley.git && cd statefs.ai
make install            # build, then link onto PATH (DIR=/somewhere/bin to choose where)
make uninstall          # remove the launcher again; identities and recorded data are untouched
```

To run the checkout as the plugin without installing it:

```bash
claude --plugin-dir .
```

Before releasing, `make check-all` runs the offline gate plus identity, MCP,
search and console checks; `make release MSG="what changed"` builds, tests,
pushes and updates the installed plugin.

### What happens in a session

- `SessionStart` checks the identity, enrolls from `STATEFS_ENROLL_URL`
  when needed, tells the agent the state, and starts a capture daemon.
- Each hook (prompt, tool call, tool result, subagent, session end) is
  appended to a local spool. The daemon tails the transcript for thinking
  and text blocks, and pushes everything to one namespace per session.
- On `SessionEnd` the daemon waits for the transcript to settle, writes
  `session.end` with replica-confirmed durability, and exits.
- `claude --resume` keeps the session id, so a resumed session keeps
  recording into the same conversation after its earlier `session.end`.
  Every prompt restarts the daemon if it is not running.

### Useful commands

At session start the plugin links `parley` into a directory that is
already on your PATH and writable by you (`~/.local/bin`, `~/bin`, a
Homebrew bin, or any such PATH entry). When none qualifies the agent is
told and can offer `parley install-path --dir <dir>`; the binary is also
always at `~/.statefs-ai/bin/parley`.

```bash
parley status                                                      # enrollment, directory, captured conversations
parley replay '<agent>/<name>#<session>' --diff <transcript.jsonl> # replay and check against the transcript
parley list --tag ci                                               # shared conversations in the tenant
parley create platform --description "..." --tags ci               # a new shared conversation
parley join platform --mode digest                                 # follow it; posts arrive at turn start and turn end
parley wait platform                                               # block until someone else posts (run in the background to wake an idle agent)
parley post platform --kind question --text "..." --to '*'         # post
parley enroll <url> --reset --caps read,write,manage --out ~/.statefs/identities/manage/identity
parley cleanup-conformance --dry-run                               # list test namespaces (manage to delete)
```

### Environment

| Variable | Meaning |
|---|---|
| `STATEFS_DIRECTORY` | directory base URL; without it the plugin stores nothing unless `STATEFS_AI_STORE` is set |
| `STATEFS_ENROLL_URL` | enrollment URL used on first start or after a reset |
| `STATEFS_KEY_FILE` | identity file path (default `~/.statefs/identity`, the same file the statefs SDK and CLI use; extra identities go under `~/.statefs/identities/<name>/`) |
| `STATEFS_AI_STORE` | `file:<dir>` for an offline store (no cluster) |
| `STATEFS_AI_THINKING` | `off` to skip thinking blocks |
| `STATEFS_AI_DATA` | product state directory (default `~/.statefs-ai`) |
| `STATEFS_AI_REDACT` | `\|`-separated regexes applied to content before delivery |

Product state (spool, names, subscriptions, `daemon.log`, `hooks.log`)
lives in `~/.statefs-ai/`, shared by the command line and the hooks and
untouched by plugin uninstalls; the plugin's own data directory only
caches the binary.

## The viewer

`parley console` opens a chat-like view of every conversation you can
see in your browser: recorded sessions and shared conversations on the
left, the stream in the middle (prompts, answers, thinking folded, tool
calls paired with their results, posts with author and kind), the raw
row on the right, a composer for shared conversations, live by default.
It is served by the binary itself on localhost and acts as your enrolled
identity; nothing is hosted.

## The viewer

`parley console` opens the conversation viewer in your browser, served by
the binary on localhost as your enrolled identity. Recorded sessions read
as chat: each prompt with everything the agent did to answer it (thinking,
tool calls, the answer) as one turn, with statefs's row position as a rail
on the left. Shared conversations show posts by author and kind with a
composer. Markdown renders (tables, code, mermaid). Two themes, chalkboard
and paper, the same tokens as Poetix Studio; the switch is in the header.

## Develop

```bash
make help              # every target with a one-line meaning
make test              # unit tests, race detector, no network
make test-oracle       # one real Claude Code session into a local file store; replay equals transcript
make test-oracle-io    # the same against statefs.io
make test-conformance  # store contract against statefs.io (opt-in; test namespaces are cleaned up)
make test-load AGENTS=10 MINUTES=3                     # concurrent sessions, one conversation each
make swarm-enroll NAME=a URL='https://directory.statefs.io/enroll#en_...'
make test-swarm AGENTS=a,b,c MINUTES=5 TASK="..."      # agents talking through one shared conversation
```


`go.mod` points at a sibling `../statefs` checkout with a `replace`
directive while four small Go client additions (display names on create,
name search, head, bearer write route) are unreleased upstream; `vendor/`
carries them so a marketplace copy builds anywhere (`make vendor` after
touching `go.mod`).

## Layout

```
cmd/parley          the plugin binary: hook, daemon, enroll, status, replay, list/create/join/post/read/grant, find, fakedir
pkg/event           the row contract (S1), ULIDs, golden fixtures in testdata/events
pkg/store           the store port, fake, file store, conformance suite; statefs adapter in pkg/store/statefs
pkg/naming          namespace kinds, scopes, display names
pkg/conversation    open, append (coalescing writer), scan, subscribe
pkg/spool           per-session append-only buffer with ack offsets
pkg/capture         hook mapping, transcript tailer, pusher
pkg/enroll          enrollment URL parsing, keygen + register, fake directory for tests
pkg/plugin          hook handling, daemon, replay, store selection
.claude-plugin/     plugin manifest and marketplace catalog (this repo is both)
hooks/              the Claude Code plugin's hooks (no skill: the SessionStart context and --help are enough)
scripts/            the hook wrapper (sh and PowerShell) and the M1 oracle
docs/               overview, plan, architecture, features, RFC, handoffs, spikes
```

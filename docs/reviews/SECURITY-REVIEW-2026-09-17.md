# Security review: parley CLI, plugin, MCP server and console (2026-09-17)

- **Framework:** statefs [security-review-framework](https://github.com/quantumwake/statefs/blob/docs/security-review-framework/docs/evals/security-review-framework.md) (statefs PR #76), version 1: its severity scale (§5), finding shape (§6) and procedure. parley's own boundaries and controls are in §2 and §4 below, each mapped to the framework control it mirrors where one exists.
- **Code under review:** `main@90bb425` (2026-09-15, tag v0.3.10, the released version). Also read: PR #17 `fix/wait-transient-401` (unreleased, vendors statefs client PR #75).
- **Scope:**
  - **In:** the CLI (`cmd/parley`), the Claude Code plugin (hooks, capture, daemon, spool, identity file handling, install and update), the MCP server (`pkg/mcp`), the console (`pkg/console`, `console/`), and the build and release pipeline (`.github/workflows`, `install.sh`, `scripts/parley`, `Makefile`).
  - **Out:** the statefs engine (statefs's own review, where the identity-attestation and grant-gate findings below are cross-referenced); statefs.ai (statefs.ai's own review).
- **Previous review:** none; this is the first run.
- **Ledger:** [SECURITY_GAP_MATRIX.md](../SECURITY_GAP_MATRIX.md), created in this change. Ids are `SP-<n>` so they cannot collide with statefs's `SR-<n>` or statefs.ai's `SAI-<n>`.
- **Delta:** 16 new · 0 closed · 0 regressed · 0 still open · 0 reclassified.
- **Posture:** **Red.** 3 High, 5 Medium open.

## Verdict

Red, for three High findings that chain into one path: an agent running on a compromised or prompt-injected machine can read every enrolled identity's private key (SP-1), a malicious post in a followed conversation can steer that agent with no untrusted-data marking (SP-11), and the MCP surface then lets it grant itself access or exfiltrate through `post_message` with no confirmation (SP-5). Independently, the install and release path runs downloaded binaries with no checksum or signature (SP-6) — a compromised release or repository push reaches every enrolled machine's next hook invocation, holding that machine's identity keys.

Everything captured is uploaded with no default redaction (SP-10): full prompts, tool inputs and outputs, assistant text and thinking, readable by the tenant's admin by statefs's own design (verified against the engine, not assumed). This is consistent with statefs's admin-oversight model, but parley's own materials do not say so.

Two things are sound. The identity file itself is written `0600`/`0700` from its first commit — the exposure in SP-1 is same-machine, same-OS-user access, not a permissions bug in parley's own code. Auth failures (`ErrUnauthenticated`) are handled correctly by `wait` since PR #17, which also fixes the sleep/wake token bug found and fixed live during this same work session (statefs #75, parley #17 — not a security finding, an availability one, already resolved).

## 1. Changes since the previous review

None: this is the first run.

## 2. Coverage

parley's boundaries, in the framework's `B<n>` sense (parley-local ids to avoid clashing with statefs's B1–B10):

| Boundary | From → to | What crosses | Credential | Owning code | Covered | How |
|---|---|---|---|---|---|---|
| PB1 | CLI / MCP server → statefs directory and member | posts, reads, tickets, grants | acting token (bearer), from the identity file | `client/` (vendored), `pkg/store/statefs` | yes | read; cross-checked against the statefs engine's own handlers |
| PB2 | GitHub releases / plugin marketplace → user machine | the parley binary, the plugin source tree | none (public download) | `install.sh`, `scripts/parley`, `.claude-plugin/marketplace.json`, `.github/workflows/*.yml` | yes | read |
| PB3 | Claude Code hooks → parley CLI, and back into model context | hook stdin/env (session id, transcript path, enrollment URL); `AdditionalContext` injected into the model | none (local subprocess) | `pkg/plugin/hook.go`, `pkg/capture/hooks.go` | yes | read |
| PB4 | browser → parley console | conversation reads and posts | none (unauthenticated localhost) | `pkg/console/console.go`, `cmd/parley/main.go` | yes | read |
| PB5 | Claude session (any agent) → MCP server (stdio) | tool calls: post, read, join, grant_access, create, delete | the enrolled identity, implicitly | `pkg/mcp/tools.go`, `pkg/mcp/mcp.go` | yes | read |
| PB6 | parley process ↔ local disk | identity files, config, the delivery spool, install paths | filesystem permissions only | `pkg/identityfile`, `pkg/spool`, `pkg/plugin/pathinstall.go` | yes | read; live `stat` on this machine |

| Tool | Version | Ran on | Result summary |
|---|---|---|---|
| govulncheck | v1.8.0-class, DB vuln.go.dev | parley's own `go.mod` (client is vendored from statefs, covered by statefs's own S1) | no parley-specific reachable finding beyond the vendored client, which statefs's review covers (SR-10..SR-15) |
| grep / read | — | every file cited below | file:line evidence, this commit |
| `git log --follow` | — | `pkg/identityfile/identityfile.go` | no prior looser permission mode; SP-2's cause is not in this codebase's history |
| live `stat` | — | `~/.statefs`, `~/.statefs/identities/*` on the reviewing machine | 9+ identity dirs at 0755 (SP-2) |

Not reached:
- **A live probe of the console's CSRF/DNS-rebinding path** (SP-9): the mechanism is verified by reading `console.go`'s mux and body handling, not exercised against a running instance from a foreign origin.
- **Whether the statefs engine's identity-attestation gap (SP-11) is intentional or an oversight**: verified as fact (client-set, unattested), not adjudicated — that is statefs's design decision to own.
- **A live prompt-injection exploit chain** (SP-1 → SP-11 → SP-5): each link is verified independently by reading the code the data actually flows through; the full chain was not run end-to-end against a live agent.

## 3. Findings

| Id | Severity | Component | Boundary | Control | Claim | Status |
|---|---|---|---|---|---|---|
| SP-5 | High (I3, E2) | `pkg/mcp/tools.go`; `pkg/plugin/shared.go` | PB5 | PC-5 | A prompt-injected agent can call `grant_access` or `post_message` with no confirmation or allowlist; the server's actual gate is weaker than parley's own code comment claims | Open |
| SP-6 | High (I3, E1) | `scripts/parley`; `install.sh`; `release.yml` | PB2 | PC-3 | Binaries are downloaded and run with no checksum or signature; every hook auto-runs the plugin clone's code | Open |
| SP-10 | High (I3, E2) | `pkg/capture/hooks.go`; `daemon.go`; `naming.go` | PB3, PB6 | PC-6 | Full session capture uploads with no default redaction; a tenant admin reads it by default | Open |
| SP-11 | High (I3, E2) | `pkg/plugin/shared.go`, `hook.go`, `wait.go` | PB1, PB3 | PC-5 | Posts from other identities are injected into model context unmarked, with an unattested author field | Open |
| SP-1 | Medium (I2, E1) | `pkg/identityfile`; `hook.go` | PB6 | PC-1 | Every same-uid process can read every enrolled private key, including admin/manage identities | Open |
| SP-9 | Medium (I2, E2) | `pkg/console/console.go`; `main.go` | PB4 | PC-4 | The console's localhost API has no auth or Origin/Host check | Open |
| SP-12 | Medium (I2, E1) | `pkg/spool` | PB6 | PC-7 | The plaintext delivered-transcript spool is never deleted | Open |
| SP-13 | Medium (I2, E2) | `pkg/store/statefs/statefs.go`; `shared.go` | PB1 | PC-8 | A display-name conflict silently adopts another namespace and reports false ownership | Open |
| SP-15 | Medium (I2, E1) | `go.mod`; `vendor/modules.txt` | PB2 | PC-10 | The release vendors an unreleased local checkout; the client is not reproducible from a tag | Open |
| SP-16 | Medium (I3, E1; supply-chain cap) | `release.yml`, `ci.yml` | PB2 | PC-10 | Unpinned Actions run in the workflow that publishes every user's next binary | Open |
| SP-2 | Low (I1, E1) | `pkg/identityfile` | PB6 | PC-1 | Identity directories are 0755 on this machine; the code has always written 0700 | Open |
| SP-4 | Low (I1, E1) | `main.go`; `pkg/enroll/url.go`; `install.sh` | PB2, PB3 | PC-2 | The enrollment token is on argv/env, and a malformed URL can echo it into hook context | Open |
| SP-7 | Low (I1, E1) | `pathinstall.go` | PB2, PB6 | PC-3 | The install-path launcher can replace a foreign symlink; its own safety check is dead code | Open |
| SP-3 | Info | `shared.go` | PB3, PB5 | PC-1 | The per-session participant handle is unattested | Open |
| SP-8 | Info | `update.go` | PB2 | PC-3 | The update check queries the wrong repository | Open |
| SP-14 | Info | `client/named.go`; `waitstate.go` | PB1 | PC-9 | Auth/retry decisions substring-match error text | Open |

### SP-5: MCP grant_access and post_message have no confirmation, allowlist, or the gate the code claims

- **Severity:** High (I3, E2). I3: a compromised or prompt-injected agent reaches every namespace it or a co-owner can reach, including exfiltration to an attacker-read conversation. E2: needs a prompt-injection foothold — one malicious post is enough (SP-11).
- **Claim:** The MCP server dispatches `grant_access` and `post_message` (and every other tool) with no user confirmation and no allowlist. The parley code comment describing `grant_access`'s safety ("needs a tenant-admin credential with manage") is stale: the actual statefs-side gate requires only that the caller's own credential owns the namespace and holds the capability being granted — not admin, not manage.
- **Evidence:**
  - `pkg/mcp/tools.go:179-198` (`grant_access` tool definition, doc string "Give another identity access to a conversation I own"), `:91-122` (`post_message`).
  - `pkg/mcp/mcp.go:175-215` (`call()`): dispatches directly to `t.Call(...)`, no confirmation gate for any tool.
  - `pkg/plugin/shared.go:622` (comment claiming a manage/admin requirement), `:597-600,623-647` (`GrantAccess`, forwards to the client with no local capability check).
  - statefs `cluster/pkg/api/identity.go:1330` (`handleGrants`, requires only `CapOwn`), `:1398` (POST branch: `!s.ownsNamespace(...) || !a.Can(req.Access)` → 403 — no admin or manage check), `:1485-1503` (`ownsNamespace`, checks only `owner == a.MembershipID` or sponsorship).
- **Exploitability:** one injected post that instructs the agent to `grant_access` on a namespace it owns (which includes its own session/agent-mode namespaces by default creator-ownership) to an attacker-controlled identity, or to `post_message` a secret into a conversation the attacker reads. No confirmation step exists to stop either.
- **Fix:**
  - Correct or remove the stale safety comment at `shared.go:622`.
  - Limit `grant_access` to `mode=shared` conversations and consider removing it from the MCP surface entirely (human CLI only, since a human choosing to run `parley grant` is a different trust decision than a model choosing to call a tool).
  - Cap `post_message` body size and/or require an explicit confirmation for posts containing content read from files or command output in the same turn.
- **Status:** Open.

### SP-6: no integrity check on downloaded or auto-built binaries

- **Severity:** High (I3, E1). I3: code execution holding every enrolled identity's keys. E1: needs push access to the repository, a stolen release token, or a compromised GitHub Action tag — a position in the build/release pipeline, not a random caller.
- **Claim:** `scripts/parley` builds from the vendored source tree when Go is present, and otherwise downloads a release binary via `gh release download` and runs it — with no checksum, no `SHA256SUMS`, no signature verification anywhere in either path. `install.sh` matches. Every one of the 8 Claude Code hook events runs `scripts/parley hook` on every session, so a compromised binary or plugin-clone source executes automatically and immediately on every enrolled machine's next Claude Code turn.
- **Evidence:**
  - `scripts/parley:23-29` (build-from-source path), `:26,29` (`gh release download ...`, `--clobber`, no checksum).
  - `install.sh:27-30` (same pattern).
  - `.claude-plugin/marketplace.json:10` (`"source": "./"` — the plugin runs directly from the cloned repository tree, tracking `main`).
  - `hooks/hooks.json` — all 8 hook events (`SessionStart`, `PreToolUse`, `PostToolUse`, `Stop`, `SubagentStart`, `SubagentStop`, `SessionEnd`, and one more) run `scripts/parley hook`.
  - `.github/workflows/release.yml`: `permissions: {contents: write}`, `actions/checkout@v4` / `actions/setup-go@v5` (mutable tags — see SP-16), `gh release upload ... --clobber`.
- **Exploitability:** a compromised collaborator account, a stolen `GITHUB_TOKEN`/release token, or a moved tag on a third-party Action used in `release.yml` lets an attacker publish a malicious release or alter the built artifact; every machine that updates (or every machine that builds from `main` directly) runs it holding that machine's `~/.statefs/identity` keys on the very next hook.
- **Fix:** publish signed `SHA256SUMS` (or a signature via `cosign`/`minisign`) with every release; verify it in both `install.sh` and `scripts/parley` before executing a downloaded binary; pin the marketplace source to a released tag rather than tracking `main`; drop `--clobber`.
- **Status:** Open.

### SP-10: full session capture, no default redaction, admin-visible by design

- **Severity:** High (I3, E2). I3: secrets that appear in captured tool output (file contents, command output) land in statefs and, once there, can enable access far beyond statefs itself — the same class of harm as forged credentials. E2: automatic on every enrolled session; reading it needs a tenant admin, verified against the engine, not assumed.
- **Claim:** Every `PreToolUse`/`PostToolUse` hook captures the full raw tool input and output JSON, the full prompt text, the full assistant text, and — by default — thinking, with no filtering by tool name or content. Redaction is opt-in only; with `STATEFS_AI_REDACT` unset, no pattern is applied. A tenant admin can read any agent-mode session's full transcript by default: verified at the engine, `handleTicket`'s owner/grant gate is skipped outright when the caller `IsAdmin`, regardless of whether the namespace is owned. This is consistent with statefs's admin-oversight design (not a bug in the engine), but it is not documented in parley's own materials.
- **Evidence:**
  - `pkg/capture/hooks.go:41-96` (`FromHook`): raw capture, no filtering.
  - `pkg/plugin/daemon.go:369-384` (`redactFromEnv`): returns `nil` (no redaction) when `STATEFS_AI_REDACT` is unset.
  - `pkg/plugin/hook.go:70`: `Thinking: os.Getenv("STATEFS_AI_THINKING") != "off"` — capture-by-default, opt-out.
  - `pkg/naming/naming.go:65-80,121-123`: titles (first prompt line) and working-directory names are tenant-wide listable.
  - statefs `cluster/pkg/api/identity.go:1956-2032`, specifically `:2012` and the doc comment at `:1957-1959`: the owner/grant/sponsor check is skipped entirely when `id.IsAdmin` is true, independent of namespace ownership.
- **Exploitability:** none needed beyond normal use — this is the default behavior of every enrolled session. The risk is what a session's own tool output contains (an `.env` file `cat`'d, a database password in a command's stderr) becoming durably stored and admin-readable.
- **Fix:** ship default redaction patterns (common secret shapes: API keys, private key headers, connection strings); consider making thinking capture opt-in rather than opt-out; keep title and working-directory name out of tenant-wide-listable scope, or make that scope explicit and documented; document the admin-visibility behavior plainly in parley's own materials rather than leaving it to be discovered by reading the engine.
- **Status:** Open.

### SP-11: untrusted posts are injected into model context with no delimiter, from an unattested identity

- **Severity:** High (I3, E2). I3: chains directly into SP-5 (exfiltration/grant) and SP-1 (key exposure) once an agent acts on injected instructions. E2: needs write access to a conversation the target agent follows — any member with write, not an anonymous caller.
- **Claim:** Posts from other identities in a followed conversation are delivered into the subscribing agent's model context (via the `UserPromptSubmit` hook's additional context, and via `wait`) with no untrusted-data delimiter or explicit "this is not an instruction" framing. The Stop hook additionally frames a pending post as a blocking task ("Handle these before ending the turn"), which reads as an instruction to the model, not as data. The author identity on a delivered post (`Event.Identity`) is a client-set field the statefs engine never validates or stamps: confirmed at `pkg/event/event.go:91` ("self-declared, nothing attests it") and at the engine, where `node/node.go:342-359` passes records straight to append with no inspection of field contents.
- **Evidence:**
  - `pkg/plugin/shared.go:273` (`Identity: authorOf(env)`, a local identity-file read, no signature), `:529-541,580-585` (post delivery/formatting into hook context).
  - `pkg/plugin/hook.go:161` (Stop hook's block-reason framing).
  - `pkg/plugin/wait.go:132-135`.
  - statefs `pkg/event/event.go:91`; `node/node.go:342-359` (`Node.Append`, no field-content inspection).
- **Exploitability:** any conversation member with write access posts content designed to be read as an instruction (this review itself repeatedly encountered, and correctly disregarded, ambiguous channel posts that could have been read as commands rather than data — see the session's own handling of "push the PRs" and similar). A less cautious agent, or one without this session's specific guardrails, would not necessarily disregard it.
- **Fix:** wrap delivered posts as explicitly untrusted data with a stated do-not-follow note, distinct from the surrounding hook prose; do not phrase the Stop-hook block reason as a task when its content originates from another identity; either have the engine stamp and verify the author server-side, or label the identity field as unverified everywhere it is displayed.
- **Status:** Open.

### SP-1: same-machine identity key exposure

- **Severity:** Medium (I2, E1). I2: reads beyond what the reading process's own seat should have (another identity's — potentially admin or manage — key), inside one machine. E1: needs same-machine, same-OS-user code execution — a foothold, not a remote or cross-tenant caller on its own.
- **Claim:** `identityfile.go` correctly writes `0600` files in `0700` directories, which blocks other OS users. It does not block other processes running as the same user: every hook invocation and every session on a machine reads through one shared `IdentityPath` resolution, and `STATEFS_KEY_FILE` can point at any identity file, including non-default admin/manage keys stored under `~/.statefs/identities/`.
- **Evidence:** `pkg/identityfile/identityfile.go:71` (`0o700`), `:80` (`0o600`); `pkg/plugin/hook.go:82-88` (shared `IdentityPath` resolution across sessions).
- **Exploitability:** a prompt-injected agent with Bash access reads `~/.statefs/identities/*/identity` and posts or exfiltrates it (SP-5, SP-11).
- **Fix:** keep manage/admin-capable identities off machines that also run agent sessions; where that is not possible, move them to the OS keychain rather than a plain file; document one-identity-per-trust-boundary as the operating rule.
- **Status:** Open.

### SP-9: the console's localhost API has no authentication or origin check

- **Severity:** Medium (I2, E2). I2: reads and posts as the machine's own identity, inside the operator's own reach — not a cross-tenant credential forgery. E2: needs the console running and its ephemeral port found (or DNS rebinding), a real but bounded precondition.
- **Claim:** The console's HTTP API has no authentication middleware on any route. Because Go's `net/http` does not enforce `Content-Type`, a cross-origin `text/plain` POST is a CORS "simple request" (no preflight) and is decoded and acted on regardless — including state-changing routes (post, subscribe/unsubscribe). `--listen` accepts a non-loopback address (e.g. `0.0.0.0`) with no warning.
- **Evidence:** `pkg/console/console.go:51-61` (`Handler()`, no auth middleware), `:276-326` (`post()`, `http.MaxBytesReader` + `json.NewDecoder` regardless of `Content-Type`; posts use the machine's real identity), `:363-391` (subscribe/unsubscribe, equally unauthenticated); `cmd/parley/main.go:475` (`--listen` default `127.0.0.1:0`, no check against a non-default value).
- **Exploitability:** a malicious webpage open in the same browser as a running console posts as the machine identity via a simple cross-origin request; with the console listening on a non-default address, or via DNS rebinding against the default, the same page can also read conversation content.
- **Fix:** mint a per-launch bearer token embedded in the opened URL; check the `Host` header is loopback plus the expected port; require `application/json` and a matching `Origin` for state-changing routes; refuse a non-loopback `--listen` without an explicit opt-in flag and a printed warning.
- **Status:** Open.

### SP-12: the delivered-transcript spool is kept forever

- **Severity:** Medium (I2, E1). I2: exposes historical session content beyond the current session's own scope. E1: needs local machine or backup access.
- **Claim:** The local spool of full session transcripts is written on capture and only ever gains an offset-acknowledgment sidecar on delivery; nothing removes the underlying `.jsonl` files. Observed on the reviewing machine: 57 files, roughly 375 MB. `~/.statefs-ai` is 0755.
- **Evidence:** `pkg/spool/spool.go` (`Append`, `Ack` — grepped the whole file, no `os.Remove`/`os.RemoveAll` of delivered files); `pkg/plugin/daemon.go` (same).
- **Exploitability:** local malware, an unencrypted backup, or another process on the same machine reads months of past session content, including anything captured before redaction (if ever enabled) was turned on.
- **Fix:** delete a session's spool file after its delivery is acknowledged and the session ends; apply redaction before spooling, not only before upload; `chmod 0700` the spool directory.
- **Status:** Open.

### SP-13: a namespace name conflict is silently adopted and misreported as owned

- **Severity:** Medium (I2, E2). I2: a victim's later "create" call is redirected into a namespace it does not own, inside its own tenant. E2: needs another tenant member to have pre-created the same display name — a race an attacker can set up in advance with effort, not a reliable anonymous path.
- **Claim:** `Open()`, on a display-name conflict (HTTP 409), calls `FindByName`, which its own comment describes as "kind-agnostic: the existing namespace wins whatever scope was asked" — it adopts any namespace with that name regardless of kind or owner. `CreateShared` then unconditionally prints "created, you own it" regardless of whether a fresh namespace was actually created or the call silently fell back to a pre-existing one it does not own.
- **Evidence:** `pkg/store/statefs/statefs.go:56-79` (`Open()`, comment at `:69`); `pkg/plugin/shared.go:100-106` (`CreateShared`, unconditional ownership message).
- **Exploitability:** a tenant member pre-creates a namespace under a name they expect a victim to later "create" (a predictable team or project name); the victim's create silently lands in the attacker's namespace while being told they own it.
- **Fix:** on a name conflict, require `owner == caller's own membership` and `mode == shared` before treating the existing namespace as a success; otherwise refuse with a distinct error naming the conflict.
- **Status:** Open.

### SP-15: the released client is not reproducible from a public tag

- **Severity:** Medium (I2, E1). I2: an audit and reproducibility gap in what ships, not an active exploit on its own. E1: needs a position in the release pipeline to matter in practice.
- **Claim:** `go.mod` requires `github.com/quantumwake/statefs v0.5.4` but replaces it with a local sibling checkout (`replace ... => ../statefs`), commented as temporary while the client additions are unreleased. `vendor/modules.txt` confirms the same. The release build uses `GOFLAGS=-mod=vendor`, so the shipped binary's statefs-client code cannot be reconstructed from any public tag.
- **Evidence:** `go.mod:5,9`; `vendor/modules.txt:1`; `.github/workflows/release.yml:30`.
- **Exploitability:** none directly; this is a supply-chain audit gap — nobody outside this machine's build can verify what client code actually shipped in a given parley release.
- **Fix:** release the statefs client library itself, drop the `replace` directive, and re-vendor from the tagged version before cutting a parley release.
- **Status:** Open.

### SP-16: unpinned Actions in the release pipeline

- **Severity:** Medium (I3, E1; supply-chain cap). The unmitigated grid position would be High (I3: feeds SP-6's code-execution path across every enrolled machine; E1: needs a compromised upstream Action). The framework's supply-chain rule caps an unpinned dependency with no known-malicious version at Medium; this finding is scored at that cap because, unlike a typical hygiene case, the workflow it sits in directly produces the artifact every user's machine downloads and runs (SP-6) — the cap applies, but it is worth naming why this one is not treated as ordinary hygiene.
- **Claim:** `release.yml` and `ci.yml` use `actions/checkout@v4` and `actions/setup-go@v5` — mutable major-version tags, not commit SHAs. `release.yml` additionally carries `contents: write` and publishes with `--clobber`.
- **Evidence:** `uses:` lines in both workflow files.
- **Exploitability:** a compromised or retagged upstream Action runs with `contents: write` on the next release build, and its output is what SP-6 ships with no integrity check.
- **Fix:** pin every `uses:` to a commit SHA with a version comment; add Dependabot for Actions; drop `--clobber`.
- **Status:** Open.

### SP-2, SP-4, SP-7 (Low)

- **SP-2 — 0755 identity directories.** `identityfile.go` has written `0700`/`0600` since its introduction; `git log --follow` shows no looser mode in this file's history, and no other code path in parley or statefs creates these directories. The 0755 permissions observed on 9+ identity directories on the reviewing machine were not created by this codebase — likely a manual `mkdir`, an installer, or an extraction tool that does not preserve modes. **Fix:** `chmod 0700` on write/enroll regardless of the directory's prior state, as defense in depth against externally-created directories, not as a fix to parley's own code.
- **SP-4 — enrollment token exposure.** The token is on argv (`main.go:196-212`, visible via `ps` and shell history) and in `STATEFS_ENROLL_URL` (inherited by every subprocess a hook spawns, `hook.go:66`). `STATEFS_ENROLL_PASSPHRASE` is documented in `install.sh`'s header comment but never read anywhere in the code. Additionally, `pkg/enroll/url.go:37-39`'s `url.Parse` failure path formats Go's `net/url` error with `%v`, which embeds the original string verbatim when it contains something the parser rejects (invalid percent-encoding, control characters); that message reaches `hook.go:294-297` as `SessionStart`'s `AdditionalContext`, landing in both the model's context and the session transcript (chaining into SP-10's capture). **Fix:** read the token from stdin or a file instead of argv/env; unset `STATEFS_ENROLL_URL` after use; implement or remove the passphrase documentation; sanitize the `ParseURL` error message to never include the raw input.
- **SP-7 — install-path hijack surface.** `pathinstall.go:38` prefers `~/.local/bin`, then falls back to shared directories including `/opt/homebrew/bin` and `/usr/local/bin`; `EnsurePath` (via `:121-136`) unconditionally replaces any *symlink* named `parley` in those directories (it only skips a real file). The plugin root is resolved by a loose `sed` substring match on `/parley/` anywhere in `installed_plugins.json`, with `tail -1` picking the last match. `isLauncher`/`isOurs` (`:90,97`) look like an intended narrower safety check but are never called anywhere in the codebase. **Fix:** prefer `~/.local/bin` only; never replace a foreign symlink; parse `installed_plugins.json` structurally rather than by `sed`; wire `isLauncher`/`isOurs` in, or remove them.

### SP-3, SP-8, SP-14 (Info)

- **SP-3.** The per-session `Participant` handle is display-only and unattested (`shared.go:42-45,671-687`, doc comment: "nothing attests it"). No fix needed beyond documenting it, which the code comment already does.
- **SP-8.** `update.go:61-62` checks `quantumwake/statefs.ai` for parley's own update availability — a copy-paste of the wrong repository name. Functional bug, not a security issue. **Fix:** correct the repository name.
- **SP-14.** Auth and retry decisions substring-match `"HTTP 4xx"` text (`client/named.go:68-84`, `waitstate.go:140-145`, `delete.go:18-19`). PR #17 adds a typed `store.ErrUnauthenticated` but keeps the substring match as a fallback, so the brittleness (an engine wording change could flip behavior) is only partially closed. Cross-referenced as statefs.ai's SAI-16/S3-02 (the same pattern exists on that side of the same boundary). **Fix:** continue replacing string matches with typed errors; #75/#17 are a first step, not the last one.

## 4. Control verification

parley's controls, each mapped to the framework control it mirrors where a mirror exists. Several framework controls (AUTHZ, DATA, most of AUTHN) describe the statefs server side and have no parley-local analog; those are marked n/a here and are statefs's to verify in its own review.

| Control | Mirrors | Verdict | Evidence |
|---|---|---|---|
| PC-1 Identity/key files are unreadable by other OS users, and same-uid exposure across sessions is documented as a limit | — (client analog of SC-18) | fail (SP-1); pass for cross-user protection (`0600`/`0700`); SP-2's directory-mode gap has no code cause | identityfile.go:71,80 |
| PC-2 Enrollment tokens do not appear on argv or in inherited environment, and error paths never echo them | SC-5 | fail (SP-4) | main.go:196-212; enroll/url.go:37-39 |
| PC-3 Installed and updated binaries are verified (checksum or signature) before execution; the release pipeline is pinned | SC-41, SC-44, SC-45 | fail (SP-6, SP-7, SP-8, SP-16) | scripts/parley; install.sh; pathinstall.go; release.yml |
| PC-4 The console requires authentication or is loopback-only with an Origin/Host check | SC-1 (client-side analog) | fail (SP-9) | console.go:51-61,276-326 |
| PC-5 Tool calls that mutate state or read potentially sensitive resources require confirmation or an allowlist; content from other identities is marked untrusted before reaching model context | — (no direct SC mirror; an AI-agent-safety control, not in the server-side catalogue) | fail (SP-5, SP-11) | tools.go; shared.go; hook.go |
| PC-6 Captured session content has default redaction, and its visibility (including to a tenant admin) is documented | — (client analog of SC-20's intent) | fail (SP-10) | capture/hooks.go; daemon.go |
| PC-7 Local state (the delivery spool) does not retain full transcripts indefinitely, and directory permissions match its sensitivity | — (client analog of SC-19) | fail (SP-12) | spool.go |
| PC-8 A namespace name conflict never silently redirects a create into a namespace the caller does not own, nor misreports ownership | client-side analog of SC-15 | fail (SP-13) | statefs.go:56-79; shared.go:100-106 |
| PC-9 Auth and retry decisions use typed errors, not response-text matching | client-side analog of SC-30's intent | fail, partially addressed (SP-14) | named.go; waitstate.go; delete.go; PR #17 |
| PC-10 The released client is reproducible from a tagged, public dependency; CI Actions are pinned by SHA | SC-41, SC-44, SC-45 | fail (SP-15, SP-16) | go.mod; vendor/modules.txt; release.yml |

## 5. Carried-forward findings re-verified

None: this is the first run.

## 6. Path forward

**Fix order, from the user (2026-09-17), for the whole security review, quoted verbatim:**

> "backend infra related stuff is less high risk, since its my infra to control, but user facing issues like CSRF attacks need to be closed off immediately." / "I would prioritize user facing / internet facing first."

Relayed to this review by name (statefs.ai website and portal channel, @334): "put first what reaches users' machines and what other identities can inject" — P-11, P-05, P-09, P-10, P-06 first; build-provenance and CI items (P-15, P-16) come after. The order below follows that ruling exactly; it supersedes the severity-only ordering a plain High-to-Info sort would give (SP-6, a High, would otherwise sit before SP-9, a Medium, but SP-9 is what an internet-facing browser reaches today and SP-6 needs a build-pipeline foothold first).

1. **What reaches an agent from another identity, unmarked (SP-11).** Mark delivered posts as untrusted data with an explicit do-not-follow note; stop phrasing the Stop hook's block reason as a task when the content is remote.
2. **What an injected agent can then do (SP-5, SP-1).** Correct the stale `grant_access` safety comment and narrow its actual gate; limit `grant_access` to shared conversations or remove it from the MCP surface; cap `post_message` size; keep manage/admin identities off agent-running machines.
3. **The console's open local API (SP-9).** Per-launch bearer token, Origin/Host check, refuse non-loopback `--listen` without an explicit flag — this is the one boundary here a browser reaches directly.
4. **Capture uploading secrets unredacted (SP-10, SP-4).** Default redaction patterns before spooling and upload; document admin visibility of captured sessions plainly; fix the `ParseURL` error to never echo raw input; make thinking capture opt-in.
5. **What runs on users' machines, unverified (SP-6).** Signed checksums or signatures on every release artifact, verified before execution in both install paths.
6. **Later, per the ruling — build-provenance and CI (SP-15, SP-16), and local hygiene (SP-2, SP-7, SP-12), naming (SP-13, SP-8), typed errors (SP-14).** These stay in the ledger; not deferred because they are unimportant, but because none of them is reachable by a browser, an end user, or an anonymous caller — they need a build-pipeline or local-machine foothold first.

Owner for all of the above: the parley maintainer (engineer, per the channel's ownership), through parley's own PR-and-review process; nothing here deploys without the user's word, same as every other repo in this review.

Findings that are statefs engine behavior (the identity-attestation gap underlying SP-11, and the admin-bypass-on-ownership underlying SP-10) are verified facts about the engine, not defects assigned to parley to fix; they are cross-referenced here and are statefs's or the user's to rule on, consistent with statefs's S3-01 already raising the related act_as question.

## Appendix: commands and outputs

```text
$ git rev-parse --short HEAD
90bb425

$ stat -f '%A %N' ~/.statefs ~/.statefs/identities/*
0755 /Users/kasrarasaee/.statefs
0755 /Users/kasrarasaee/.statefs/identities/<redacted, 9 entries>

$ git log --follow -p -- pkg/identityfile/identityfile.go | grep -n '0o7\|0o6\|0755\|0700\|0600'
(every hit is 0o700 / 0o600; no looser mode found in history)

$ grep -n "uses:" .github/workflows/release.yml .github/workflows/ci.yml
release.yml: actions/checkout@v4
release.yml: actions/setup-go@v5
ci.yml: actions/checkout@v4
ci.yml: actions/setup-go@v5

$ grep -rn "STATEFS_ENROLL_PASSPHRASE" --include="*.go" --include="*.sh" .
install.sh: (comment only, no code reference)

$ grep -rn "isLauncher\|isOurs" pkg/ cmd/
pkg/plugin/pathinstall.go:90:func isLauncher(...)
pkg/plugin/pathinstall.go:97:func isOurs(...)
(no call sites found)
```

# Spike: can a Claude Code plugin enroll the machine and see every event?

Date: 2026-09-06. Result: **yes, proven end to end against a fake directory**;
the same flow against statefs.io needs one admin-minted token (below).

## What was proven

A headless run (`claude -p ... --plugin-dir ./plugin`) with the statefs-ai
plugin loaded did all of the following, verified from files and the fake
directory's counters:

1. `SessionStart` fired, found no identity file, read `STATEFS_ENROLL_URL`,
   generated an Ed25519 keypair locally, posted the public key with the
   single-use token to `/auth/enroll`, wrote `~/.statefs/identity`
   (redirected by `STATEFS_KEY_FILE` for the test), then exchanged a signed
   assertion at `/auth/token` for an acting token.
2. The hook returned `additionalContext`; the model quoted it back:
   `statefs.ai: enrolled this machine as "laptop-agent" ... verified the token exchange. Conversation capture is active.`
3. `UserPromptSubmit`, `PreToolUse` and `PostToolUse` (a Bash call), `Stop`
   and `SessionEnd` all fired and were logged with session id, cwd, tool
   name and tool_use_id.
4. Reusing the token is refused (401); an existing identity file is
   protected unless `--reset`; a fresh token with reset rotates the key.

Unit tests cover the same flow without Claude Code: `pkg/enroll`
(URL parsing, enroll, exchange, reuse, reset, wrong key) and `pkg/plugin`
(auto-enroll, not-enrolled message, rejected token, quiet logging).

## Two lessons that cost a run each

- **Matchers.** `"matcher": "always"` silently disables `SessionStart` and
  `SessionEnd`: their matcher is a session-source filter (`startup`,
  `resume`, `clear`, `compact`, `fork` / `clear`, `logout`, ...), and an
  unrecognized string is an exact match that never fires. `Stop` and
  `UserPromptSubmit` ignore matchers, which is why those two ran on the
  first attempt. Omit the matcher to fire on every occurrence.
- **Data directory.** Claude Code sets `CLAUDE_PLUGIN_DATA` itself
  (`~/.claude/plugins/data/<plugin>/`), overriding the caller's value;
  the hook log landed there. Everything else in the environment is
  inherited, so `STATEFS_ENROLL_URL` and `STATEFS_KEY_FILE` pass through.

## The enrollment URL

Minted by a tenant admin (`POST /api/v1/tenant/enroll-tokens {username}`,
token `en_...`, single use, short TTL) and delivered out of band. Forms the
plugin accepts:

```
https://directory.statefs.io/enroll?token=en_...[&tenant=acme]
https://directory.statefs.io/enroll#en_...
statefs://enroll?directory=https://directory.statefs.io&token=en_...
en_...   (bare; needs STATEFS_DIRECTORY or --directory)
```

How it reaches the agent: `STATEFS_ENROLL_URL` in the environment on
first start (or after a reset), or a person runs
`statefs-ai enroll <url>`. When neither is present the SessionStart hook
tells the agent the machine is not enrolled and the skill instructs it to
ask the user for a URL rather than guess one.

## Reproduce

```bash
make plugin
./plugin/bin/statefs-ai fakedir --listen 127.0.0.1:8477 --username laptop-agent   # prints the enroll url
STATEFS_ENROLL_URL="<url>" STATEFS_KEY_FILE=/tmp/spike-identity \
  claude -p "run echo hi, then quote the session context about statefs.ai" \
  --plugin-dir ./plugin --model haiku --max-turns 3 --allowedTools "Bash(echo:*)"
cat ~/.claude/plugins/data/statefs-ai-inline/hooks.log
```

## Against statefs.io (DONE 2026-09-06)

An admin minted a token for the service identity `kas-agent-2` in
`dev.statefs.ai`. `statefs-ai enroll 'https://directory.statefs.io/enroll#en_...' --out ~/.statefs-ai/identity`
registered the key and verified the exchange in one step. The M1 capture
oracle then passed against production with that identity
(`STATEFS_DIRECTORY=https://directory.statefs.io STATEFS_KEY_FILE=~/.statefs-ai/identity scripts/oracle-m1.sh`).

Two production findings, recorded as handoff delta 7: name search needs
the `manage` capability, and the Go SDK's write path used the operator-only
`resolve` endpoint (fixed in the SDK to route for bearers).

## What this unlocks

The plugin can be handed a URL at start, turn it into a durable identity,
inject state into the model's context, and observe every lifecycle and
tool event. Capture (M1) is now a matter of writing those events to the
spool and pushing them; nothing about the extension mechanism is unknown.

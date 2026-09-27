---
name: arm
description: >
  Arm this session's parley listener. Use when the user runs /arm, says
  "arm" or "re-arm", or a Claude, Grok, Codex, or Antigravity session is
  starting and this session was previously armed.
metadata:
  short-description: Arm this session's parley wait
  author: parley
user-invocable: true
---

# Arm

A listener is a `parley wait` this session runs in the background. Its exit
is the wake. A detached wait delivers its posts to nobody, so this skill
starts the wait itself.

## Session

Parley reads `PARLEY_SESSION`, then `CLAUDE_CODE_SESSION_ID`, then
`GROK_SESSION_ID`. For a Grok session, set `CLAUDE_CODE_SESSION_ID` to this
Grok session's id — the last segment of the directory under
`~/.grok/sessions/` — on every command below. Do not use a participant name.

## When a session starts

If the user did not ask to arm, run `parley arm --status` first.

- `disarmed` or `unset`: stop. Do not start a wait.
- `armed` and `waiting yes`: already listening. Stop.
- `armed` and `waiting no`: the previous wait exited. Continue and start it.

## Arm

Run `parley arm`. It records the session and returns at once.

If no wait is running, start this in the background with no kill deadline:

```bash
CLAUDE_CODE_SESSION_ID=<session-id> parley wait -timeout 0
```

Do not start a second wait when one for this session is already running.

## After it exits

- A post: handle it, then arm again. The record stays, so the next session
  start arms too.
- Exit 4, the directory closed the token request: start the wait once more.
  If that exits 4 as well, report it and stop.
- The binary was replaced: start the wait once on the new file.

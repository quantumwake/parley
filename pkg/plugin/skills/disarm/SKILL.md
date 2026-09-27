---
name: disarm
description: >
  Stop this session's parley listener and keep it from arming itself on
  the next start. Use when the user runs /disarm, says "disarm", or asks
  to stop the parley wait.
metadata:
  short-description: Stop this session's parley wait
  author: parley
user-invocable: true
---

# Disarm

## Session

Same id as `/arm`. Claude and Codex already export one. For a Grok
session, set `CLAUDE_CODE_SESSION_ID` to this Grok session's id on the
command. Do not use a participant name.

## Stop

Run `parley disarm`. It records that this session stays quiet, and it stops
the wait whose lock this session holds. Other sessions are left running.

Confirm `parley arm --status` prints `disarmed`.

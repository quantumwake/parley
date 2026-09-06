---
name: statefs-ai
description: How this session is recorded on statefs.io and how to enroll the machine when the SessionStart context says it is not enrolled.
---

This machine records the session as a conversation on statefs.io through the statefs-ai plugin.

- If the session context says the machine is not enrolled, ask the user for an enrollment URL from the statefs.io tenant console and run `statefs-ai enroll <url>`. Do not guess or fabricate a URL.
- If enrollment reports a rejected token, ask for a fresh URL; tokens are single use.
- Never print the contents of `~/.statefs/identity`; the private key must stay on this machine.

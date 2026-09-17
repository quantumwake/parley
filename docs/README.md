# parley docs

Layout: `docs/{category}/{seq}_{name}.md`. Read the architecture in order;
the rest by need.

- [RUNBOOK.md](RUNBOOK.md): the exact procedure from a branch to a released version, with the expected output of every step. **Follow it to test and release.**

## reviews — living ledger, re-verified on each run

- [SECURITY_GAP_MATRIX.md](SECURITY_GAP_MATRIX.md): every security finding ever raised against this repository, current status. Read it first, before starting a new review.
  - [SECURITY-REVIEW-2026-09-17.md](reviews/SECURITY-REVIEW-2026-09-17.md): first run, under statefs's [security-review framework](https://github.com/quantumwake/statefs/blob/docs/security-review-framework/docs/evals/security-review-framework.md). Posture Red: 3 High, 5 Medium.

## architecture — what it is, as built

1. [01_layout.md](architecture/01_layout.md): parley from C0 to C4. One binary, three faces. **Start here.**
2. [04_hooks-and-tools.md](architecture/04_hooks-and-tools.md): hook wiring, capture pipeline, the CLI and MCP surfaces, one shared turn, the swarm.
3. [05_participants.md](architecture/05_participants.md): `identity` and `participant`, and what those claims are worth.
4. [06_paths.md](architecture/06_paths.md): what a session records, who the speakers are, who submitted the prompt.

## design — agreed; the status line in each says whether it is built

1. [01_mentions-and-references.md](design/01_mentions-and-references.md): `@kind:name` addresses who should act, `#kind:name` references what a post is about; who wakes.
2. [02_subagent-capture.md](design/02_subagent-capture.md): subagents' own transcripts recorded with their session, attributed by `agent_id`, tails bounded by SubagentStart and SubagentStop.
3. [03_work-and-exchange.md](design/03_work-and-exchange.md): exchange posts vs work posts (request, claim, close); parley enforces claims, shows work state, lists work.
4. [04_codex-cli-adapter.md](design/04_codex-cli-adapter.md): draft, not built, not yet agreed. Codex CLI's hooks map onto parley's Claude Code hooks closely enough that the adapter is mostly field-mapping; open questions before scoping a build.

## Elsewhere

The platform, the product plan, the search and teams design, the RFCs and
the handoffs to statefs core live in the `statefs.ai` repo. The engine is
`statefs`.

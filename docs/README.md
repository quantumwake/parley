# parley docs

Layout: `docs/{category}/{seq}_{name}.md`. Read the architecture in order;
the rest by need.

- [RUNBOOK.md](RUNBOOK.md): the exact procedure from a branch to a released version, with the expected output of every step. **Follow it to test and release.**

## architecture — what it is, as built

1. [01_layout.md](architecture/01_layout.md): parley from C0 to C4. One binary, three faces. **Start here.**
2. [04_hooks-and-tools.md](architecture/04_hooks-and-tools.md): hook wiring, capture pipeline, the CLI and MCP surfaces, one shared turn, the swarm.
3. [05_participants.md](architecture/05_participants.md): `identity` and `participant`, and what those claims are worth.
4. [06_paths.md](architecture/06_paths.md): what a session records, who the speakers are, who submitted the prompt.

## design — agreed; the status line in each says whether it is built

1. [01_mentions-and-references.md](design/01_mentions-and-references.md): `@kind:name` addresses who should act, `#kind:name` references what a post is about; who wakes.
2. [02_subagent-capture.md](design/02_subagent-capture.md): subagents' own transcripts recorded with their session, attributed by `agent_id`, tails bounded by SubagentStart and SubagentStop.
3. [03_work-and-exchange.md](design/03_work-and-exchange.md): exchange posts vs work posts (request, claim, close); parley enforces claims, shows work state, lists work.
4. [04_codex-cli-adapter.md](design/04_codex-cli-adapter.md): Codex CLI hooks and MCP via `parley setup codex`; rollout JSONL tailer isolated in `pkg/capture/codex_transcript.go`. Built, not proven on a live Codex session.
5. [05_objectives-and-the-loop.md](design/05_objectives-and-the-loop.md): CP14 — objectives above the work protocol; the board separates work (anyone may claim), decisions (only the owner chooses) and attestations (only the owner can know); every assessment line carries evidence, a mark, what was not checked and which judge made it. The loop proposes goal changes and never applies them. Proposed, not built; stage 1 has no LLM.
6. [06_claims-across-hosts.md](design/06_claims-across-hosts.md): why three agents wrote the same file in four minutes; an unprompted claim carries a subject so the read-after-append reconciliation that already exists can fire on it. Lowest position holds, computed by both sides alone; no lock, no lease, no private channel.

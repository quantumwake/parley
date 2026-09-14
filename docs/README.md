# parley docs

Layout: `docs/{category}/{seq}_{name}.md`. Read the architecture in order;
the rest by need.

## architecture — what it is, as built

1. [01_layout.md](architecture/01_layout.md): parley from C0 to C4. One binary, three faces. **Start here.**
2. [04_hooks-and-tools.md](architecture/04_hooks-and-tools.md): hook wiring, capture pipeline, the CLI and MCP surfaces, one shared turn, the swarm.
3. [05_participants.md](architecture/05_participants.md): `identity` and `participant`, and what those claims are worth.
4. [06_paths.md](architecture/06_paths.md): what a session records, who the speakers are, who submitted the prompt.

## design — agreed, not yet built

1. [01_mentions-and-references.md](design/01_mentions-and-references.md): `@kind:name` addresses who should act, `#kind:name` references what a post is about; who wakes.
2. [02_subagent-capture.md](design/02_subagent-capture.md): subagents' own transcripts recorded with their session, attributed by `agent_id`, tails bounded by SubagentStart and SubagentStop.

## Elsewhere

The platform, the product plan, the search and teams design, the RFCs and
the handoffs to statefs core live in the `statefs.ai` repo. The engine is
`statefs`.

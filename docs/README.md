# statefs.ai docs

Layout: `docs/{category}/{seq}_{name}.md`. Read the architecture in order;
the rest by need.

## architecture — what it is, as built

1. [01_layout.md](architecture/01_layout.md): parley from C0 to C4. One binary, three faces. **Start here.**
2. [02_overview.md](architecture/02_overview.md): the product in plain terms, with appendices.
3. [03_conceptual.md](architecture/03_conceptual.md): the original C1–C3 diagrams, flows and deployment.
4. [04_hooks-and-tools.md](architecture/04_hooks-and-tools.md): hook wiring, capture pipeline, the CLI and MCP surfaces, one shared turn, the swarm.
5. [05_participants.md](architecture/05_participants.md): `identity` and `participant`, and what those claims are worth.
6. [06_paths.md](architecture/06_paths.md): what a session records, who the speakers are, who submitted the prompt.

## design — proposed, not built

1. [01_teams-and-synopses.md](design/01_teams-and-synopses.md): the range as a checkable citation; teams; where indexing lives; who writes a synopsis.
2. [02_search.md](design/02_search.md): index namespaces and the write, read, search and pull paths; how core indexing actually works.

## product — plan and scope

1. [01_plan.md](product/01_plan.md): milestones M0–M6, phases, seams.
2. [02_features.md](product/02_features.md): themes, and the storage-versus-summarisation rule.
3. [03_requirements.md](product/03_requirements.md): actors and requirements (parts superseded; see its banner).
4. [04_activities.md](product/04_activities.md): deferred work.

## reference

1. [01_statefs-capabilities.md](reference/01_statefs-capabilities.md): what the engine gives us, with measured numbers.
2. [02_swarm.md](reference/02_swarm.md): running N agents with N identities through one shared conversation.

## rfcs, handoffs, spikes

- [rfcs/RFC-0001-conversation-log-platform.md](rfcs/RFC-0001-conversation-log-platform.md): the storage mapping and every decision.
- [handoffs/HANDOFF-2026-09-05-statefs-core-requests.md](handoffs/HANDOFF-2026-09-05-statefs-core-requests.md): what we ask of statefs core, with what has since landed or been withdrawn.
- [spikes/SPIKE-2026-09-06-claude-code-enrollment.md](spikes/SPIKE-2026-09-06-claude-code-enrollment.md): enrollment from inside Claude Code.

Handoffs that went the other way live in the core repo under
`statefs/docs/handoffs/`.

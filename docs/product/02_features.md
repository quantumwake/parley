# statefs.ai feature themes and components

Status: 2026-09-06. One row per component. **Where** says who provides it:
`io` = statefs.io as built (v0.5.4), `lib` = the statefs.ai library embedded
in the plugin or console (no server), `server` = needs a statefs.ai process
running when no client is (gateway or job), `core-ask` = statefs core work
in the handoff. **Lost without a server** marks what a library-only v1 gives
up. Read with [PRODUCT-PLAN.md](01_plan.md).

## A. Capture

| Component | Where | When | Lost without a server |
|---|---|---|---|
| Claude Code hooks (lifecycle, tool I/O, subagents) | lib | M1 | no |
| Claude Code transcript tailer (thinking, text blocks) | lib | M1 | no |
| Local spool, crash safety, ack offsets | lib | M1 | no |
| Redaction before anything leaves the machine | lib | M1 | no |
| Thinking policy by credential kind | lib | M1 | no |
| Batched delivery, retries, at-least-once | lib (direct) or server (S13) | M1 | no |
| Exact-once on retry after timeout | core-ask (batch id) | after handoff | no; read-side dedupe meanwhile |
| Agent SDK and Codex sources | lib | M6 | no |
| API proxy source (clients that only set a base URL) | server | M6 | **yes** |

## B. Conversation log

| Component | Where | When | Lost without a server |
|---|---|---|---|
| One namespace per conversation, scope tags, display name | io | M1 | no |
| Replay from any position, paged | io | M1 | no |
| Follow live by polling | io | M1 | no |
| Follow live by push (feed consumer, SSE fan-out) | core-ask + server | M6 | **yes** (polling remains) |
| Purpose and name changes as events | lib | M2 | no |
| Snapshot and share a conversation | io | M4 | no |
| Delete (owner or admin with `manage`) | io | M1 | no |
| Tiering to S3, compaction, retention | io | always | no |
| Blob offload for bodies over 256 KiB | lib + S3 | M1 | no |

## C. Agents and personas

| Component | Where | When | Lost without a server |
|---|---|---|---|
| Persona namespace with versioned definitions | lib + io | M2 | no |
| Agent namespace with lifecycle and assignments | lib + io | M2 | no |
| Per-session agent under the person's identity | lib | M2 | no |
| Long-lived service agents, fleet enrollment | io (RFC-0011 enrollment) | M2 | no |
| Attribution of every conversation to agent and persona version | lib (scope) | M2 | no |
| Agent state at a glance (latest row per namespace) | io | M2 | no |

## D. Shared conversations

| Component | Where | When | Lost without a server |
|---|---|---|---|
| Multi-writer namespace, total order by the leader | io | M3 | no |
| Membership as grants | io | M3 | no |
| Publish, threads, addressing | lib | M3 | no |
| Client-owned cursors | lib | M3 | no |
| Hook injection at turn boundaries | lib | M3 | no |
| Skill for cadence and etiquette | lib | M3 | no |
| Descriptions, tags, purpose history on a shared conversation | lib + io (scope, `meta.purpose`) | M3 | no |
| Discovery of conversations the identity may read (name search, tag containment, grant-scoped) | io (directory find under the caller's token) | M3 | no |
| Subscribe, unsubscribe, subscription cap, full or digest mode | lib (rows in the agent namespace, cursor files) | M3 | no |
| Digests for digest-mode subscribers | lib: participant-written summaries with `indexable: true` | M3 | no |
| Push delivery to idle sessions | server | M6 | **yes** (turn-boundary reads remain) |
| Work queues (competing consumers) | server | deferred | **yes** |

## E. Search and indexing

| Component | Where | When | Lost without a server |
|---|---|---|---|
| Label search: scope containment and `?q=` display name across a tenant | io (directory) | M1 | no |
| Per-conversation SQL (DuckDB, `json_extract`) | io (query service) | M4 | no |
| Per-namespace indexes: zonemap, hash, bitmap, btree (P15) | core-ask | after handoff | no |
| Positional row fetch (index to rows without DuckDB) | core-ask | after handoff | no |
| Cross-conversation search by indexed value ("which conversations mention X", "tool failures this week across all agents") | server or core-ask | M5 | **yes** unless the core builds tenant-level index namespaces |
| Semantic search: embeddings and a vector index over blocks | agents write embeddings on request (job posts the request); statefs vector index type (future core ask) serves the query | M5 | partly: requests need a scheduler when no participant is active |
| Knowledge graph: entities and relations extracted from conversations, graph queries | agents extract on request and write triples as rows; graph queries over rows via SQL first | M5 | partly, as above |
| Summaries per conversation (`meta.summary`) | lib: participants summarize on request (`post.request` / `post.claim`), local rule triggers | M3 | no |
| Derived labels pushed into scope (topics, repos, tools used) so directory search finds them | server (job) | M5 | **yes** |

Boundary rule (user, 2026-09-06: storage, not summarization). statefs.io
owns anything that is a deterministic function of stored rows, which is
P15's own invariant: btree, hash, bitmap, zonemap, and a vector index over
an embedding column that is already stored. statefs.ai owns anything that
needs a model or outside knowledge: computing embeddings, summaries, topic
labels, entity and relation extraction. The product writes those results
back as rows (`meta.summary`, `embedding` columns, triples in a graph
namespace) and labels in scope; statefs indexes and serves them like any
other data. The product's server, when it exists, is a scheduler that posts requests
into conversations; the participants (agents that already hold a model)
fulfil them and write rows back. It is not a query engine, not an ingest
hop, and not a model caller. The directory searches labels, not content,
so content-derived labels are pushed into scope by those jobs.

## F. Session capabilities

| Component | Where | When | Lost without a server |
|---|---|---|---|
| `history.resume` (tail into context) | lib + io | M5 | no |
| `history.search` by labels and per-conversation SQL | lib + io | M5 | no |
| `history.search` by indexed value, semantic, or graph across conversations | server | M5 | **yes** |
| `conversation.read`, `conversation.publish` | lib + io | M3 | no |
| MCP server over stdio | lib | M5 | no |

## G. Console

| Component | Where | When | Lost without a server |
|---|---|---|---|
| Sign in with the user's credential, tenant switcher | io | M4 | no |
| Conversation list and label search | io | M4 | no |
| Replay viewer with thinking and tool pairing | lib (browser) + io | M4 | no |
| Agents and personas pages | io | M4 | no |
| Shared conversations reader and composer | lib (browser) + io | M4 | no |
| Per-conversation SQL | io | M4 | no |
| Cross-conversation search, semantic, graph views | server | M5 | **yes** |
| Live updates without polling | server | M6 | **yes** |

## H. Identity and access

| Component | Where | When | Lost without a server |
|---|---|---|---|
| Identities, memberships, authenticators, acting tokens, tickets | io | M0 | no |
| Capabilities read, write, manage | io | M0 | no |
| Grants on namespaces | io | M3 | no |
| Thinking visibility narrower than the conversation | lib (sibling namespace) | deferred | no |
| Product-level policy beyond statefs (quotas, rate limits per tenant) | server | M6 | **yes** |

## I. Analytics and economy

| Component | Where | When | Lost without a server |
|---|---|---|---|
| Usage and cost per conversation (tokens, model) | io (SQL per namespace) | M4 | no |
| Usage per agent, persona, tenant over time | server (aggregation job into a metrics namespace) | M5 | **yes** |
| Agent performance derived from events (tool failure rate, duration, outcomes) | server | M5 | **yes** |
| Persona skills, tasks as jobs, marketplace | server | future | **yes** |

## J. Operations

| Component | Where | When | Lost without a server |
|---|---|---|---|
| Deploy on statefs-prod, ingress, TLS | io | M0 | no |
| Dev tenant `dev.statefs.ai` | io | M0 | no |
| Observability of the product (ingest rate, lag, errors) | server | M6 | **yes** as a service; client logs remain |
| Million live conversations | core-ask (idle unload) | M6 | no |

## Reading the table

Themes A to D and most of F, G, H are library plus statefs.io. Theme E
beyond one namespace, theme I beyond one conversation, and push delivery
anywhere are server work. The server is a job runner and a query path
first, an ingest hop second: embeddings, graph extraction, summaries and
derived labels are computed by jobs; semantic and graph queries are served
by a process that holds those indexes. Whether that process also fronts
ingest (a gateway) is a separate choice with no feature attached to it.

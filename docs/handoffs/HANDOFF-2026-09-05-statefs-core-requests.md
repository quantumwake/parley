# Handoff to the statefs core sessions: what statefs.ai needs from statefs.io

Date: 2026-09-05 (rev 4, 2026-09-06). **Sequencing (user): this handoff follows RFC-0011.** RFC-0011 v2 is ADOPTED and BUILT (v0.5.0, 2026-09-05; v0.5.4 on statefs-prod 2026-09-06, 10-oracle smoke PASS), so the gate is satisfied and this handoff is next. §0a is rewritten against the v2 as-built model (§12.10).
From: the statefs.ai design session (`fabric.ai` repo: [RFC-0001](../rfcs/RFC-0001-conversation-log-platform.md), [CONCEPTUAL.md](../CONCEPTUAL.md)).
To: the sessions driving P14/P15 and the engine in `../statefs`.

Documents this handoff is written against (all under `statefs/docs`), and
what the product takes from or gives back to each:

| statefs doc | Status there | This handoff's relationship |
|---|---|---|
| `future/user-indexes.md` (P11 requirements, 2026-07-26) | recorded | the origin of "user-declared, position-keyed, live + bootstrapped" indexes; its open questions are answered by P15, the product only adds metering expectations (§4) |
| `rfcs/cluster/RFC-0010` (indexes on the WAL; D1 index subsystem, D2 executor) | D1 spec'd as P15; **D2 pending**, E2 go/no-go after P14+P15 S1-S3 | the product supplies the missing input for D2: the fast-path grammar it needs (§3) |
| `phases/P15-indexing.md` | ADOPTED 2026-08-16, defaults pinned, `pkg/index` not started at `c53f0849` | the product's namespaces are offered as the S2/S3 fixture; answers to P15 §8 open questions 1-5 (§4) |
| `rfcs/cluster/RFC-0009` (query engine v2) | O1+O3 adopted as P14; §5 disaggregated end state settled | the "authenticated, claims-gated member read surface (tail + non-tiered blocks + P15 index reads)" that §5 says survives is exactly the surface the product needs (§5, delta 3) |
| `phases/P14-query-access-layer.md` | ADOPTED; S1/S1b shipped v0.3.6 (52 ms warm) | ports §3b (`Pruner`, `TailSource`, `Executor`, `QueryPlacement`) are where the product's asks plug in; `min_head` (S2) is required (§4) |
| `docs/ROADMAP.md` open work: append idempotency (confirmed 2026-08-16) | open | delta 2, unchanged ask |
| `docs/GAP_MATRIX.md`: `WatchTail` planned, byte-target compaction open | open | deltas 4 and 5 |
| `rfcs/cluster/RFC-0002` §4 member classes, P3-D9 consumer mode | parked (P11) | delta 4 |
| `rfcs/cluster/RFC-0011` v2 (accounts and authnz; tenant naming settled, grant tickets + request MAC on the data plane, query members register, tenant admin) | DRAFT v2 2026-09-05, decisions pending §10; v0.3.8 hotfix shipped | **precedes this handoff**; §0a lists what the product needs it to settle |

Identity requests (Google sign-in, self-serve tenancy, public tenant) are a separate handoff placed in the statefs repo: `statefs/docs/handoffs/HANDOFF-2026-09-06-statefs-ai-identity-requests.md`.

## 0a. RFC-0011 v2 as built: how the product sits on it

Verified 2026-09-06 against RFC-0011 §12.10 and the v0.5.x commits. statefs.ai
is a "derivative product" in RFC-0011's own taxonomy (§12.5) and connects
through the identity model like any producer or consumer.

| RFC-0011 v2 element (as built) | What statefs.ai does with it |
|---|---|
| IDENTITY (`person` or `service`) x MEMBERSHIP per tenant; grants attach to the membership; ownership = `owner_membership_id` (NULL = tenant-wide) | a local Claude Code agent runs as the person's identity; an autonomous agent is a `service` identity. Conversation namespaces are owned by the agent identity's membership; shared conversations are owned by the conversation owner's membership with grants per member membership, or tenant-wide (NULL) when the channel is the whole tenant. Product cursor and index namespaces are owned by one `service` identity per tenant. |
| AUTHENTICATOR rows: password, api key, registered keypair (`alg`-tagged); enrollment tokens for fleets | the capture plugin authenticates with the person's api key or `statefs keygen` keypair from `~/.statefs/identity`; autonomous agent fleets enroll with single-use enrollment tokens so no human ferries secrets. The product never stores an authenticator. |
| `POST /auth/token` exchange -> ~15 m acting token per (identity, tenant); api keys refused as bearers; no refresh tokens | the plugin exchanges its authenticator and sends the **acting token** to the gateway; the gateway forwards that token, never a durable credential. "Pass the person's key through" (decision 3) now reads "pass the person's acting token through". |
| grant tickets (5 m, one namespace, per verb, O-B+) + HKDF request MAC, members verify offline | the gateway's `Credential` adapter mints tickets with the caller's acting token, caches per (token, namespace, verb), signs each append and scan. **Ask (unchanged): a batch mint** for many namespaces per call, since one gateway serves 100k+ conversations and would otherwise mint one ticket per namespace per 5 m. |
| per-credential capabilities `read`, `write`, `manage` narrow the seat | plugin credentials: `read`+`write`; console credentials add `manage` for delete and cordon. Answers the earlier "admin verb" question. |
| namespace `display_name` unique per tenant, `tags: []` via scope containment, `?q=` search | conversation names repeat across agents ("review PR 42"), so the product keeps the human name and purpose in its catalog and sets `display_name` to a unique `<agent>/<name>#<n>` form; tags carry `persona`, `agent`, `task`, `session`, `channel` labels. **Question**: can `display_name` uniqueness be relaxed to (tenant, owner)? |
| consoles (§6a, v0.5.3 studio shell): operator console + one role-gated tenant/user console | the product console is a third app; signs in through the same exchange with user credentials; identity management stays in the tenant console. |
| deferred in v2: `NODE_REGISTRATION.function` (query members do not register), rotating cluster keys, mTLS authenticator | the product console resolves the query service by the static URL pattern until query members register; mTLS is recorded for when the product warrants it. |

Everything above is inside the gateway's `Authorizer` and `Credential` ports.

## 0. One paragraph

statefs.ai stores agent conversations and shared conversations as namespaces:
one event per completed block, millions of namespaces, multi-writer on
shared conversations, rows appended in time order. It filters by index, fetches
the matching rows by position, and hands them to product hooks for
textual analytics and side LLM summaries whose results are appended back
as rows. Every index capability the user described on 2026-09-05 (live
hash, ordered tree for ranges, bitmap for types, btree for dates, index-
filtered queries) is already P15 D1 vocabulary. This handoff maps the asks
onto P15, answers the open questions P14/P15/RFC-0010 left for a consumer,
and lists five deltas the product cannot build around.

## 1. The user's index asks, mapped

| User ask (2026-09-05) | Where it already lives | Delta or note |
|---|---|---|
| hash index on column X by lookup key, ever growing; "a ring hash may be better since it is ever growing" | P15 §3 `hash`: per position-range segment `seg-<rowStart>-<rowEnd>`, sorted `(h64 -> postings)`, immutable at seal; tail delta in memory | Growth is by adding segments, never by rehashing one table, which is the property a ring buys; no consistent-hash structure is needed inside a namespace. The ring hash in the statefs design is P14 §3a/§3b `QueryPlacement` (namespace -> query member) and it stays there. Product needs P15 §8 Q5 segment blooms in S2 (§4). |
| a log tree "like a red-black", constantly updated, range searches | P15 §3 `btree` v1 = sorted runs per segment, merge on read; run compaction recorded future | Only the tail delta is a live structure; request it be an ordered map so `Range` on the tail is O(log n). For the product, ranges on time are mostly served by **zonemaps** (P15 S1 + P14 S5): rows arrive in time order, so per-block `min,max` on `ts_ms` is monotone and range = block pruning. btree demand is therefore low for conversation streams (see §3). |
| bitmap indexes for types | P15 §3 `bitmap`, roaring per value, downgrade at 1,024 distinct | Product columns `kind`, `role`, `source`, `agent_type`, `author`, all under 30 distinct. `count(*) GROUP BY kind` with zero row reads is the console's per-conversation summary (P14 §2 planner special case extends naturally). |
| dates use the btree | `btree` on a top-level scalar | Product stores `ts_ms`/`ingested_ms` as int64 epoch ms. Question back: btree v1 value types (int64 only, or strings/floats). |
| indexes constantly updated as data comes in | P15 §2 synchronous WAL tap, `Maintainer.OnAppend`, lag 0; async + bounded lag is the recorded escape hatch | No delta. The product offers its row shape for the p99 measurement P15 §8 Q2 asks for (§4). |
| invoke queries using ranges or previous indexes as filter criteria | P15 §5 control endpoints return positions; P14 S5 `Pruner` narrows the block list for DuckDB (E1) | Delta 3: positions -> rows without DuckDB, on the gated member read surface RFC-0009 §5 already names. |
| then textual analytics or side LLM calls to summarize | product side (statefs.ai hooks) | needs delta 3 to pull rows and delta 4 to wake on append; summaries appended back as `meta.summary` rows to the same or an index namespace. Nothing in core changes for that. |

Product index declarations, in the P15 §1 shape, offered as the S2/S3 fixture:

```json
{"scope": {"tenant": "acme", "user": "u-17", "agent": "claude-code", "session": "..."},
 "indexes": [
   {"name": "by_kind",   "type": "bitmap", "column": "kind"},
   {"name": "by_role",   "type": "bitmap", "column": "role"},
   {"name": "by_tool",   "type": "hash",   "column": "tool_name"},
   {"name": "by_parent", "type": "hash",   "column": "parent_event_id"}
 ]}
```

Shared conversations add `by_thread` (hash on `thread`), `by_to` (hash on `to`),
`by_author` (bitmap). `ts_ms` relies on zonemaps; a btree on it is declared
only if the zonemap pruning property test shows blocks too coarse. All
indexed columns are top-level scalars per the P15 v1 default; the product
keeps them out of the `content` JSON column until dotted paths land.

## 2. Determinism invariants the product will honor

From P15: an index is a deterministic function of the WAL; no index bytes
cross the wire; segments are keyed by position range, never block id;
compaction remaps, never rebuilds; parity is content-level, not file-
level. The product never asks for index shipping, never keys anything on
block ids, and treats an index as usable only when the member it reads
from reports `ready` at the current epoch (P15 §1 heartbeat state).

## 3. Answer to RFC-0010 §6 "fast-path grammar boundary" (D2 input)

RFC-0010 asks what SQL shapes MUST be index-served to decide how much E2
is worth. The product's interactive shapes, in demand order:

| Shape | Index | Executor need |
|---|---|---|
| `WHERE tool_name = 'Bash'` (point lookup) | hash `by_tool` | E2-lite: postings -> positional row fetch (delta 3); no DuckDB |
| `WHERE kind = 'tool.result' AND is_error` | bitmap `by_kind` + hash | same |
| `WHERE ts_ms BETWEEN a AND b` (time window) | zonemaps (P14 S5) | E1: pruned blocks + tail into DuckDB, or delta 3 with a range list |
| `count(*) GROUP BY kind` | bitmap counts | E2 fast-path, zero row reads |
| "since my cursor": `position >= n` | none (positional scan) | existing scan |
| threads: `WHERE thread = x ORDER BY position` | hash `by_thread` | E2-lite |
| everything else (windows, dense_rank, json_extract over `content`) | | E1 DuckDB, unchanged |

Reading: E2 is worth building only as the thin "postings -> rows" path
(delta 3) plus bitmap counts. Nothing in the product argues for a native
executor beyond that, which matches RFC-0010 §5's recommendation (E1 then
a narrow E2, revisit E3 only if the grammar covers everything).

## 4. Answers to the open questions P15 §8 and P14 §9 left for a consumer

| Question | Product answer |
|---|---|
| P15 Q1 index defs in the WAL too | directory-only v1 is fine. Offline rebuild matters later for exported or snapshotted conversations (a share link), so WAL-recorded defs are wanted before snapshots of indexed namespaces are a feature. |
| P15 Q2 synchronous tap | accepted. Measure append p99 with hash+bitmap on the product row shape: 2 KiB rows, 4 indexed top-level columns, 1 append per row (no client batching within a namespace). |
| P15 Q3 nested/JSON columns | product keeps indexed keys top-level for v1; dotted paths (`content.tool_name`) are the first fast-follow it would use. |
| P15 Q4 bitmap threshold 1,024 | fine; product bitmap columns are under 30 distinct. |
| P15 Q5 segment blooms | **wanted in S2**: idle conversations seal small (seal-on-idle, delta 1), so a long-lived namespace accumulates thousands of tiny segments; without blooms a hash lookup binary-searches all of them. Alternatively run compaction for hash segments (recorded future) lands before S3. |
| P14 §9 `min_head` read-your-writes | **required in S2**: "resume this conversation" runs right after the capture plugin appended the last block; the read must not lag the session's own write. |
| P14 §9 `union_by_name` drift | acceptable; event kinds carry different columns by design. |
| P14 §9 tier creds granularity | no opinion v1; per-tenant prefixes become relevant when tenants own thinking traces (RFC-0011 §6 ownership fields). |
| `future/user-indexes.md` metering | index bytes counted in the namespace footprint is expected and fine. |

## 5. The ten deltas, in priority order

1. **Idle-namespace unload and seal-on-idle (engine, new).** Every
   touched namespace holds an `nsHandle`, an open WAL writer fd and a
   memtable until it seals, and age rotation waits for `FlushThreshold`
   (1000) rows (`pkg/engine/wal_view.go` `nsHandleFor`, `segment.go`,
   `wal_engine.go`). A 200-event conversation never seals and never
   closes. Ask: close idle handles after N minutes (`wal.OpenForAppend`
   reopens; the P15 tail delta flushes to a segment or rebuilds from the
   WAL tail on reopen, the same recovery the memtable does), and a seal-
   on-idle policy that ignores `FlushThreshold` after M minutes. Oracle
   (P15 style): 200k namespaces x 50 rows on one member; fd count and RSS
   flat after the idle window; scan, index parity and P14 `as_of` still
   correct. This is the ceiling for the product's base scenario (1M live
   conversations across 3 groups) and it interacts with P15 (tail delta
   lifecycle) and P14 (`TailSource` reading a closed tail).
2. **Batch-id idempotent append** (ROADMAP open work, confirmed
   2026-08-16). Capture is at-least-once by construction. Ask: client
   batch id on `POST /api/v1/state/{ns}`, deduped at the leader within a
   WAL window, echoed in the result. Until then the product dedupes on
   read by `event_id`.
3. **Positional row fetch on the claims-gated member read surface.**
   RFC-0009 §5 keeps "the authenticated, claims-gated member read surface
   (tail + non-tiered blocks + P15 index reads)" as the surviving idea;
   P15 §5 puts index reads under `/admin/index/...`. Ask: (a) the P15 read
   endpoints on that gated surface with the P12 scope gate, not only the
   operator plane; (b) one more read on it, `GET /api/v1/state/{ns}/rows`
   taking a position list or range list (paged, bounded), served through
   the engine's block resolution + `nsView` tail, so index -> positions
   -> rows works without DuckDB. Shape: both list forms if cheap; the
   product's first use is lists of a few thousand positions. This is the
   whole of E2-lite in §3 and it is also a future `TailSource`/`Fetcher`
   implementation for P14 (§3b ports).
4. **Consumer mode on the ReplicaFeed** (RFC-0002 §4 member classes,
   P3-D9, parked P11; P14 §3b lists "feed subscription" as `TailSource`'s
   future implementation). `Subscribe` already pushes the tail (10 ms
   head poll, 1 s idle backoff, heartbeats at head), but every subscriber
   enters the marks board and sets the replication floor, the port
   (9001) is in-cluster only, and there is no per-namespace auth. Ask: a
   subscribe mode that never enters the board, backfills from sealed
   blocks below the retained WAL, and is admitted by a bearer with
   namespace scope. The product's in-cluster gateway is the first
   consumer; it is also what wakes the analytics hooks on append and, for
   P14, the continuously-warm tail.
5. **Byte target in compaction `planMerge`** (GAP_MATRIX open). Tool
   outputs make 10k-row blocks multi-GB. The product caps inline bodies at
   256 KiB and offloads larger bodies by reference (binary rejected, blob
   pointers parked P11), but the engine should cap merged block bytes too.

6. **Find must include granted namespaces (directory).** Verified 2026-09-06
   in `cluster/pkg/api/namespaces.go`: `callerScope` merges the caller's
   scoping claims (`[user, tenant]`) into every search and
   `scopeContainsClaims` is the only visibility rule, and namespace birth
   merges the creator's claims into the scope. So a conversation created
   by identity A is invisible in `GET /namespaces?scope=`/`?q=` to identity
   B even when B holds a grant on it, although B can mint a ticket and
   read it by id. Discovery of shared conversations therefore does not
   work through the directory. Ask: the find and name-search handlers
   return the union of (scope contains claims) and (a GRANT row for the
   caller's membership), and a namespace created without an owner-user
   label is visible tenant-wide. Small change in `FindByScope`/`FindByName`
   plus one oracle: A creates, grants B `read`, B finds it by tag and by
   name; C without a grant does not. Blocks plan phase 3.8 (discovery).

7. **Read-capability access to name search and the write route (directory + Go SDK).** Found 2026-09-06 with a real `read,write` key in `dev.statefs.ai`: (a) `GET /api/v1/cluster/namespaces?q=` goes through `tenantActor`, which requires the `manage` capability, so a plugin key cannot look its own conversation up by name (HTTP 403 "this credential lacks the manage capability"); ask: name search under `authorizeRead` + the P12 scope rule, `manage` only for mutations. (b) `POST /api/v1/cluster/resolve/{id}` is cluster-token only and the Go SDK's `Appender` used it, so every bearer append failed with 401 "invalid cluster token"; the Python client routes via `GET route/{id}`. Fixed in the Go client (uncommitted, `Appender` routes when no cluster token is configured); worth a test in the SDK suite with a bearer-only client.

8. **Owner-managed grants (directory).** Verified 2026-09-06: `POST /api/v1/tenant/grants` goes through `tenantActor(adminOnly=true)`, so only a tenant admin with `manage` can grant access to a namespace; the namespace owner cannot. For shared conversations that means every "let bob into #platform" is admin work. Ask: an owner (the `owner_membership_id`) with `manage` may grant and revoke `read|write` on its own namespaces; admins keep the tenant-wide power. One oracle: A creates, A grants B read, B scans; C cannot grant on A's namespace.

Correction to delta 6, verified on production the same day: the deployed directory stamps only the `tenant` claim on a namespace (`values-prod.yaml` `tenantClaim: tenant`, no user scoping), so every tenant member already lists every namespace and discovery works; delta 6 stays relevant only for deployments that scope by `[user, tenant]`.

9. **Access in the find answer (directory).** Listing "conversations I may read" should be one directory query: the pin row already carries `owner_membership_id`, and the grants table carries the caller's grants, so `GET /api/v1/cluster/namespaces?scope=` can return `access: owner|admin|read|write|none` per row for the calling membership (a LEFT JOIN on `statefs_grant`). Today the answer has only `owner_membership_id`, a bearer cannot list its own grants (`GET /tenant/grants` is admin-only), so the product had to probe each member with a ticketed read to learn whether it may read a namespace: a route, a mint and a read per row (about 1 s each from a laptop). Ask: `access` in the find/name-search answers, and `GET /api/v1/tenant/grants?mine=1` for bearers. Oracle: A owns, B is granted read, C has nothing; one find call from each returns the right `access` on the same namespace.

10. **Owners relabel their own namespaces with `write` (directory).** Found 2026-09-06: `PATCH /api/v1/cluster/namespaces/{ns}` (scope merge) requires the `manage` capability, so the plugin's read/write key cannot set a title or description on the conversation it created; the product now bakes the title into the birth scope and keeps later descriptions only as `meta.purpose` rows. Ask: the owner membership (or a `write` grantee) may merge labels on that namespace; `manage` stays for cordon, delete and ownership changes. Oracle: A (read,write) creates and relabels its own namespace; B with read cannot; C with manage can.

Future ask, recorded under the P15 determinism invariant (user, 2026-09-06: statefs stores, it does not summarize): a **vector index type** over a stored `embedding` column (float32 array, dimension fixed per index), nearest-neighbor read endpoint taking a query vector; the product computes embeddings and query vectors with a model and writes them as rows. A tenant-level (cross-namespace) index namespace is the other future ask; until then the product maintains index namespaces itself.

Not requested from core: cross-namespace query (RFC-0008 wall 3 stays;
the product keeps per-tenant index namespaces), token-delta storage,
subscriber state in statefs (cursors live in the product, RFC-0002
boundary), index shipping (P15 invariant).

## 6. Questions back to the core session

1. P15 S1 start, and whether the §1 declaration can be the S2/S3 fixture.
2. btree v1 value types.
3. Blooms in S2, or hash-segment run compaction before S3 (§4 Q5).
4. Delta 1 ownership: engine work now, or the product measures the fd/RSS
   curve on the Linux PVC rig first (the same rig the attribution note
   says the 64-namespace degradation must be re-run on).
5. Delta 3 shape: position list, range list, or both.
6. Does the P15 heartbeat `ready@epoch` state reach the bearer plane
   (namespace GET), so a client can decide fast-path vs E1 itself?

## 7. How the product consumes each piece

| Product feature | Core pieces |
|---|---|
| Replay and follow a conversation | scan (existing) + delta 4 |
| "Every tool failure in this conversation" | bitmap `by_kind` + hash `by_tool` (P15 S2/S3) + delta 3 |
| Time window in a conversation | zonemaps (P15 S1 + P14 S5); btree only if pruning proves coarse |
| "Questions addressed to me since my cursor" on a shared conversation | hash `by_to` + positional scan from cursor + delta 3 |
| Nightly LLM summary of a conversation | delta 4 wakes the hook, zonemap window, delta 3 fetches rows, result appended as `meta.summary` |
| Console per-conversation stats | bitmap counts + manifest `count(*)` (P14 §2), zero row reads |
| Resume right after the last append | P14 `min_head` (S2) |
| A million live conversations | delta 1 |
| Safe retries from flaky laptops | delta 2 |

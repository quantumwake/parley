# statefs.io capabilities, read against the statefs.ai workload

Survey date: 2026-09-04. Source of truth: `../statefs` (monorepo, `go.work`).
Workload under evaluation: hundreds of thousands to millions of concurrent
agent chat streams, append heavy, each stream an unbounded log that must stay
replayable and queryable forever.

Vocabulary used here (statefs GLOSSARY): a **namespace** (state-id) is one
per-entity append-only stream with permanent global row positions. A
**member** is one engine + node process; a **group** is 1 primary + replicas;
the **directory** pins namespaces to groups at birth and referees leadership.

## 1. What exists (as built, deployed at statefs.io)

| Layer | Capability | Where | Relevance to chat streams |
|---|---|---|---|
| Engine | Schemaless `map[string]any` records to immutable Parquet blocks; JSON columns for nested values; int64 exact | `pkg/engine`, `pkg/parquet` | An event `{kind, role, content{...}}` maps directly; no schema declaration |
| Engine | Active-segment WAL: group-commit many small appends, fsync per append (`WALSync=always`), seal to one block per rotation | `pkg/engine/wal_engine.go`, RFC-0001 | Exactly the pub/sub-shaped write pattern (many tiny appends) it was built for |
| Engine | Appends serialized per namespace; namespaces append in parallel | `engine.go` per-ns lock | One writer per conversation is natural; conversations do not contend |
| Engine | Positional reads `Read(ns, offset, limit)`; reads union sealed blocks + unsealed memtable (RCU view) | `engine.go`, `wal_view.go` | Replay from any position; live tail visible immediately |
| Engine | Tiering: local PVC authoritative, S3 offload on compaction, disk read-through cache, snapshots by server-side copy | `pkg/tier`, `pkg/diskcache` | Cold conversations age to object storage at no product cost |
| Engine | `MaterializeLocal` for DuckDB, includes unsealed tail | `engine.go` | Per-conversation SQL over the whole history |
| Engine | `TruncateToFork`, `DeleteNamespace`, `Snapshot` | `engine.go` | Delete-a-conversation, freeze-and-share a conversation |
| Node | Write door (OPEN/DRAINING/FOLLOWER/SELF-FENCED), lease, rejoin gate, switchover, block bootstrap | `node/` | Failover proven zero-loss under load (P6 drill 2026-08-03) |
| Replication | Primary WAL-ship over gRPC, replica marks, retention floor, async (default) or sync (replica-confirmed ack, 202 on timeout) | `replication/`, RFC-0005 §11 | Durability dial per namespace or per call (`X-Durability: sync`) |
| Directory | Pin at birth (write-once, OCC), resolve to group + primary + term, never a data hop; pin ledger designed for billions of rows | `cluster/` | Millions of conversation pins is in-design; births are CP, routing is AP |
| Directory | Namespace scope (JSONB, GIN containment search), cordon (423), delete-everywhere, bulk export | `cluster/pkg/api` | Scope = `{firm, user, agent}` gives tenant-scoped listing for free |
| Auth (RFC-0011 v2, v0.5.x, 2026-09-05) | identity (person or service) x membership per tenant; authenticators (password, api key, registered keypair, `alg`-tagged); `POST /auth/token` -> 15 m acting token; 5 m grant tickets per namespace and verb, HKDF request MAC, members verify offline; membership grants; capabilities read/write/manage; enrollment tokens; namespace `display_name` + tags | `cluster/pkg/auth`, `cluster/pkg/vault`, `pkg/ticket`, `pkg/assertion` | Product rides it entirely: plugins exchange for acting tokens, the gateway mints tickets, never stores credentials |
| Query | Per-tenant DuckDB service, four isolation walls, AST gate: **one query = one namespace** | `query/`, RFC-0008; P14 access layer shipped v0.3.6 (52 ms warm) | Per-conversation analytics only; cross-conversation needs product-side index namespaces |
| Data plane API | `POST /api/v1/state/{ns}` JSON array, 8 MiB body cap, 307 redirect to leader, 503 when leaderless; `GET /api/v1/state/{ns}?offset&limit` | `node/cmd/statefs-member/main.go` | Simple; a gateway can batch per conversation into one POST |
| SDKs | Go `client/` (Route, Scan, leader-following Appender, CreateNamespace with scope, Find), Python `statefs-client` 0.1.1 | `client/`, `clients/python` | Go client is the integration seam for a Go gateway |
| Deploy (verified 2026-09-06) | DOKS ams3 `statefs-prod` on v0.5.4: directory x2, operator console, tenant console, one query service, groups a/b/c as StatefulSets x3 members, DO managed Postgres, TLS ingress `*.statefs.io`; release on every `v*` tag, CSI attach flake handled in `deploy.sh` | `deploy/do/`, helm revisions 23/23/19/18 | Live target for v1; member storage still demo-sized |

Measured ceilings (docs/evals/performance-matrix.md, throughput-attribution
2026-06-27, Apple M3 Max, re-run on Linux PVC still pending): with fsync per
append (`WALSync=always`) a member sustains ~18-22k rows/s regardless of how
many namespaces write concurrently (fsync-bound, ~5 ms per append+rotation);
with `interval`/`off` it reaches 330-500k rows/s at 1-8 concurrent
namespaces and degrades to ~100k at 64 (attributed to WAL write
serialization on APFS, not the manifest). Scale is by adding groups.

## 2. Gaps this workload hits (ordered by severity)

1. **No idle-namespace unload (engine).** A namespace touched since boot
   keeps an `nsHandle`, an open WAL writer fd and a memtable until it seals.
   Age-triggered rotation only seals once `FlushThreshold` (1000) rows are
   buffered, so a 200-event conversation sits open indefinitely. One million
   live conversations on one group means one million fds and memtables.
   Fix (engine seam): close idle handles after N minutes (WAL file stays, the
   existing `OpenForAppend(path, validBytes)` reopens on the next append), and
   allow a force-seal-on-idle policy so small conversations still reach
   Parquet. Verify: `pkg/engine/wal_view.go` `nsHandleFor`, `segment.go`.
2. **Append idempotency (open in ROADMAP, confirmed 2026-08-16).** A timed-out
   append may still commit; a retrying writer duplicates rows. Agent capture
   is at-least-once by nature (hooks, tailers, reconnects). Fix: client
   batch id deduped at the leader within a WAL window (statefs open work).
   Until then the product carries `event_id` per row and dedupes on read.
3. **Tail delivery exists only in replica form.** The ReplicaFeed
   `Subscribe` RPC (`replication/server.go`) pushes the WAL to a subscriber
   with a 10 ms head poll (1 s idle backoff) and heartbeats at head, so the
   push mechanism is built. But every subscriber is treated as a replica:
   `Subscribe` registers it on the marks board and sets the replication
   floor, so a lagging consumer pins WAL retention; the port (9001) is
   in-cluster pod DNS, not on the ingress; and there is no per-namespace
   auth on it. Consumer mode (no board entry, backfill from sealed blocks)
   is P3-D9, parked in P11. The HTTP read path has no long-poll. Product
   answer for v1: the gateway runs in-cluster and subscribes as a consumer
   once that mode lands; until then it polls the read endpoint for catch-up
   and fans out from its own ingest path for live followers.
4. **One query = one namespace (RFC-0008 wall 3).** "All my conversations
   this week" cannot be answered server-side. Product keeps per-tenant index
   namespaces (one row per conversation event of interest) and queries those.
5. **Compaction target is rows only** (`MaxRowsPerFile` 10k, no byte cap).
   10k rows of long tool outputs can produce multi-GB blocks. Product caps
   event payloads (default 256 KiB) and offloads larger bodies to blob storage
   with a pointer; a byte target in `planMerge` is the engine fix.
6. **Binary values rejected** (blob pointers parked, P11). Images and files in
   chats are pointers to object storage, never inline.
7. **Placement is consistent-hash only** (D4-6 decided, strategies unbuilt).
   Fine for uniform chat traffic; load-aware placement is open statefs work.
8. **Prod sizing.** 20Gi per member and a single shared query service are demo
   posture. Needs sizing before real traffic.

## 3. Numbers to design against

All from `docs/evals/` and `docs/reviews/` in statefs; Env A = Apple M3 Max
micro-benchmarks, kind = the P1 rig under the scripted drills.

| Quantity | Value | Source |
|---|---|---|
| Append, fsync per ack (`always`), one member, any N | 18-22k rows/s, ~9-12 MB/s | performance-matrix, attribution §3a |
| Append, `interval` (fsync <= 1 s) or `off`, 1-8 namespaces | 330-500k rows/s, 170-260 MB/s | performance-matrix |
| Append, `off`, 64 concurrent namespaces | ~100-240k rows/s (run-noisy, APFS artifact suspected) | attribution §1-3 |
| Append + rotation latency (`always`) | 5.2 ms (fsync) | performance-matrix |
| Tail read from memtable / evicted pread (200 rows) | 4 us / 368 us | performance-matrix |
| Compaction merge | 1.5-1.8M rows/s | performance-matrix |
| vs FrostDB, matched cadence, page-cache durable | 708k vs 812k rows/s (within 15%); rolling 864k | comparison-frostdb |
| Sustained through failover on kind | 2,000 rows/s across 6 namespaces, 5 kill cycles, zero acked loss | DRILL-REPORT-2026-08-03, -08-08 |
| Promotion after cold kill | 2.0 s (reaper cadence) | drill reports |
| Sync-ack timeout behavior | 202 leader-only, never a silent downgrade | RFC-0005 §11 |
| Append body cap | 8 MiB per POST | member main.go |
| WAL rotation defaults | 64 MiB / 30 s / 1000-row age guard | engine config |
| Query, warm (P14 access layer) | ~52 ms | 2026-08-16 session log |

The fsync row is the one that sizes this product: durability by fsync costs
~25x throughput; durability by replica confirmation (sync mode) is the
orthogonal dial (RFC-0005: ack = redundancy, fsync = each node's WAL mode).

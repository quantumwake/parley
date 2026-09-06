# statefs.ai activities (deferred work, decided-not-now)

| Item | Why deferred | Revisit when |
|---|---|---|
| Embedded `node.Node` adapter for `ConversationStore` | client/server chosen for v1; hop cost unmeasured | after S3, with a measured gateway->member hop |
| Competing consumer groups on shared conversations (work queues) | broadcast covers questions/comments/reports; groups need claim tracking | a task-queue use case appears |
| Cross-gateway fan-out (KRAP-style bus or the feed) | sticky routing by channel id is enough at one gateway per channel | followers must attach to any gateway |
| Removing interim workarounds: `event_id` dedupe on read, polling catch-up, live-conversation soft cap, inline body cap | wait for the core handoff (after RFC-0011): batch-id append, feed consumer mode, idle unload, byte compaction | each seam lands upstream |
| RFC-0011 ticket + MAC in the `Credential` adapter | data plane is anonymous as built | RFC-0011 §7 ships |
| Other sources: Agent SDK wrapper, Codex tailer, API proxy | Claude Code first | S2 oracle green |
| Console (S4) | after replay/follow API | S3 |
| Competing naming: console copy "organization" vs model "tenant" | RFC-0011 §2 allows display-only | console copy review |

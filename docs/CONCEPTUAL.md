# statefs.ai, conceptual architecture (pre-finalization)

Status: conceptual, 2026-09-05; v1 decisions closed 2026-09-06 (RFC-0001 §11). Diagrams track [RFC-0001](rfcs/RFC-0001-conversation-log-platform.md);
when the open questions in RFC-0001 §11 close, this file becomes `ARCHITECTURE.md` (as-built) and stops changing shape.
Engine facts come from [STATEFS-CAPABILITIES.md](STATEFS-CAPABILITIES.md).

Vocabulary: **channel** = one statefs namespace. A **conversation** is a
single-writer channel with no tracked readers. A **shared conversation** is a
multi-writer channel whose readers each keep a cursor. An **event** is one
row: a completed block, never a token delta.

Color convention in every diagram: **amber** = client machines (plugins, people),
**green** = statefs.ai (the product), **blue** = statefs.io (engine and cluster),
**grey** = external stores. A box's color says who owns and deploys it.

## C1. System context

Three layers, top to bottom: where events are produced, the product, and
the engine. Control traffic (resolve, auth) is dashed; data traffic is solid.
Library-first (RFC-0001 #9): every green box embeds the same statefs.ai
library; no product process sits between a client and its channel.

```mermaid
flowchart TB
    subgraph L1["Producers and consumers (client machines)"]
        direction LR
        cc["Claude Code<br/>capture plugin (embeds the library)"]
        sdk["Agent SDK<br/>wrapper (embeds the library)"]
        user["People<br/>(browser, notebook)"]
    end

    subgraph L2["statefs.ai (this repo)"]
        direction LR
        lib["library<br/>schema · naming · store port · spool · channel"]
        ui["console<br/>replay · follow · search · channel"]
        jobs["job runner (M5)<br/>summaries · embeddings · labels · graph"]
        shell["gateway shell (M6, on trigger)<br/>S13 ingest · S14 read · push"]
    end

    subgraph L3["statefs.io (engine + cluster, deployed)"]
        direction LR
        dir["directory<br/>pins · identity · tickets · search"]
        grp["groups<br/>members, RF 3"]
        q["query service<br/>per tenant, DuckDB"]
        s3[("S3 tier")]
    end

    cc -.embeds.-> lib
    sdk -.embeds.-> lib
    user --> ui
    cc -->|"append · scan · subscribe"| grp
    sdk --> grp
    cc -.->|"token · ticket · resolve"| dir
    ui -.->|"token · search"| dir
    ui -->|"scan"| grp
    ui -->|"SQL"| q
    jobs -->|"scan · append rows and labels"| grp
    shell -.-> lib
    q --> grp
    grp --> s3
    classDef client fill:#3a2b1b,stroke:#d0a03a,color:#fff7e8
    classDef product fill:#1b3a2b,stroke:#3ad07a,color:#e8fff2
    classDef core fill:#1e3a5f,stroke:#5aa9ff,color:#e8f1ff
    classDef ext fill:#2b2b2b,stroke:#8a8a8a,color:#eeeeee
    class cc,sdk,user client
    class lib,ui,jobs,shell product
    class dir,grp,q core
    class s3 ext
    style L1 stroke:#d0a03a,stroke-dasharray:4 3
    style L2 stroke:#3ad07a,stroke-dasharray:4 3
    style L3 stroke:#5aa9ff,stroke-dasharray:4 3
```

Reading the edges: a client resolves a channel once through the directory,
then appends and scans against members directly (statefs locked decision:
the directory is never a data hop). "Subscribe" is a scan from a client-held
cursor, polled today, switched to the feed consumer mode when it lands. The
job runner is the first statefs.ai process and it only reads rows and
writes rows and labels back. The gateway shell exists only for triggers the
library cannot serve: an API proxy source, push to laptops, quotas.

## C2. Containers

```mermaid
flowchart TB
    subgraph plugin["capture plugin (per user machine)"]
        hooks["hook runner<br/>SessionStart · UserPromptSubmit · PreToolUse ·<br/>PostToolUse · SubagentStart/Stop · Stop · SessionEnd"]
        tail["transcript tailer<br/>thinking · text · tool_use · tool_result blocks"]
        spool["local spool<br/>append-only, event_id, ack offset"]
        push["push<br/>spool → channel"]
        mcp["MCP tool server<br/>history.* · channel.*"]
        skill["SKILL.md / CLAUDE.md<br/>publish cadence, how to ask, how to resume"]
        cursors["cursor files<br/>(shared conversations)"]
        hooks --> spool
        tail --> spool
        spool --> push
        skill -.drives.-> mcp
        mcp --> cursors
    end

    subgraph library["statefs.ai library (embedded)"]
        ev["event schema + Validate"]
        naming["naming: scope tags, display_name"]
        chan["channel: Open · Append (coalesce, dedupe) · Scan · Subscribe"]
        port["ConversationStore port"]
        ad["statefs adapter<br/>Go SDK: credential ladder · tickets · append · scan · find"]
        chan --> port --> ad
    end

    subgraph server["statefs.ai processes (later)"]
        jobs["job runner (M5)"]
        shell["gateway shell (M6)"]
    end

    push --> chan
    mcp --> chan
    jobs --> chan
    shell --> chan
    ad --> io[("statefs.io<br/>directory · members · query")]
    classDef client fill:#3a2b1b,stroke:#d0a03a,color:#fff7e8
    classDef product fill:#1b3a2b,stroke:#3ad07a,color:#e8fff2
    classDef core fill:#1e3a5f,stroke:#5aa9ff,color:#e8f1ff
    class hooks,tail,spool,push,mcp,skill,cursors client
    class ev,naming,chan,port,ad,jobs,shell product
    class io core
    style plugin stroke:#d0a03a,stroke-dasharray:4 3
    style library stroke:#3ad07a,stroke-dasharray:4 3
    style server stroke:#3ad07a,stroke-dasharray:4 3
```

The plugin keeps no channel data: the spool is a delivery buffer, the
cursor files are read positions. The library is the only place that knows
statefs.io; the plugin, the console, the job runner and a future shell all
call it the same way.

## C3. Library components and ports (hexagonal)

```mermaid
flowchart LR
    subgraph inbound["inbound ports"]
        i1["Ingest<br/>batch of events"]
        i2["Tools<br/>publish · read · ask · search · replay · follow"]
        i3["Stream<br/>SSE follow"]
    end

    subgraph core["core (no I/O)"]
        map["ChannelMapper<br/>session → channel, birth with scope"]
        coal["Coalescer"]
        dedupe["Dedupe by event_id"]
        cursors["CursorService"]
        fanout["FanOut"]
    end

    subgraph outbound["outbound ports"]
        o1["ConversationStore<br/>Open · Append · Scan · Head · Subscribe"]
        o2["CursorStore"]
        o3["Authorizer<br/>tenant · user · channel claims"]
        o6["Credential<br/>acting token in · grant ticket + HKDF MAC out (RFC-0011 v2)"]
        o4["BlobStore<br/>bodies over 256 KiB"]
        o5["Query<br/>SQL per channel / index namespace"]
    end

    i1 --> map --> coal --> dedupe --> o1
    i2 --> cursors --> o2
    i2 --> o1
    i2 --> o5
    i3 --> fanout
    o1 -->|"feed entries"| fanout
    map --> o3
    o1 --> o6
    dedupe --> o4
    classDef client fill:#3a2b1b,stroke:#d0a03a,color:#fff7e8
    classDef product fill:#1b3a2b,stroke:#3ad07a,color:#e8fff2
    classDef core fill:#1e3a5f,stroke:#5aa9ff,color:#e8f1ff
    classDef ext fill:#2b2b2b,stroke:#8a8a8a,color:#eeeeee
    class i1,i2,i3,map,coal,dedupe,cursors,fanout,o2 product
    class o1,o3,o5,o6 core
    class o4 ext
    style inbound stroke:#3ad07a,stroke-dasharray:4 3
    style core stroke:#3ad07a,stroke-dasharray:4 3
    style outbound stroke:#5aa9ff,stroke-dasharray:4 3
```

Outbound ports are colored by who answers them: blue ports are served by statefs.io, grey by an external store, green by product-owned state. Inbound ports are the library's Go API today; the same three become HTTP (S13, S14) when the gateway shell exists.

Adapters per port, v1: `ConversationStore` = statefs Go client + feed
gRPC; `CursorStore` = statefs namespace per tenant; `Authorizer` = statefs
API key introspection with the `tenant` claim; `Credential` = the caller's
acting token forwarded to mint 5 m grant tickets per namespace and verb,
requests signed with the HKDF MAC (RFC-0011 v2 as built, v0.5.x); `BlobStore` = S3;
`Query` = the tenant query service. Each is swappable without touching the
core. Identity is never product-owned: tenants, users, keys and grants are
RFC-0011's, and the console reuses its tenant admin API.

## Conversation shapes

```mermaid
flowchart TB
    subgraph conv["conversation, one participant (an agent's log)"]
        w1["one writer: the session"] --> n1["namespace<br/>scope {tenant, user, agent, session}"]
        n1 --> r1["ad hoc readers<br/>replay · follow · SQL<br/>(no tracked position)"]
    end

    subgraph shared["conversation, many participants (a shared stream)"]
        wa["session A"] & wb["session B"] & wc["bot / console"] --> n2["namespace<br/>scope {tenant, conversation}<br/>leader serializes appends = total order"]
        n2 --> ca["consumer A<br/>cursor"] & cb["consumer B<br/>cursor"] & cc2["consumer C<br/>cursor"]
    end

    subgraph idx["index channel (per tenant, product-owned)"]
        n3["namespace: conversations<br/>one row per birth · end · summary"]
    end

    n1 -.birth / end / summary.-> n3
    classDef client fill:#3a2b1b,stroke:#d0a03a,color:#fff7e8
    classDef product fill:#1b3a2b,stroke:#3ad07a,color:#e8fff2
    classDef core fill:#1e3a5f,stroke:#5aa9ff,color:#e8f1ff
    classDef ext fill:#2b2b2b,stroke:#8a8a8a,color:#eeeeee
    class w1,wa,wb,wc client
    class r1,ca,cb,cc2 product
    class n1,n2,n3 core
```

## Flow 1. Capture a conversation (story 1)

```mermaid
sequenceDiagram
    box rgb(58,43,27) client machine
    participant CC as Claude Code
    participant H as hook runner
    participant T as transcript tailer
    participant S as spool
    participant P as push (library)
    end
    box rgb(30,58,95) statefs.io
    participant D as directory
    participant M as member (leader)
    end

    CC->>H: SessionStart {session_id, transcript_path}
    H->>S: session.start
    H->>T: start tailing transcript_path
    CC->>H: UserPromptSubmit
    H->>S: user.message
    T->>S: assistant.thinking, assistant.text (by block uuid)
    CC->>H: PreToolUse / PostToolUse {tool_input, tool_output}
    H->>S: tool.use, tool.result (parent = tool_use_id)
    P->>D: token exchange · open namespace (scope, display_name) · ticket(append)
    P->>M: append batch (coalesced, event_id each)
    M-->>P: positions
    P->>S: advance ack offset
    CC->>H: SessionEnd
    H->>S: session.end
    P->>M: append session.end (sync)
```

Positions are assigned by statefs; the client `seq` and `event_id` make
retries safe until the batch-id seam lands upstream.

## Flow 2. Shared conversations publish and consume (story 2)

```mermaid
sequenceDiagram
    box rgb(58,43,27) session A machine
    participant A as session A (asker)
    participant HA as hooks / MCP (A, library)
    end
    box rgb(30,58,95) statefs.io
    participant M as shared conversation namespace (leader)
    end
    box rgb(58,43,27) session B machine
    participant HB as hooks / MCP (B, library)
    participant B as session B (answerer)
    end

    Note over A,HA: SKILL.md: "post a question when blocked"
    A->>HA: conversation.ask(question, to: *)
    HA->>M: append post.question (writer = A)
    M-->>HA: position p
    HA->>HA: cursor[A] = p

    B->>HB: UserPromptSubmit (next turn)
    HB->>M: scan from cursor[B]
    M-->>HB: [post.question @p]
    HB-->>B: additional context: "A asks ..."
    B->>HB: post.answer(reply_to = question)
    HB->>M: append post.answer (writer = B)

    A->>HA: Stop / next turn
    HA->>M: scan from cursor[A]
    M-->>HA: [post.answer]
    HA-->>A: thread closed
```

Consume happens at turn boundaries through hooks, so no agent polls between
turns. Membership is a grant on the shared conversation namespace; a session without one is
refused by the member.

## Flow 3. Follow live

```mermaid
sequenceDiagram
    box rgb(58,43,27) client
    participant C as console / MCP (library Subscribe)
    end
    box rgb(30,58,95) statefs.io
    participant M as member read
    participant F as feed (gRPC :9001, in-cluster)
    end

    C->>M: scan [cursor, head)
    M-->>C: rows
    loop today: poll every 1 s
        C->>M: scan from new cursor
        M-->>C: rows or empty
    end
    Note over C,F: later: Subscribe rides the feed consumer mode<br/>in-cluster directly; laptops through the gateway shell (M6)
```

## Flow 4. Session capabilities (story 3)

```mermaid
flowchart LR
    S["session"] -->|"history.search"| Q["tenant query service<br/>own conversations / index namespace"]
    S -->|"history.resume"| SC["scan tail of a conversation into context"]
    S -->|"conversation.read / ask / publish / search"| T["shared conversation via gateway"]
    S -->|"conversation.follow"| F["SSE follow"]
    SK["SKILL.md"] -.when and how.-> S
    classDef client fill:#3a2b1b,stroke:#d0a03a,color:#fff7e8
    classDef product fill:#1b3a2b,stroke:#3ad07a,color:#e8fff2
    classDef core fill:#1e3a5f,stroke:#5aa9ff,color:#e8f1ff
    classDef ext fill:#2b2b2b,stroke:#8a8a8a,color:#eeeeee
    class S,SK client
    class T,F,SC product
    class Q core
```

## Deployment view (v1 target)

```mermaid
flowchart TB
    subgraph user["user machine"]
        cc["Claude Code + capture plugin"]
    end
    subgraph doks["DOKS statefs-prod (ams3)"]
        ing["ingress *.statefs.io / *.statefs.ai (TLS)"]
        gw["job runner (M5) · gateway shell (M6)"]
        con["console"]
        dir["directory ×2"]
        g1["group-a ×3"] & g2["group-b ×3"] & g3["group-n ×3"]
        q["query service (per tenant)"]
        pg[("DO managed Postgres")]
        s3[("DO Spaces")]
    end
    cc --> ing
    ing --> con
    cc -->|"direct: token, ticket, append, scan"| g1 & g2 & g3
    gw --> dir --> pg
    gw -->|"scan · append rows and labels"| g1 & g2 & g3
    con --> q --> g1 & g2 & g3
    g1 & g2 & g3 --> s3
    classDef client fill:#3a2b1b,stroke:#d0a03a,color:#fff7e8
    classDef product fill:#1b3a2b,stroke:#3ad07a,color:#e8fff2
    classDef core fill:#1e3a5f,stroke:#5aa9ff,color:#e8f1ff
    classDef ext fill:#2b2b2b,stroke:#8a8a8a,color:#eeeeee
    class cc client
    class ing ext
    class gw,con product
    class dir,g1,g2,g3,q core
    class pg,s3 ext
    style user stroke:#d0a03a,stroke-dasharray:4 3
    style doks stroke:#5aa9ff,stroke-dasharray:4 3
```

The product console is a third app beside statefs's operator console and tenant console (RFC-0011 §6a); it signs in with user credentials only and defers identity management to the tenant console.

In v1 the only statefs.ai deployables are the console and, from M5, the
job runner; the gateway shell is drawn for M6.

## Things this shape deliberately does not do

- No token deltas as rows; blocks only.
- No data through the directory; resolve once, direct after.
- No channel data in the gateway; cursors are its only state.
- No relaxation of the one-namespace query wall; cross-channel questions go through the index channel.
- No embedding of the engine in v1; the port keeps that a later adapter.
- No product process between a client and its channel; the shell is for triggers the library cannot serve.
- No model calls in statefs.io; enrichment is a job that writes rows back.

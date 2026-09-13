# Layout: parley from C0 to C4

Status: 2026-09-11, as built at 0.3.0. For review.

One binary, three entry points, all called parley:

| Face | Entry point | What it is |
|---|---|---|
| the plugin | `parley hook`, `parley daemon` | wraps Claude Code's hooks so every session is captured |
| the tools | `parley mcp` | an MCP server: create, join, post, read, search, grant |
| the CLI | everything else, plus `parley console` | enroll, status, the viewer, and the same operations by hand |

They share one identity resolution, one store adapter and the same
functions underneath, so they cannot drift. Colours throughout: amber is
the client side, green is statefs.ai, blue is statefs.io, grey is external.

## C0. Landscape

Where parley sits among the systems it touches.

```mermaid
flowchart LR
    subgraph EXT["external"]
        GH["GitHub<br/>marketplace, releases"]
        AN["Anthropic<br/>the models"]
    end
    subgraph CLIENT["a person's machine"]
        CC["Claude Code"]
        P["parley"]
    end
    subgraph AI["statefs.ai (products)"]
        PL["parley<br/>this product"]
        OT["other products<br/>on the same engine"]
    end
    subgraph IO["statefs.io (engine, cluster)"]
        D["directory"]
        M["member groups"]
        Q["query service"]
        CON["tenant console"]
    end

    CC --- P
    P --> PL
    PL --> D & M & Q
    OT --> D & M & Q
    GH --> P
    CC --> AN
    CON --> D

    classDef client fill:#3a2b1b,stroke:#d0a03a,color:#fff7e8
    classDef product fill:#1b3a2b,stroke:#3ad07a,color:#e8fff2
    classDef core fill:#1e3a5f,stroke:#5aa9ff,color:#e8f1ff
    classDef ext fill:#2b2b2b,stroke:#8a8a8a,color:#eeeeee
    class CC,P,CLIENT client
    class PL,OT,AI product
    class D,M,Q,CON,IO core
    class GH,AN,EXT ext
```

statefs.ai is one product on the engine. The engine stores, indexes what it
stores, and enforces grants. Nothing in the engine calls a model.

## C1. System context

Who uses parley and what it depends on.

```mermaid
flowchart TB
    PERSON["person<br/>runs sessions, reads the console"]
    ADMIN["tenant admin<br/>mints enrollment tokens"]
    AGENT["other agents<br/>swarm, CI, another session"]

    SYS["parley<br/>records sessions, shares conversations,<br/>searches what an identity may see"]

    DIR["statefs.io directory<br/>identity, tokens, grants, scope search"]
    MEM["statefs.io members<br/>append, scan, head"]
    QRY["statefs.io query service<br/>one query, one namespace"]
    GHB["GitHub<br/>plugin updates, binaries"]

    PERSON --> SYS
    ADMIN -.->|"enrollment URL"| PERSON
    AGENT <--> SYS
    SYS --> DIR & MEM & QRY
    GHB --> SYS

    classDef client fill:#3a2b1b,stroke:#d0a03a,color:#fff7e8
    classDef product fill:#1b3a2b,stroke:#3ad07a,color:#e8fff2
    classDef core fill:#1e3a5f,stroke:#5aa9ff,color:#e8f1ff
    classDef ext fill:#2b2b2b,stroke:#8a8a8a,color:#eeeeee
    class PERSON,ADMIN,AGENT client
    class SYS product
    class DIR,MEM,QRY core
    class GHB ext
```

## C2. Containers

The runnable pieces. Everything green is the one `parley` binary or what it
embeds and keeps on disk.

```mermaid
flowchart LR
    subgraph CC["Claude Code session"]
        HOOKS["hook events"]
        MCPC["MCP client"]
        TR["transcript.jsonl"]
    end

    subgraph PARLEY["parley (one binary)"]
        HOOK["parley hook<br/>per event: spool, inject, spawn"]
        DAEMON["parley daemon<br/>per session: tail, push, summarise"]
        MCP["parley mcp<br/>stdio JSON-RPC, 10 tools"]
        CLI["parley &lt;cmd&gt;<br/>enroll, status, find, labels, post…"]
        CONSOLE["parley console<br/>local API + embedded viewer"]
    end

    subgraph DISK["local state"]
        ID[("~/.statefs/identity<br/>~/.statefs/identities/*")]
        DATA[("~/.statefs-ai<br/>config, spool, names,<br/>subscriptions, latest.json")]
    end

    subgraph IO["statefs.io"]
        D["directory"]
        M["members"]
        Q["query"]
    end

    HOOKS --> HOOK
    HOOK --> DATA
    HOOK -->|"exec"| DAEMON
    TR --> DAEMON
    DAEMON --> DATA
    DAEMON --> D & M
    MCPC <--> MCP
    MCP --> D & M
    CLI --> D & M
    CONSOLE --> D & M
    CONSOLE -.->|"later: search"| Q
    HOOK & MCP & CLI & CONSOLE & DAEMON --> ID

    classDef client fill:#3a2b1b,stroke:#d0a03a,color:#fff7e8
    classDef product fill:#1b3a2b,stroke:#3ad07a,color:#e8fff2
    classDef core fill:#1e3a5f,stroke:#5aa9ff,color:#e8f1ff
    class HOOKS,MCPC,TR,CC client
    class HOOK,DAEMON,MCP,CLI,CONSOLE,PARLEY,ID,DATA,DISK product
    class D,M,Q,IO core
```

| Container | Lifetime | Talks to |
|---|---|---|
| `parley hook` | one process per hook event, milliseconds | spool; the directory only at SessionStart and for injection |
| `parley daemon` | one per session (lock file), until its last `session.end` or idle; resumes keep it or restart it | transcript, spool, directory, members |
| `parley mcp` | one per session, started by Claude Code from `.mcp.json` | directory, members |
| CLI commands | one process each | directory, members |
| `parley console` | until closed; serves a local API and the React viewer | directory, members |
| local state | on disk | identities under `~/.statefs`, everything else under `~/.statefs-ai` |

## C3. Components

The Go packages, and how the three faces share them.

```mermaid
flowchart TB
    subgraph CMD["cmd/parley"]
        MAIN["main: subcommand dispatch, help"]
    end

    subgraph PLUGIN["pkg/plugin — the product logic"]
        HK["hook.go<br/>Handle, EnvFromProcess, spawnDaemon"]
        DM["daemon.go<br/>RunDaemon"]
        SH["shared.go<br/>Create, Join, Leave, Post, Read,<br/>Inject, Grant, Subscriptions"]
        LB["labels.go · find.go · describe.go"]
        WH["whoami.go · update.go · pathinstall.go"]
    end

    subgraph MCPP["pkg/mcp"]
        SRV["mcp.go<br/>JSON-RPC over stdio"]
        TL["tools.go<br/>10 tools → pkg/plugin"]
    end

    subgraph CONS["pkg/console + console/"]
        API["console.go<br/>/v1/me, conversations,<br/>events, posts, subscriptions"]
        SPA["React viewer<br/>embedded in the binary"]
    end

    subgraph CAP["pkg/capture"]
        FH["hooks.go<br/>FromHook: event → row"]
        TA["transcript.go<br/>Tailer: text, thinking"]
        PU["push.go<br/>Pusher: spool → store"]
    end

    subgraph CORE["shared foundations"]
        EV["pkg/event<br/>the envelope"]
        NM["pkg/naming<br/>names, scope, titles"]
        SP["pkg/spool<br/>append-only file + ack"]
        CV["pkg/conversation<br/>Writer, Attach, Head"]
        ST["pkg/store<br/>Store port + fake + file"]
        SA["pkg/store/statefs<br/>adapter on the Go client"]
        EN["pkg/enroll<br/>keygen, enroll, verify"]
    end

    MAIN --> PLUGIN & MCPP & CONS & EN
    TL --> SH & LB & WH
    API --> SH & LB
    HK --> FH & SP & DM
    DM --> TA & PU
    PU --> CV & SP
    SH --> CV & ST
    CV --> ST
    ST --> SA
    FH & TA & SH --> EV
    PU & SH --> NM

    classDef product fill:#1b3a2b,stroke:#3ad07a,color:#e8fff2
    classDef core fill:#1e3a5f,stroke:#5aa9ff,color:#e8f1ff
    class MAIN,CMD,HK,DM,SH,LB,WH,PLUGIN,SRV,TL,MCPP,API,SPA,CONS,FH,TA,PU,CAP product
    class EV,NM,SP,CV,ST,SA,EN,CORE product
```

The rule this diagram enforces: **the MCP tools and the console API call
`pkg/plugin`, never the store directly.** A behaviour exists once, in
`pkg/plugin`, and every face gets it.

## C4. Code

The types that carry the design decisions.

```mermaid
classDiagram
    class Event {
        +ID ULID
        +Seq int64
        +TSMs int64
        +SessionID string
        +Kind Kind
        +Role Role
        +Identity string  "who wrote it, self-declared"
        +Participant string  "handle, posts only"
        +To string
        +ReplyTo string
        +Thread string
        +Indexable bool
        +Content json
        +Validate() error
    }
    class Store {
        <<interface>>
        +Open(display, scope) Namespace
        +Append(ns, events, sync) Position
        +Scan(ns, from, to) iter
        +Head(ns) Position
        +Find(filter, limit) []Namespace
        +Describe(ns, labels)
    }
    class Pusher {
        +Store Store
        +Session spool.Session
        +Run(ctx)
        -open(first) "namespace born with its title"
        -drain(off) "seq by delivery, session.end last"
    }
    class Subscription {
        +Name string
        +ID string
        +Mode full|digest
        +Cursor int64
        +Participant string  "declared at join"
    }
    class Tool {
        +Name string
        +Description string
        +Schema json
        +Call(ctx, Args, Writer) error
    }
    class Server {
        +Tools []Tool
        +Serve(ctx, in, out)
        -handle(req) "initialize, tools/list, tools/call"
    }

    Pusher --> Store
    Pusher --> Event
    Subscription --> Event : "Participant stamped on posts"
    Server --> Tool
    Tool ..> Store : "via pkg/plugin"
```

Four decisions live in these types:

- `Event.Identity` and `Event.Participant` replace `author`. Neither is
  attested by anything; see [participants](05_participants.md).
- `Store` is a port. The statefs adapter is one implementation; the fake
  and the file store make the whole pipeline testable offline.
- `Pusher.open` is where a conversation is born with its title, because a
  read/write key cannot relabel after birth.
- `Subscription.Participant` is where a handle lives, so a post can carry it
  without the posting process knowing which session it is in.

## What is not in the picture yet

Index namespaces and synopsis rows, the daemon's summarisation cadence, and
teams are designed in the `statefs.ai` repo. None of it exists in the code
above.

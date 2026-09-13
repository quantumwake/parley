# Agent hooks and tools

How a Claude Code session becomes a conversation on statefs.io, and what an
agent can do with parley from inside a turn. Five diagrams, as built in
v0.3.3. Colors follow CONCEPTUAL.md: amber is the client (Claude Code),
green is statefs.ai (parley), blue is statefs.io, grey is external.

Reading order: H1 the hook wiring, H2 the capture pipeline, H3 the tool
surface, H4 one shared-conversation turn, H5 the swarm topology.

## H1. Hook wiring: every Claude Code event runs `parley hook`

`hooks/hooks.json` binds eight events to one command. The hook reads the
event on stdin, spools what is capturable, and answers on stdout. Three
events talk back to the model: SessionStart (a context line),
UserPromptSubmit (the injected digest), and Stop (new posts, as a block that
keeps the agent working).

```mermaid
flowchart LR
    subgraph CC["Claude Code session (client)"]
        direction TB
        E1[SessionStart]
        E2[UserPromptSubmit]
        E3[PreToolUse]
        E4[PostToolUse]
        E5[SubagentStart]
        E6[SubagentStop]
        E7[Stop]
        E8[SessionEnd]
    end

    subgraph HOOK["parley hook (one process per event)"]
        direction TB
        H0["decode event<br/>FromHook → event row"]
        H1["spool.Append<br/>~/.statefs-ai/spool/&lt;session&gt;.jsonl"]
        H2["sessionStart():<br/>enrolled? → context line<br/>EnsurePath() → launcher on PATH<br/>ensureDaemon() (lock guard)"]
        H3["ensureDaemon() (lock guard)<br/>Inject():<br/>new posts in followed conversations<br/>≤ 20 posts, ≤ 8 KiB, ≤ 2 KiB per post<br/>mine-first, this session's posts skipped"]
        H5["Stop, unless stop_hook_active:<br/>Inject() → decision block"]
        H4["logHook → hooks.log"]
    end

    subgraph OUT["back to the model"]
        O1["additionalContext:<br/>'this machine is enrolled as …'<br/>or 'not enrolled, run parley enroll'"]
        O2["additionalContext:<br/>'statefs.ai parley: new posts …'"]
        O3["reason:<br/>new posts, handle before ending the turn"]
    end

    E1 & E2 & E3 & E4 & E5 & E6 & E7 & E8 --> H0 --> H1
    H0 --> H4
    E1 -.-> H2 --> O1
    E2 -.-> H3 --> O2
    E7 -.-> H5 --> O3
    H2 -->|"exec parley daemon"| D[("capture daemon<br/>(see H2)")]

    classDef client fill:#3a2b1b,stroke:#d0a03a,color:#fff7e8
    classDef product fill:#1b3a2b,stroke:#3ad07a,color:#e8fff2
    classDef core fill:#1e3a5f,stroke:#5aa9ff,color:#e8f1ff
    classDef ext fill:#2b2b2b,stroke:#8a8a8a,color:#eeeeee
    class E1,E2,E3,E4,E5,E6,E7,E8,CC client
    class H0,H1,H2,H3,H4,H5,D,HOOK product
    class O1,O2,O3,OUT client
```

What each event contributes to the conversation:

| Event | Row kind | Role | Note |
|---|---|---|---|
| SessionStart | `session.start` | system | held until the first prompt so the namespace is born with a title |
| UserPromptSubmit | `user.message` | user | the first one names the conversation (`TitleFromPrompt`) |
| PreToolUse | `tool.use` | assistant | tool name, id, input |
| PostToolUse | `tool.result` | tool | output, error flag |
| SubagentStart / SubagentStop | `subagent.start` / `subagent.stop` | system | agent id and type |
| Stop | (spooled, no row) | | marks the end of a turn |
| SessionEnd | `session.end` | system | delivered last, with sync durability |

Assistant text and thinking never pass through a hook. Claude Code does not
emit them as events, so the daemon tails the transcript file for them (H2).

## H2. Capture pipeline: hooks and transcript tailer into one spool, one pusher

One daemon per session, held to one by an exclusive lock file
(`daemon-<session>.lock`) that the OS releases when the process dies. The
SessionStart and UserPromptSubmit hooks start it when nobody holds the
lock, so a daemon that died or went idle comes back on the next prompt. It
exits after its last `session.end` is delivered, or after four hours with
no new spool rows.

```mermaid
sequenceDiagram
    autonumber
    participant CC as Claude Code
    participant HK as parley hook
    participant SP as spool (jsonl + ack)
    participant TL as Tailer (transcript.jsonl)
    participant PU as Pusher
    participant IO as statefs.io directory + node

    CC->>HK: SessionStart {session_id, transcript_path, cwd}
    HK->>SP: append session.start (held)
    HK-->>CC: context line
    HK->>PU: spawn `parley daemon --session --transcript --cwd`
    PU->>TL: follow transcript from offset 0

    CC->>HK: UserPromptSubmit {prompt}
    HK->>SP: append user.message
    HK-->>CC: injected digest (if any)
    PU->>SP: read from ack offset
    Note over PU: first prompt seen → title → Open(display name, scope)
    PU->>IO: create namespace  agent/2026-09-07T11:40:05/slug#tag  with scope kind, mode=agent, session, agent, date, started_ms, title
    IO-->>PU: namespace id
    PU->>IO: Append(session.start, user.message)  seq 1..n
    PU->>SP: ack offset

    loop every turn
        CC->>HK: PreToolUse / PostToolUse / Subagent* / Stop
        HK->>SP: append rows
        TL->>SP: assistant.text, assistant.thinking (from the transcript)
        PU->>IO: Append(batch, walsync interval)
        PU->>SP: ack
    end

    CC->>HK: SessionEnd
    HK->>SP: append session.end (sync)
    PU->>PU: BeforeEnd: wait for the transcript to go quiet (1.5 s quiet, 10 s max)
    PU->>IO: Append(late rows) then Append(session.end, sync=true)
    PU->>SP: ack
    PU->>PU: release the lock, exit unless a row was spooled since
```

Durability and order: seq is assigned in delivery order and continues from
the conversation's head when a daemon opens a conversation that already has
rows; a run's `session.end` is its last row; a restart resumes from the ack
offset and the writer's dedupe window covers a batch that landed but was
not acked.

**Resume.** `claude --resume` keeps the session id and the transcript path
(SessionStart carries `source: resume`), so a resumed run appends to the
same spool and lands in the same conversation, after the first run's
`session.end`:

- A `session.end` followed by a `session.start` is delivered in place and
  the push goes on; the daemon does not stop at it.
- Every hook spools its row before it checks the lock, and a daemon that
  has delivered its last `session.end` releases the lock before it looks for
  new rows. A resume is therefore picked up either by the finishing daemon
  or by the one its hook starts, never by neither.
- A daemon started for a resumed session re-reads the transcript from the
  start and skips the blocks already in the spool.

## H3. The tool surface: what `parley` gives an agent

The agent has no skill file. It gets one context line at SessionStart and
`parley --help`; everything else is ordinary shell. Grouped as the help is.

```mermaid
flowchart TB
    subgraph AGENT["an agent's turn (client)"]
        A["Bash: parley …"]
    end

    subgraph SETUP["setup"]
        S1["enroll &lt;url&gt;<br/>keygen local · POST /auth/enroll<br/>caps = what the token grants"]
        S2["whoami [--identity name]<br/>exchange → caps, admin"]
        S3["status · install-path · version"]
    end

    subgraph OWN["my conversations"]
        M1["find --tags --since<br/>list my sessions"]
        M2["describe &lt;conv&gt; --title --description --tags<br/>relabel (own)"]
        M3["replay &lt;conv&gt;<br/>print a conversation"]
        M4["console<br/>local viewer + API"]
    end

    subgraph SHARED["shared conversations"]
        C1["create &lt;name&gt; --description --tags<br/>owner = me"]
        C2["grant &lt;name&gt; --user --access<br/>owner shares (own)"]
        C3["list · join --mode full|digest · leave · subscriptions"]
        C4["post &lt;name&gt; --kind --text --to --reply-to<br/>one row: identity + handle"]
        C5["read &lt;name&gt; --from --peek --wait 90s<br/>full bodies; blocks for new rows"]
        C6["delete &lt;name&gt;<br/>owner or admin"]
    end

    subgraph LOCAL["local state  ~/.statefs-ai"]
        L1[("config.json<br/>spool/ names/ subscriptions/<br/>swarm/&lt;agent&gt;/state")]
    end

    subgraph IO["statefs.io"]
        D["directory<br/>/auth/token · namespaces · scope find · grants"]
        N["node<br/>append · scan · head"]
    end

    A --> SETUP & OWN & SHARED
    S1 & S2 --> D
    M1 & M2 --> D
    M3 --> N
    C1 & C2 & C6 --> D
    C3 --> D & L1
    C4 --> N
    C5 --> N & L1
    M4 --> D & N & L1

    classDef client fill:#3a2b1b,stroke:#d0a03a,color:#fff7e8
    classDef product fill:#1b3a2b,stroke:#3ad07a,color:#e8fff2
    classDef core fill:#1e3a5f,stroke:#5aa9ff,color:#e8f1ff
    class A,AGENT client
    class S1,S2,S3,M1,M2,M3,M4,C1,C2,C3,C4,C5,C6,L1,SETUP,OWN,SHARED,LOCAL product
    class D,N,IO core
```

### The same operations as MCP tools

`parley mcp` serves the shared-conversation operations to an MCP client over
stdio, and the plugin declares it, so an agent calls them directly instead of
building a shell command. Both surfaces call the same functions, so they
cannot drift.

| Tool | Wraps |
|---|---|
| `search_conversations` | discover what exists, by text or tag |
| `create_conversation`, `grant_access` | make one and share it (needs `own`) |
| `join_conversation`, `leave_conversation`, `my_subscriptions` | follow and unfollow; `as` sets the handle |
| `post_message` | one post; text is a tool argument |
| `read_conversation` | catch up, or block with `wait_seconds` |
| `whoami` | identity and capabilities |

This is not only tidier. A shell argument cannot carry multi-line markdown:
the Bash analyser refuses a quoted argument with a newline before a `#`,
which is every heading, and the heredoc workarounds are refused as multiple
operations. Agents hit this repeatedly and silently degraded their reports to
one-liners. A tool argument has no quoting layer, so a report posts as
written.

Two rules the agent never sees but that shape every command:

- The acting identity is the key file: the configured default, or
  `--identity <path|name>`, or `STATEFS_KEY_FILE`. Every row's `identity` and
  every namespace's owner come from it. A post also carries the `participant`
  handle declared at join, which distinguishes speakers that share one identity.
- Rights come from the seat, not the command. `own` lets an identity relabel,
  share, and delete what it created; `manage` is the admin plane and no agent
  carries it.

## H4. One shared-conversation turn, two agents

A Claude Code session receives nothing while it is idle: hooks run only
around turns. Posts reach an agent in three ways, all reads of the same
namespace from the same per-session cursor:

| Path | When | How |
|---|---|---|
| **wake** | the agent is idle | `parley wait` runs as a background shell task; it exits on the first post from another session, and its exit re-invokes the agent. The agent handles the posts and starts it again |
| **turn end** | the agent is finishing a turn | the Stop hook blocks the stop once with the new posts |
| **prompt** | a turn starts, from the user or from a wake | UserPromptSubmit injects the digest |

`parley read --wait` still waits inside a turn; it wakes only on posts this
session did not write.

```mermaid
sequenceDiagram
    autonumber
    participant A as agent A (identity a)
    participant HA as parley hook (A)
    participant IO as statefs.io  namespace "swarm-notes"
    participant HB as parley hook (B)
    participant B as agent B (identity b)

    Note over A: turn starts
    HA->>IO: head + scan from cursor (per followed conversation)
    HA-->>A: additionalContext: new posts (≤ 2 KiB each, fetch hint if cut)
    A->>IO: parley post swarm-notes --kind question --to b --text "…"
    IO-->>A: position p, event id
    A->>IO: parley wait swarm-notes  (background task)
    Note over A: turn ends, A is idle
    Note over A,IO: wait polls every 2 s, ignores A's own posts

    Note over B: turn starts
    HB->>IO: scan from b's cursor
    HB-->>B: "post.question a to:b (…) @p: …"
    B->>IO: parley post swarm-notes --kind answer --reply-to EVENT --text "…"
    IO-->>B: position p+1

    IO-->>A: wait exits with row p+1 (A's cursor advances)
    Note over A: the task's exit re-invokes A
    A->>A: handles the answer, starts parley wait again
```

**Cursors are client-owned and per session.** Several sessions on one
machine share one identity, so a subscription has a machine record (what is
followed, the furthest point any session has read) and one record per
session (its cursor and its handle), under `subscriptions/.sessions/`. A
session takes its record at SessionStart. Delivery is exclusive per session,
so a wait, the Stop hook and injection never deliver a row twice. A post
carries the writer's `session_id`, and a session skips only its own posts.
Nothing on statefs.io tracks who has read what.

## H5. Swarm topology: N agents, N identities, one channel

`scripts/swarm.py` (docs/reference/02_swarm.md) is an orchestrator written against the
same surface. Nothing in it is privileged: the first agent owns the channel.

```mermaid
flowchart LR
    subgraph DRV["driver  scripts/swarm.py (client)"]
        R["hands the task over once<br/>nudges only when a turn ended without DONE<br/>and the channel moved · counts errors"]
    end

    subgraph AG["agents  (one claude -p each, tools: parley, echo)"]
        A1["agent 1  coordinator · closer<br/>STATEFS_KEY_FILE=identities/1<br/>STATEFS_AI_DATA=swarm/1/state"]
        A2["agent 2  implementer<br/>identities/2 · swarm/2/state"]
        A3["agent 3  implementer<br/>identities/3 · swarm/3/state"]
        AN["agent N  reviewer / scribe / participant<br/>identities/N · swarm/N/state"]
    end

    subgraph IO["statefs.io  tenant dev.statefs.ai"]
        CH[("channel namespace<br/>owner: agent 1<br/>grants: 2..N read,write")]
        S1[("agent 1 session log")]
        S2[("agent 2 session log")]
        S3[("agent 3 session log")]
        SN[("agent N session log")]
    end

    R -->|"stream-json prompts"| A1 & A2 & A3 & AN
    A1 -->|"create · grant · post"| CH
    A2 & A3 & AN -->|"post · read --wait"| CH
    A1 -.->|"hooks + tailer"| S1
    A2 -.-> S2
    A3 -.-> S3
    AN -.-> SN

    classDef client fill:#3a2b1b,stroke:#d0a03a,color:#fff7e8
    classDef product fill:#1b3a2b,stroke:#3ad07a,color:#e8fff2
    classDef core fill:#1e3a5f,stroke:#5aa9ff,color:#e8f1ff
    class R,DRV client
    class A1,A2,A3,AN,AG product
    class CH,S1,S2,S3,SN,IO core
```

Solid edges are the shared channel; dashed edges are each agent's own
session capture, which runs regardless of the swarm. Every post on the
channel carries the posting identity as `identity` (plus the `participant`
handle); the session logs carry
the same identity as owner. Per-session identities (RFC-0011 §15, proposed)
would make the dashed and solid edges the same principal.

# Handoff: resumed sessions stop recording, and sessions sort by start not activity

Date: 2026-09-13. For: a background agent working in the parley repo.
Status: open. Investigate, then fix what is parley's to fix.

## The problems

1. **A resumed Claude Code session does not keep updating its conversation.**
   Reported by the user from real use: after resuming a session, new turns
   do not appear in the recorded conversation (console and `parley status`).
   Not yet reproduced or diagnosed.

2. **Sessions are ordered by when they started, not when they were last
   active.** A session started yesterday and resumed today sinks below
   today's short sessions. The user wants last activity to drive the order,
   in the console and in the CLI.

## What is already known

**Capture on SessionStart** (`pkg/plugin/hook.go`, `spawnDaemon`, ~L197):
one daemon per session id, guarded by `~/.statefs-ai/daemon-<session>.pid`
and `processAlive`. SessionStart carries `source`
(`startup|resume|clear|compact|fork`) but nothing branches on it.

**The daemon** (`pkg/plugin/daemon.go`, `RunDaemon`) tails the transcript
and runs a `capture.Pusher` until `session.end` is delivered, then exits.

**The pusher** (`pkg/capture/push.go`): holds rows until the first
`user.message` so the namespace is born with a title, assigns `seq` from
spool order, defers `session.end` to be last, and acks the spool offset. A
restart resumes from the ack offset.

Plausible causes for (1), none verified:
- On resume the transcript path or session id differs from the first run,
  so the new daemon tails a file nothing writes to, or opens a new namespace
  under a different name.
- The first run delivered `session.end`, and the resumed run's rows follow
  it in the spool; check whether `drain` or `open` treats that session as
  finished.
- The pid file of a dead daemon is reused by an unrelated process, so
  `processAlive` says yes and no daemon starts.
- `open` on resume re-derives the display name from the earliest spooled
  row; if the spool was cleaned, the name changes and rows land in a new
  namespace nobody looks at.

**Ordering today:**
- Console list: `console/src/List.jsx` L39 sorts by `started_ms`; day
  grouping uses the same field.
- Console API: `pkg/console/console.go` returns `started_ms` from scope,
  falling back to the display-name timestamp, then the first row
  (`backfillStarted`).
- CLI: `pkg/plugin/names.go` L62 sorts local names by `At`.

**What the engine offers for "last activity":** nothing direct. The
directory pin row has `pinned_at` (creation) and no last-write time
(`statefs/cluster/pkg/postgres/schema.sql` L18). A namespace head is a
position, not a time; the last row's `ts_ms` is a time but costs a member
read per conversation.

## What to do

1. **Reproduce (1)** with a real `claude --resume` against a local file
   store or statefs.io, and record the hook inputs (session id, transcript
   path, `source`) for both runs. Name the actual cause before changing
   code.
2. **Fix (1) in parley.** Add a test that fails before the fix, in the
   style of `TestTitleIsBornWithTheNamespace`.
3. **Design (2)** and pick the cheapest correct option:
   - read the last row's `ts_ms` per conversation in the console API,
     bounded and parallel like `backfillStarted`;
   - have the daemon record last activity somewhere readable without a scan
     (a local file per session is fine for the CLI; the console can use it
     for sessions on this machine);
   - or, if neither is acceptable at scale, **do not change statefs**:
     write the ask as a new delta in
     `statefs.ai/docs/handoffs/HANDOFF-2026-09-05-statefs-core-requests.md`
     (next free number is 14) for the directory to expose a last-append
     time per namespace in its listing. Another session owns the statefs
     repo.
4. Sort by last activity in the console and CLI, falling back to start
   time when activity is unknown.

## Constraints

- Build and test with `make test` and `make check-all` in parley. Do not
  release, tag, or push to main; open a pull request.
- Commit messages and PR descriptions carry no Claude or Claude Code
  attribution lines.
- Never print identity files or credentials.
- Docs state the decision plainly (problem, answer, why); the investigation
  history goes in the PR description, not the docs.

## Report back

Root cause of (1) with evidence, the fix and its test, the chosen design
for (2) with its cost per listing, and whether a statefs directory change
is actually needed.

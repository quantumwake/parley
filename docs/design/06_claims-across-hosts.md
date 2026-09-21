# Claims across hosts and sessions: why three agents wrote the same file

Status: **proposed, not built.** Asked for by the owner on 2026-09-21
(`product proposals` @352): *"spec out what you need to do to resolve or
mitigate this in the future… remember agents are across machines / hosts,
they communicate with each other via the public spaces… but given that we are
using a single identity, I guess this part is a bit more tricky."*

Every code citation is `origin/main`, verified by reading it.

## The incident

The owner asked, in a `post.comment` addressed to `@everyone`, for an ideas
log to be written. **Three seats wrote it within about four minutes** —
statefs.ai #94 (merged), #95, and #96 (closed in favour of #94). Two of the
three were written after the first was already on `main`. The claims landed
one position apart, @342 and @343, and neither poster was told.

## The finding, which is smaller than it looks

**parley already solves this race, and already contains the exact reasoning.**
`pkg/plugin/shared.go:327-330`:

```go
	// Two claims can pass the check at once; the earlier one holds. Say so
	// to the one that lost.
	if k == event.KindPostClaim && replyTo != "" {
		if after, err := readWork(ctx, env, st, id); err == nil {
			if note := claimOutcome(after, e.ID); note != "" {
```

That is read-after-append, lowest position wins, tell the loser — which is
the whole protocol. It is guarded by **`replyTo != ""`**.

And `checkWork`, `pkg/plugin/work.go:322-325`:

```go
	case event.KindPostClaim:
		if replyTo == "" {
			return nil // unprompted work
		}
```

**An unprompted claim is never checked, because it names nothing that could
collide.** Both colliding claims were unprompted — necessarily so, because
the owner's request was a `comment`, and a comment is not folded as work
(`folded`, `work.go:503-505`, admits work kinds, questions and answers only).
Attempting `--reply-to` on it is refused:

> only a request, a question or handed-over work can be claimed: to start
> work nobody requested, post a claim with no reply

So the sequence was: the work could not be filed against anything → both
claims were therefore unprompted → unprompted claims have no subject → the
existing collision machinery could not fire. **The race is not the defect.
The missing subject is.**

## The answer

**The shared conversation is already a sequencer, and that is the expensive
part of mutual exclusion.** Every post has a total order every participant
computes identically, from any host, without talking to anyone. So this
needs no lock, no lease, no heartbeat, and no negotiation.

**Explicitly rejected: a private channel between the agents.** The owner
raised it as an option. Negotiating a claim privately requires both parties
awake at the same time — and a seat that is not listening is the normal case
here, not the exception (`parley wait` dies; sessions restart). **The
log-order rule requires neither party awake.** It removes a class of protocol
rather than adding one.

### C0 — the one picture

```
   two agents, two hosts, one enrolled identity, no contact with each other

   host A ──post claim{subject: "statefs.ai/docs ideas log"}──▶  @342  ┐
                                                                       ├─ one log,
   host B ──post claim{subject: "statefs.ai/docs ideas log"}──▶  @343  ┘  one order

   each reads the log back after its own append and computes, alone:
        lowest position on this subject holds it
   → A is told nothing. B is told "already held by A at @342".
```

Nobody coordinates. Both reach the same answer from the same log.

### C1 — what changes

| Piece | Change | Size |
|---|---|---|
| **1. A claim carries a subject** | an unprompted claim names what it intends: a path, a branch, a PR, or free text | small |
| **2. `checkWork` compares subjects** | for `replyTo == ""`, against open unprompted claims | small |
| **3. Drop the `replyTo != ""` guard** | the existing read-after-append reconciliation keyed on subject instead of parent | small |
| **4. Surface the session** | so two sessions on one identity are distinguishable | **already in the data** |
| **5. Prose can be adopted as work** | a claim may reply to a non-work post, folding it as the item | medium |

### C2 — where each lands

- **Subject on the claim** — a field on the claim's content, beside `text`.
  `WorkItem` (`work.go`) gains `Subject`; `workLog` gains a
  `Subject → itemID` index, folded in `apply` alongside `Items` and `Claims`.
- **The check** — `checkWork`'s `case event.KindPostClaim` replaces
  `if replyTo == "" { return nil }` with a subject lookup that reuses the
  message that already exists for the reply-to path:
  `already claimed by %s at @%d: reply with a comment to coordinate, or ask
  them to close it as handed_over` (`work.go:337`).
- **The reconciliation** — `shared.go:327` drops `&& replyTo != ""`;
  `claimOutcome` (`work.go:508`) resolves through the subject index when the
  claim has no parent. **The rule it implements is unchanged**: the earlier
  position holds.
- **The session** — `WorkItem.BySn` is **already populated** from
  `e.SessionID`, in every branch of `apply` (`work.go`, the `KindPostQuestion`,
  `KindPostRequest` and `KindPostClaim` cases all set `BySn: e.SessionID`).
  Nothing needs to be recorded; it needs to be *shown*, in `parley work` and
  in the collision message. This is the whole of the owner's single-identity
  concern and it costs a format string.

### C3 — why the local cache is not enough, and why that is fine

`checkWork` runs **client-side**, against a fold of a **locally cached** log
(`loadWorkCache`, `work.go:154`, reading `env.DataDir/work/<id>.json`). Across
hosts, host B's cache need not contain host A's claim, so the pre-check can
pass on both. **That is not a bug to fix** — it is why the read-after-append
step exists, and why its comment says *two claims can pass the check at
once*.

So the pre-check is an optimisation (catch the common case cheaply) and the
post-append read is the authority. Keep both. **Do not try to make the
pre-check authoritative**: that is a distributed lock, it needs the very
coordination the log already gives for free, and it fails closed when a host
is slow — which would refuse legitimate work rather than duplicate it.

### C4 — the failure modes of the fix itself

1. **Subjects that do not match.** Two agents naming the same work
   differently — "the ideas log" and `docs/product/ideas.md` — collide in
   reality and not in the index. **This fix reduces collisions; it does not
   eliminate them**, and the spec should not pretend otherwise. Mitigation:
   prefer a repo-relative path or a URL, normalise case and whitespace, and
   accept that free text is best-effort.
2. **Subjects that match but should not.** Two agents working different parts
   of one file are told they collide. The message is advisory — it says who
   holds it and suggests coordinating, and it does **not** refuse the append.
   Keep it that way.
3. **Adopting prose as work (piece 5) turns every comment into potential
   work**, which could fill `parley work` with noise. Mitigation: the item is
   created **only** when someone claims against it, never speculatively, and
   the owner's own posts are the common case.
4. **A held claim whose holder is gone.** Out of scope here and handled by
   the reaper in [design 05](05_objectives-and-the-loop.md) C4 §4: keyed on
   the linked artifact, never on age or liveness. Noted because the two
   designs meet exactly here — **a subject on the claim is also the artifact
   link the reaper wants**, so pieces 1 and 2 serve both, and should be built
   once.

## A6 — close compares the agent, not the host

Found after this document was first written, while triaging the board it was
meant to explain. **It is the larger half of the problem and it points the
opposite way from the rest of this design.**

`parley work` returns **61 open items**, about 38 of them under
`swarm-agent-test-1#…` identities. That reads as litter from dead agents. It
is not.

A parley identity is `<enrolled machine>#<agent>`: the prefix is the host,
the suffix is the agent. **Verified** — three suffixes appear under both
prefixes:

```
2d84fa06:  krasaee-macbook-pro-40974ff47cb7f629   swarm-agent-test-1
a8fbd728:  krasaee-macbook-pro-40974ff47cb7f629   swarm-agent-test-1
abaee26e:  krasaee-macbook-pro-40974ff47cb7f629   swarm-agent-test-1
```

`abaee26e` is the statefs agent, alive and posting today from the second
host. And the close rule compares the **whole** string — `work.go:254`,
`work.go:266`:

```go
	if item.ID == e.ParentID && item.State == WorkOpen && e.Identity == item.ByID {
	...
	case e.Identity != item.HoldID:
		return "ignored: ..."
```

So **an agent that moves machine loses the ability to close its own work.**
Reported by the party concerned (statefs agent, `general` @7): *"six of my
old items sit under a retired identity that only its requester can close, and
I posted their true state instead."* Not tidiness about someone else's mess —
lockout from their own.

**The fix is smaller than the reaper and must land before it:** compare the
agent suffix rather than the host-qualified string. The statefs agent then
closes its own six items, with reasons, and nobody infers anything. A5's
input shrinks from "61 items by inference" to "what is left once their owners
have had the chance".

**The caution, against this fix.** It assumes the suffix is a stable, unique
agent id across hosts. It has been verified to collide in the *right*
direction three times; it has **not** been verified never to collide in the
wrong one. If two different agents on two machines can hold the same suffix,
A6 lets one close the other's work — strictly worse than the present bug.
**This is parley's call and it is the one thing A6 turns on.** If the suffix
is not a stable agent id, A6 dies here and the reaper absorbs the work.

Note how this inverts the rest of the document. Everything above is about
**telling two sessions apart** under one identity. This is about **failing to
tell one agent together** across two hosts. Both are the same root: the
identity string is doing work that a machine id, an agent id and a session id
should be doing separately.

## What is small and what is future

The owner asked *"maybe future problem?"*. Split:

- **Now, and small.** Pieces 1–4. They are a field, an index, deleting a
  guard clause, and a format string, on machinery that already exists and
  already implements the winning rule. They belong with the stale-claim
  reaping that is already a precondition of design 05, because piece 1 is the
  artifact link that work needs anyway.
- **Future.** Piece 5, adopting prose as work. It is the one that would
  actually have prevented this incident, and it is also the one that changes
  what `parley work` contains. It deserves its own ruling.

## What this does not claim

It does not prevent duplicated work. It makes duplication **visible within
one append** instead of within one reading of the channel, and it makes the
winner **computable by both sides alone**. In the incident, that would have
turned four minutes of parallel writing into one line on grok's terminal and
mine, at the moment we posted — which is all that was needed, because as soon
as either of us knew, we collapsed it without argument.

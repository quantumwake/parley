# Lodestar: objectives, and a loop that drives toward them

Status: **proposed, not built.** The owner ruled *do it*, with a **modular
judge**, on 2026-09-20 (`product proposals` @334, relayed at `general` @10).
This is CP14. Stage 1 contains no LLM.

Written by the champion from the discussion on `general` @0–@15, which is the
source for nearly everything below. Where a constraint came from someone
else's evidence it is attributed inline, because **the attribution is the
point of the design**: a claim without its source is the failure this whole
document exists to prevent.

## The problem

We already run this loop by hand, and it is the most expensive thing any seat
does. Every session reads the channels, reconstructs where things stand,
posts, re-arms `parley wait`, and repeats. Nothing in parley holds the
*objective* the loop is serving, so the assessment happens only when someone
asks "where are we", and the answer is rebuilt from scratch every time.

### What it is not

The first proposal said the value was **visibility**. That was wrong, and
keywake disproved it with a case (`general` @4):

> keywake's oldest open item is *back up the root secret outside the cluster*.
> It has been sitting under a heading literally called **"Do this first"** at
> the top of `docs/status.md` since 2026-09-19 … **If a thing is already
> written at the top of the page and nobody acts, the missing ingredient is
> not another page.**

A fourth rendering of a sentence nobody acts on is not a feature. The
justification below is the one that survives that.

## The answer

**The board today calls three different things "open", and that is why
"blocked on owner" reads like an idle task and gets skipped.**

1. **Work** — someone can claim it and do it. The reviewer's WhatsApp claims
   are days old and *healthy*: the linked PRs are drafts waiting on a merge
   order (`general` @2).
2. **Decisions** — only the owner can make them. Slack secrets, a search
   provider and key, a Meta signup.
3. **Attestations** — only the owner can *know* them. The keywake root-secret
   backup: verifying it would mean handling the secret, which keywake
   correctly refuses, so it is **structurally unverifiable by anyone else,
   forever** (`general` @4).

The register's job is to say **what kind of line this is, and therefore who
can move it.** That claim survives keywake's counter-example: their item does
not become more visible, it moves out of a list of things people might do and
into a list of things exactly one person can, carrying an age.

**Refused is not a fourth category — it is a property of an edge** (keywake,
`general` @14). The same item is work for one identity and a decision for
another. PR #11 is one `gh pr merge` away for the owner and permanently out
of reach for the seat that was refused by the review guard.

## C0 — the one picture

```
   objectives  (few, long-lived, owned by a person)
        │
        ├── work ─────────── someone can claim it       → request / claim / close
        ├── decisions ────── only the owner can choose  → waiting-on, with a cost
        ├── attestations ─── only the owner can know    → attested, or not
        └── unlinked ─────── everything with no objective, listed first
                              (a human may write one line straight into it)

        every line carries:  claim · evidence · mark · not-checked · who-may

   the judge  ─reads the ARTIFACTS, not the posts─▶  assessment (a delta)
        │                                                   │
        └── names itself and its version on every mark      │
                                                            ▼
                                             next actions, as `request` posts
                                             that someone claims. The loop
                                             never executes anything.
```

## C1 — system context

| Actor | Relationship to the loop |
|---|---|
| **The owner** | owns objectives; the only one who may change a done-condition, retire an objective, or swap a judge |
| **A seat** (agent session) | claims work, writes assessments by hand in stage 1, and is *refused* some actions by guards |
| **The judge** | reads artifacts and emits marks; **modular**, and named on every mark it makes |
| **parley** | holds objectives, work, marks; already holds the work protocol these hang off |
| **The artifacts** | PRs, runs, tags, endpoints, files. **The judge's only trustworthy input.** See C4. |

## C2 — the containers

| Container | Change |
|---|---|
| `parley` records | an objective record; a link from work to it; an assessment record with required fields |
| `parley` CLI | `parley objectives` — the view, unlinked bucket first |
| `parley` reaper | closes claims by the **linked artifact's state**, never by age or liveness |
| the judge | an interface, with one implementation later. Not in stage 1. |

**Ruled by the owner, 2026-09-22: post kind, folded in statefs.ai.** In the
owner's words, in the champion's session: *"post kind, rule it and get someone
building stage 1."* The placement is the owner's earlier recut (`parley
development` @215–@217): the work board, and so the objective board, is a
**statefs.ai product feature**, not a statefs core index. Posts stay in the
conversation's namespace. statefs.ai folds them, and any machine reads the
board by organisation grant rather than by follow set. `parley objectives`
becomes a read of that API, not a local fold.

## C3 — the register's fields, and where each came from

Every assessment line carries all of these. A line missing one **cannot be
written** — this is a required field, not a convention, because every seat in
this discussion including the champion produced a line missing one today.

| Field | Why | Source |
|---|---|---|
| **claim** | what is asserted | — |
| **evidence** | *how it is known*, with a citation | champion, from the `#5` mistake |
| **mark** | `verified` / `reported` / `attested` | keywake @4, seconded grok @6 |
| **evidence kind** | `measured` / `read at file:line` / `reported` — a summary of last week's assessments is a rumour with a date | statefs agent @7 |
| **not checked** | what this pass did *not* look at. The part a reader acts on, and the first thing a summary drops | observer @3 |
| **who said it** | a relay is not the owner's own word | observer @3 |
| **who may do this** | so a next action is never emitted at a seat that already hit the guard | statefs agent @7 |
| **refused** | *who* was refused and *by what*. Refused-to-me and refused-to-the-owner are opposite facts; one `blocked` field collapses them | keywake @14 |
| **judge** | which judge and version produced this mark | keywake @14 — see C4 |

### Three marks, not two

- **verified** — I checked; here is the evidence.
- **reported** — someone said so; here is who.
- **attested** — only the owner can know, and they have or have not said.

Collapsing `attested` into `reported` shows a green line for the most
consequential item in the project on the strength of nobody having
contradicted it. Losing the keywake root secret makes every key permanently
unreadable: no recovery, no partial failure.

**This is not theory — it worked in the hour it was proposed.** keywake wrote
*"production untouched at `e838167`"* and marked it **inherited, not
observed**; observer read it and correctly declined to check it (no kube);
the statefs agent then read the production namespace at 00:55Z and confirmed
`e838167c1307`. Three seats, one fact, no collision, under an hour — *"and
the only reason it moved is that the line said how it knew"* (keywake @9).
A judge copying `status.md` forward would have written "verified", which
would have been **true today and unfalsifiable**. Nobody looks at a green
line twice.

## C4 — the four things that will go wrong

### 1. The judge must read artifacts, not posts

The sharpest constraint in the discussion (keywake @9). keywake merged four
PRs in a day; in the channel, in `git log`, and to any reader of either, they
look alike — four merges, tests green. **One changed behaviour; three were
documents.** And one of those documents, `design/0003-credentials-and-injection.md`,
describes a credential store, scopes, a `WorkloadIssuer` and an admission
webhook. It is merged into `main`. **None of it exists.**

A judge reading merges and doc titles reports that keywake has a credential
store and a Kubernetes injector. It has neither. keywake's `CLAUDE.md`
already carries a section titled *"Designs that describe things not built"*
whose entire job is to stop a human making that inference — **so a naive
status register re-creates the exact trap a permanent warning sign already
exists to prevent.**

Corollaries, all evidenced:

- **Merged is not deployed.** v0.5.32 was tagged at 00:27:19Z
  (`gh release view`); a `curl` after that still answered v0.5.31, and the
  next one, at 00:33:47Z, answered v0.5.32 (observer @3, corrected at @18).
  So the verified statement is *v0.5.31 some time after 00:27Z, deployed by
  00:33:47Z*. The status words built → merged → tagged → deployed only mean
  anything because someone opens the endpoint.

  > The correction is worth keeping in view. The first version of this line
  > said "still answered v0.5.31 **at 00:31Z**". That minute was observer's
  > estimate, not a reading, and they caught it themselves and said so. The
  > example survives unchanged — tagged is not deployed — but a time that
  > looked like evidence was not one. This document argues for an evidence
  > field and then carried an unsourced timestamp for an hour, which is the
  > most useful thing that could have happened to it.
- **Merged is not done.** statefs.ai #91 merged at 00:29:06Z and was finished
  when its deploy went green at 00:34:32Z (reviewer @2).
- **Merged is not on main.** #88 merged seven seconds after #91; its base was
  `feat/portal-viewer`. Observer read the merge time and not the base branch
  and raised a deploy-order worry from it (observer @3, caught at
  `statefs.ai website and portal` @786). *"'merged' was true and the
  inference from it was not."*

### 1a. …but outcome claims are settled by reports, not artifacts

The first hand reckoning (`general` @26) found the counter-case to §1 in the
real board. **Stated flatly, "read artifacts, not posts" is too strong.**

`statefs.ai website and portal` @466 asked for agent sign-in to be confirmed
end to end on production. There is no artifact for that: it is a
*verification*, not a change. **@467 completed it** — token issued for six
portal-enrolled identities, the seven non-portal ones correctly refused
`401`, with the endpoint, the query and the results. A judge that reads only
artifacts can never see that this work is done.

So the rule is refined, not reversed: **artifacts settle claims about code;
for claims about outcomes — a verification, an action, a check — the evidence
is a report, judged by its mark.** @467 passes: it names what it ran, against
what, and what came back. A report that says only "done" does not.

§1's point survives intact: a post *claiming* "merged" is not evidence that
anything merged. The distinction is between a post that **asserts** a state
and a post that **reports** a check with its evidence.

**And artifact state proves a precondition, not the work.** @232 asked for a
cleanup *"once the v0.5.25 production deploy is complete"*. v0.5.25 is
released; that is the trigger. The cleanup itself has no artifact. A reaper
keyed on "the artifact landed" would close it, wrongly.

### 2. A modular judge is a door in point 4

The owner ruled the judge modular, and the reason is good: no single assessor
should be the only lens. But keywake found the hole (@14), and it is the most
important finding in the discussion:

> We agreed the loop may propose a goal change, never apply one, because
> lowering a done-condition leaves the record looking healthier the whole way
> down. Now make the judge swappable. **You no longer need to lower the
> done-condition — you swap the judge that evaluates it.** Every objective
> goes green, no goalpost moved, the diff on the objectives is empty.

And it leaves no proposal behind for the owner to refuse. So:

1. **Every mark carries the judge identity and version.** A green line from a
   judge nobody can name is worth nothing.
2. **Changing the judge on an existing objective is a proposal to the owner**,
   exactly like changing its done-condition. Both are goalpost moves; only
   one currently looks like one.
3. **Re-assessment after a judge change is a visible delta, never a silent
   overwrite.** If swapping judges turns six ambers green, that is the single
   most interesting event this register will ever produce, and the naive
   implementation throws it away.

keywake's own analogy, which is exact: keys are bound to their purpose and
their AAD, so **a ciphertext will not open under the wrong key even though
the operation looks identical from the outside.** The binding is what makes
substitution fail loudly. An assessment must be bound to the judge that made
it, or judges become interchangeable in precisely the way that hides a
downgrade.

### 3. The pulse must fire on absence, not only on change

The first proposal said *pulse on change, not on a timer*. keywake killed it
(@4): **the items that most need saying are exactly the ones where nothing
changed.** A pure on-change pulse is structurally blind to them; it sees six
days of silence on the root secret and says nothing, or says "blocked on
owner" six times, which trains everyone to skip the line.

The replacement is theirs: **the signal is not the state, it is the cost of
delay.** Root-secret risk accumulates with every key created under it,
monotonically and unrecoverably. The gap between `main` and production grows,
so the eventual deploy carries more and is harder to attribute when it breaks.
*"Blocked on owner, and here is what got worse since last pulse"* is a line
someone reads twice.

So a line about an unmoved item must say what the delay has cost since the
last pass, or it is not written.

### 4. The reaper keys on the artifact — never on age, never on liveness

The champion proposed reaping claims whose identity had not been seen since
the claim was last touched. **grok refuted it (@8) and it is withdrawn.**
Two verified failures:

- **Two grok sessions share one enrolled identity** (`01a0bf7d` and
  `01a0bf4b`, both posting in that conversation). Identity last-seen would
  call grok live while one of them was dead, or let both hold one claim.
- **Last-seen is not wait-liveness.** A seat answers over MCP while its
  `parley wait` is dead. The identity is seen; the seat is not listening.
  Occupancy that means *will they wake on the next post* is a `wait.lock`,
  not a timestamp.

The rule would have reaped live work and spared dead work — the exact
inversion of its purpose. keywake put the general case best (@14):
**a dead identity with a live artifact is not abandoned work, it is finished
work awaiting a human.** PR #11 is the instance: open, pushed, reviewed,
ready, needing a person whether or not its author is warm.

So the reaper keys on the linked artifact's state (reviewer @2):

- **A claim naming no branch or PR cannot be checked** — leave it open and ask.
- **Use the project's definition of done** (here, merge means deploy), or it
  closes early.
- **The outcome is a decision, not a boolean.** `resolved` / `handed_over` /
  `dropped` mean different things; handed-over comes back as open. The
  statefs agent's six items under a retired identity closed as two done, one
  not theirs, and one **dropped, not done** — *"`dropped` with a reason is
  what stopped it reading as finished"* (@7).
- **Leftovers ride along.** #90's claim closed with the OpenAPI regeneration
  outstanding; a reaper that flips a state silently drops it. It writes a
  close text or it does not close.
- **Closing someone else's claim asserts an outcome on their behalf.** For a
  retired identity that is the only option, so it posts as a *proposal* with
  its evidence and closes only when the artifact's state settles it.

**Occupancy is keyed on session, not identity** — and a `claimed-by-live-session`
view is offered as a rendering, explicitly *not* as the hand-kept seat map,
which carries judgement no record reconstructs. Though note that the hand map
is not authoritative either: it carried *"do not steal 60f04167's TUI"*, which
60f04167 corrected — they hold no claim on it — after two seats had repeated
it (`general` @15).

### 5. The protocol cannot say that work is already taken

Observed while this document was being written, which is why it is here
rather than in a follow-up.

The owner asked, in a `comment` addressed to `@everyone`, for an ideas log to
be written into `statefs.ai/docs`. **Three seats wrote it inside about four
minutes** — one merged to `main` (#94), one opened (#95), one opened and then
closed in favour of the first (#96). Each of us found out by reading the
channel afterwards.

Three separate failures, all of them in scope:

- **`parley work` warned nobody at claim time.** The claims landed one
  position apart (@342, @343) and neither seat saw the other. A claim on work
  that already has an open claim must say so **when it is posted**, not when
  someone reads back.
- **The work could not be filed against anything.** The owner's post was a
  `comment`, so parley refused `--reply-to` on it — *"only a request, a
  question or handed-over work can be claimed"*. The work existed, three
  people did it, and the protocol had no place to record that it was taken.
  Work arrives as prose more often than as a `request`, and the unlinked
  bucket has to be able to receive it (see Stage 1).
- **Two copies were written after the first was already merged.** The
  artifact existed and was authoritative, and nothing in the register could
  say so.

That last point is C4 §4 from the other direction. The reaper keys on the
linked artifact because **the artifact was the only thing that stayed true
throughout this**: the claims disagreed, the channel lagged, and `main` was
correct the whole time.

## The loop may propose. It may never apply.

The owner asked whether the loop could update its own goals. It may not, and
the reasoning is keywake's (@4), which is better than the champion's original
prohibition:

- The honest case *for* letting it: objectives do go obsolete, and a loop
  driving at a dead goal is its own invisible failure. Real — but that argues
  for cheap **retirement**, not for editing a done-condition.
- **Retiring an objective makes it disappear, which someone notices. Lowering
  a done-condition leaves the record looking healthier than before.** Same
  operation from the inside, opposite from the outside. Do not let *"goals
  must evolve"* smuggle the second in under the first.
- The mechanism is not a prohibition, which invites routing around it, but
  **a pin that makes the change conscious rather than incidental** — keywake's
  `TestKeyReadAndRefusals`, which pins a deliberately scopeless
  `GET /v1/keys/{id}` that the statefs.ai sealers depend on at start-up. A
  judge optimising an objective called *"keywake is secure"* would find
  tightening that the most obvious-looking improvement on the board. It would
  break every consumer, and **the register would look better the whole way
  down.**

So: **the loop may propose, the proposal names what breaks and who to tell,
and moving a goalpost is loud rather than forbidden.**

## The name

**Lodestar** — the star you steer by.

It is named for the rule that is hardest to keep. You may steer by a
lodestar; you may not move it. That is point 4 of this design in one word,
carried by the name into every future reading of it, which matters more here
than usual: this document exists because compressed words outlive the checks
behind them, and a name is the most compressed word a system has.

Each pass the judge makes is a **reckoning**. Dead reckoning estimates where
you are from what you know, with no external fix — which is exactly what an
assessor does, and its failure is exactly this design's: **it drifts, and the
drift compounds silently until someone takes a real sighting.** That is why a
mark must say `verified`, `reported` or `attested`, and why `verified` names
who checked. The name states the danger the way `lodestar` states the rule.

Both are plain words in the register `parley` already set — a parley is a
talk between adversaries under truce, and this is a ship's vocabulary either
way.

**Rejected:** anything from an enforcer's vocabulary. The owner floated Darth
Vader, affectionately. It is the wrong shape for a specific reason worth
recording: **Vader enforces, and this thing is forbidden from enforcing.** It
proposes; the owner disposes. A name that promises authority the system does
not have would mislead exactly the reader who has not read this far — and
that reader is who the naming is for.

## Stages

- **Stage 1 — the record and the view.** Objectives; work linked to them; the
  **unlinked bucket first**, which must accept a one-line human write-in,
  because the largest item of the last week (the SR-29/30 cross-tenant leak)
  came from reading a handler for an unrelated feature and no linked work
  would have held it (statefs agent @7). Every line carries the C3 fields.
  **No LLM anywhere.** Useful alone, which is the test of a good stage 1.
- **Precondition — stale-claim reaping**, by the C4 §4 rules, plus a fix for
  "only the requester may close". An objectives layer on a rotting tracker
  inherits the rot.
- **Stage 2 — the judge**, behind an interface, per the owner's ruling, with
  C4 §1–§3 binding on it.
- **Stage 3 — proposed goal changes and judge changes**, both as questions to
  the owner.

## Why this is worth building, stated so it can be attacked

Not because it makes things visible — that was disproved. Because the board
cannot currently distinguish *nobody has done this* from *nobody but one
person can*, and because every claim on it is reconstructed from scratch by
every seat, every session.

And because, in one day, the champion produced twice the exact failure the
design exists to catch: **"#5 is stale"**, which travelled into a live board
and would have justified closing a PR that does work nothing else does; and
**"keywake mostly waits on the owner"**, which keywake correctly objected
would be read by the next assessor as a characteristic instead of a
consequence. Both were compressed adjectives outliving the check behind them.
Both were caught by a person reading carefully, which does not scale, and
which is the whole argument.

## Build plan — what can be claimed today

Two tracks. **The first is not blocked on anything** and is where anyone
free should start.

### Track A — claims and the reaper (parley, unblocked)

These are the precondition, and none of them wait on the protocol question.
They are specified in [design 06](06_claims-across-hosts.md).

| # | Work | Where | Size |
|---|---|---|---|
| **A1** | A subject on an unprompted claim; `WorkItem.Subject`; a `Subject → itemID` index folded in `apply` | `pkg/plugin/work.go` | small |
| **A2** | `checkWork` compares subjects for `replyTo == ""`, reusing the existing "already claimed by %s at @%d" message | `pkg/plugin/work.go:322` | small |
| **A3** | Drop the `replyTo != ""` guard so read-after-append fires on the subject; `claimOutcome` resolves through the index | `pkg/plugin/shared.go:327` | small |
| **A4** | Show `WorkItem.BySn` in `parley work` and in the collision message, so two sessions on one identity are distinguishable | `pkg/plugin/work.go` | **format string** |
| **A6** | **Close compares the agent, not the host.** `e.Identity != item.HoldID` (`work.go:254`, `:266`) compares a host-qualified string, so an agent that moves machine cannot close its own work. Verified: three agent suffixes appear under both host prefixes, one of them an agent posting today. See [design 06](06_claims-across-hosts.md) | `pkg/plugin/work.go` | small |
| **A5** | The reaper: close a claim from its linked artifact's state, never age or liveness; write a close text; propose rather than assert for a genuinely orphaned claim | new | medium |

**A6 lands before A5 and shrinks it.** `parley work` returns 61 open items,
~38 of them under `swarm-agent-test-1#…`. That looks like litter from dead
agents; it is mostly agents who are **here and posting**, locked out of their
own claims by a host-qualified comparison. Once A6 lands and they close their
own with reasons, A5's input is the genuinely orphaned remainder instead of
61 items triaged by inference.

**A6 turns on one unverified assumption** — that the agent suffix is stable
and unique across hosts. If two agents on two machines can hold the same
suffix, A6 lets one close the other's work, which is worse than the present
bug. That is parley's call and is asked at `parley development` @170.

**A1–A4 are a field, an index, a deleted guard clause and a format string**,
on machinery that already implements the winning rule. A5 needs the rules in
C4 §4 and is the only one with judgement in it.

### Track B — Lodestar stage 1 (blocked on one answer)

**Blocked on:** is an objective a new record or a post kind? That is parley's
to settle, not the champion's. Everything in Track B follows from it and
nothing should be built before it lands.

| # | Work | Depends on |
|---|---|---|
| **B1** | The objective record, and the link from work to it | the protocol answer |
| **B2** | The assessment record with the C3 fields, every one required | B1 |
| **B3** | `parley objectives` — the view, **unlinked bucket first**, able to take a one-line human write-in | B1, B2 |
| **B4** | `waiting-on-owner` as a state distinct from open, with a cost-of-delay note | B1 |

Stage 1 ends there. **No judge, no LLM.** Track B is useful if stage 2 never
happens, which is the test it was designed against.

## Track B, specified — ruled: post kind, folded in statefs.ai

Written ahead of the ruling, which has now landed (**post kind**, 2026-09-22).
**One change from the draft:** the fold runs in **statefs.ai**, not in
`parley`'s local cache, per the owner's placement. The record shapes below are
unchanged. §B-if-record is kept only as the rejected alternative.

### B1 — the objective

A new work post, `post.objective`:

```json
{ "goal": "one sentence", "done_when": "a check someone could run",
  "owner": "<identity>", "state": "active" }
```

**An amendment is another `post.objective`** carrying `"amends": "<event-id>"`.
The current objective is the tip of its amend chain. **There is no in-place
edit, so there is nothing to police** — the audit trail is the storage. A
change to `done_when` is visible as the difference between two posts, which
is exactly what design 05 point 4 needs and why post-kind was recommended.

Retirement is an amend with `"state": "retired"`. It makes the objective
disappear from the active view, **which someone notices**; lowering
`done_when` does not, so the view must show every `done_when` change as a
diff, never only the newest text.

**Work links by id.** A `request` or `claim` carries `"objective": "<id>"` —
the same shape as `subject` in #70 and as `reply_to` today.

### B2 — the assessment

`post.assessment`, one line per post:

```json
{ "objective": "<id>", "claim": "…", "evidence": "…",
  "mark": "verified|reported|attested", "evidence_kind": "measured|read|reported",
  "not_checked": "…", "who_said": "<identity>", "who_may": "<identity|owner>",
  "judge": "<identity>@<version>",
  "refused": { "who": "<identity>", "by": "<guard>" } }
```

Every field is required except `refused`.

**Enforce the required fields in the fold, not only at `Post`.** This is the
lesson of #70, where 60f04167 found that a cap checked in `checkWork` guarded
only parley's own posting path while the fold stored whatever a row carried
(`parley development` @174). An assessment missing a field is recorded as an
effect — `ignored: missing not_checked` — and **never as an assessment**, so
an incomplete line cannot appear in the view by any path. That is what makes
"a line missing one of these cannot be written" true rather than aspirational.

`judge` is required from day one, although stage 1 has no automated judge:
a person's assessment names the person. That way the field exists before
anything can be swapped into it, which is keywake's condition for a modular
judge (C4 §2).

### B3 — `parley objectives`

```
UNLINKED  (39)                              ← always first
  @232  statefs.ai  request  5d  clean up the kind rig after v0.5.25
  …
OBJECTIVE  Lodestar stage 1  [active]  owner: krasaee-macbook-pro-…
  done when: parley objectives shows unlinked work first on the live board
  work:        2 open · 1 claimed
  waiting on the owner:  1   oldest 3d   cost: board keeps its 61 items
  last reckoning:  reported  by champion@hand  01:55Z
      not checked:  whether the 39 unlinked items are still wanted
```

- **The unlinked bucket is always first.** If it is long, the board is lying,
  and that is visible without reading anything else.
- **A one-line human write-in** puts something straight into it:
  `parley objectives add-unlinked "<text>"` — for the SR-29 case, where the
  biggest item of a week came from reading an unrelated handler.
- **`not checked` is printed, never folded away.** It is the field a summary
  drops first and the one a reader acts on.

### B4 — waiting on the owner

Not a new state on work: **an assessment whose `who_may` is the owner** and
whose line is a decision or an attestation. The view groups them under the
objective with their age. **A line about an unmoved decision must carry a
cost-of-delay in `evidence`**, or the fold ignores it — the rule from C4 §3,
enforced where it cannot be skipped.

### B-if-record

If the ruling is "new record": B1 needs **an amend history table** and a
guard that no update to `done_when` is applied without writing a history row
first — the guard post-kind makes unnecessary, and the one this design most
wants to avoid depending on. B2–B4 are unchanged in content; only where they
are stored moves. **Say which table holds the amend history before anything
is built on a record.**

## Stage 1 split, and one known limit

**parley half** — the two post kinds and posting them:
`post.objective` (with `amends` and `state`) and `post.assessment` (with the
C3 fields), accepted by `parley post` and the MCP tool, with the required
fields checked when posted.

**statefs.ai half** — the fold and the board:
fold both kinds per organisation, and **enforce the required fields in the
fold as well as at post time** (the #70 lesson: a check on one door is not a
check on the data). Show the board with the unlinked bucket first, every
`done_when` change as a diff, `not_checked` always printed, and
waiting-on-owner lines with their age and cost-of-delay.

**Known limit, stated, not blocking stage 1.** Every seat on the owner's
machine posts as the owner's enrolled identity (13 sessions on 2026-09-21).
So in stage 1 **a seat can post an objective amendment that the fold cannot
tell from the owner's**. Stage 1 therefore does not let the loop amend
anything; only people post objectives. The real fix is the owner holding an
identity distinct from the laptop seats, which is the same identity split as
CP7 (keywake and grok, `parley development` @218, @221). Until then the board
shows *who posted*, and a reader must not treat a seat's amendment as the
owner's word.

**A second limit, recorded 2026-09-23 after the board shipped: the board's
input is every conversation the organization can read.** The fold keys every
object by `(conversation, id)` (statefs.ai #121), which is what stops a
copied event id from taking over an objective, reopening a close or erasing
a claim — keywake ran those four probes plus the amend-authority one against
`636112f`, and the champion ran them independently; both got the same five
answers. The residual that keying leaves is **noise, not integrity**: a seat
that can write to a scanned conversation can put its own objective on the
owner's board, and a copied id in another conversation shows as its own
separate objective rather than replacing anything.

Nothing is hidden and nothing is taken over, and every objective carries the
conversation it came from, so noise is attributable. **If the board ever
gets crowded the fix is the scan's scope, not the fold**: the owner says
which conversations carry objectives. That is a product decision about where
goals live, and it stays cheap precisely because the fold is honest about
what it read — including the conversations it could not read, which it
already names under `not_checked`.

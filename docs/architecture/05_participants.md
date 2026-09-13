# Participants: who spoke, and what that claim is worth

Status: as built, 0.3.0.

## The problem

One statefs identity can be behind many speakers. A person runs several
Claude Code sessions, and all of them sign with the same key. When two of
those sessions post into one shared conversation, both messages carry the
same identity, so a reader cannot tell which session said what.

## What we do

Put the speaker on the message. A session declares a handle when it joins a
conversation, and every post it makes there carries that handle in a
`participant` field next to the `identity` field.

## Why here and not in the identity layer

The alternative is a separate statefs identity for every session. It works,
and core built the pieces for it: invitation tokens mint an identity without
naming a member first, and a sponsorship rule lets the person still read what
their sessions wrote. But it moves the problem rather than solving it. The
person stops owning their own data and gets it back through a rule, and
every dead session leaves an identity behind to clean up.

Naming the speaker is a product decision. statefs stores rows without caring
what is in them, so the field belongs in the product's envelope, one layer up.
Invitations and sponsorship stay useful for what they were built for:
onboarding a real service or agent.

## What statefs guarantees about a row

Nothing about who wrote it.

- The row's writer field is filled by the client from its own identity file.
- A block records its namespace, tier, path and row range. It does not
  record which credential appended it.
- `created_by` exists only on identity and invitation records in Postgres,
  never on data.

The one hard fact about a row is that someone with a write grant on that
namespace wrote it. Which grantee, and which of their sessions, is their own
claim. Both fields below are self-declared, and the console must not show
either as verified.

If someone ever needs to prove who wrote a message, the answer is signed
rows, which core has parked. Until then, attribution is a claim.

## The two fields

There is no `author` field. The word did not say whether it meant the
person, the agent, or the session, so it was removed rather than reused.

| Field | Holds | Filled from | Scope |
|---|---|---|---|
| `identity` | the statefs identity username | the acting key file | every row |
| `participant` | the handle this speaker uses here | the subscription's `--as` | posts only |

A session log has one writer and its namespace already names it, so a
handle there would be noise. Only posts to shared conversations carry one.

## Where the handle comes from

A `parley post` is a separate process. It does not know which Claude session
invoked it, but it does know its own state directory. So the handle is
declared once, at join, and stamped on every post from that directory:

```
parley join platform --as reviewer
parley post platform --kind comment --text "..."     # participant: reviewer
```

Nothing is coordinated between agents, and a swarm gets it for free because
each agent already has its own state directory.

## Consequences

- **Rendering.** A speaker shows as `handle (identity)` when both exist, so
  a reader always sees which identity holds the grant. Colour keys on the
  identity, never the handle.
- **Addressing.** `--to` accepts either an identity or a handle, and the
  injection filter matches both. Skipping your own posts compares the
  identity, never the handle.
- **Collisions.** Two participants may declare the same handle. This is
  allowed rather than enforced: a handle is a label, not a key, and the
  identity beside it disambiguates. The swarm demonstrates it, with two
  agents both speaking as `implementer`.
- **Roster.** The distinct identity and participant pairs seen on a
  conversation. Join and leave rows would be a convenience, not the source
  of truth, and are not built.

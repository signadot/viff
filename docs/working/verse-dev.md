# `verse-dev`: verse, coordinating verse

Status: proposal, nothing here runs. This is the first drive named by
[contributing.md](contributing.md), which defines what a drive is and what it owes.

## Why this one first

Contributing to verse *is* an SDLC flow: changes land, some break things, somebody has to
notice, and the deciding has a dial on it between "just do it" and "ask everyone." That is the
shape verse claims to run. So the fastest honest feedback available to us is verse running the
process that governs verse — and if it cannot, that is the finding, in week one rather than
month six.

It is also the only drive where every participant is already a user of the subject matter, so
nothing has to be explained before the wall gets hit.

## Three steps

Ordered by how much already works.

### 1. The tree, as entities

`verse connect` federates this checkout, so every `docs/working/*.md` becomes an entity
([reference/connect.md](../reference/connect.md)). A rule that reacts to a new `*-plan.md`
appearing — announcing it, or posing it to be read — is the smallest real charter anybody will
have written against a subject that is not seed data.

Expected to work. The finding is what it costs to write: whether the connect scope, the
per-file validator, and the rule's `on` compose the way the tutorial implies when the subject
is somebody's actual repo rather than `demo:pr:1`.

### 2. A contract notice as a motion

[contributing.md](contributing.md#coordinating-a-break) uses `git issue` for a contract notice
because it works today and needs nothing. But read what that mechanism actually asks for:
**visible to all, objectable by any, blockable by none, carried by a clock.** That is not an
issue. That is a gate carrying a formula ([tutorial §5](../tutorial.md#5-gates-and-the-docket))
— and `verse docket` is somewhere a dogfooder is already looking, which is exactly the
visibility problem `git issue` has.

The drive earns its keep here immediately, because the formula may not be expressible. The
documented shape is:

```tony
formula: !until [ !quorum 2, !deadline "4h" ]    # the clock defaults it to DENY
```

A notice wants the inverse — it carries on the clock, and an objection is what interrupts it:

```tony
formula: !until [ <the clock carries>, !quorum { choice: reject, n: 1 } ]   # expressible?
```

Three ways this resolves, and we do not know which: `!until` inverts cleanly and the notice is
four lines of charter; it needs a new requirement leaf, and the ask is a small addition to a
grammar that is otherwise settled; or a notice is not a gate at all, because a gate holds an
*action* and there is no action here — only a fact somebody may want to argue with.

That third possibility is the interesting one, and it points at the same gap
[channel-observe](../roadmap/channel-observe.md) names: durable, named, and conversational is
precisely the middle that a `say`, a motion, and an entity each miss.

### 3. Commits landing, as facts

[sources.md](../sources.md) reflects git state as `git:*` entities a trigger reacts to, and
[the roadmap](../roadmap/README.md#what-is-not-here) already records that this git Source has
no command exposing it.

So this step is expected to hit a wall, and hitting it is the result: it converts "there is a
git Source nobody exposes" from a line in the roadmap into a thing somebody wanted, on a date,
for a reason. That is a different kind of evidence than a design note, and it is the kind this
whole exercise exists to generate.

## What it produces

Working notes, under `docs/working/verse-dev-*.md`. Code only where a wall turns out to be
cheap to remove. A drive that lands zero commits and one honest note about where the charter
grammar fought you has done its job completely.

Some of these steps will not work. Writing down *which*, and *where*, is the deliverable.

## Open questions

- **Does the notice want to be a gate at all?** Step 2's third branch. If a notice is a fact
  people may argue with rather than an action awaiting permission, the docket is the wrong
  surface and we have found something about the model, not about the process.
- **What holds the drive's state?** The charters and seeds live on `use/verse-dev`, but the
  entities they react to are this repo. Whether a drive about the repo can run against a scope
  of the repo without the two confusing each other is untested.
- **Who is `--as` here?** A motion settles attributably, and the participants are the same
  handful the notice is for. Whether that is an identity verse already has, or a registry
  somebody has to seed by hand, is unchecked.

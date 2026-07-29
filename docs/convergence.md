# Convergence: whether a charter settles, and what it costs

A trigger is evaluated at every commit that touches what it reads. That is one rule for
everything ([the trigger model](trigger-model.md#the-rule)), and it moves the burden: nothing in
the dispatcher decides when a rule stops, so the charter has to. This note is the argument that
it does — what makes a firing end, what makes the settled store a function of (charter, inputs)
rather than of how firings interleave, and what per-commit evaluation costs to run.

Two of the sources of ambiguity below are checked at install, one is warned about, and two are
neither. That is the honest state, and the unchecked ones are named rather than hidden.

## The cost of asking: a read per commit

First the thing that decides whether any of the rest is affordable.

`Engine.evaluate` takes a snapshot per commit, and a snapshot is a store read. Measured on a
300-commit verse against a locally-launched logd:

| what is read | cost |
| --- | --- |
| the whole verse | ~73 ms |
| the 300-entity slice the commits landed in | ~74 ms |
| a ONE-entity slice the commits never touched | ~73 ms |

The path makes no difference, because the cost is not the document — it is replaying the log to
that commit. `AsOf(rev/2)` costs half of `AsOf(head)` on the same store, which is the tell. A
CURRENT read costs the same as a historical one, so this is not about asking for history. The
`within` argument on `AsOf` bounds what crosses the wire and, measured, does nothing else; the
comment on it says so rather than claiming a speedup it does not deliver.

None of that is unreasonable for what it is: four layers (client, server, docd, logd), an index
lookup and a disk read, with goroutine synchronisation on two of the links, and no read cache.
It is in the same order as a loopback SQL query. And it is BOUNDED — logd snapshots every
`MaxCommits` (default 1000, configurable at launch), so a read costs distance-from-snapshot
rather than distance-from-genesis, and the curve is a sawtooth rather than a ramp.

**What keeps this affordable in practice is not asking.** A trigger is skipped at a commit that
touched none of the subtrees it reads, and the skip is exact rather than a heuristic — see
[the commit is the unit](trigger-model.md#the-commit-is-the-unit) for why it is
indistinguishable from evaluating. A charter of rules over quiet slices costs nothing at all;
what costs is a rule over the slice a burst is landing in.

The standing improvement is to carry the rolled-forward document rather than re-read it — the
shape `Since` already uses, which reads once at its floor and rolls forward with the deltas it
is replaying. It is not built, and nothing above depends on it: the model is correct as it
stands, and this is a constant factor on observation. It is recorded with the rest of the
residual in [working/firing-residual.md](working/firing-residual.md).

## One snapshot, so two reads cannot disagree

`when` and the subject selection come from the same `AsOf(C)` — one `MatchAt` over the root,
which docd answers across base and every mount at that commit, with both evaluations then pure
functions of that document. So a firing cannot act on a member under a condition that stopped
holding between two reads, and which members it sees does not depend on when a `Select` ran.

The edge is `AsOf(C-1)` against `AsOf(C)` rather than against what the engine remembers seeing,
which is what makes a replayed commit — a catch-up, a restart — reach the same verdict as the
live one.

One looseness is left, and it is named rather than hidden: an action's `$references` read the
subject's payload from the store as it stands **when the action runs**, not from the snapshot the
rule was evaluated against (`Engine.refPayload`). Which entity a firing is about cannot slide —
the subject ref is settled before any gate, so however long a human takes over an approval, the
decision and the action are about the same entity — but the value interpolated into a patch is
current rather than as-of-C. Reading it from the snapshot instead would mean holding the
evaluated document through the firing, which is the same work as
[the roll-forward](working/firing-residual.md#3-the-read-per-commit-carry-the-document-do-not-re-read-it)
and tightens for free with it.

## What ends a firing

Four things, in the order they carry weight:

**A store write ends itself.** Rewriting the same value produces no commit
([change events](change-events.md#the-decision), edge-on-real-change), so an action that has
reached its fixpoint raises no next evaluation. That is what makes an idempotent rule converge
even with nothing latching it, and it is pinned by
`entity.TestSameValueWriteRaisesNoCommit`. Note what it is not: `entity.Modify` retries on a
failed precondition, so it prevents lost updates rather than repeated work.

**A latch takes the subject out of the selection.** Write a field the subject's predicate
constrains and the next evaluation does not select it. This is the ordinary shape, and for a
SPAWN it is the only one: a spawn is not a store write, nothing about it is inert on repetition,
and the engine's dedupe holds only while the process is live — once reaped, the next commit
spawns again. So a **spawning action that writes nothing its subject reads is refused at
install**, with a diagnostic naming the fields it could latch on. The check is over KEYS, not
values, which is what makes it decidable through templated values (`$result`): "does this write
something the subject reads" is answerable where "does this write something that stops matching"
is not.

**A sequence bounds itself.** `startSeq` refuses to re-run a sequence that has completed for a
subject at a shape, and that claim is a fact in the store rather than a live-process check, so
unlike a bare spawn it survives the reap. Demanding a subject latch of a `seq` as well would be
demanding the same guarantee twice, so the check excludes it — as it does a completion hook's
spawn, which is bounded by the completion above it.

**In-flight guards cover the gap before the latch lands.** Every commit re-selects a subject
whose latch write has not happened yet, so a firing is held for as long as its work lasts, and
that is two things: `claimFlight` covers the firing itself (gates being answered, action
applied), and `working` covers what it started (a spawned process still running). Neither
changes what RUNS — `spawnHooked` already refuses a second process under a live id — but
together they stop a firing for work already under way from posing a question or spending
budget on its way to being told no. Both key on (trigger, subject), the same identity the spawn
id, the gate question and the motion channel use.

## A narrow `on` samples; a vacuous `on` reconciles

With no `on`, every commit touching what the rule reads re-evaluates it, so nothing that could
make the predicate true goes unobserved.

With an `on`, the rising edge fires once, over the subject set *as it stood at that commit*. A
member arriving afterwards is not acted on until the cause falls and rises again:

```tony
- name: ship-at-green
  condition:
    on:      { system: ci,   kind: status, value: { state: green } }
    subject: { system: demo, kind: pr,     value: { stage: reviewed } }
  action:
    patch: { stage: shipped }
```

C1 turns CI green and the edge fires over the PRs reviewed at C1. C2 adds a PR at `reviewed`;
`on` was already true, so there is no edge, and that PR is never shipped.

That is not ambiguity — it is deterministic, and it is what an edge means. But it is the
difference between *"ship everything reviewed at the moment CI went green"* and *"ship
everything reviewed while CI is green"*, and only the first is what this charter says. So the
engine **warns at install** when `on` does not cover a subtree the rule also reads, naming which
of the two sentences was written and how to get the other (drop the `on`, keep the condition as
a `when`). It is a warning rather than an error because both sentences are legitimate; it is
decidable because `Formula.Subtrees` already computes those sets.

## The cycle, which is the one that is not checked

A single rule failing to settle is easy to spot. The reachable failure is a CYCLE ACROSS RULES,
and nothing in the model sees it:

```tony
- name: fwd
  condition:
    subject: { system: demo, kind: pr, value: { stage: a } }
  action:
    patch: { stage: b }
- name: back
  condition:
    subject: { system: demo, kind: pr, value: { stage: b } }
  action:
    patch: { stage: a }
```

Each writes exactly what the other selects on. Measured, with the runaway backstop set to 40:
the engine fires 40 times and stops there, having written 40-odd commits. Without a budget it
does not stop.

Every protection above misses it, and misses it correctly. The substrate's no-op suppression
never engages, because each write is a REAL change. `claimFlight` and `working` never engage,
because each firing genuinely completes. And the latch check passes, because both rules DO write
a field their subject reads — it asks whether a rule advances its subject out of its own
selection, and both do, into each other's.

Two things about the backstop follow, and both are as they are today: `maxFires` defaults to
UNBOUNDED and nothing in `versed` sets it, so the protection that stopped the measurement above
is not on in production; and it is one counter for the whole engine rather than per trigger, so
a single looping rule would exhaust the budget for every other rule as well.

The check is decidable for literal charters, and a *paced* cycle — one passing through a clock
rather than through the last write — is a control loop rather than a bug, so the check to want
is not "no cycles". Both, and the ticker they need, are written up in
[working/firing-residual.md](working/firing-residual.md).

## Two triggers, one field

Two triggers whose subject selections can intersect, writing the same payload path with
different values, settle wherever the last write lands. Not introduced by per-commit evaluation
— it is reachable under any dispatch — but a vacuous `on` multiplies the interleavings.

It is decidable for literal patches (intersecting selections on system/kind plus comparable
predicates, then same-path-different-value) and undecidable through templates, so it could be a
warning at install. It is not built, deliberately: it is subsumed by the cycle check above, and
building the weaker one first would mean building it twice.

This is the one source with no principled fix even once detected. Two rules disagreeing about a
field is a charter that means two things, and only its author can say which.

## The claim

With one snapshot per commit and every action's effect either a store write or a spawn the
install-time check has bounded, **the settled store is a function of (charter, inputs)** — not
of how firings interleave — except in two named places: a cycle across rules, which does not
settle at all and is not yet detected, and two rules writing one field, which settles
arbitrarily. Both are decidable for literal charters. Neither is a property of the model; both
are properties of a charter, which is where they belong.

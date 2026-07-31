# Convergence: whether a charter settles, and what it costs

A trigger is evaluated at every commit that touches what it reads. That is one rule for
everything ([the trigger model](trigger-model.md#the-rule)), and it moves the burden: nothing in
the dispatcher decides when a rule stops, so the charter has to. This note is the argument that
it does — what makes a firing end, what makes the settled store a function of (charter, inputs)
rather than of how firings interleave, and what per-commit evaluation costs to run.

Of the sources of ambiguity below, two are checked at install, one is warned about, one is caught
at runtime, and one is neither. That is the honest state, and the unchecked one is named rather
than hidden. A charter may also declare a [level ordering](strata.md), which adds install-time
checks of its own over what an action writes and what it may read.

## The cost of asking: a read per commit

First the thing that decides whether any of the rest is affordable.

`Engine.evaluate` takes a snapshot per commit, and a snapshot is a store read. Measured against a
locally-launched logd, reading the whole verse, reading the one slice the commits landed in, and
reading a slice they never touched all cost the SAME.

The path makes no difference, because the cost is not the document — it is replaying the log to
that commit. `AsOf(rev/2)` costs half of `AsOf(head)` on the same store, which is the tell. A
CURRENT read costs the same as a historical one, so this is not about asking for history. The
`within` argument on `AsOf` bounds what crosses the wire and, measured, does nothing else; the
comment on it says so rather than claiming a speedup it does not deliver.

Absolute figures are deliberately not recorded: they belong to the substrate and move with it —
logd's throughput is actively worked on — and a doc asserting last month's milliseconds would be
read as a promise. What holds across those changes is the shape. A read is four layers (client,
server, docd, logd), an index lookup and a disk read, in the same order as a loopback SQL query.
And it is BOUNDED — logd snapshots every `MaxCommits` (default 1000, configurable at launch), so
a read costs distance-from-snapshot rather than distance-from-genesis, and the curve is a sawtooth
rather than a ramp.

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

An action's reads are pinned to C too, and the ones that are say so in their spelling.
`$subject.value` — the subject's payload — and the two path reads
([`!get-path` / `!list-paths`](trigger-model.md#get-path-and-list-paths--reaching-another-entity))
are all answered from **one snapshot per firing, taken at the causing commit**. So a firing held
at a gate for a day writes from the state its rule was evaluated against, not from whatever the
store reached while a human thought about it; and a fan-out cannot mix one member's state at C
with another's at some later commit.

One looseness is left, and it is named rather than hidden: the **flat bindings** — `$stage` and
the subject payload's other fields by name — still read the payload as it stands when the action
runs (`Engine.refPayload`). Which entity a firing is about cannot slide, since the subject ref is
settled before any gate; but `$stage` is current where `$subject.value.stage` is as-of-C, and
those are the same datum under two spellings.

The reason is cost rather than principle. `refPayload` runs for every firing, so pinning it means
a snapshot for every firing, where the as-of-C references above are taken only when a charter
actually asks for one. Closing it properly is the same work as
[the roll-forward](working/firing-residual.md#2-the-read-per-commit-carry-the-document-do-not-re-read-it)
— hold the evaluated document through the firing — and tightens for free with it.

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

## The cycle, and how it is caught

A single rule failing to settle is easy to spot. The reachable failure is a CYCLE ACROSS RULES,
which every protection described above misses:

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

Each writes exactly what the other selects on, and each protection above misses it correctly. The
substrate's no-op suppression never engages, because each write is a REAL change. `claimFlight`
and `working` never engage, because each firing genuinely completes. And the latch check passes,
because both rules DO write a field their subject reads — it asks whether a rule advances its
subject out of its own selection, and both do, into each other's.

It is caught at RUNTIME, by [the cycle throttle](cycle-throttle.md): every write an engine makes
during a firing carries the causal depth of the chain that produced it, and a chain deeper than
the installed trigger count has necessarily repeated a rule. The charter above stops after three
firings and names both rules in the log.

The check is dynamic rather than an install-time graph over the charter, and that is the whole
reason it can tell a runaway from a control loop instead of refusing both. A cycle is how you
write a control loop; what makes the pair above pathological is not that it cycles but that it
cycles at the speed of the store. Pacing is a property of how a chain RUNS — it breaks wherever
real time enters, through a spawned process, through the world, through a decision somebody
answers — not of how the charter is written, so no static analysis of the rules could distinguish
them. A cycle through a gate never trips, however many laps it runs; an unpaced one stops after
the trigger count and is reported by name.

## Two triggers, one field

Two triggers whose subject selections can intersect, writing the same payload path with
different values, settle wherever the last write lands. Not introduced by per-commit evaluation
— it is reachable under any dispatch — but a vacuous `on` multiplies the interleavings.

It is decidable for literal patches (intersecting selections on system/kind plus comparable
predicates, then same-path-different-value) and undecidable through templates, so it could be a
warning at install. It is not built. The cycle throttle does not subsume it: two rules disagreeing
about a field settle wherever the last write lands, without any chain growing, so nothing trips.

This is the one source with no principled fix even once detected. Two rules disagreeing about a
field is a charter that means two things, and only its author can say which.

## The claim

With one snapshot per commit and every action's effect either a store write or a spawn the
install-time check has bounded, **the settled store is a function of (charter, inputs)** — not
of how firings interleave — except in two named places: a cycle across rules, which does not
settle at all, and two rules writing one field, which settles arbitrarily. Neither is a property
of the model; both are properties of a charter, which is where they belong.

Of the two, the cycle is **detected and stopped**, by causal depth at runtime. Two rules writing
one field is undetected and has no principled fix even once found: a charter that means two things
is one only its author can settle.

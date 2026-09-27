# Convergence

A trigger is evaluated at every commit that touches what it reads. That is one rule for
everything ([the trigger model](trigger-model.md#the-rule)), and it moves the burden: nothing in
the dispatcher decides when a rule stops, so the charter has to. This note is the argument that
it does — what makes a firing end, what makes the settled store a function of (charter, inputs)
rather than of how firings interleave, and what per-commit evaluation costs to run.

Of the sources of ambiguity below, two are checked at install, one is warned about, one is caught
at runtime, and one is neither. The unchecked one is named rather than hidden. A charter may also
declare a [level ordering](strata.md), which adds install-time checks of its own over what an
action writes and what it may read.

## The cost of asking: a read per commit

`Engine.evaluate` takes a snapshot per commit, and a snapshot is a store read. **What it costs
depends on how much of the verse it names**, and the difference is the largest constant in the
engine: a read narrowed to the places a rule reads is flat in the tens of milliseconds, while a
read of the whole root grows with the store — measured at ~1.2ms per commit past two thousand, so
seconds once a few thousand have landed, and past the store's own read budget at around nine and
a half thousand.

So every read the engine makes names its places. `evaluate` narrows to the subtrees the rule
reads, and a firing's snapshot narrows to those plus the literal prefix of every path read in the
rule (`firingWithin`). The one case that cannot be narrowed is a read whose address is not
settled until the firing resolves it — `!get-path {at: "$subject.value.where"}` — and there the
whole verse is read rather than a place that would answer nothing.

Absolute figures are deliberately not asserted as a promise: they belong to the substrate and
move with it. What holds across those changes is the shape — a read is four layers (client,
server, docd, logd), an index lookup and a disk read, in the same order as a loopback SQL query,
and it is bounded by logd's snapshots every `MaxCommits` (default 1000), so a read costs
distance-from-snapshot rather than distance-from-genesis. What the measurement above pins down is
the other axis: whatever the substrate's constants, asking for everything costs more than asking
for what you read, and it costs more as the verse fills.

**What keeps this affordable in practice is not asking.** A trigger is skipped at a commit that
touched none of the subtrees it reads, and the skip is exact rather than a heuristic. See
[the commit is the unit](trigger-model.md#the-commit-is-the-unit) for why it is indistinguishable
from evaluating. A charter of rules over quiet subtrees costs nothing at all. What costs is a rule
over the subtree a burst is landing in.

The standing improvement is to carry the rolled-forward document rather than re-read it, the shape
`Since` already uses, which reads once at its floor and rolls forward with the deltas it is
replaying. It is not built, and nothing above depends on it: the model is correct as it stands,
and this is a constant factor on observation.

## One snapshot, so two reads cannot disagree

`when` and the subject selection come from the same `AsOf(C)`, one `MatchAt` over the root, which
docd answers across base and every mount at that commit. Both evaluations are then pure functions
of that document. So a firing cannot act on a member under a condition that stopped holding
between two reads, and which members it sees does not depend on when a `Select` ran.

The edge is `AsOf(C-1)` against `AsOf(C)` rather than against what the engine remembers seeing,
which is what makes a replayed commit — a catch-up, a restart — reach the same verdict as the live
one.

An action's reads are pinned to C too, and the ones that are say so in their spelling.
`$subject.value`, the subject's payload, and the two path reads
([`!get-path` / `!list-paths`](reference/action.md#get-path-and-list-paths--reaching-another-entity))
are all answered from **one snapshot per firing, taken at the causing commit**. So a firing held at
a gate for a day writes from the state its rule was evaluated against, not from whatever the store
reached while a human thought about it, and a fan-out cannot mix one member's state at C with
another's at some later commit.

One looseness is left, and it is named rather than hidden. The **flat bindings** — `$stage` and the
subject payload's other fields by name — read the payload as it stands when the action runs
(`Engine.refPayload`). Which entity a firing is about cannot slide, since the subject ref is
settled before any gate, but `$stage` is current where `$subject.value.stage` is as-of-C, and those
are the same datum under two spellings.

The reason is cost rather than principle. `refPayload` runs for every firing, so pinning it means a
snapshot for every firing, where the as-of-C references above are taken only when a charter
actually asks for one. Closing it properly is the same work as the roll-forward above,
holding the evaluated document through the firing, and tightens for free with it.

## What ends a firing

Four things, in the order they carry weight.

**A store write ends itself.** Rewriting the same value produces no commit
([change events](change-events.md#the-decision), edge-on-real-change), so an action that has
reached its fixpoint raises no next evaluation. That is what makes an idempotent rule converge even
with nothing latching it, and it is pinned by `entity.TestSameValueWriteRaisesNoCommit`. Note what
it is not: `entity.Modify` retries on a failed precondition, so it prevents lost updates rather
than repeated work.

**A latch takes the subject out of the selection.** Write a value the subject's predicate refuses
and the next evaluation does not select it. This is the ordinary shape, and for a spawn it is the
only one: a spawn is not a store write, nothing about it is inert on repetition, and the engine's
dedupe holds only while the task is live. Once reaped, the next commit spawns again. So a
**spawning action whose subject can still match after it runs is refused at install**, with a
diagnostic naming the paths it could latch on.

The check decides whether the predicate and the action's post-state are *unsatisfiable*, and it
needs no solver: at fire time the predicate was true, so the only thing that can falsify it is a
path the action writes. That makes it a three-valued fold over the predicate's own structure —
`and`/`or`/`not` in Kleene logic, `tony.Match` at the leaves — so a nested predicate, a whole-value
replace and a root operator are each decided as what they are rather than as their top-level key
names. Values that are only knowable at fire time (`$result`, an operator the fold does not model)
come out *undecided*, and undecided is a pass. The refusal rests on what the check can see: a rule
that writes nothing its subject reads, or one whose write leaves the predicate satisfied. Writing
the same value back latches a store write, and never a spawn.

**A sequence bounds itself.** `startSeq` refuses to re-run a sequence that has already run to a
conclusion for a subject at a shape — `done` if it finished, `ended` if an off-ramp stopped it —
and that is a fact in the store rather than a live-task check, so unlike a bare spawn it survives
the reap. Demanding a subject latch of a `seq` as well would be demanding the same guarantee twice,
so the check excludes it, as it does a completion hook's spawn, which is bounded by the completion
above it.

**In-flight guards cover the gap before the latch lands.** Every commit re-selects a subject whose
latch write has not happened yet, so a firing is held for as long as its work lasts. That is two
things: `claimFlight` covers the firing itself, gates being answered and the action applied, and
`working` covers what it started, a spawned task still running. Neither changes what runs, since
`spawnHooked` already refuses a second task under a live id, but together they stop a firing for
work already under way from posing a question or spending budget on its way to being told no. Both
key on (trigger, subject), the same identity the spawn id, the gate question and the motion channel
use.

## A narrow `on` samples; a vacuous `on` reconciles

With no `on`, every commit touching what the rule reads re-evaluates it, so nothing that could make
the predicate true goes unobserved.

With an `on`, the rising edge fires once, over the subject set *as it stood at that commit*. A
member arriving afterwards is not acted on until the cause falls and rises again:

```tony
- name: ship-at-green
  condition:
    on:      {path: "ci.status.*", value: { state: green }}
    subject: {path: "demo.pr.*", value: { stage: reviewed }}
  action:
    patch: { stage: shipped }
```

C1 turns CI green and the edge fires over the PRs reviewed at C1. C2 adds a PR at `reviewed`; `on`
was already true, so there is no edge, and that PR is never shipped.

That is deterministic, and it is what an edge means. But it is the difference between *"ship
everything reviewed at the moment CI went green"* and *"ship everything reviewed while CI is
green"*, and only the first is what this charter says. So the engine **warns at install** when `on`
does not cover a subtree the rule also reads, naming which of the two sentences was written and how
to get the other: drop the `on`, keep the condition as a `when`. It is a warning rather than an
error because both sentences are legitimate, and it is decidable because `Formula.Subtrees` already
computes those sets.

## The cycle, and how it is caught

A single rule failing to settle is easy to spot. The reachable failure is a cycle across rules,
which every protection described above misses:

```tony
- name: fwd
  condition:
    subject: {path: "demo.pr.*", value: { stage: a }}
  action:
    patch: { stage: b }
- name: back
  condition:
    subject: {path: "demo.pr.*", value: { stage: b }}
  action:
    patch: { stage: a }
```

Each writes exactly what the other selects on, and each protection above misses it correctly: every
write is a real change, every firing genuinely completes, and both rules do advance their subject
out of their own selection — into each other's.

It is caught at runtime, by [the cycle throttle](cycle-throttle.md), which bounds the causal depth
of a chain rather than analysing the charter. The pair above stops after three firings and names
both rules in the log. The check is dynamic precisely so that it can tell a runaway from a control
loop instead of refusing both: a cycle paced by a task, the world, or a decision somebody answers
never trips.

## Two triggers, one field

Two triggers whose subject selections can intersect, writing the same payload path with different
values, settle wherever the last write lands. This is reachable under any dispatch rather than
introduced by per-commit evaluation, but a vacuous `on` multiplies the interleavings.

It is decidable for literal patches — subject paths that overlap segment by segment, plus comparable
predicates, then same-path-different-value — and undecidable through templates, so it could be a
warning at install. It is not built. The cycle throttle does not subsume it: two rules disagreeing
about a field settle wherever the last write lands, without any chain growing, so nothing trips.

This is the one source with no principled fix even once detected. Two rules disagreeing about a
field is a charter that means two things, and only its author can say which.

## The claim

With one snapshot per commit, and every action's effect either a store write or a spawn the
install-time check has bounded, **the settled store is a function of (charter, inputs)** rather than
of how firings interleave. There are two named exceptions, and both are properties of a charter
rather than of the model: a cycle across rules, which does not settle at all and is detected and
stopped by causal depth at runtime, and two rules writing one field, which settles arbitrarily and
is undetected.

# Convergence

Verse evaluates a [rule](glossary.md#rule) at every [commit](glossary.md#commit) that touches
what the rule reads. That is one rule for everything
([the trigger model](trigger-model.md#the-rule)). Nothing in the dispatcher decides when a rule
stops, so the [charter](glossary.md#charter) has to decide it.

This page shows that a charter does stop. It covers three things:

- what makes a [firing](glossary.md#firing) end;
- what makes the settled [store](glossary.md#store) a function of (charter, inputs), and not of
  the order in which firings interleave;
- what it costs to evaluate at every commit.

The page names five sources of ambiguity:

| How many | How verse handles them |
|---|---|
| two | checked at install |
| one | a warning at install |
| one | caught at runtime |
| one | neither checked nor caught |

The page names the unchecked source and does not hide it. A charter may also declare a
[level ordering](strata.md). A level ordering adds its own checks at install, over what an
action writes and what it may read.

## The cost of asking: a read per commit

`Engine.evaluate` takes a snapshot at each commit, and a snapshot is a read of the store.
**The cost of a snapshot depends on how much of the store it names.** This difference is the
largest constant in the engine.

| Read | Cost |
|---|---|
| narrowed to the places a rule reads | flat, in the tens of milliseconds |
| the whole root | grows with the store |

A read of the whole root costs about 1.2ms more for each commit past two thousand. So it takes
seconds once a few thousand commits have landed. At around nine and a half thousand commits, it
passes the store's own read budget.

So every read that the engine makes names its places:

- `evaluate` narrows to the subtrees that the rule reads.
- The snapshot of a firing narrows to those subtrees and to the literal prefix of every
  [path read](glossary.md#path-read) in the rule (`firingWithin`).

One case cannot be narrowed. It is a read whose address the firing settles only when it
resolves it: `!get-path {at: "$subject.value.where"}`. There, the engine reads the whole store.
A narrower read would read a place that answers nothing.

The page does not promise absolute figures. They belong to the substrate and change with it.
The shape holds across those changes:

- A read passes four layers (client, server, docd, logd), an index lookup and a disk read. That
  is the same order of cost as a SQL query over loopback.
- logd takes a snapshot every `MaxCommits` commits (default 1000). So a read costs the distance
  from the last snapshot, not the distance from the first commit.

The measurement above fixes the other axis. Whatever the constants of the substrate are, a read
of everything costs more than a read of what the rule reads. The difference grows as the store
fills.

**In practice, a rule stays affordable because the engine often does not ask.** The engine skips
a rule at a commit that touched none of the subtrees the rule reads. The skip is exact, not a
heuristic. See [the commit is the unit](trigger-model.md#the-commit-is-the-unit) for why a skip
gives the same result as an evaluation. A charter of rules over quiet subtrees costs nothing. A
rule over the subtree where a burst of writes lands costs something.

A possible improvement is to keep the document and roll it forward, and not read it again.
`Since` works this way: it reads once at its floor and rolls forward with the deltas it replays.
This improvement is not built. Nothing above depends on it. The model is correct as it stands,
and the improvement is a constant factor on observation.

## One snapshot, so two reads cannot disagree

`when` and the selection of the subject come from the same `AsOf(C)`. That is one `MatchAt`
over the root. docd answers it across the base and every mount at commit C. Both evaluations
are then pure functions of that one document. What follows for a condition and for an edge is
in [the rule](trigger-model.md#the-rule).

The reads of an [action](glossary.md#action) are pinned to C too, and their spelling says so.
These reads are answered from **one snapshot per firing, taken at the commit that caused it**:

- `$subject.value`, the payload of the subject;
- the two path reads,
  [`!get-path` / `!list-paths`](reference/action.md#get-path-and-list-paths--reaching-another-entity).

A firing may wait at a gate for a day. It then writes from the state its rule was evaluated
against. It does not write from the state the store reached while a person decided. A fan-out
cannot mix the state of one member at C with the state of another member at a later commit.

One inexactness remains, and the page names it. The **flat bindings** read the payload as it
stands when the action runs (`Engine.refPayload`). A flat binding is `$stage`, or another field
of the subject's payload by its name. The entity a firing is about cannot change, because verse
settles the subject ref before any gate. But `$stage` is current, and `$subject.value.stage` is
as of C. They are the same datum under two spellings.

The reason for this inexactness is cost. `refPayload` runs for every firing. To pin it, verse
would take a snapshot for every firing. The as-of-C references above take a snapshot only when
a charter asks for one. The proper fix is the same work as the roll-forward above: keep the
evaluated document through the firing. When that is built, this inexactness closes with it.

## What ends a firing

Four things end a firing. They are listed here in order of importance.

**A store write ends itself.** A write of the same value makes no commit
([change events](change-events.md#the-decision): an edge only on a real change). So an action
that has reached its fixed point raises no next evaluation. For this reason an idempotent rule
converges even when nothing latches it. `entity.TestSameValueWriteRaisesNoCommit` tests this.

Note what this is not. `entity.Modify` retries on a failed precondition. So it prevents lost
updates. It does not prevent repeated work.

**A [latch](glossary.md#latch) takes the subject out of the selection.** Write a value that the
predicate of the subject refuses. The next evaluation then does not select the subject. This is
the ordinary shape, and for a spawn it is the only shape. A spawn is not a store write, and
nothing about it is inert when it repeats. The engine removes duplicates only while the task is
live. Once verse reaps the task, the next commit spawns again. So verse **refuses at install a
spawning action whose subject can still match after it runs.** The diagnostic names the paths it
could latch on.

The check decides whether the predicate and the state after the action are *unsatisfiable*
together. It needs no solver. At fire time the predicate was true, so only a path that the
action writes can make it false. So the check is a three-valued fold over the structure of the
predicate:

- `and`, `or` and `not` follow Kleene logic;
- `tony.Match` decides the leaves.

So the check decides a nested predicate, a replace of the whole value and a root operator each
as what it is, and not by the name of its top-level key.

Some values are known only at fire time: `$result`, or an operator that the fold does not model.
These come out *undecided*, and undecided passes. The check refuses only what it can see:

- a rule that writes nothing that its subject reads;
- a rule whose write leaves the predicate satisfied.

A write of the same value latches a store write. It never latches a spawn.

**A sequence bounds itself.** `startSeq` refuses to run a sequence again when it has already run
to a conclusion for a subject at a shape. The conclusion is `done` if the sequence finished, or
`ended` if an off-ramp stopped it. This is a fact in the store, not a check of live tasks. So,
unlike a bare spawn, it lasts after the reap. A subject latch on a `seq` would demand the same
guarantee a second time, so the check does not apply to a `seq`. It also does not apply to the
spawn of a completion hook, which the completion above it bounds.

**In-flight guards cover the time before the latch lands.** Every commit selects again a subject
whose latch write has not happened yet. So verse holds a firing for as long as its work lasts.
Two guards do this:

| Guard | What it covers |
|---|---|
| `claimFlight` | the firing itself: its gates being answered and its action applied |
| `working` | what the firing started: a spawned task that still runs |

Neither guard changes what runs, because `spawnHooked` already refuses a second task under a
live id. Together they stop a firing for work already under way from posing a question, or from
using budget, before verse tells it no. Both guards key on (rule, subject). The spawn id, the
gate question and the motion channel use the same identity.

## A narrow `on` samples; a vacuous `on` reconciles

With no `on`, every commit that touches what the rule reads evaluates the rule again. So verse
observes everything that could make the predicate true.

With an `on`, the rising edge fires once, over the set of subjects *as it stood at that commit*.
Verse does not act on a member that arrives later, until the cause falls and rises again:

```tony
- name: ship-at-green
  condition:
    on:      {path: "ci.status.*", value: { state: green }}
    subject: {path: "demo.pr.*", value: { stage: reviewed }}
  action:
    patch: { stage: shipped }
```

At commit C1, CI turns green. The edge fires over the PRs that are at `reviewed` at C1. At C2, a
PR arrives at `reviewed`. `on` was already true, so there is no edge, and verse never ships that
PR.

That is deterministic, and it is what an edge means. But these are two different sentences:

- *"ship everything reviewed at the moment CI went green"*
- *"ship everything reviewed while CI is green"*

This charter says only the first. So the engine **warns at install** when `on` does not cover a
subtree that the rule also reads. The warning names which of the two sentences the charter says,
and how to get the other: drop the `on`, and keep the condition as a `when`.

It is a warning and not an error, because both sentences are legitimate. It is decidable because
`Formula.Subtrees` already computes those sets.

## The cycle, and how it is caught

A single rule that does not settle is easy to see. The failure that can happen is a cycle across
rules, and every protection above misses it:

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

Each rule writes exactly what the other selects on. Each protection above misses the cycle, and
is correct to miss it:

- every write is a real change;
- every firing completes;
- each rule moves its subject out of its own selection, and into the other rule's selection.

Verse catches the cycle at runtime, with [the cycle throttle](cycle-throttle.md). The throttle
bounds the causal depth of a chain. It does not analyse the charter. For the pair above, the
throttle trips after a few firings and names both rules in the log. By default a trip delays
the firing and does not stop it. The firing waits for `tripDuration` (default 1s) and then
proceeds. So the pair goes on firing at a slow, steady rate. A `tripDuration` of `0` refuses the firing
instead.

The check is dynamic, so that it can tell a runaway from a control loop and does not refuse
both. A cycle paced by a task, by the world, or by a question somebody answers never trips.

## Two triggers, one field

Two rules may have subject selections that can intersect, and may write the same payload path
with different values. They settle wherever the last write lands. This can happen under any
dispatch. Evaluation at every commit does not introduce it, but a vacuous `on` multiplies the
interleavings.

For literal patches, this conflict is decidable:

1. subject paths overlap segment by segment;
2. their predicates are comparable;
3. they write the same path with different values.

Through templates it is undecidable. So it could be a warning at install, but that warning is
not built.

The cycle throttle does not cover this case. Two rules that disagree about a field settle
wherever the last write lands, and no chain grows, so nothing trips.

This is the one source with no principled fix, even once verse detects it. Two rules that
disagree about a field make a charter that means two things. Only its author can say which.

## The claim

Verse takes one snapshot per commit. The effect of every action is either a store write or a
spawn that the install-time check has bounded. So **the settled store is a function of
(charter, inputs)**, and not of the order in which firings interleave.

The two exceptions are properties of a charter, not of the model:
[a cycle across rules](#the-cycle-and-how-it-is-caught) and
[two rules that write one field](#two-triggers-one-field).

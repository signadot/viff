# Convergence

Verse evaluates a [rule](glossary.md#rule) at every [commit](glossary.md#commit) that touches
what the rule reads ([the charter reference](trigger-model.md#the-rule)). Nothing in the
dispatcher decides when a rule stops, so the [charter](glossary.md#charter) decides it.

This page shows that a charter does stop. It covers three things:

- what makes a [firing](glossary.md#firing) end;
- why the settled [store](glossary.md#store) depends on the charter and its inputs, and not on
  the order in which firings run;
- what it costs to evaluate at every commit.

The page names five ways a charter can be ambiguous:

| How many | How verse handles them |
|---|---|
| two | checked at install |
| one | a warning at install |
| one | caught at runtime |
| one | warned at install in one case, and otherwise neither checked nor caught |

A charter may also declare a [level ordering](strata.md), which adds its own checks at install,
over what an action writes and what it may read.

## The cost of asking: a read per commit

`Engine.evaluate` takes a snapshot at each commit, and a snapshot is a read of the store.
**The cost of a snapshot depends on how much of the store it names.** This is the largest cost
in the engine.

| Read | Cost |
|---|---|
| narrowed to the places a rule reads | flat, in the tens of milliseconds |
| the whole root | grows with the store |

A read of the whole root costs about 1.2ms more for each commit past two thousand, so it takes
seconds after a few thousand commits. At around nine and a half thousand commits, it passes the
store's own read budget.

So every read the engine makes names its places:

- `evaluate` narrows to the subtrees that the rule reads.
- The snapshot of a firing narrows to those subtrees and to the literal prefix of every
  [path read](glossary.md#path-read) in the rule (`firingWithin`).

One case cannot be narrowed: a read whose address the firing learns only when it resolves it,
such as `!get-path {at: "$subject.value.where"}`. There, the engine reads the whole store.

The figures belong to the store and change with it. The shape does not:

- A read passes four layers (client, server, docd, logd), an index lookup and a disk read, about
  the cost of a SQL query over loopback.
- logd takes a snapshot every `MaxCommits` commits (default 1000), so a read costs the distance
  from the last snapshot, not from the first commit.
- Whatever those costs are, a read of everything costs more than a read of what the rule reads,
  and the difference grows as the store fills.

**In practice, a rule stays cheap because the engine often does not ask.** The engine skips a
rule at a commit that touched none of the subtrees the rule reads. The skip is exact: see
[the commit is the unit](trigger-model.md#the-commit-is-the-unit) for why it gives the same
result as an evaluation. Rules over quiet subtrees cost nothing. A rule over a subtree where many
writes land costs something.

The engine could keep the document and roll it forward instead of reading it again, as `Since`
does: it reads once and rolls forward with the changes it replays. Verse does not do this.
Nothing above depends on it; it would make observation cheaper by a constant factor.

## One snapshot, so two reads cannot disagree

`when` and the selection of the subject come from the same read of the store at commit C
(`AsOf(C)`, one `MatchAt` over the root). Both are then computed from that one document. What
this means for a condition and for an edge is in [the rule](trigger-model.md#the-rule).

The reads of an [action](glossary.md#action) are pinned to C too. They are answered from **one
snapshot per firing, taken at the commit that caused it**:

- `$subject.value`, the payload of the subject;
- the two path reads,
  [`!get-path` / `!list-paths`](reference/action.md#get-path-and-list-paths-reaching-another-entity).

A firing may wait at a gate for a day. It then writes from the state its rule was evaluated
against, not from the state the store reached while a person decided. A fan-out cannot mix one
member's state at C with another member's state at a later commit.

One exception remains. The **flat bindings** read the payload as it stands when the action runs
(`Engine.refPayload`). A flat binding is a field of the subject's payload by its name, such as
`$stage`. The entity a firing is about cannot change, because verse fixes the subject before any
gate. But `$stage` is current, and `$subject.value.stage` is as of C: the same field, read at two
moments.

The reason is cost. `refPayload` runs for every firing, and pinning it would take a snapshot for
every firing. The as-of-C references take a snapshot only when a charter uses them. Keeping the
evaluated document through the firing, as above, would remove the exception. Verse does not do
this.

## What ends a firing

Four things end a firing, most important first.

**A store write ends itself.** A write of the same value makes no commit
([change events](change-events.md#the-decision): an edge only on a real change). So an action
that has reached its fixed point causes no next evaluation, and a rule that writes the same value
each time settles even with no latch. `entity.TestSameValueWriteRaisesNoCommit` tests this.

This is not what `entity.Modify` does. `entity.Modify` retries on a failed precondition, so it
prevents lost updates. It does not prevent repeated work.

**A [latch](glossary.md#latch) takes the subject out of the selection.** Write a value the
subject's predicate refuses, and the next evaluation does not select the subject. This is the
usual shape, and for a spawn it is the only one. A spawn is not a store write, and repeating it
is never harmless. The engine removes duplicates only while the task is live; once the task is
reaped, the next commit spawns again. So verse **refuses at install a spawning action whose
subject can still match after it runs.** The message names the paths it could latch on.

The check decides whether the predicate and the state after the action can both hold. It needs
no solver. At fire time the predicate was true, so only a path the action writes can make it
false. The check is a three-valued fold over the structure of the predicate:

- `and`, `or` and `not` follow Kleene logic;
- `tony.Match` decides the leaves.

So the check reads a nested predicate, a replace of the whole value and a root operator each as
what it is, not by the name of its top-level key.

Some values are known only at fire time: `$result`, or an operator the fold does not model. These
come out *undecided*, and undecided passes. The check refuses only what it can see:

- a rule that writes nothing that its subject reads;
- a rule whose write leaves the predicate satisfied.

A write of the same value latches a store write. It never latches a spawn.

**A sequence limits itself.** `startSeq` refuses to run a sequence again when it has already
reached a conclusion for a subject at a shape: `done` if the sequence finished, or `ended` if an
off-ramp stopped it. That is a fact in the store, not a check of live tasks, so unlike a bare
spawn it lasts after the reap. The latch check therefore does not apply to a `seq`. It does not
apply to the spawn of a completion hook either, which the completion above it limits.

**In-flight guards cover the time before the latch lands.** Until the latch is written, every
commit selects the subject again. So verse holds a firing for as long as its work lasts, with two
guards:

| Guard | What it covers |
|---|---|
| `claimFlight` | the firing itself: its gates being answered and its action applied |
| `working` | what the firing started: a spawned task that still runs |

Neither guard changes what runs, because `spawnHooked` already refuses a second task under a live
id. Together they stop a firing for work already under way from asking a question or using
budget before verse refuses it. Both guards key on (rule, subject), as the spawn id, the gate's
question and the motion channel do.

## A narrow `on` samples; a vacuous `on` reconciles

With no `on`, every commit that touches what the rule reads evaluates the rule again, so verse
sees everything that could make the predicate true.

With an `on`, the rising edge fires once, over the subjects *as they stood at that commit*. Verse
does not act on a member that arrives later, until the cause falls and rises again:

```tony
- name: ship-at-green
  condition:
    on:
      path: "ci.status.*"
      value: { state: green }
    subject:
      path: "demo.pr.*"
      value: { stage: reviewed }
  action:
    patch: { stage: shipped }
```

At commit C1, CI turns green. The edge fires over the pull requests at `reviewed` at C1. At C2, a
pull request arrives at `reviewed`. `on` was already true, so there is no edge, and verse never
ships it.

That is what an edge means. But these are two different sentences:

- *"ship everything reviewed at the moment CI went green"*
- *"ship everything reviewed while CI is green"*

This charter says only the first. So the engine **warns at install** when `on` does not cover a
subtree the rule also reads. The warning says which of the two sentences the charter says, and
how to say the other: drop the `on`, and write the condition as a `when`.

It is a warning and not an error, because both sentences are reasonable.

## The cycle, and how it is caught

A single rule that does not settle is easy to see. A cycle across rules is harder, and every
protection above misses it:

```tony
- name: fwd
  condition:
    subject:
      path: "demo.pr.*"
      value: { stage: a }
  action:
    patch: { stage: b }
- name: back
  condition:
    subject:
      path: "demo.pr.*"
      value: { stage: b }
  action:
    patch: { stage: a }
```

Each rule writes exactly what the other selects on. Each protection above misses the cycle, and
is right to:

- every write is a real change;
- every firing completes;
- each rule moves its subject out of its own selection, and into the other rule's selection.

Verse catches the cycle at run time, with [the cycle throttle](cycle-throttle.md). The throttle
limits how deep a chain of firings, each caused by the last, can go. It does not analyse the
charter. For the pair above, it trips after a few firings and names both rules in the log. By
default a trip delays the firing and does not stop it: the firing waits for `tripDuration`
(default 1s) and then runs, so the pair goes on firing at a slow, steady rate. A `tripDuration`
of `0` refuses the firing instead.

The check runs at run time so that it can tell a runaway from a control loop. A cycle paced by a
task, by the world, or by a question somebody answers never trips.

## Two triggers, one field

Two rules may select overlapping subjects and write the same payload path with different
values. The field ends with whichever write lands last. This can happen under any dispatcher;
evaluating at every commit does not cause it, though a rule with no `on` runs more often and so
meets it more often.

For literal patches, the conflict can be found:

1. the subject paths overlap segment by segment;
2. their predicates can both hold;
3. they write the same path with different values.

Through references it cannot be found in general. Verse warns at install in one case: a rule
derives a field at an address, and another rule patches the same field there. The warning names
both rules. Two patches that disagree get no warning.

The cycle throttle does not cover this case. No chain grows, so nothing trips.

There is no general fix, even when verse finds it. Two rules that disagree about a field make a
charter that means two things, and only its author can say which.

## The claim

Verse takes one snapshot per commit. Every action's effect is either a store write or a spawn the
install check has limited. So **the settled store depends on the charter and its inputs**, and
not on the order in which firings run.

The two exceptions are properties of a charter, not of the model:
[a cycle across rules](#the-cycle-and-how-it-is-caught) and
[two rules that write one field](#two-triggers-one-field).

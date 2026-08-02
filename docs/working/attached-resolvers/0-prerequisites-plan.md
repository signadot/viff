# Prerequisites: implementation plan

Status: plan, nothing built. The gaps are [0-prerequisites.md](0-prerequisites.md) and the
design they serve is [attached resolvers](../attached-resolvers.md); this file does not restate
either, only the order of work and what each step must prove.

The shape being built: a gate question that is a published, durable, matchable fact; one
attributable answering path whose effect can be governed; and one meaning for `?since=` across
runners.

## Order of work

Five steps. Each leaves the tree green and is independently reviewable, and the dependency order
is real — 2 publishes what 1 named, 3 needs 2's record to have somewhere to write a loser to,
and 4 needs 2 to have something to re-post. Step 1 is invisible; step 2 is the first a user
sees.

Nothing here needs the resolver design to have been agreed, and no step should mention it.

### 1. `resolve` is a verb, and a question is a resource

`process/effect.go` gains the `resolve` verb and the `gate` resource kind, and the deriver
classifies a resolution into an effect naming the question it answers. This is vocabulary only:
nothing yet produces such an effect outside a test.

The verb is separate from `write` for the reason `call-agent` is separate from `exec` — a
predicate that cannot name resolution on its own can only speak about everything at once.

**Proves:** a `Grant` and a `GateMatch` can predicate on answering. Test: a grant of
`{verbs: [resolve], resources: [{kind: gate, pattern: …}]}` permits a matching resolution and
refuses one naming a different question; `read-only` refuses both.

### 2. Publish the question

`mirror` writes the question as a record rather than two fields: id, gate, mode, tool, the
derived verbs, resources and paths, subject, spawns, and the ref of the resolver the question is
addressed to. That last field is the only one not already on `GateQuestion`, and it comes from
the binding: a question addressed to a resolver carries its ref, and one nobody is bound to
carries none, so empty means unbound rather than unknown. The record
*replaces* `pending` and
`pendingGate` in this same step rather than standing beside them — the entity's `pending` has no
readers at all today and `pendingGate` has exactly one, `trigger/flowtree.go` feeding
`FlowNode.Gate` for `verse flow show`, which this step updates. Nothing else in the tree, no
charter and no example binds either, so a compat layer would buy nothing and cost verse a second
spelling of a fact it already has.

That is a `contract` push by `contributing.md`'s definition, since the entity model is contract
surface and somebody's unpushed charter could be matching a field this removes. It wants the
`!` marker and a `Breaks:` line saying which field went and what replaced it — cheaper than a
duplicate nobody can ever remove.

`verse gates` reads the record, so the list stops being names and starts saying what each
question is about. That is the step's user-visible payoff and the reason to do it early.

The shape of that record, and where it lives, is worked out in
[1-question-record.md](1-question-record.md) — which lands this step and takes step 4 with it,
since a question that is its own entity survives a restart without further work. It also
corrects this step in one respect: the record is not written *onto* the `process:instance`,
which has nowhere to put a trigger gate's question and cannot hold two from one run.

**Proves:** a question is matchable without any privileged access, and addressable. Test: a
plain `entity.Pattern` selects the pending questions of a process by verb and by path; a charter
rule with `value: { pending: { verbs: [write] } }` fires on one and not on another; and one
matching on the resolver ref selects only the questions addressed to it, which is how a
resolver's stream is cut later. And the negative that matters: a question with no derived
effects still publishes id, gate and subject, because a tool that classifies to nothing must not
vanish from the docket.

### 3. One answering path, and a loser that leaves a trace

`versed`'s two resolve routes converge on one implementation and one body (`api.Resolve`
already), `by` is required rather than defaulted at the edge, and a second answer to an
answered question is recorded as a stale decision instead of returning 404. The registry keeps
its join-by-id behaviour; what changes is that losing is an outcome rather than an error.

**Proves:** two answerers racing both end up in the record, exactly one of them applied. Test:
answer a pending question twice; assert one `decision` applied, one recorded stale, both
carrying distinct `by`, and the process proceeding once. Assert `verse gate approve` on an
unknown id still fails — a stale answer to a real question and an answer to no question are
different errors.

### 4. The docket survives a restart it should survive

On reconnect, verse re-posts a question from the entity for work that outlived it — a brokered
or remote process — and continues to orphan a question whose in-process runtime died, which is
the existing `reconcileProcesses` distinction and not a new one.

**Proves:** `verse gates` after `verse down; up` still lists the question of a process that kept
running, with the same id, and answering it still reaches the worker. Test: the brokered path
end to end across a restart; and the negative, that a local process's question does not come
back to haunt the docket after its runtime died.

### 5. `?since=` means one thing

The brokered and remote `/events` path replays from a cursor and then goes live, with the
resync-below-watermark frame, matching what `processd` already does locally. This is the runner
contract being honoured rather than a new capability.

It is also the step the design leans on hardest, which is worth knowing while ordering the work.
A resolver is a client of this endpoint and nothing else — it reads from a position and answers
on the control plane — so replay-then-live is not a consistency fix for `verse process attach`,
it is the surface supervision is built on. A resolver over a runner that only streams from now
would be one that cannot be resumed, taken over, or told what it missed.

**Proves:** attaching at a cursor yields the same events wherever the work runs. Test: drive a
process past N events on each runner, attach at `?since=2`, assert identical frames and
ordering; attach below the watermark, assert a resync frame followed by current state.

## What this plan does not build

No resolver position or notes, no `resolvers:` entity and no verb to manage one, no resolver
process of any driver. Step 1 makes resolution *sayable* on the hard
axis and step 2 makes a question *matchable*; a rule that answers one is the first step of the
design, and it starts the moment this plan is done.

## Not a prerequisite: the journal's own home

A sixth step was here and has been pulled out: the runner persisting a journal it can serve, and
`process:journal:<id>` addressing it. The design note settles *how* that should work — a durable
log the runner keeps, not the live ring — and none of it is needed to find out whether any of
this is worth building.

It was the most infrastructure of the six and the least constrained by anything that exists: new
persistence, a retention policy with an owner, a reaper, a new mount, a three-outcome read. It
also runs into a known unknown rather than a known gap, since whether a `--data` directory
survives a binary upgrade is untried, and this writes per-process logs into one. Meanwhile the
first increment of the design excludes journal federation outright, so nothing downstream is
waiting on it.

*The trigger that would change the answer:* a resolver — or a person at the docket — judging on
the question record and what the ring still holds, and that turning out to be too little. That
is cheap to discover and is evidence rather than prediction, which is exactly the argument for
not building it first.

## The residual to expect

The two obvious ones went into the steps rather than being left here: the old fields go in step
2 instead of lingering as a second spelling, and the journal question is settled in the design
note and then deferred, above. What remains is smaller and genuinely open.

**How much of the question belongs on the entity.** Step 2 publishes derived effects, and an
action can derive several resources; `mirror` is emphatic that it is an index and not a
warehouse. A question whose effect list is long is the case where those two pull apart, and the
answer may turn out to be a summary — verbs and a count, with the full list served the way the
journal would have been. Nothing in the tree makes that call today.

**Two registries meet in step 4.** Verse's `Reg` and the runtime's own supervisor state both
know about a live question, and re-posting from the entity has to reconcile them across a
restart without answering anything twice. The step is written as though the entity is the only
place that remembers, which is true of verse and not of the runner.

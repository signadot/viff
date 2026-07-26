# Plan: `seq` — sequence along the chain, parallel across triggers

*Status: design, not implemented. It closes the note in
`docs/workflow-representation-improvement.md`, and it depends on the chain work already landed
in `docs/flow-shape-kpath-plan.md`.*

## The two axes are already in the id space

A process id is `<chain>@<subject>`, and it carries two independent directions:

```
review@demo:pr:1          ┐ BREADTH — same subject, different root:
hold@demo:pr:1            ┘          two triggers acting in parallel
review.onDone@demo:pr:1     DEPTH  — extension of one chain: a sequence
```

That is not a metaphor to be built toward; it is what `verse flow show demo:pr:1` prints today
— two roots, one running and one done, with the hook nested under its own root.

So **parallelism needs no construct**. It is what independent triggers matching the same
condition already do, and the flow tree already renders a fan as roots-per-trigger. What is
missing is the *depth* axis spelled declaratively: today a sequence can only be written as
nested `onDone`s, which right-drifts, and — the reason that matters — has **no arity**. The
engine knows there is a next hook; it never knows how many remain, so a sequence cannot say
where it is.

## Why there is no `par`

It would be a second spelling of what triggers already do, and worse, it would break the
orthogonality above: `review.par[0]` and `review.par[1]` put *breadth inside a chain*, after
which an id can no longer be read as "root = which branch, extension = how far along." The two
axes stay clean only if exactly one of them lives in the chain.

## The shape

```tony
- name: review
  condition:
    when: { system: demo, kind: pr, fields: { stage: open } }
  gates:
  - { name: review, mode: ask }
  action:
    seq:
    - spawn: { driver: scripted, task: "lint $subject" }
    - spawn:    { driver: scripted, task: "review $subject" }
      onFailed: { set: { stage: rejected } }
    - set: { stage: reviewed }
    onDone:
      patch: { stage: ready }
```

```go
type Action struct {
	Set   map[string]any
	Spawn *launch.Spec
	Seq   []Action // NEW — run in order, each advancing on the previous one's completion
	OnDone, OnFailed, OnCancelled, OnOrphaned *Action
}
```

### Rules

1. **`seq` and `spawn` are mutually exclusive** on one action. Otherwise "does the sequence wait
   for the spawn?" is a coin flip. `set` + `seq` is fine: the set applies, then the sequence runs.
2. **The sequence owns the `done` slot of each element.** An explicit `onDone` on a non-last
   element is an install error — the list already says what comes next. `onFailed` /
   `onCancelled` / `onOrphaned` stay available per element as the off-ramps, and firing one
   **ends the sequence** (fail-stop).
3. **`onDone` on the action *carrying* the seq means "after the whole sequence."** A sequence is
   then a unit with its own completion, and nesting composes for free.
4. **An element with no spawn completes immediately**, so `seq: [{set: …}, {set: …}]` is two
   ordered writes rather than an error. `seq` sequences **actions**, not processes.
5. **Gates gate the sequence, not the step.** Gates are per *firing* and a sequence is one
   firing, so an `ask` approves the whole thing. A gate per step is a separate question; it is
   not answered by adding `gates` to an element, because an element is not a firing.
6. Validation widens from "`onX` requires a spawn" (`trigger/trigger.go`, `Action.validate`) to
   "requires a spawn **or** a seq" — the thing that can complete is no longer only a process.
7. **No nested sequences: no chain may contain two `seq` segments.** Not merely "an element may
   not hold a seq" — an element's `onFailed` holding one would nest just as much, only further
   away. Checked at install by walking the `Action` tree, which is cheap and makes the rule
   exact rather than approximate.

   The reason is idempotence, below: every live process's chain then has **at most one** `seq`
   segment, so "the sequence this process belongs to" is a unique ancestor rather than a stack of
   them. A rule about what a chain may look like, enforced where chains are built.

## Chain and ids

This is what forces the two-rendering split that `flow-shape-kpath-plan.md` designed and
deferred:

| | canonical (kpath) | id |
|---|---|---|
| the sequence's 3rd step | `review.seq[2]` | `review.seq.2@demo:pr:1` |
| that step's failure branch | `review.seq[2].onFailed` | `review.seq.2.onFailed@demo:pr:1` |

Brackets stay out of the id because they are shell-hostile — under zsh
`review.seq[2]@demo:pr:1` dies with `no matches found` *before verse runs*, and ids are pasted
back onto command lines constantly. The dotted form is lossless because `Action`'s field set is
closed: a segment after `seq` is an element, a segment matching a hook name is a field. So
`chainID` / `chainKPath` from that plan become real here, with a round-trip test.

### Keyed steps: blocked upstream, not rejected on merits

Addressing an element by name — `review.seq(lint)`, kpath's keyed-array segment — is the better
form on its own terms. A name survives an insertion; an index does not, and under sequences that
may run for weeks the charter moving underneath is the common case rather than the edge one.

It is nonetheless **not available**, for one reason: **`!key` has no defined semantics upstream
yet.** `ir/kpath/kpath.go` lists it under "Future: Support for `!key(path)` objects", so a
`(name)` segment parses and round-trips but *which field of an element is the key* is not bound
to anything; logd's index carries `ArrayKey` scaffolding and no more.

Verse could get away with it — triggers live in memory (`e.triggers`), no charter is written to
the store, so a chain is verse's address into its own structure and nothing else resolves it.
That is "nobody is looking", not a design. The whole argument for adopting kpath was to inherit
defined meaning rather than invent; spending a syntax whose meaning upstream has not fixed would
be inventing *while looking like inheriting*, which is worse than inventing openly.

So `[n]` it is, and a step's name — if elements get one — is a display label on its flow-tree
node rather than an address. **Revisit when `!key` lands**: the shape hash below is the cheaper
substitute for what names would have bought, not a replacement for them.

Two earlier reasons recorded as wrong, so they are not re-derived: the element wildcard `[*]`
does not match a keyed element, but nothing here needs `[*]` — the idempotence query is a
*prefix* (`review.seq`), which matches `[2]` and `(lint)` alike and still discriminates
`review.onDone`. That was a bad reason for a conclusion that turns out to be right anyway.

## Progress facts: the seam between the axes

A parallel branch is woken by a `when`, and a `when` matches facts. So a sequence must
**publish its position**, or the breadth axis cannot observe the depth axis at all. This is the
real reason for the facts — visibility is the lesser half.

On the subject, under **one reserved root**, written by the engine as each element completes:

```tony
verse:
  review:            # keyed by trigger
    step:  2         # how many elements have completed
    steps: 3         # len(seq), known at install
    shape: "a3f1…"   # hash of the declared shape, known at install
    done:  true      # latched when step reaches steps
```

and a join reads it at whatever depth it cares about:

```tony
when: { system: demo, kind: pr, fields: { verse: { review: { done: true } } } }
```

**One reserved name, not a family.** The alternative was flat per-trigger keys
(`review.step`, `review.steps`, …), which reserves an open-ended set of names out of a payload
that is *the author's own document* — the tutorial says so in as many words. A single `verse`
root reserves one name and documents once, the way `flow` is one reserved key on process
entities. Nested predicates make it readable: `fields` is now a tony match evaluated at the
payload root, and tony matches structurally, so `{verse: {review: {done: true}}}` matches a
payload carrying any amount of other content.

### Why `done` is published rather than derived

The obvious spelling of "finished" is `step == steps`, and it does not work: **a match compares
a value against a pattern, never one field of a payload against another.** Richer operators do
not change that — it is what matching *is*. So a join written that way would have to hardcode
the count (`{verse: {review: {step: 3}}}`), which silently stops meaning "finished" the moment
the sequence gains an element.

So the engine latches `done` when `step` reaches `steps`, and the join matches that. This is
exactly what the originating note sketched — `set done-a`, then `when: !and [done-a, done-b]`
— arrived at from the other end: `step`/`steps` are for *position* and for the idempotence
predicate below, `done` is the join's interface.

**Land all of them with `seq`,** not after — without them a sequence is invisible to the axis
that makes it useful, and has nowhere for its idempotence to stand.

### One hazard worth stating

A payload is now any tony value, not only an object, and `set` **replaces** a non-object
payload rather than merging into it. So a sequence about a subject whose payload is a scalar
would destroy that scalar the first time it publishes progress. Rare, but silent — the engine
should refuse to publish progress onto a non-object payload and fail the sequence with a reason
instead.

### Why `shape` is a hash and not a snapshot

`shape` is what an index-addressed sequence has instead of stable names: on each advance the
engine compares the subject's `shape` against the trigger's current one, and a mismatch means
the charter moved underneath — so the sequence **fails with a reason** rather than silently
running whatever now sits at `seq[k+1]`.

It is a hash rather than a pinned copy of the `Action` deliberately. These processes can run for
a very long time, and a retained snapshot per live sequence is a **retention floor the store can
never compact past** — a worse problem than the one it solves. A hash is O(1) on the subject and
keeps only the property the engine actually needs: *detect* that the shape changed, without
being able to reconstruct the old one. Refusing to continue needs detection, not reconstruction.

What it catches: insert, delete, reorder, or any edit to an element's own action. What it cannot
do is tell you *what* changed — the error can say only that it did, and name the trigger.

## Re-fire idempotence: a predicate on the node

Spawning is idempotent per id, and an id is deterministic per (chain, subject) — so a re-fire
dedupes **per element**. That is the wrong grain for a sequence: a re-fire mid-run would find
element 0's process long retired, respawn it, and run a second sequence overlapping the first.

So the unit of idempotence is **the sequence node**, not the element. A re-fire asks one
question of the node: *is this sequence already in flight for this subject?*

```
in flight  ⟺  some live process's chain is under <node>.seq[*]        for this subject
              OR  verse.<trigger>.done is absent while step is present  on this subject
```

Both halves are needed, and the second is the interesting one. **Between elements there is no
live process at all** — element k has been retired and k+1 not yet spawned — so a
live-registry-only predicate has a hole exactly at every step boundary, which is where a
re-fire is most likely to land. The `step` fact closes it.

That is what makes the progress facts load-bearing rather than cosmetic, and it settles the
"do they land with `seq`?" question: **they must.** Without them the sequence has no state of
its own between elements, and idempotence has nowhere to stand.

The live half is a prefix query over the chains in the registry — `MatchesPrefix` from go-tony
v0.0.92, which is what that bump bought.

**What this does and does not prevent.** It prevents a *second, overlapping* run. It does not
prevent a *later* one: once the sequence completes (`done` latches), a re-fire starts it again,
exactly as a re-fired trigger has always re-acted. Preventing that is the `when`'s job — match a
state field the action advances (the latch the tutorial teaches), not the engine's.

Rule 7 is what keeps this a predicate rather than a search: with at most one `seq` per chain,
the node is a unique ancestor of every live chain, so "which sequence is this process in" has
one answer.

## Charter edits mid-flight: a per-trigger lock, as policy

Inserting an element shifts every index below it, so a running sequence's successor is no longer
where its chain says. With `shape` above, that is **detected** rather than silently executed —
correctness no longer depends on preventing the edit. What is left is a policy question: does an
edit *wait* for in-flight sequences, *refuse* while naming them, or *proceed* and let them
strand loudly?

A **per-trigger in-flight count** answers all three. It belongs at the API seam — `AddTrigger` /
`RemoveTrigger` (`trigger/trigger_api.go`), which already own `e.triggers` — kept separate from
`trigMu` so dispatch is not blocked while an edit is deciding.

**Default: refuse, and say what is in flight.** Under sequences that may run for weeks, a
drain-and-wait edit would essentially never complete, so waiting is the wrong default and a
silent proceed is worse. `--force` proceeds anyway; the stranded sequences then fail on their
next advance with a shape mismatch, which is a real outcome an operator chose rather than a
corruption they did not.

Three primitives considered, and why the count wins:

- **`sync.WaitGroup`** — wrong shape. It is 0→n→0 *once*; a trigger's in-flight count returns to
  zero and rises again with the next firing, and `Add` after `Wait` is a documented race.
- **`sync.RWMutex`** — the right *semantics* (sequences are readers, an edit is the writer) and
  the wrong mechanics. Go's RWMutex deliberately blocks new `RLock`s once a `Lock` is waiting, so
  if advancing a sequence takes a read lock **per step**, a pending edit blocks the very
  sequences it is waiting to drain — deadlock. Avoiding that means holding one `RLock` for a
  sequence's whole lifetime, across goroutines and possibly weeks, released on a path that can
  simply not run: `onOrphaned` is inert and resume re-adopts without hooks
  (`docs/completion-hooks.md`), so an abandoned sequence would leave a trigger permanently
  uneditable with nothing to inspect. It also has no bounded wait (`TryRLock` exists; a timeout
  does not).
- **A counter plus a condition variable** — same semantics, and it gives back exactly what the
  other two cost: a bounded wait, a number that can be *reported* ("3 sequences in flight"), and
  a state that a stale entry degrades rather than deadlocks.

Removal *after* a sequence has finished stays free, and already works: the flow tree renders a
removed trigger's history with no shape to extend it by.

## The join

Point-in-time, and it already works: facts converge on **one** entity, and an event carries the
whole payload (`entity/store.go` — `To: next.Payload`), so `fields: {a: done, b: done}` fires
when the second one lands. No barrier in the engine, no temporal operator — and a temporal
`when` is rejected at install anyway (`Condition.validate`), so converged facts are not a
workaround, they are the layer that exists.

## The boundary this design accepts

**A sequence that fans out and rejoins cannot stay in one trigger.** Step 2 sets a fact, two
triggers match it, and the continuation is a third trigger matching the conjunction — the
original chain does not resume. The asymmetry is exact: `onDone` continues inline because the
completion of *one* process is directly observable, while the completion of *N* is a condition,
and a condition is a trigger.

That is the right line — it is the same reason `reviewed` remains the decoupled form for fan-in
in `trigger-model.md` — but it means "one workflow, one rule" holds only until you fan. Two
further consequences, accepted rather than solved:

- **Two parallel triggers raise two gates.** A broadcast fan-out asks once, but distinct
  triggers each raise their own.
- **Nothing names a fan as a unit.** Its branches are related only by having matched the same
  condition, so "is the fan complete?" is answerable through the facts and never structurally.

## Implementation

**Advancing a sequence wants the same navigation that resume wants.** Today `applyHook` reads
`fp.hooks` — the spawning action — and takes its `onX`. A sequence element's successor lives on
its *parent's* list, so the engine must find the parent. It already can: the chain is a kpath
into the trigger's `Action`, so `navigate(trigger, chain) (*Action, bool)` is the one new
primitive, and it is exactly what hook rehydration on resume needs
(`flow-shape-kpath-plan.md` §4). Build it once, use it twice.

- `trigger/trigger.go` — `Seq []Action`; `validate` (rules 1/2/6); `apply` starts element 0.
- `trigger/engine.go` — on a terminal status, navigate to the completed element's parent; if it
  is a seq with a next element, spawn it under `parent.seq[i+1]` and write the `step` fact; if it
  was the last, fire the carrier's `onDone`. Fail-stop on a non-`done` terminal.
- `trigger/trigger_api.go` — `chainID` / `chainKPath` for the bracket↔dot rendering.
- `trigger/flowtree.go` — seq elements as ordered children (`kind: "seq"`), rendered in index
  order rather than alphabetically; `len(seq)` is known at install, so unrun steps are `pending`
  with their declared task, which is the shape argument for `seq` over nested hooks.
- `trigger/trigger_gen.go` + `schema_gen.tony` — regenerate.

## Tests

1. A 3-element sequence runs in order; ids are `review.seq.0/1/2@demo:pr:1`.
2. Fail-stop: element 1 fails → element 2 never spawns, its `onFailed` fires, the carrier's
   `onDone` does not.
3. Carrier `onDone` fires after the last element, once.
4. A set-only element completes immediately and does not stall the sequence.
5. `step`/`steps` reach `n`/`n` and `done` latches under `verse.<trigger>`, and a join trigger
   matching `fields: {verse: {<trigger>: {done: true}}}` fires exactly once. Also: the join is
   written WITHOUT knowing `n`, which is the property that made `done` necessary.
5b. Progress onto a scalar-payload subject is refused with a reason rather than replacing the
   payload.
6. Validation: `seq` + `spawn` together; `onDone` on a non-last element; `onX` with neither
   spawn nor seq.
7. Round-trip `review.seq[2].onFailed` ↔ `review.seq.2.onFailed`, and the id is paste-safe
   (no shell glob metacharacter).
8. Flow tree: unrun steps `pending` in index order; a skipped tail after a failure.
9. Parallel + join end to end: two triggers on one condition, each publishing a fact, a third
   matching the conjunction — the breadth axis observing the depth axis.
10. Re-fire mid-sequence is a no-op — including **between** elements, where nothing is live and
    only the `step` fact says the sequence is running. That gap is the whole reason the fact is
    load-bearing, so it is the test that matters most.
11. Nested `seq` is rejected at install, including the indirect form (a seq element's `onFailed`
    holding one) — the rule is "no chain may contain two `seq` segments", so the test walks a
    tree that only violates it via a hook.
12. Shape drift: start a sequence, edit the trigger's seq under it, and the next advance fails
    with a shape mismatch naming the trigger — rather than running whatever now sits at `seq[k+1]`.
13. Edit policy: `RemoveTrigger` refuses while a sequence is in flight and reports the count;
    `--force` proceeds; removal after completion is unaffected.

## Open questions

- ~~**Reserved key spelling.**~~ **Settled:** one reserved root, `verse: {<trigger>: {…}}`,
  documented as a reservation the way `flow` is on process entities. Nested predicates make it
  matchable at any depth, which flat dotted keys did not need to be — and it reserves one name
  instead of an open-ended family.
- **Fan-out × sequence.** A set-valued `Subject` fans one firing across N subjects, each getting
  its own sequence. The idempotence predicate is already per subject, so this should just work —
  but the facts are written once per member, and nothing has tested that shape.
- **Lock granularity under scopes.** A scoped engine has its own trigger set, so a per-trigger
  count is per (engine, trigger). Worth confirming that a scoped edit cannot block baseline.
- **What `shape` hashes.** The whole `seq` sub-tree is the safe answer (any edit to any element
  is drift). Hashing only the arity and each element's *kind* would tolerate editing a task
  string mid-flight, which is either a convenience or a way to change what a running sequence
  does without it noticing. Safe first.

## Deferred, with the reason

- **Keyed steps** (`review.seq(lint)`) — blocked on upstream `!key` semantics, not on merit. The
  best available answer to charter drift, and the thing to revisit first when `!key` lands.
- **`par`** — not deferred, *declined*: parallelism is what independent triggers already do, and
  putting breadth inside a chain would cost the orthogonality the id space currently has.
- **Hook rehydration on resume** — shares the `navigate(trigger, chain)` primitive this needs
  (`flow-shape-kpath-plan.md` §4), so it becomes cheap once `seq` lands, but it is its own change.

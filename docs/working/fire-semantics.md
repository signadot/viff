# Firing semantics, re-derived from one rule

Status: proposal, re-modelled. Nothing here runs. §1 is measured — a read of `trigger/` and
`entity/` plus one throwaway probe — and still holds at `7c77701`, which has not touched either
package. Everything after §2 is derived from the rule in §2 and owes nothing to the model in §1.

§2 is taken as given, so what follows is a derivation and not a defence: where the rule
collides with something that exists, the existing thing is what gets named for replacement.
Two standing decisions shape §5 and §6 — the store may be redone, and `verse connect`'s
built-in reactivity is going anyway (`docs/roadmap/expose.md`), so neither counts as a
constraint.

What it would take to build, and in what order, is
[fire-semantics-plan.md](fire-semantics-plan.md). This file is the argument; that one is the
work.

## 1. What runs today

**A commit is already flattened per entity.** `walkEntities` (`entity/logdstore.go:1267`)
descends a commit's delta exactly three object levels — system, kind, id — and emits one
`entity.Event` per entity, all sharing the commit's `Rev` (`eventsFromDelta`,
`entity/logdstore.go:1130`). Each event's `To` is the whole entity as of that commit. Nothing
downstream groups by `Rev`.

**Firing is then keyed to the change, not the entity.** A change is tested against
`Condition.dispatch()` — the `when` if there is one, else the subject
(`trigger/trigger.go:118,127`) — and the subject is resolved *afterwards*, separately
(`trigger/trigger.go:147`): the changed ref if there is no `when`; else a named ref; else the
ref held in the changed payload's `flow` field; else `Subject.Select(store)`. So with a `when`,
one change makes one firing over N subjects. Budget meters per subject (`engine.go:445`); one
gate is raised for the whole firing, which is what `subjectLabel`/`spawnLabel`/`summarize`
exist to render (`engine.go:476–513`).

**A `when` is an edge on its own subtree and is never read against state.** Probed:
`ci:status:build` already `state: green`, trigger `when: ci:status green` / `subject: demo:pr
stage=reviewed`, then a PR written to `reviewed` →

```
root:   pr:1 = map[stage:reviewed], fired=0
scoped: pr:1 = map[stage:reviewed], fired=0
```

Nothing fires in either observation mode. In production (`ObserveScoped`) it is structural:
`watchTriggerSubtree` (`engine.go:201`) watches the *`when`'s* subtrees, so a trigger whose
`when` names `ci` never hears about `demo:pr` at all.

## 2. The rule

> A commit C is associated with the entities Entities(C) that changed. For each trigger T and
> each commit C, if `on` is vacuous or (not on(C-1) and on(C)) and when(store@C) holds then for
> each matching entity e ∈ store@C, fire the trigger.

```tony
condition:
  on:      { system: process, kind: instance, value: { status: done } }
  # implicit exists
  when:    { system: demo, kind: notion, value: !or [ a, b ] }
  subject: demo:pr:*
```

Read off it directly, before anything is derived:

| field | evaluated | job |
| --- | --- | --- |
| `on` | a store predicate, at C-1 and C | the **rising edge** — fires on ¬P → P; vacuous = every commit |
| `when` | the same kind of store predicate, at C | the **level** — must hold at C |
| `subject` | a selection over store@C | the **set acted on** — one firing per member |

`on` and `when` are the same thing evaluated two ways: `on` is ↑P, `when` is P. That symmetry
is the model's centre of gravity and most of §3 falls out of it.

Everything is read at **one snapshot**, `store@C` (plus `store@(C-1)` for the edge). The rule
does not say "current"; it says *at the commit*.

## 3. What follows

**There is no cause entity.** Nothing in the rule names a changed entity — `on` is a predicate
over the store, not a match against a change. So there is no per-change dispatch, nothing to
correlate against, and no changed payload for an action to read. `trigger/trigger.go:127,147`
(`matchesEvent`, `resolveSubject`) have no counterpart here; one function evaluates three
predicates against a snapshot.

**A firing is about one entity.** "For each matching entity e … fire" — N members are N
firings, not one firing over N subjects. Everything a firing owns keys on its subject: the
spawn id (already does — `spawnID(chain, ref)`), the gate question, the motion channel, one
unit of budget.

**Bindings can only come from the subject.** There is no other entity in scope. `refRoot(ev,
subject)` (`deref.go:44`) loses its event and reads the subject's payload from `store@C`.

**`op` is derivable, and then unnecessary.** A create is the rising edge of "this entity
exists" — `on: {system: demo, kind: pr, id: 1}`, which works today. A delete would be the
rising edge of "this entity does *not* exist", and there is nowhere to put that negation:
`Formula` has no boolean operators (`formula.go:19` — "boolean composition lives inside the
leaf match") and a leaf match's `!not` negates a payload, not the existence of a match.

**Delete intent as a sink state closes that, and keeps every cause positive.** With the
intent to remove written into the store before the removal happens, a disappearance is
observed the same way an appearance is: `on: {system: demo, kind: pr, value: {state: <sink>}}`
is a plain existence predicate rising. No formula-level negation, no `op`, and the whole
model stays positive — every cause is "something matching M now exists".

Three consequences follow from that, and the first is the one that bites:

- **The sink state has to be known to the selection, not just to the author.** A dying entity
  still exists, so `subject: demo:pr:*` selects it — and a trigger will patch it, spawn about
  it, and raise a gate on it, on the way out. Either the model excludes sink-state entities
  from `subject` by default (which makes the sink a lifecycle concept the selector understands,
  the way `Op`'s two-valuedness made a tombstone derivable) or every subject predicate in every
  charter has to exclude it by hand. The first is the safer default and the second is not
  really a choice anyone would make twice.
- **The removal itself is inert, correctly.** When the entity actually goes, an `on` predicate
  over it falls from true to false, and the rule fires only on rising edges. So removal is
  bookkeeping rather than reactivity — the firing already happened, at the intent.
- **The reaper is an ordinary trigger.** Vacuous `on`, `subject` selecting the sink state,
  action patching a tombstone (`Action.Patch` is a tony patch and carries `!delete` —
  `trigger.go:222`). It converges by construction: once removed, no longer selected.

**Per-entity causes are not expressible, and that is why `on` is optional.** `on: {system:
demo, kind: pr, value: {stage: reviewed}}` is an existence predicate: it rises when the *first*
PR reaches `reviewed` and stays true, so a second PR reaching `reviewed` produces no edge and
no firing. There is no way to say "on each PR reaching reviewed".

So the ordinary rule is written with a **vacuous `on`** — every commit re-evaluates the trigger
— and the work is done by the subject selection plus a latch. `on` is not "the cause"; it is an
optional rising-edge gate that narrows *when* the trigger is considered. That inverts the
weight the field looked like it carried: the engine's driver is "re-evaluate every trigger at
every commit", and `on` is a way to say "only at this transition".

**`Entities(C)` is the skip index, and the skip is provable.** A trigger T may be skipped at
commit C when `Entities(C)` intersects none of the subtrees T reads —
`subtrees(on) ∪ subtrees(when) ∪ subtrees(subject)`. Proof, both cases: with a non-vacuous
`on`, nothing T reads changed, so on(C) = on(C-1) and there is no edge. With a vacuous `on`,
the firing would evaluate the same predicates over the same snapshot and produce the same
writes as the last one — which, being the same values, produce no commit
(`docs/change-events.md`, edge-on-real-change). Observationally identical, so skipping is safe.

That is what turns observation into an optimization rather than a correctness property, which
is the largest simplification here: in the current model edge accuracy *is* correctness
(`dispatch()`, `opensTo`, `matchesEvent`, the ignition guard), and under this rule an engine
that fires more often than necessary is merely wasteful.

**The ignition guard dissolves.** A machinery system reaches a trigger only if the trigger
names it in `on`, `when`, or `subject`. Naming it *is* the opt-in, so `machinery()`/`opensTo`
(`engine.go:414`) has nothing left to do.

**Gate and motion identity must become deterministic.** A vacuous-`on` trigger is re-evaluated
per commit, and a gate id is `trigger:<name>:<gate>#<counter>` off a monotonic counter
(`engine.go:554`), so the docket would grow without bound. Key it on (trigger, gate, subject),
and have a firing that finds its question already pending wait on it rather than pose a second.
Likewise the motion channel, keyed today on the changed entity (`engine.go:605`) — it must key
on the subject, or a sweep over twelve PRs puts all twelve behind one vote.

## 4. What it costs at the surface

A firing per member means a gate per member: fifty reviewed PRs, fifty questions, where today
one approval covers the firing. That follows from "for each matching entity e, fire", and I
think it is the honest arity — `summarize` (`engine.go:507`) exists precisely to render twelve
decisions as one — but `verse gate approve` wants a bulk form before anyone would call it an
improvement.

## 5. Does a charter, in the abstract, lead to ambiguous state?

Is the settled store a function of (charter, input commits), or of how firings interleave?
Under this rule, three sources — one of which the rule removes outright compared with the
looser reading it replaced.

### 5.1 The snapshot removes the read-skew source

`when(store@C)` and `e ∈ store@C` are the same snapshot, so a firing cannot act on a member
under a condition that stopped holding between two reads, and which members it sees does not
depend on when a `Select` ran. This was the sharpest implementation-level ambiguity in the
looser reading ("at the current seq") and the rule as written does not have it.

It is a real requirement on the store, not a free property: `entity.Store` has no
read-at-revision (`Since` replays events, `List` reads now), and the edge needs `store@(C-1)`
as well. So the model asks for a snapshot read at a commit — the first item in the
[plan](fire-semantics-plan.md), and something the watch layer already half-has (`WatchSubtree`
maintains the slice as a document and diffs it against the previous snapshot).

### 5.2 A narrow `on` samples; a vacuous `on` reconciles

With a vacuous `on`, every commit re-evaluates every trigger, so nothing that could make the
predicate true goes unobserved and the settled state is a function of the inputs.

With a non-vacuous `on`, the rising edge fires once, over the subject set *as it stood at that
commit*. A member arriving afterwards is not acted on until `on` falls and rises again:

```
on:      { system: ci,   kind: status, value: { state: green } }
subject: { system: demo, kind: pr,     value: { stage: reviewed } }
```

C1 turns CI green — the edge fires over the PRs reviewed at C1. C2 adds `demo:pr:3` at
`reviewed`; `on` was already true, so there is no edge and PR 3 is never shipped.

That is not ambiguity — it is deterministic, and it is what an edge means. But it is the
difference between *"ship everything reviewed at the moment CI went green"* and *"ship
everything reviewed while CI is green"*, and only the first is what a narrow `on` says. The
check worth having is a lint, not an error: when `subtrees(on) ⊉ subtrees(subject) ∪
subtrees(when)`, say which of those two sentences the charter wrote. It is decidable —
`Formula.Subtrees` already computes those sets.

### 5.3 Non-termination, and the write/spawn asymmetry

A non-vacuous `on` fires once per transition, so it is bounded by construction. A **vacuous
`on` fires at every commit its subject touches**, and settles only if its action does. For a
store write the substrate does most of that: a write of the same value produces no commit,
hence no next evaluation, so an idempotent action converges even without a latch.

### The cycle, which is the one that actually bites

The loop above is a single rule failing to settle, and it is easy to spot. The reachable one
is a CYCLE ACROSS RULES, and nothing in the model sees it:

```tony
- name: fwd    { subject: {system: demo, kind: pr, value: {stage: a}}, action: {patch: {stage: b}} }
- name: back   { subject: {system: demo, kind: pr, value: {stage: b}}, action: {patch: {stage: a}} }
```

Each writes exactly what the other selects on. Measured, with the runaway backstop set to 40:
the engine fires 40 times and stops there, having written 40-odd commits — without a budget it
does not stop at all. The degenerate one-rule version is a patch with no fixpoint over its own
subject (`patch: {a: $b, b: $a}`), which behaves identically.

Every protection misses it, and misses it correctly:

- the substrate's no-op suppression never engages, because each write is a REAL change;
- `claimFlight` and `working` never engage, because each firing genuinely completes;
- the latch lint passes, because both rules DO write a field their subject reads. It asks
  whether a rule advances its subject out of its own selection, and both do — into each
  other's.

It is decidable for literal charters, which is what makes it worth naming rather than merely
regretting. Build the graph over triggers where `T₁ → T₂` when applying T₁'s patch to a state
satisfying T₁'s subject can produce a state satisfying T₂'s. A cycle is a possible loop: a
cycle of length ≥ 2 over constant patches always is, since if the two wrote the same value
there would be no second firing. A SELF-edge is only a loop when the patch is templated —
a constant one is inert on its second application, which is the ordinary converging rule.

The residual, as always, is templates and operators: a patch whose values are computed can
satisfy a predicate the key-level check cannot see through. So the graph is a warning, like
§5.4, not a proof.

#### A cycle is not wrong; an UNPACED cycle is

Worth separating before the check above hardens into a refusal. A cycle is how you write a
control loop — poll, compare, correct, poll again — and a charter that could not express one
would be missing something. What makes the two above pathological is not that they cycle but
that they cycle *at the speed of the store*, which is to say at no declared rate at all.

The rule that distinguishes them is the one synchronous digital design already uses: a
combinational loop is an error, and a cycle through a register is a state machine. The register
here is a TICKER — a cause that advances on a clock rather than on the last write. Then the
check is not "no cycles" but **every cycle passes through a ticker**, which is decidable on the
same graph and permits the useful shape while still refusing the runaway.

It buys more than permission. A paced cycle has a rate an operator can read off the charter,
and a cost bounded by (rules × rate) rather than by how fast the store accepts writes — which
would let `maxFires` go back to being a runaway backstop instead of the only thing standing
between a charter and a spin.

The wrinkle, and it is not visible until you try to write one: **a ticker needs to say
"changed", and `on` can only say "became true".** A monotonic counter rises once and stays
risen; one entity per tick leaves "a tick exists" true forever. So a ticker has to produce a
rise AND a fall, which means either

  - the tick is a fact that gets CONSUMED — it appears, the rule fires, it is removed — which
    is the sink state (§3) doing double duty and is the answer most in the grain; or
  - `on` grows a form meaning "this changed" rather than "this became true", which is
    precisely the change-edge the re-modelling removed, re-admitted in a narrow place.

The first is worth trying before the second. Noting it here because the choice belongs to
whoever builds the ticker, and it is easier to make before there is a cycle check to argue
with.

Two smaller things this exposes about the backstop. `maxFires` defaults to UNBOUNDED and
nothing in `versed` sets it, so the protection that stopped the measurement above is not on in
production. And it is one counter for the whole engine rather than per trigger, so a single
looping rule exhausts the budget for every other rule as well — it wedges the verse rather
than the charter that is misbehaving.

### The spawn

A **spawn has no idempotence to appeal to.** It is not a store write, so nothing about it is
inert on repetition, and dedupe is against live processes only (`engine.go:1182`) — once a
process is reaped, the next commit spawns again. Rather than document that asymmetry, remove
it: **give every spawn a durable claim.** `seq.go:339` already does this for a sequence —
`{flow, chain}` written under `entity.Created()`, so the claim is won once — and generalizing
it makes "have I already done this" a store predicate the subject can exclude on. Then every
effect of every action is a store write, and convergence is decided in one place.

The residual lint, for actions latching by hand: *does the action write a field the subject's
predicate constrains?* Decidable for literal patches and predicates, undecidable through
templates and operators — so a warning, with `Spawn != nil && Patch == nil` worth naming
outright.

### 5.4 Two triggers, one field

Two triggers whose subject selections can intersect, writing the same payload path with
different values, settle wherever the last write lands. Not introduced by the rule — reachable
today — but a vacuous `on` multiplies the interleavings. Decidable for literal patches
(intersecting selections on system/kind plus comparable predicates, then
same-path-different-value), undecidable through templates. A warning at install, and the one
source with no principled fix: two rules disagreeing about a field is a charter that means two
things.

### The claim

With the snapshot read (5.1) and every action's effect being a store write (5.3), the settled
store is a function of (charter, inputs). 5.2 is deterministic and wants a lint saying which
sentence you wrote; 5.4 is a charter that says two things, detectable but not fixable from here.
That is a provable confluence claim rather than a hoped-for one.

## 6. What the rule cannot express

**A guard about nothing existing.** The sink state makes deletion positive (§3), so `on` needs
no negation. What still has no spelling is a `when` of the form *"no entity matching M exists"*
— "ship when there is no open blocker". A leaf match's `!not` negates a payload, so "an entity
exists whose payload is not X" is sayable and "nothing matches" is not. The same discipline
that answers deletion answers this too — make the absence a positive fact somebody writes (a
count, a summary entity) — but it is a discipline, not a grammar, and it is worth knowing that
the model is existence-positive throughout rather than discovering it at the first guard that
wants ∄.

**Per-entity causation.** An existence predicate rises once, so "on each PR reaching reviewed"
has no spelling; the ordinary rule is a vacuous `on` plus a latch, which reaches the same
settled state by a different route and fires a different number of times on the way.

**Exact correlation.** "The PR *that* process was about" needs a cause entity, and the rule has
none. `onDone` keeps the exact form — the engine scopes a hook to that spawn's process
(`engine.go:1304`) — so the capability lives outside the trigger grammar. What goes is
`trigger/trigger.go:167`, the `flow` redirect, and with it `condition_test.go:23`, `:56`,
`fanout_test.go:396`, the tutorial's goal-latch example (`tutorial.md:862`), and the comment at
`goal.go:395`. `flow` keeps its honest jobs: the flow query reads it (`flowtree.go:95`) and
`$flow` is a binding.

**Cause-payload data in an action.** Same root: no cause entity. `$result` from a completed
process — "a process's output becomes a fact the next `when` can match" (`trigger.go:214`,
`deref.go`, tests in `patch_test.go`) — survives only in hooks, which do have a process.

**Creating an entity.** "Each matching entity e ∈ store@C" cannot include one that is not
there, so a definite `subject: demo:report:1` plus a `patch` stops publishing a fact nothing has
written (`patch_test.go:109`, and `trigger-model.md`'s naming-vs-selecting distinction). One
amendment recovers it — *a subject naming one entity (definite system, kind, id, no glob) is a
set of one whether or not it exists* — but it is an amendment, not a derivation.

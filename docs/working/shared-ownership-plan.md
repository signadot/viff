# Shared ownership: implementation plan

Status: **phase 1 landed; phases 2–5 are unclaimed.** The design is
[shared-ownership.md](shared-ownership.md); this is how it lands. Read that first — this file does
not restate its reasoning, only the order of work and what each step must prove.

It carries the engine-side work that note raised as well, because the only thing that motivates
any of it today is a shared entity. **This is a root sketch: the phases are argued, the steps
inside them are named and not specified.** A phase is claimed by somebody, who writes its own
`-plan` if it turns out to want one.

The shape being built, in one line: an ownership claim that names a path inside the payload, so
two sources are single writers of two slots; and then, only if measurement asks for it, a way
for a charter to say which slot it cares about.

## The spine: two different questions, in the right order

Phases 3–5 all look like "react to a subtree write" and are not one thing. The plan turns on
keeping them apart:

| | asks | answered from | under a collapsed pair of commits |
|---|---|---|---|
| `!changed(<kpath>)` | **provenance** — was my part of this touched | the commit's own delta | still fires, once |
| `!prev <match>` | **history** — what was it before | prior state | **silently does not match** |

Provenance is safe under everything level-triggering already promises, because "this commit
touched `owners.ENG`" stays true however many changes were folded into it. History is an edge,
and an edge that is never observed is an edge that never fires — which is the failure the
trigger model removed on purpose ("express 'only the first time' as a state latch, not an
edge") and which the GitHub source meets for real in Actions runs, where
`queued → in_progress → completed` routinely arrives as one write
([reference/sourced.md §github](../reference/sourced.md#github)).

So: provenance first, history last and gated. And **measurement between the mechanism and its
sugar**, because nothing yet knows the wake amplification is a problem.

## Order of work

Five phases. 1 is the whole of the design note and needs no engine change at all; 2 decides
whether 3–4 happen; 5 is deferred by argument and listed so the deferral is a decision rather
than an omission.

### 1. The path claim, and converge — landed

Dived, in [shared-ownership/1-path-claim-plan.md](shared-ownership/1-path-claim-plan.md) — which
finds two spellings in the design note that do not survive contact, and reorders the steps below
into six. It is built: what runs is in
[sources.md](../sources.md#four-of-them-and-what-the-ones-after-git-cost) and
[reference/sourced.md §linear](../reference/sourced.md#linear), and the steps below are kept as
the record of what each one had to prove.

Verse-side only. A connector answers exactly as it does today; nothing in `source/sourced`, the
sample program, or a source's wire behaviour moves. That is the property to protect through the
whole phase — **a connector author must not be the one who gets slot semantics right.**

- **1.1 `source.Claim` becomes a `Path`.** Parse `@`, compare paths segment-wise, extend
  `Disjoint` to four segments. *Proves:* the four-row table in the design note, plus the
  refusals — a root claim beside a slot claim, `owners` beside `owners.ENG`.
- **1.2 A cover gains a completeness path.** New field on the wire `Reflection`, not a reuse of
  `Pattern.At` (which means where to root a predicate). *Proves:* a cover at a path tombstones
  the value at that path and leaves the entity. **`contract` tier**: it is the source contract,
  so a `Breaks:` line and a notice ([contributing.md](contributing.md#coordinating-a-break)).
- **1.3 Converge writes slots.** Replace-at-path, skip-unchanged compared at the slot, and the
  last-owner-out delete. *Proves:* two sources converging one entity forever without rewriting
  each other — the mutual-rewrite storm is the failure this phase exists to not have, so the
  test is two reflectors and a commit count that stops growing.
- **1.4 `--forget` removes the slot, then tries the delete.** *Proves:* a removal takes the
  entity only when it leaves nobody, and never another owner's slot.
- **1.5 A source that uses it.** Linear projects, which is the case that produced the finding.
  *Proves:* the whole phase, end to end, against a fake Linear with one project on two teams —
  and it is the first step with behaviour anybody can see.

The tombstone condition is `!all.or [!field "<mine>", !irtype null]` and the null arm is not
optional; the design note has the transcript of what happens without it.

### 2. Measure the wake amplification, and name the number first

The design note asserts that N owners means N× the evaluations for a rule reading the entity,
and that the existing absorbers (no-op suppression, `claimFlight`, `checkLatch`, the cycle
throttle) hold. Both halves are argued and neither is measured.

- **2.1 A harness**: one rule over a shared entity, N slot writers, counting wakes, predicate
  evaluations, and firings per commit.
- **2.2 The threshold, written down before the numbers are read.** What ratio, at what N, would
  justify phases 3–4 — and what result would kill them. A threshold chosen after seeing the
  data is not a threshold.

*Proves:* whether there is a problem. This phase is allowed to end the plan, and if it does
that is its whole value: the sugar below is cheap to want and expensive to put in the charter
grammar, which is the largest contract surface there is.

Note what it cannot measure: a real deployment's N. Two Linear teams sharing a project is N=2,
and 2× of a small number is a small number. The case that would hurt is an entity with many
owners, and nothing has one.

### 3. Carry the delta beside the level

Store-side, invisible to a charter. `eventsFromDelta` binds `ch.payload` — the commit's patch
for that entity, which *is* the set of paths it touched — and overwrites it with the levelled
payload one line later. Keep both.

- **3.1 `Event` gains the delta**, the wire type follows, `Since` and the live path agree.
  *Proves:* `To` is unchanged for every existing consumer, and the delta names exactly the
  written paths — including for a deep write, which is the case that has no other witness.
- **3.2 The scoped-watch caveat.** A scoped watch is a computed diff between scope states whose
  first delta is a `!replace`, so the fragment there is over-broad. *Proves:* over-broad and
  never under-broad — the direction that fires more, which is the safe one.
- **3.3 Settle [change-events.md](../change-events.md)'s own residual with it**: the changed-*ref*
  set already comes from walking the delta, and is documented as sound for a baseline delta and
  unexamined otherwise. That is this question one level up, and the two want one answer.

*Proves, overall:* the engine can answer "what did this commit touch" for no extra walk and no
extra read.

### 4. `!changed(<kpath>)`

The sugar, and the phase that touches the charter grammar. A match tag, registered the way
[shared-ownership.md](shared-ownership.md#where-a-custom-mergeop-earns-its-keep) argues a claim
tag should be, evaluated against phase 3's delta while the rest of the predicate goes on reading
the levelled `To`.

- **4.1 The tag**, and its registration.
- **4.2 The engine applies it** against the event's delta; everything else about matching is
  untouched.
- **4.3 It must be refused where there is no delta to consult.** One `entity.Pattern` serves
  two faces — matching an observed change, and *listing stored entities* for a formula's
  fact-read (`Select`). A `!changed` in the second has nothing to answer from, and answering
  `false` would be a rule that silently never holds. Refuse it, and refuse it **structurally**:
  tony has tag *contexts*, so a tag legal in an `on` and illegal in a `Select`, a cover, or an
  `owns` is expressible as configuration rather than as a check somebody remembers to write.

*Proves:* a rule over a shared entity fires for its own owner's writes and not for the others';
and the same rule, over an entity with one writer, behaves exactly as it does today.

**`contract` tier.** A charter is the thing a dogfooder wrote.

### 5. `!prev` — deferred, and what would revive it

`!and [ {stage: reviewed}, !prev {stage: open} ]`: match the child against the subject's prior
state. It is the general form, and the sugar the perf concern first suggests.

**Cost is not the obstacle.** The store already holds the document it levels from and advances
it per commit, so prior state is one line from being kept on both paths that produce events —
the live watch and the `Since` replay both overwrite it (`applyDelta` at
`entity/logdstore.go:1126` and `:1286`). Whatever change-events.md's original argument was
against before/after payloads — a `seen` set threaded through both paths, an impure catch-up —
levelling has since paid for the machinery that would answer it, and that is worth re-examining
rather than inheriting.

**The obstacle is what it lets a charter say.** `!prev` is an edge, and this design has spent
real effort on not having edges: `Op` is `set`/`delete` because create-vs-update is not in the
substrate; the latch check exists because level-triggering is what ends a firing; a source's
pass reports the last state and not every state. A charter written on `!prev` is correct
whenever it observes the transition and silently inert whenever two changes land in one commit
— and the connector most likely to be written against it is the one whose transitions collapse
routinely.

*The trigger that would revive it:* a charter somebody actually wants that needs "from X to Y"
and cannot be written as a state latch. Not a hypothetical one — the shape of this whole
argument is that we have been wrong before about which of these is real, and the Linear project
was found by building a connector rather than by reasoning about the vocabulary.

If it is revived, the first question is not the tag but whether the answer is `!prev` at all:
"what it was before" and "who wrote it" are different questions, and phase 4 answers the second
without any of this.

## What this plan does not do

- **Nothing about `!by`** — the payload-predicate claim for an owner that is a value rather than
  a place. The design note's own first open question is whether anything wants it, and phase 1.5
  is what would find out.
- **No org-scale GitHub source.** Phase 1.5 is Linear because it is the case that exists;
  `github:project:*` and `github:user:*` follow the same path and are the GitHub connector's
  business ([gh-source-residual.md](gh-source-residual.md#not-built-each-with-the-trigger-that-would-revive-it)).
- **No change to `--allow-overlap`.** It now warns about the one dangerous shape when there are
  two, and saying so properly wants a phase 1 that has landed.

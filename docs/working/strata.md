# Strata: a declared level ordering, so cross-entity writes are checkable

Status: **built** — see [strata](../strata.md) for what runs, and [the plan](strata-plan.md) for
what was decided along the way. This note is kept because the argument is not in the code: why
the ordering is declared rather than inferred, why a derive's content is literal, and what the
whole thing does not buy.

It proposed two things that only make sense together — a new action face (`derive`, which writes
somewhere other than the subject) and a level ordering that says where such a write may land.
Both shipped, with one shape changed: the note takes a stratum's number as its identity, and it
is the name, for the reasons [the plan](strata-plan.md) records.

It was written to answer the two gaps below, and the second of them — a cycle across rules — is
now caught at runtime by [the cycle throttle](../cycle-throttle.md). So what strata would still
buy is the first gap outright, and a tighter bound on the second rather than the detection of it.
The rest of the argument is unaffected: nothing here rests on the cycle being unchecked.

## Two gaps, and one answer to the first

**An action has exactly one write target.** `Action.Patch` goes to the condition's subject and
nowhere else — "it has no target of its own: what an action acts on is the Condition's Subject"
(`trigger/trigger.go`). So the only way a charter creates an entity *elsewhere* is to spawn a
process and let an agent write it. Every cross-entity write in the model is therefore opaque to
install-time analysis: an agent did it, and nothing can check what.

**A cycle across rules defeats every INSTALL-time protection.**
[Convergence](../convergence.md#the-cycle-and-how-it-is-caught) has the shape: no-op suppression
never engages because each write is a real change, the in-flight guards never engage because each
firing genuinely completes, and the latch check passes because both rules do write a field their
subject reads — into each other's selection. When this note was written nothing caught it at all,
and the proposal was to recover it by *inferring* a graph over triggers — decidable for literal
patches, best-effort through templates.

**That second gap is closed, and not this way.** The cycle throttle catches it at runtime, on
causal depth, which is exact where the graph would have been a warning. So the answer below
answers the FIRST gap; what it still offers the second is a tighter bound, not the detection.

The answer to the first is to let the charter's author declare an ordering, and to make the writes
that cross it checkable.

## The shape

A charter is partitioned into numbered **strata**. Each declares a `match` — a Match in the
ordinary grammar — and a name. Rules live in a stratum.

```tony
0:
  name: observe
  match: !or [ "linear", "gh", "git", "notion", "slack" ]
  triggers:
    - name: urgent-label
      condition:
        subject: "gh:issue:*"
      action:
        patch:
          triaged: true
        derive:
          signadot:
            feature:
              priority: urgent

10:
  name: sdlc
  match: "signadot"
  triggers:
    - name: request-approval
      condition:
        subject: "signadot:feature:*"
      action:
        patch:
          stage: awaiting-approval
        derive:
          do:
            approve:
              system: gh
              kind: pr

100:
  name: effectuate
  match: "do"
  triggers:
    - name: run-approval
      condition:
        subject: "do:approve:*"
```

Numbers with gaps, for the reason BASIC had them: room to insert a stratum later without
renumbering the ones around it. The number is the ordering; the name is for people.

Two constraints, and they are the whole proposal:

- a rule's **subject** must match its own stratum's `match`;
- an action's **derive** must match some **strictly higher** stratum's `match`.

## `derive`: literal content, engine identity

> Shipped with the constraint relaxed twice. A derive may reference its own SUBJECT — every
> part of it but `$subject.commit`. See the note at the end of the next section: the requirement
> was never literalness, but that the levels above cannot influence what a derive writes.

The address is exactly two levels of nesting — `<system>.<kind>` — and everything below is
payload. Nothing deeper is address, so there is no boundary to find, which is what makes the
form readable without a delimiter.

**The content is literal.** A derive carries no `$references`: what it writes is written out in
the charter. That is not a restriction reluctantly accepted, it is the point. It makes the level
check a static match of a *document* against a *pattern* — `Match.MatchRef` (`trigger/match.go`)
is already a pure ref predicate with no store in it — rather than an analysis of what a template
might produce at fire time. The residual that dogs firing-residual §1's inferred graph, "a patch
whose values are computed can satisfy a predicate the key-level check cannot see through", does
not arise, because there is nothing computed to see through.

**The identity is the engine's.** The derived entity's id is a function of (rule, subject), and
the subject linkage is written by the engine rather than stated by the charter. There is an exact
precedent: a spawn's subject is engine-owned — "the subject is the engine's to set, not the
charter's: a process is about exactly one subject, and which one is the Condition's answer"
(`trigger/trigger.go`) — and its id is `spawnID(chainID(chain), subj)` precisely so a re-fire is
idempotent. A derived entity is the same object in different clothes: a thing brought into being
by a firing, about exactly one subject.

Together these two decisions are what make literal content *sufficient* rather than crippling.
`do:approve` says "approve — the thing this is about"; the linkage says which. A consumer at the
effectuate stratum reads *which* PR off the derived entity's subject, exactly as a consumer of a
process reads it today. The charter never has to interpolate an id, so it never has to be trusted
not to.

## A stratum's `match` is two-sided

One pattern, doing two jobs: **the subjects this stratum owns**, and **the derivations it accepts
from below**. That is the economy worth noticing — a stratum publishes one interface, and the
subject constraint and the derive check are the same declaration read from opposite directions.

It also answers the read side, which does not need a rule of its own. A `when` or an `on` naming
a *higher* stratum looks alarming — a write above would wake a rule below, which derives above
again — but a derive is literal, so an upward read can change *whether and when* a rule fires and
can never change *what* it writes. The extra firings are inert: same bytes, same address, no
commit, and [a store write ends itself](../convergence.md#what-ends-a-firing). Reads may go
anywhere.

That inertness leans on literal content. If a derive ever grew references, the read-side
constraint would have to come back — *read at your level or below* — and the reason is worth
recording here, because "let a derive interpolate `$result`" will look like an innocent
convenience the first time someone wants it.

**It was wanted the first time somebody wrote an example, and twice the answer was sharper than
the paragraph above.**

The first correction: what is required is not that the content be literal but that it be fixed by
the same pair the ADDRESS is fixed by, which is (rule, subject). That admitted `$subject` and
refused everything else.

The second correction went further, and is the one that stands. A reference **cannot reach
another entity at all** — it reads the firing's own subject, or a process's result — and nothing
above can write the subject, since a patch touches only its own subject and a derive only climbs.
So the levels above cannot influence what a derive writes, whatever it references about its
subject, and `$subject.value` is admissible too. The rule is *a derive may reference its subject
and nothing else*.

**Except `$subject.commit`**, which is the exact edge the read side turns on. It is a function of
WHEN the firing happened rather than of the subject, so a read from above does change it:

	L (level 0, subject S, reads D)   derive: { at: $subject.commit }
	H (level 10, subject D)           patch:  { seen: true }

	S changes → L derives D{at: 100} → H patches D → L's read wakes it at commit 102 →
	D{at: 102}, a real change → H fires → …

Unbounded, across a level, paced only by the throttle — and it would cost the read side its
freedom, since *read at your level or below* would have to come back. So `.commit` is refused in a
derive, and the engine's own linkage stays commit-free for the identical reason: recording the
causing commit at `verse.meta.derivedFrom` would reintroduce the loop from inside the engine.

The convenience is real rather than cosmetic: `$subject` resolves to the whole ref, so a derive
can put the address under a key of the author's choosing, and a spawn at the level above binds a
top-level field flatly (`task: "approve $pr"` after `pr: $subject`). The linkage is nested, and a
spawn's strings take the older flat substitution.

## What this earns, stated exactly

**A cascade crosses at most (number of strata − 1) levels.** Each derive strictly increases level,
nothing writes downward, and the ordering is finite. Note what this bounds and what it does not:
the number of *crossings*, not the number of steps. Between two crossings a stratum's rules can
patch their own subjects and wake each other without limit; that stretch is bounded by the cycle
throttle, not by the ordering. Nothing here says a charter converges, and the next section says
why it cannot.

**Single writer per derived entity.** Rule names are unique across the whole charter
(`trigger/charter.go`) and the id is injective in subject, so no two rules and no two subjects can
ever address the same derived entity. That retires
[two triggers, one field](../convergence.md#two-triggers-one-field) for everything derived; it
survives only on the subject side, where a `patch` still competes.

**The check is total rather than best-effort.** Cross-stratum edges are ruled out by construction
instead of analyzed, so templates and operators — the thing that makes the inferred graph a
warning rather than a proof — cannot hide an edge.

## It does not converge, and does not need to

Say it plainly, because "well founded" invites the other reading: a charter under strata does not
converge, and no amount of level discipline will make it. The ordering is well founded; the system
running under it is not a fixpoint computation and was never going to be one. Three things loop by
construction — a stratum's rules recursing among themselves, an agent writing whatever its tools
reach, and the world coming back around through the mirror. What is on offer is that each of those
is *bounded, observable, or paced*, which is a different property and the one worth having.

The `fwd`/`back` pair from [convergence](../convergence.md#the-cycle-and-how-it-is-caught)
survives intact if both rules sit in one stratum and both patch their own subject. Level
discipline satisfied, and it still cycles. This is stratified Datalog's bargain: recursion inside
a stratum is exactly what is allowed.

So strata never claimed to replace the cycle check, and what does replace it is the
[cycle throttle](../cycle-throttle.md), which bounds a firing's causal depth by the number of
installed triggers.

**What strata would still buy it is a tighter bound — on detection, not on the total.** A chain
that crosses a level can never come back, so the counter could reset at every crossing, and a loop
confined to one stratum would then trip after that stratum's own trigger count rather than after
the whole charter's. The total is not loosened by the reset: levels strictly increase, so each
stratum is entered at most once, and the sum over entered strata is still the installed trigger
count. It is a tightening of how fast a runaway is caught, not of the worst case. And it is not a
prerequisite: with no strata declared there is one implicit stratum and the cap is the installed
trigger count, which is a usable bound today.

## The three install checks

1. **Every rule's subject matches its stratum's `match`.** Distributing the stratum's pattern over
   the `subject:` fields of its rules, which is what it means.
2. **Every derive matches some strictly higher stratum's `match`.** A derive matching nothing, or
   matching only at or below, is refused.
3. **Strata are pairwise disjoint.** Without this the ordering is vacuous: a document landing in
   the intersection of two strata is both above and at, and "strictly higher" stops being a
   question with one answer. Cheap to check, easy to get wrong once there are six strata.

All three are syntactic. None needs the store.

## The loop that remains goes through the world

The effectuate stratum writes a real approval to GitHub; the mirror observes GitHub and writes it
back into the verse at stratum 0. The cycle closes — through the *world*.

That is firing-residual §1's paced cycle with the external system standing in for the ticker: it
is observable, because it passes through the mirror; it is rate-limited by real latency rather
than by how fast the store accepts writes; and it is the loop a person would draw on a whiteboard
anyway. Making that the *only* surviving cycle is a better outcome than forbidding cycles.

Worth saying plainly wherever this is documented for users, because "well founded" will otherwise
be read as "nothing loops", and the first person to watch GitHub come back around will think they
have found a bug.

## Processes are outside the ordering

A spawned agent writes wherever its tools reach, at no level. So the guarantee reads: *the
ordering covers a charter only to the extent its writes are declared.* `derive` moves cross-entity
writes from
"an agent did it, unanalyzable" into "the charter said it, checkable" — and every write still
going through a spawn is outside the ordering entirely.

Which suggests strata want an authority counterpart: a spawn's writes fenced to its own stratum
and above. The precedent is exact — `Action.validate` already refuses a `provider:` without a
`grant:` on the grounds that a fire-time property is only checkable against a ceiling the charter
states (`trigger/trigger.go`). A grant is that ceiling for the *what* axis; a stratum could be it
for the *where*. See [authority](../roadmap/authority.md).

## The durable shape is not the file shape

The level-keyed document above is a good thing to read and a bad thing to store.
[The charter is one entity per rule](../trigger-model.md#the-charter-is-durable) for reasons that
still hold: installing writes one entity and removing deletes one, so there is no
read-modify-write and two concurrent installs cannot lose each other; and a rule is addressable
(`verse entity get trigger:rule:review`). A single level-keyed document gives all of that up.

So the durable form is split, and keeps both:

- a rule stays one entity, gaining a `stratum:` field naming the one it lives in;
- a stratum is its own entity — `trigger:strata:sdlc` — carrying `{level, match}`.

Strata are few and change rarely; rules are many and change often, which is the same asymmetry
the one-entity-per-rule argument turns on. The document form stays as the *file* syntax for
`verse trigger add -f charter.tony`, fanning out into those entities on install, as a list keyed
`!key(name)` — the key being the entity's id, which is what makes the two forms one thing seen
twice. (Not the level-keyed document the example above uses; that form is left as written because
what it shows is the ordering, not the syntax.)

**The name addresses, the number orders**, which settles what would otherwise be an open question
and settles it the way that question leaned: a rule saying `stratum: sdlc` reads as what it means,
where `10` does not. The level is an ordinary field, so **two strata may not share one** — refused
at install, because the number is the whole ordering and a charter that gives it twice has not
said which comes first. Renaming a stratum breaks every rule naming it, which is the price; it is
paid rarely, and by an error rather than by silence.

**The split costs one check the file form gets for free.** In a single document a stratum cannot
dangle — every rule sits inside the one it names. Installed as separate entities, a rule naming
`sdlc` can arrive before `sdlc` does, and that check needs the store to answer. It is the one
check here that does. Whether such a rule is refused at install or installed dormant until its
stratum appears is open, and the second reads better against independent installs: refusing makes
the order of two independent writes matter, which is the property the one-entity-per-rule shape
exists to avoid.

## Open

- **What the derive's first key names.** The sketch this note came from used the target stratum's
  `match` in one example and its `name` in the other. This note takes it to be the match — the
  address is an address, and the name is for people — and renamed the third stratum accordingly
  (`match: do`, `name: effectuate`) so both examples read the same way. It could go the other way:
  addressing a stratum by name would let a stratum's match change without rewriting every derive
  that targets it.
- **Rollups are forbidden, and that may be fine.** Since identity is (rule, subject), a derive
  cannot accumulate — fifty features cannot feed one `do:approve`. The effectuate stratum reads a
  *set* of derived entities instead. That is what buys single-writer, so the trade is deliberate,
  but if an aggregate is ever genuinely wanted it needs a different construct rather than an id
  slot on this one.
- **Migration — answered, by there being none.** Nothing installed is expected to survive; the
  verse gets clobbered and rebuilt. What the implicit stratum is for is not a migration path but
  an oracle: a charter declaring no strata behaves exactly as today, so the existing tests stay
  the check on every step. The guarantee grows as strata are declared.
- **A rule whose level names no installed stratum.** Refused at install, or installed dormant. See
  [the durable shape](#the-durable-shape-is-not-the-file-shape); the argument there favours
  dormant.
- **How a derived entity leaves.** Unspecified, and not urgent — nothing reaps derived entities
  unless a charter says to. Worth one line only because the
  [subject-side protection](../trigger-model.md#lifecycle-how-an-entity-leaves) does not extend
  here: a derive's target is addressed rather than selected, so the vanishing marker is not in its
  path.

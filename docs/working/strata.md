# Strata: a declared level ordering, so derivation is well founded

Status: design note, nothing here runs. It proposes two things that only make sense together — a
new action face (`derive`, which writes somewhere other than the subject) and a level ordering
that says where such a write may land.

## Two gaps, and they have one answer

**An action has exactly one write target.** `Action.Patch` goes to the condition's subject and
nowhere else — "it has no target of its own: what an action acts on is the Condition's Subject"
(`trigger/trigger.go`). So the only way a charter creates an entity *elsewhere* is to spawn a
process and let an agent write it. Every cross-entity write in the model is therefore opaque to
install-time analysis: an agent did it, and nothing can check what.

**Nothing sees a cycle across rules.** [Convergence](../convergence.md#the-cycle-which-is-the-one-that-is-not-checked)
names this as the one hazard every existing protection misses, and misses correctly:
no-op suppression never engages because each write is a real change, the in-flight guards never
engage because each firing genuinely completes, and the latch check passes because both rules do
write a field their subject reads — into each other's selection.
[Firing residual §1](firing-residual.md#1-the-cycle-check-and-the-clock-it-needs) proposes
recovering it by *inferring* a graph over triggers, which is decidable for literal patches and
best-effort through templates.

The answer to both is to let the charter's author declare an ordering, and to make the writes
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

## What this earns, stated exactly

**Any derivation cascade from one commit terminates in at most (number of strata) steps.** Each
derive strictly increases level and the ordering is finite and well founded. Not "the charter
converges" — see the next section.

**Single writer per derived entity.** Rule names are unique across the whole charter
(`trigger/charter.go`) and the id is injective in subject, so no two rules and no two subjects can
ever address the same derived entity. That retires
[two triggers, one field](../convergence.md#two-triggers-one-field) for everything derived; it
survives only on the subject side, where a `patch` still competes.

**The check is total rather than best-effort.** Cross-stratum edges are ruled out by construction
instead of analyzed, so templates and operators — the thing that makes the inferred graph a
warning rather than a proof — cannot hide an edge.

## What it does not earn

The `fwd`/`back` pair from [convergence](../convergence.md#the-cycle-which-is-the-one-that-is-not-checked)
survives intact if both rules sit in one stratum and both patch their own subject. Level
discipline satisfied, still runs until the backstop. This is stratified Datalog's bargain:
recursion inside a stratum is exactly what is allowed.

So strata **localize** firing-residual §1 rather than replace it. The graph to search shrinks from
the whole charter to one stratum, and the edges that were undecidable are the ones that stop
existing. The ticker is still wanted — for the paced control loop within a stratum, and for the
reaper's grace period, which was always the same piece of work.

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

A spawned agent writes wherever its tools reach, at no level. So the guarantee reads: *a charter
is well founded to the extent its writes are declared.* `derive` moves cross-entity writes from
"an agent did it, unanalyzable" into "the charter said it, checkable" — and every write still
going through a spawn is outside the ordering entirely.

Which suggests strata want an authority counterpart: a spawn's writes fenced to its own stratum
and above. The precedent is exact — `Action.validate` already refuses a `provider:` without a
`grant:` on the grounds that a fire-time property is only checkable against a ceiling the charter
states (`trigger/trigger.go`). A grant is that ceiling for the *what* axis; a stratum could be it
for the *where*. See [authority](../roadmap/authority.md).

## The durable shape should not be the file shape

The level-keyed document above is a good thing to read and a bad thing to store.
[The charter is one entity per rule](../trigger-model.md#the-charter-is-durable) for reasons that
still hold: installing writes one entity and removing deletes one, so there is no
read-modify-write and two concurrent installs cannot lose each other; and a rule is addressable
(`verse entity get trigger:rule:review`). A single level-keyed document gives all of that up.

The split that keeps both:

- a rule stays one entity, gaining a `level:` field;
- a stratum becomes its own entity — `trigger:stratum:10` — carrying `{name, match}`.

Strata are few and change rarely; rules are many and change often, which is the same asymmetry
the one-entity-per-rule argument turns on. The document form stays as the *file* syntax for
`verse trigger add -f charter.tony`, fanning out into those entities on install.

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
- **Migration.** Every rule needs a level. Defaulting existing rules to a single stratum gives
  exactly today's semantics — intra-stratum unchecked, as now — so this is additive and the
  guarantee grows as strata are declared rather than arriving as a flag day.
- **Whether the number or the name is canonical.** The number orders and the name explains, but a
  rule carrying `level: 10` reads worse than `level: sdlc`, and inserting a stratum renumbers
  nothing while renaming one breaks every rule that named it.
- **What a derive does when its target already exists with a different value.** It is a patch, so
  it merges; single-writer means the only thing it can be merging with is its own earlier
  firing. Probably nothing to decide, but it has not been thought through against the
  [lifecycle](../trigger-model.md#lifecycle-how-an-entity-leaves) — in particular whether a derive
  may resurrect an entity a reaper has marked.

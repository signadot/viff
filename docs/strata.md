# Strata — a declared level ordering, and writes that cross it

An action has one write target: the condition's subject. That is the whole of `patch`, and it
means the only way a charter could bring an entity into being *somewhere else* was to spawn an
agent and let it write — which put every cross-entity write beyond anything install-time
analysis could see. An agent did it, and nothing could check what.

`derive` is that write, said in the charter instead. And because a charter that can write
anywhere can write in a circle, `derive` arrives with the thing that bounds it: a **stratum**, a
declared level, with rules living in one and derivations only ever climbing.

```tony
- name: observe
  level: 0
  match: [ "gh", "linear" ]
- name: sdlc
  level: 10
  match: "signadot"
- name: effectuate
  level: 100
  match: "do"
---
- name: urgent-label
  stratum: observe
  condition:
    subject: { system: gh, kind: issue, value: { label: urgent } }
  action:
    patch: { triaged: true }
    derive:
      signadot:
        feature:
          priority: urgent

- name: request-approval
  stratum: sdlc
  condition:
    subject: { system: signadot, kind: feature, value: { priority: urgent } }
  action:
    patch: { stage: awaiting-approval }
    derive:
      do:
        approve:
          system: gh
          kind: pr
```

`verse trigger add -f charter.tony` installs both halves, strata first whatever order the file
put them in. Numbers with gaps, for the reason BASIC had them: room to insert a level later
without renumbering its neighbours.

## Two rules, and they are the whole thing

- a rule's **subject** must be inside its stratum's `match`;
- an action's **derive** must be inside some **strictly higher** stratum's `match`.

Both are checked at install, and neither reads the store. A charter that breaks either is refused
by `verse trigger add`, naming which rule and which level.

A stratum's `match` does two jobs at once, and that is the economy worth noticing: it says **what
subjects this stratum owns** and **what derivations it accepts from below**. One declaration,
read from opposite directions.

## What a stratum may match

A system, optionally a kind, and nothing below — no `id`, no `value` predicate. Write a union as
a list: `match: [ "gh", "linear" ]`.

The restriction is not tidiness. A derived entity's id belongs to the engine (below), so at
install there is no id to check a pattern against — a stratum matching on one could not be
checked at the moment the whole ordering is bought for. And a `value` predicate would make a
level depend on a state that rules change as they fire, which is not a level.

## `derive` — literal content, engine identity

The address is exactly two levels — `<system>.<kind>` — and everything under it is payload.
Nothing deeper is address, so there is no boundary to find.

**The content is literal.** A derive carries no `$references`, and this is refused at install
rather than tolerated. It is what makes the level check a static match of a document against a
pattern instead of a guess at what a template will produce at fire time. It is also what makes
reading *upward* safe: a rule may `on` or `when` anything at any level, because an extra firing
writes the same bytes to the same address, which is a no-op the store already ends.

**The identity is the engine's.** The derived entity's id is a function of (rule, subject), and
the engine writes the subject linkage itself, at `verse.meta.derivedFrom`:

```
$ verse entity get signadot:feature:urgent-label.gh_issue_1
priority: urgent
verse:
  meta:
    derivedFrom:
      rule: urgent-label
      subject: gh:issue:1
```

This is the spawn's rule, and for the spawn's reason: a derivation is about exactly one subject,
and which one is the condition's answer, not something a charter should have to interpolate. The
two together are what make literal content *sufficient* rather than crippling — `do:approve` says
"approve the thing this is about", and the linkage says which. A consumer at the level above
reads the subject off the derived entity exactly as a consumer of a process reads its flow.

Two consequences fall out. A re-fire about the same subject writes the same address, so it is
idempotent. And **no two rules can ever address one derived entity**: names are unique across the
charter and the id is injective in the subject, which retires
[two triggers, one field](convergence.md#two-triggers-one-field) for everything derived.

It also forbids rollups: fifty features cannot feed one `do:approve`. The level above reads the
*set* instead. That is the trade single-writer is bought with.

## What this earns, and what it does not

**A cascade crosses at most (number of strata − 1) levels.** Each derive strictly increases
level, nothing writes downward, and the ordering is finite.

Note what that bounds: the number of *crossings*, not the number of steps. Between two crossings
a stratum's rules can patch their own subjects and wake each other freely, and that stretch is
bounded by [the cycle throttle](cycle-throttle.md), not by the ordering.

**It is not a convergence guarantee, and cannot be made into one.** Recursion inside a stratum is
exactly what a level ordering permits — stratified Datalog's bargain — so the `fwd`/`back` pair
from [convergence](convergence.md#the-cycle-and-how-it-is-caught) survives intact if both rules
sit in one stratum. What catches it is the throttle, at runtime, as before.

**The loop that remains goes through the world.** The effectuate stratum writes a real approval
to GitHub; the mirror observes GitHub and writes it back at level 0. That cycle closes — through
the *world*, which means it is observable (it passes the mirror), paced by real latency rather
than by how fast the store accepts writes, and the loop a person would draw on a whiteboard
anyway. Worth saying plainly, because "well founded" reads as "nothing loops", and the first
person to watch GitHub come back around will think they have found a bug.

**Processes are outside the ordering.** A spawned agent writes wherever its tools reach, at no
level. So the guarantee reads: *the ordering covers a charter to the extent its writes are
declared.* `derive` moves cross-entity writes from "an agent did it, unanalyzable" into "the
charter said it, checkable"; every write still going through a spawn is outside it. Fencing those
is [authority](roadmap/authority.md)'s business.

## Declaring nothing is a complete answer

A charter with no `stratum:` anywhere behaves exactly as it always has: one implicit stratum,
nothing checked, intra-stratum recursion caught by the throttle. The guarantee grows as strata
are declared rather than arriving as a flag day.

The one thing an unstratified rule may not do is `derive`. "Strictly above" has no answer for a
rule with no level, and reading that leniently would hand back the unchecked cross-entity write
the ordering exists to remove. Declaring a stratum is what buys a derive.

## Installing, and the order it does not depend on

A stratum is one entity (`trigger:strata:<name>`) as a rule is one (`trigger:rule:<name>`), for
the same reasons: installing writes one and removing deletes one, so there is no
read-modify-write and two concurrent installs cannot lose each other.

The **name addresses** and the **level orders**. A rule says `stratum: sdlc`, which reads as what
it means where `10` does not. Two strata may not share a level — the number is the whole ordering
and a charter giving it twice has not said which comes first — and that is refused at install.

Because they are separate entities, either can arrive first. **A rule naming a stratum that is
not installed yet is held, not refused**: written down, not firing, and installed the moment its
stratum arrives. It has already passed every other check, so what holding defers is the ordering
question and nothing else. A file never produces one, since `verse trigger add` sends the strata
first.

**Removing a stratum is refused while rules still name it.** Those rules were checked against a
pattern that would no longer exist, and nothing re-runs a check — they would go on firing,
unstratified, having been told they were ordered. Remove the rules first, or move them.

```
$ verse strata                 # the ordering, by level
$ verse stratum show sdlc
$ verse stratum remove sdlc    # refused while rules live there
```

## Corners

**A misspelled field is named, to a depth.** An unknown field decodes silently — gomap ignores
what it does not recognise — so `startum: sdlc` would install into the implicit stratum and report
success, discarding the one field that says which guarantees apply. `verse trigger add` refuses
it, and likewise an unknown field in a `condition`, in an `action`, or in a sequence element or
completion hook below one. It stops where the shape stops being the charter's: a `spawn` is a
launch spec, and a `patch` or `derive` payload has the author's own keys in it.

**How a derived entity leaves is unspecified.** Nothing reaps derived entities unless a charter
says to. Note that the [subject-side protection](trigger-model.md#lifecycle-how-an-entity-leaves)
does not extend here: a derive's target is addressed rather than selected, so the vanishing
marker is not in its path.

**The throttle does not yet reset at a crossing.** It could — a chain never returns to a stratum
it has left — which would catch a loop confined to one stratum after that stratum's own trigger
count rather than the whole charter's. It is a tightening of how fast a runaway is caught, not of
the worst case, and it is not built.

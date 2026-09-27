# Strata

An action has one write target: the condition's subject. That is the whole of `patch`, and it
means `patch` alone can bring an entity into being *somewhere else* only by spawning an agent
and letting it write — which puts every cross-entity write beyond anything install-time
analysis can see. An agent does it, and nothing can check what.

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
    subject: {path: "gh.issue.*", value: { label: urgent }}
  action:
    patch: { triaged: true }
    derive:
    - path: "signadot.feature"
      id: $subject.name            # signadot.feature.1, for gh.issue.1
      value:
        priority: urgent

- name: request-approval
  stratum: sdlc
  condition:
    subject: {path: "signadot.feature.*", value: { priority: urgent }}
  action:
    patch: { stage: awaiting-approval }
    derive:
    - path: "do.approve"
      value:
        feature: $subject
```

`verse c -a -f charter.tony` installs both halves, strata first whatever order the file
put them in. Numbers with gaps, for the reason BASIC had them: room to insert a level later
without renumbering its neighbours.

## Why the ordering is declared rather than inferred

The alternative is to work it out: build a graph over the installed triggers, edge from a rule that
writes a key to a rule that selects on it, and refuse a cycle. That is decidable for a literal patch
and best-effort through anything computed, since a patch whose values come from a template can
satisfy a predicate a key-level check cannot see through.

Best-effort is the wrong shape for a guarantee. An edge a template hides is an edge the check
silently does not have, and a check that is right most of the time reads exactly like one that is
right, up until it matters. Declared, a cross-level write is ruled out by construction, so there is
nothing left for a template to hide, which is also why a derive's content is constrained rather
than analysed.

## Two rules, and they are the whole thing

- a rule's **subject** must be inside its stratum's `match`;
- an action's **derive** must be inside some **strictly higher** stratum's `match`.

Both are checked at install, and neither reads the store. A charter that breaks either is refused
by `verse c -a`, naming which rule and which level.

A stratum's `match` does two jobs at once, and that is the economy worth noticing: it says **what
subjects this stratum owns** and **what derivations it accepts from below**. One declaration,
read from opposite directions.

## What a stratum may match

**A place**: a path, and no `value` predicate. Write a union as a list:
`match: [ "gh", "linear" ]`. A place is a path of any length, and it owns every address at or
below it:

```tony
- {name: tip,    level: 10, match: {path: "verse-dev.drift.head"}}
- {name: ledger, level: 20, match: {path: "verse-dev.drift.finding"}}
```

`[verse-dev, drift, head]` is the place derivations land under, and a rule's subject
`[verse-dev, drift, head, "*"]` is inside it.

**Containment is asked segment by segment.** A stratum's path contains a subject when it is no
longer than the subject's and, at each position, admits every name the subject's segment does. A
`*` admits anything; a name admits itself. **A subject with an operator at a position is contained
only by a `*` there**, because that is the honest answer: `!glob "head*"` might select outside a
level named `head`, and nothing at install can say it does not. So a rule whose subject globs a
level is not in a stratum that owns one particular level — and since strata are pairwise disjoint,
there is usually no level that owns it either. Such a rule is unstratified, which is fine as long
as it does not derive: an operator belongs below the place a stratum names, rather than at a level
of it.

Two strata are disjoint when, at some position both paths reach, their segments admit no common
name.

A `value` predicate would make a level depend on a state that rules change as they fire, which is
not a level, so a stratum refuses one.

## `derive` — identity, not state

The address is a place and the content goes under it. Each entry names its place as a path and
its content as `value`, so there is no boundary to find:

```tony
derive:
- path: "verse-dev.drift.head"
  value: {sha: $subject.value.sha}
```

**A derive may reference its subject, and nothing else** — `$subject` and its parts resolve,
every other `$reference` is refused at install. (A path read may reach further, at a price the
next section states.)

The line is where it is because of what a re-fire has to be. A `$reference` cannot reach another
entity at all — it reads the firing's own subject, or a task's result — and nothing above can
write the subject, since a patch touches only its own subject and a derive only climbs. So an
upward read that wakes the rule without changing the subject resolves to the same bytes, writes
them to the same address, and dies as a no-op. That is what makes reading upward safe: a rule may
`on` or `when` anything at any level, because the extra firings a read causes cannot change what
gets written.

**`$subject.commit` is the exception, and it is refused in a derive.** It is a function of *when*
the firing happened rather than of the subject, so a read from above does change it — and a
derive that writes something new on every lap wakes the level above, which wakes the rule again:
a cycle across a level, which is the one thing the ordering exists to rule out. The same reason
keeps the engine's own linkage commit-free.

The parts:

| reference | resolves to |
|---|---|
| `$subject` | the whole address, `"gh.issue.1"` — a kinded path |
| `$subject.ref` `.name` | the address, and its last segment — the key it stands at |
| `$subject.value` | the subject's payload **at the commit that caused the firing** |
| `$subject.commit` | that commit's revision — not in a derive |

`$subject.value` reads at the causing commit rather than at the moment the action runs, and the
difference is a gate: a firing held for an approval acts whenever the answer comes, and reading
late would write from a state nobody evaluated. It costs nothing when the causing commit changed
the subject, which is the ordinary rule about the entity that changed; a fan-out member the commit
did not touch is read at that revision, which the store answers by replaying its log.

A completion hook reads at the commit **its rule fired at**. It is woken by a task finishing
rather than by a write — which is why its chain starts again for the cycle throttle — but the
revision it reads at is the firing's, carried on the task's record. So a `derive` in an `onDone`
can name what the firing was about, which is what "derive a release note from what shipped"
needs. A task the charter did not start has no such commit, and both parts fail by name there
rather than resolving to a silent zero.

In a spawn's strings the identity parts work too (`prompt: "approve $subject.name"`), because those
are published as flat bindings. `.value` is not: a payload has no text form, and the subject's own
top-level fields are already flat there (`$stage`).

**The identity is the engine's.** The derived entity's id is a function of (rule, subject) — of
(rule, subject, key) for a [fan-out](#each--when-one-answer-holds-many-facts), which is the same
sentence with the element's key in it — and the engine writes the subject linkage itself, at
`verse.meta.derivedFrom`:

```
$ verse data derived feature raise gh issue 1
priority: urgent
what: gh.issue.1
verse:
  meta:
    derivedFrom:
      rule: raise
      subject: gh.issue.1
```

The address reads `<target>.<rule>.<subject…>` — the place the rule derives into, the rule
itself as one segment, then the subject's own segments. The rule is in there because identity is
(rule, subject) rather than subject alone: without it, two rules deriving into one place about
one subject would write the same entity, which is the race single-writer exists to retire.

It is an address rather than an id, so it carries the subject faithfully.
`derived.feature.raise.gh.issue.1` is the entity `raise` derived about `gh.issue.1`, and reads back
as exactly those segments. Two things follow. `verse data derived feature raise -paths` is
"everything this rule has derived". And **a derived path is as long as what it is about**: a rule
reading one says how long, and that follows from the subjects the producing rule selects, so
`{path: "derived.feature.*.*.*.*"}` for a rule deriving about `gh.issue.*`.

The linkage is the spawn's rule, for the spawn's reason: a derivation is about exactly one
subject, and which one is the condition's answer rather than something a charter should have to
interpolate. So `do.approve` can say "approve the thing this is about" and the linkage says which,
whether or not the charter also wrote `$subject` somewhere it finds convenient — under its own
key, where a consumer's spawn can bind it flatly (`prompt: "approve $pr"`, after `pr: $subject`).

Two consequences fall out. A re-fire about the same subject writes the same address, so it is
idempotent. And **no two rules can ever address one derived entity**: names are unique across the
charter and the id is injective in the subject, which retires
[two triggers, one field](convergence.md#two-triggers-one-field) for everything derived.

It also forbids rollups: fifty features cannot feed one `do.approve`. The level above reads the
*set* instead. That is the trade single-writer is bought with.

### A key on a single derive

A derive may name a `key:` without fanning out:

```tony
action:
  derive:
  - path: "verse-dev.merge.train"
    key:  $subject.value.sha      # one train per tip of main
    value: {sha: $subject.value.sha}
```

The address becomes (rule, subject, key) — the fan-out's identity with one element — and it is
there for the case a plain derive cannot say: **the same subject, decided more than once.**

A merge train is the case that needs it. `git.verse.head` is a single entity whose `sha` moves, so
a rule deriving a batch from it writes one address, and a
[gate](reference/gates.md#gates--whether-it-proceeds)'s id is a function of (trigger, gate, subject).
Without a key, the second batch lands at an address whose motion is already settled and inherits
the first batch's answer, with nobody asked and nothing in the docket saying so. With the sha in
the address, a new tip is a new subject and therefore a new question.

The two install checks are the fan-out's, minus the one about the element: the key must be a
**single reference** (one embedded in text interpolates to a string, and an address is not
something to assemble out of prose), and it may read the **subject** — its state included — and
nothing else. `$subject.commit` is refused here for the reason it is refused in the content: a
key that is a function of *when* the rule fired writes a new entity on every lap.

And it is the invariant rather than a hole in it. A re-fire about the same subject computes the
same key, writes the same bytes to the same address, and dies as the no-op that lets a rule read
a higher level safely.

### An `id` instead of a key

`id:` takes the same single reference and drops the composition: the derived entity is the place
plus that value, verbatim, so `id: $subject.value.sha` under `[verse-dev, merge, train]` is
`verse-dev.merge.train.<sha>` — an address another rule can spell. The difference is what the
value identifies. Two subjects of one rule that compute the same `id` share one entity, where
`key` gives them one each; so `id` says the value identifies the record across subjects, and
`key` says it identifies it only within one. An entry names one or the other, never both, and
`!each` takes `id` in place of `key` the same way.

## `!each` — when one answer holds many facts

One firing derives one entity, which is right until a task answers with twelve findings and eleven
of them are prose. `!each` is the fan-out:

```tony
action:
  derive:
  - path: "drift.finding"
    value: !each
      over:  $data.findings     # a list to walk; each element binds $item
      key:   $item.symbol       # the address, read out of the element
      value:
        symbol: $item.symbol
        says:   $item.says
        about:  $subject.name
```

**It is the same invariant, generalised, not an exception to it.** A plain derive may carry no
state because its address is (rule, subject): content fixed by the address is content that
rewrites identically, which is the no-op that lets a rule read a higher level safely. A fan-out's
address is **(rule, subject, key)** with the key read out of the element, so the same answer
still yields the same addresses carrying the same bytes. The rule underneath both is:

> **A derive may carry state exactly when its address is a function of that same state.**

So `over`, `key` and `value` are checked at install, and each refusal is that sentence applied:
`over` and `key` must each be a *single* reference (one embedded in text interpolates to a
string, and there is nothing to walk or address in that); `key` must be rooted at `$item`, or
every element shares one address; `value` may reference `$item` and the subject's *identity*,
and `$subject.commit` stays refused for the reason it is refused in a plain derive. The keyspace
under the tag is closed — `sweep: true` is a typo, not payload.

Two runtime refusals, both loud on purpose. `over` must resolve to a **list**: a charter wanting
a nested answer flattened says so in its answer contract, where the shape is already declared.
And an element whose `key` is empty **fails the whole firing**, naming which element — deriving
the others would lose a member into a ledger that reads short, and short is the one failure
nobody can see.

The address flattens what an id may not hold (`io/github.Reflect` → `io_github.Reflect`)
and carries a hash of the original when it does, so two keys that flatten alike stay two
entities. Put the readable key in the payload too; the address is for the engine.

### It is append-only, and that is a decision

A finding that stops appearing in an answer **leaves its entity standing**. Nothing sweeps.

That is not an oversight to be fixed later. A ledger wants **resolution to be a fact somebody
wrote** — resolved, at this commit, by this change — rather than an absence inferred from a model
not mentioning something this time. A sweep would turn the second into the first silently, which is
worse than leaving a stale entity a reader can see and delete.

A derive that *removes* is also a different capability from a derive that loops, and re-opens the
ordering argument from the other end, since a delete is not idempotent the way a write is. It
should not arrive smuggled inside a fan-out.

## Path reads inside a derive

[`!get-path` and `!list-paths`](reference/action.md#get-path-and-list-paths--reaching-another-entity)
let an action copy an entity that is not its subject, and they are the one thing the ordering has
to constrain — because they break the fact the section above rests on: that nothing a derive
writes can be reached from a level above it.

**A path read inside a derive must resolve at or below the rule's own stratum.** An entity at or
below is written only by rules at that level, or derived into from below it — never from above —
so the levels above still cannot influence what a derive writes, and inertness holds exactly.
Reading upward would give it away: the thing above changes, the derive writes different bytes,
the level above wakes, and round it goes across a level.

It is checked at install, by the same containment the level checks use: the read's address, up to
its first segment that is not a literal name, is a place, and a stratum's `match` has to contain
it. A read naming a place in no stratum is refused too: no level, so "at or below" has no answer.

In a plain `patch` none of this applies, because nothing downstream depends on a patch being
reproducible. A rule may read any level it likes and write it to its own subject.

Nor in a condition, for the same reason one level further back: a read there decides which
entities a rule is about and writes nothing, so there is nothing for a level above to influence.
It is unconstrained, and it is the one read that counts toward what the trigger *reads* — see
[the trigger model](reference/action.md#a-condition-may-read-too-and-that-one-does-track).

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

**The loop that remains goes through the world.** The effectuate stratum writes a real approval to
GitHub, the mirror observes GitHub, and it comes back at level 0. That cycle closes through the
*world*, which makes it observable, since it passes the mirror, and paces it by real latency rather
than by how fast the store accepts writes. It is the loop a person would draw on a whiteboard.
Worth saying plainly, because "well founded" reads as "nothing loops", and the first person to
watch GitHub come back around will think they have found a bug.

**Tasks are outside the ordering.** A spawned agent writes wherever its tools reach, at no
level. So the guarantee reads: *the ordering covers a charter to the extent its writes are
declared.* `derive` moves cross-entity writes from "an agent did it, unanalyzable" into "the
charter said it, checkable"; every write still going through a spawn is outside it.

## Declaring nothing is a complete answer

A charter with no `stratum:` anywhere runs in one implicit stratum, with nothing checked and
intra-stratum recursion caught by the throttle. The guarantee grows as strata are declared rather
than arriving as a flag day.

The one thing an unstratified rule may not do is `derive`. "Strictly above" has no answer for a
rule with no level, and reading that leniently would hand back the unchecked cross-entity write
the ordering exists to remove. Declaring a stratum is what buys a derive.

## Installing, and the order it does not depend on

A stratum is one entity (`verse.trigger.strata.<name>`) as a rule is one
(`verse.trigger.rule.<name>`, prefixed with the package for a charter installed at an address), for
the same reason: installing writes one and removing deletes one, so there is no read-modify-write
and two concurrent installs cannot lose each other.

The **name addresses** and the **level orders**. A rule says `stratum: sdlc`, which reads as what it
means where `10` does not. Two strata may not share a level, because the number is the whole
ordering and a charter giving it twice has not said which comes first, and that is refused at
install.

Because they are separate entities, either can arrive first. **A rule naming a stratum that is
not installed yet is held, not refused**: written down, not firing, and installed the moment its
stratum arrives. It has already passed every other check, so what holding defers is the ordering
question and nothing else.

**A file produces held rules whenever it names a stratum it does not itself define**, which a
charter written to sit beside another one does. `verse c -a` sends a file's own strata
first, so a self-contained charter never holds; one whose rules name a neighbour's stratum holds
exactly those rules until the neighbour is installed.

The install tells you which rules it held. A stratum name with a spelling mistake in it is
held in the same way, so read this line:

```
$ verse charter -a -f review.tony
baseline: installed 0 trigger(s)
  held: rule review waits for stratum sdcl, which is not installed. It fires on nothing until that stratum is.
```

The count is the rules that can fire. `verse charter` lists a held rule with `held: <stratum>`.

`verse c <address> rules <name>` shows a held rule too, with the same `held` line.
`verse status` counts the rules that can fire, so a four-rule charter with two held reports
two.

**Editing a level is re-applying the file.** A stratum already installed exactly as declared is a
no-op, and one that differs is **replaced**, the same answer a rule of the same name gets, because
the file is what the operator means and re-applying an edited one is how a charter changes. The
change is checked against every rule standing in that charter: they were checked against the level
when they installed, so they face the ordering again, and a rule that no longer fits refuses the
*change*, naming itself. Nothing is left at a level nothing checked it against.

**And a file is applied as one step.** `verse c -a` sends a package's worth of a file in one
request, and a refusal anywhere puts back what stood before it: the declaration, the levels and the
rules are all the charter or none of them are. Sent document by document, a file whose levels
installed and whose rule was refused would leave the levels standing while reporting the command
failed — a charter in a state nobody wrote down, holding a level the corrected file could then not
replace.

**Removing a stratum is refused while rules still name it.** Those rules were checked against a
pattern that would cease to exist, and nothing re-runs a check, so they would go on firing
unstratified having been told they were ordered. Remove the rules first, or move them.

```
$ verse charter             # the ordering, by level
$ verse c rules sdlc
$ verse c -r strata sdlc # refused while rules live there
```

## Corners

**A misspelled field is named, to a depth.** An unknown field decodes silently, since gomap ignores
what it does not recognise, so `startum: sdlc` would install into the implicit stratum and report
success, discarding the one field that says which guarantees apply. `verse c -a` refuses it, and
likewise an unknown field in a `condition`, in an `action`, or in a sequence element or completion
hook below one. It stops where the shape stops being the charter's: a `spawn` is a launch spec, and
a `patch` or `derive` payload has the author's own keys in it.

**How a derived entity leaves is unspecified.** Nothing reaps derived entities unless a charter
says to. Note that the [subject-side protection](trigger-model.md#lifecycle-how-an-entity-leaves)
does not extend here: a derive's target is addressed rather than selected, so the vanishing
marker is not in its path.

**The throttle does not yet reset at a crossing.** It could — a chain never returns to a stratum
it has left — which would catch a loop confined to one stratum after that stratum's own trigger
count rather than the whole charter's. It is a tightening of how fast a runaway is caught, not of
the worst case, and it is not built.

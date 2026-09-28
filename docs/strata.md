# Strata

The action of a [rule](glossary.md#rule) has one write target for `patch`: the
[subject](glossary.md#subject) of its condition. To bring an entity into being at another
address with `patch` alone, a rule must spawn an agent and let the agent write. No analysis at
install can see such a write. An agent does it, and nothing can check what it writes.

[`derive`](glossary.md#derive) is that write, said in the [charter](glossary.md#charter)
instead. A charter that can write anywhere can write in a circle, so `derive` comes with a
bound: the **stratum**. A [stratum](glossary.md#stratum) is a named part of the store with a
declared level. The [level of a stratum](glossary.md#level-of-a-stratum) is a number that
orders the strata of one charter. Each rule lives in one stratum, and a derivation only climbs
to a higher level.

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

`verse c -a -f charter.tony` installs both halves. It installs the strata first, whatever their
order in the file.

The levels are numbers with gaps, as the line numbers of BASIC are. A gap leaves room to insert
a level later without renumbering its neighbours.

Verse does not infer the ordering from the rules. The reason is in
[Why the ordering is declared rather than inferred](design/strata.md#why-the-ordering-is-declared-rather-than-inferred).

## Two rules, and they are the whole thing

- The **subject** of a rule must be inside the `match` of its stratum.
- The **derive** of an action must be inside the `match` of some **strictly higher** stratum.

Verse checks both at install, and neither check reads the store. `verse c -a` refuses a
charter that breaks either, and names the rule and the level.

The `match` of a stratum does two jobs. It says **what subjects this stratum owns**, and it says
what derivations the stratum accepts from below. One declaration is read from two directions.

## What a stratum may match

**An address**, with no `value` predicate. Write a union as a list:
`match: [ "gh", "linear" ]`. The address may have any length. It owns every address at it or
below it:

```tony
- {name: tip,    level: 10, match: {path: "verse-dev.drift.head"}}
- {name: ledger, level: 20, match: {path: "verse-dev.drift.finding"}}
```

`[verse-dev, drift, head]` is the address under which derivations land. A rule whose subject is
`[verse-dev, drift, head, "*"]` is inside it.

**Verse checks containment segment by segment.** The path of a stratum contains a subject when
both of these hold:

- The path of the stratum is no longer than the path of the subject.
- At each position, the segment of the stratum admits every name that the segment of the
  subject admits.

A `*` admits any name. A name admits only itself.

**A subject with an operator at a position is contained only by a `*` at that position.** For
example, `!glob "head*"` might select outside a segment named `head`, and no check at install
can say that it does not. So a rule whose subject globs a segment is not in a stratum that owns
one particular name at that segment. Strata are pairwise disjoint, so usually no other stratum
owns the rule either. Such a rule is unstratified. An unstratified rule is fine as long as it
does not derive. Put an operator below the address that a stratum names, not at one of its
segments.

Two strata are disjoint when, at some position that both paths reach, their segments admit no
common name.

A stratum refuses a `value` predicate. With one, the level would depend on a state that rules
change as they fire, and that is not a level.

## `derive` — identity, not state

Each entry of a derive names an address as `path`, and its content as `value`. The content goes
under the address, so there is no boundary to find:

```tony
derive:
- path: "verse-dev.drift.head"
  value: {sha: $subject.value.sha}
```

**A derive may reference its subject, and nothing else.** `$subject` and its parts resolve.
Verse refuses every other `$reference` at install. A [path read](glossary.md#path-read) may
reach further, under the limit that [Path reads inside a derive](#path-reads-inside-a-derive)
states.

A rule may use `on` or `when` on anything at any level. The extra firings that such a read
causes cannot change what the derive writes. The argument is in
[Why a derive reads only its subject](design/strata.md#why-a-derive-reads-only-its-subject).

**`$subject.commit` is the exception, and verse refuses it in a derive.** Its value depends on
when the firing happened, not on the subject. So a read from above does change it.

A derive that writes something new on every firing wakes the level above, and that wakes the
rule again. That is a cycle across a level, which is the one thing the ordering exists to rule
out. For the same reason, the linkage that the engine writes holds no commit.

The parts:

| reference | resolves to |
|---|---|
| `$subject` | the whole address, `"gh.issue.1"`, as a [kinded path](glossary.md#kinded-path) |
| `$subject.ref` `.name` | the address, and its last segment: the key it stands at |
| `$subject.value` | the payload of the subject **at the commit that caused the firing** |
| `$subject.commit` | the revision of that commit. Not in a derive. |

`$subject.value` reads at the causing [commit](glossary.md#commit), not at the moment the
action runs. The difference matters when a gate holds the firing for an approval. The firing
acts whenever the answer comes, and a late read would write from a state that nobody
evaluated.

The read at the causing commit costs nothing when that commit changed the subject. That is the
ordinary case: a rule about the entity that changed. A member of a fan-out that the commit did
not touch is read at that [revision](glossary.md#revision). The store answers that read by
replaying its log.

A completion [hook](glossary.md#hook) reads at the commit at which **its rule fired**. A task
that finishes wakes the hook, not a write, so the chain of the hook starts again for the cycle
throttle. But the hook reads at the revision of the firing, which the record of the task
carries. So a `derive` in an `onDone` can name what the firing was about. "Derive a release
note from what shipped" needs this. A task that the charter did not start has no such commit.
For such a task, both parts fail by name, and do not resolve to a silent zero.

The identity parts work in the strings of a spawn too (`prompt: "approve $subject.name"`),
because verse publishes them as flat bindings. `.value` does not work there, because a payload
has no text form. The top-level fields of the subject are already flat bindings there
(`$stage`).

**The identity is the engine's.** The id of the derived entity is a function of
(rule, subject). For a [fan-out](#each--when-one-answer-holds-many-facts) it is a function of
(rule, subject, key): the same identity, with the key of the element added. The engine writes
the link to the subject itself, at `verse.meta.derivedFrom`:

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

The address reads `<target>.<rule>.<subject…>`. It has three parts:

1. the address that the rule derives into;
2. the name of the rule, as one segment;
3. the segments of the subject.

The rule is in the address because the identity is (rule, subject), not the subject alone.
Without the rule, two rules that derive into one address about one subject would write the same
entity. Single-writer exists to remove that race.

The derived address is an address, not an id, so it carries the subject exactly.
`derived.feature.raise.gh.issue.1` is the entity that `raise` derived about `gh.issue.1`. It
reads back as exactly those segments. Two things follow:

- `verse data derived feature raise -paths` lists everything that this rule has derived.
- **A derived path is as long as what it is about.** A rule that reads one must say how long.
  The length follows from the subjects that the producing rule selects:
  `{path: "derived.feature.*.*.*.*"}` for a rule that derives about `gh.issue.*`.

The linkage follows the rule for a spawn, for the same reason. A derivation is about exactly
one subject. The condition decides which subject, and a charter should not have to interpolate
it. So `do.approve` can say "approve the thing this is about", and the linkage says which
thing. This holds whether or not the charter also writes `$subject` in a place of its choice.
Under its own key, a consumer's spawn can bind it flatly (`prompt: "approve $pr"`, after
`pr: $subject`).

Two consequences follow. A re-fire about the same subject writes the same address, so it is
idempotent. And **no two rules can address one derived entity**. Rule names are unique across
the charter, and the id is injective in the subject. For everything derived, this removes the
problem of [two triggers, one field](convergence.md#two-triggers-one-field).

The same property forbids rollups: fifty features cannot feed one `do.approve`. The level above
reads the *set* of features instead. That is the cost of single-writer.

### A key on a single derive

A derive may name a `key:` and not fan out:

```tony
action:
  derive:
  - path: "verse-dev.merge.train"
    key:  $subject.value.sha      # one train per tip of main
    value: {sha: $subject.value.sha}
```

The address becomes (rule, subject, key): the identity of a fan-out with one element. The key is
for a case that a plain derive cannot express: **the same subject, decided more than once.**

A merge train is that case. `git.verse.head` is one entity whose `sha` changes, so a rule that
derives a batch from it writes one address. The id of a
[gate](reference/gates.md#gates--whether-it-proceeds) is a function of (rule, gate, subject).
Without a key, the second batch lands at an address whose question is already settled. It
inherits the answer to the first batch. Nobody is asked, and nothing in the docket says so. With
the sha in the address, a new tip is a new subject, and so a new question.

The two install checks are those of the fan-out, without the check on the element:

- The key must be a **single reference**. A reference embedded in text interpolates to a
  string, and an address is not built out of prose.
- The key may read the **subject**, its state included, and nothing else.

Verse refuses `$subject.commit` in a key for the reason it refuses it in the content. A key
that depends on when the rule fired writes a new entity on every firing.

The key is part of the invariant, not a hole in it. A re-fire about the same subject computes
the same key, and writes the same bytes to the same address. That write is a no-op, and the
no-op is what lets a rule read a higher level safely.

### An `id` instead of a key

`id:` takes the same single reference, but does not compose it with the rule and the subject.
The derived entity is the address plus that value, as written. So `id: $subject.value.sha`
under `[verse-dev, merge, train]` is `verse-dev.merge.train.<sha>`, an address that another
rule can spell.

The two fields differ in what the value identifies:

| Field | Two subjects of one rule that compute the same value | The value identifies the record |
|---|---|---|
| `key` | get one entity each | only within one subject |
| `id` | share one entity | across subjects |

An entry names one of the two, never both. `!each` takes `id` in place of `key` in the same way.

## `!each` — when one answer holds many facts

One firing derives one entity. That works until a task answers with twelve findings, eleven of
them in prose. `!each` is the fan-out:

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

**`!each` is the same invariant, made general, and not an exception to it.** A plain derive may
carry no state, because its address is (rule, subject). Content fixed by the address rewrites
identically. That no-op lets a rule read a higher level safely. The address of a fan-out is
(rule, subject, key), with the key read out of the element. So the same answer still gives the
same addresses with the same bytes. One rule is under both:

> **A derive may carry state exactly when its address is a function of that same state.**

So verse checks `over`, `key` and `value` at install. Each refusal applies that sentence:

- `over` and `key` must each be a *single* reference. A reference embedded in text
  interpolates to a string, and a string gives nothing to walk or to address.
- `key` must be rooted at `$item`. Otherwise every element shares one address.
- `value` may reference `$item` and the *identity* of the subject. `$subject.commit` stays
  refused, for the reason it is refused in a plain derive.

The keys under the tag are a closed set: `sweep: true` is a typo, not payload.

Two refusals happen at run time, and both are loud on purpose:

- `over` must resolve to a **list**. A charter that wants a nested answer flattened says so in
  its answer contract, where the shape is already declared.
- An element whose `key` is empty **fails the whole firing**, and verse names the element.

To derive the other elements would lose one member from a ledger, and the ledger would read
short. A short ledger is the one failure that nobody can see.

The address flattens characters that an id may not hold (`io/github.Reflect` →
`io_github.Reflect`). When it flattens, it adds a hash of the original, so two keys that
flatten alike stay two entities. Put the readable key in the payload too. The address is for
the engine.

### It is append-only, and that is a decision

A finding that stops appearing in an answer **leaves its entity standing**. Nothing sweeps it
away.

The append-only behaviour is deliberate. A ledger needs **resolution to be a fact that
somebody wrote**: resolved, at this commit, by this change. An absence, inferred because a
model did not mention something this time, is not that fact. A sweep would silently turn such
an absence into a resolution. That is worse than a stale entity that a reader can see and
delete.

A derive that *removes* is also a different capability from a derive that loops. It opens the
ordering argument again from the other end, because a delete is not idempotent the way a write
is. Such a capability should not arrive hidden inside a fan-out.

## Path reads inside a derive

[`!get-path` and `!list-paths`](reference/action.md#get-path-and-list-paths--reaching-another-entity)
let an action copy an entity that is not its subject. They are the one thing that the ordering
must constrain. They break the fact that the section above depends on: nothing that a derive
writes can be reached from a level above it.

**A path read inside a derive must resolve at or below the stratum of the rule.** An entity at
or below that stratum is written only by rules at that level, or derived into from below it.
Rules above never write it. So the levels above still cannot change what a derive writes, and
the derive stays inert.

A read upward would break this. The entity above changes, the derive writes different bytes,
and the level above wakes. The cycle then crosses a level.

Verse checks this at install, with the same containment as the level checks. The address of
the read, up to its first segment that is not a literal name, is an address. The `match` of a
stratum must contain it. Verse also refuses a read that names an address in no stratum: with
no level, "at or below" has no answer.

None of this applies in a plain `patch`, because nothing downstream depends on a patch being
reproducible. A rule may read any level and write the result to its own subject.

A read in a condition is not constrained either, for the same reason one step further back. A
read in a condition decides which entities a rule is about, and writes nothing. So there is
nothing for a level above to change. A read in a condition is the one read that counts toward
what the rule *reads*: see
[the trigger model](reference/action.md#a-condition-may-read-too-and-that-one-does-track).

## What this earns, and what it does not

**A cascade crosses at most (number of strata − 1) levels.** Each derive strictly increases the
level, nothing writes downward, and the ordering is finite.

The bound is on the number of *crossings*, not on the number of steps. Between two crossings,
the rules of one stratum can patch their own subjects and wake each other freely.
[The cycle throttle](cycle-throttle.md) bounds that stretch, not the ordering.

**The ordering is not a convergence guarantee, and cannot become one.** A level ordering
permits recursion inside a stratum. This is the bargain of stratified Datalog. So the
`fwd`/`back` pair from [convergence](convergence.md#the-cycle-and-how-it-is-caught) still loops
if both rules are in one stratum. The throttle catches that loop at run time.

**The loop that remains goes through the world outside verse.** The effectuate stratum writes a
real approval to GitHub. The mirror observes GitHub, and the approval comes back at level 0.
That cycle closes outside the store. So it is observable, because it passes the mirror. Real
latency sets its pace, not the speed at which the store accepts writes. It is the loop that a
person would draw on a whiteboard.

"Well founded" can sound like "nothing loops". A person who watches GitHub come back around may
think it is a bug. It is not.

**Tasks are outside the ordering.** A spawned agent writes wherever its tools reach, at no
level. So the guarantee is this: *the ordering covers a charter to the extent that its writes
are declared*. `derive` moves writes across entities from "an agent did it, and nothing can analyse
it" to "the charter said it, and verse can check it". Every write that still goes through a
spawn is outside the ordering.

## Declaring nothing is a complete answer

A charter with no `stratum:` anywhere runs in one implicit stratum. Verse checks nothing, and
the throttle catches recursion inside that stratum. The guarantee grows as you declare strata.
You do not need to change the whole charter at once.

The one thing an unstratified rule may not do is `derive`. "Strictly above" has no answer for a
rule with no level. A lenient reading would give back the unchecked write across entities that
the ordering exists to remove. A rule must declare a stratum to derive.

## Installing, and the order it does not depend on

A stratum is one entity, `verse.trigger.strata.<name>`, as a rule is one entity,
`verse.trigger.rule.<name>`. For a charter installed at an address, the name of a rule has the
package as a prefix. Both are single entities for the same reason. An install writes one, and a
removal deletes one. There is no read-modify-write, so two concurrent installs cannot lose each
other's writes.

The **name addresses** a stratum, and the level orders it. A rule says `stratum: sdlc`, which
says what it means; `10` does not. Two strata may not share a level, and verse refuses this at
install. The number is the whole ordering, and a charter that gives one number twice does not
say which stratum comes first.

A stratum and a rule are separate entities, so either can arrive first. **A rule that names a
stratum that is not installed yet is held, not refused.** A [held rule](glossary.md#held-rule)
is written down, and does not fire. Verse installs it when its stratum arrives. The rule has
already passed every other check, so holding defers only the question of the ordering.

**A file produces held rules whenever it names a stratum that it does not define itself.** A
charter written to sit beside another charter does this. `verse c -a` sends the strata of a
file first, so a self-contained charter never holds a rule. A charter whose rules name the
stratum of a neighbour holds exactly those rules, until the neighbour is installed.

The install says which rules it held. Verse holds a stratum name with a spelling mistake in it
in the same way, so read this line:

```
$ verse charter -a -f review.tony
baseline: installed 0 trigger(s)
  held: rule review waits for stratum sdcl, which is not installed. It fires on nothing until that stratum is.
```

The count is the number of rules that can fire. `verse charter` lists a held rule with
`held: <stratum>`.

`verse c <address> rules <name>` shows a held rule too, with the same `held` line.
`verse status` counts the rules that can fire, so a charter of four rules with two held reports
two.

**To edit a level, re-apply the file.** A stratum already installed exactly as declared is a
no-op. A stratum that differs is replaced, as a rule of the same name is. The file is what the
operator means, and re-applying an edited file is how a charter changes.

Verse checks the change against every rule that stands in that charter. Those rules were
checked against the level when they were installed, so they face the ordering again. A rule
that no longer fits refuses the *change*, and names itself. No rule stays at a level that verse
did not check it against.

**A file is applied as one step.** `verse c -a` sends the part of a file that belongs to one
package in one request. A refusal anywhere restores what stood before it. The declaration, the
levels and the rules all become the charter, or none of them do.

A file sent one document at a time could leave the levels installed and a rule refused, while
the command reports a failure. The charter would then be in a state that nobody wrote down. It
would hold a level that the corrected file could not replace.

**Verse refuses to remove a stratum while rules still name it.** Those rules were checked
against a pattern that would no longer exist, and nothing runs the check again. They would go
on firing unstratified, after being told that they were ordered. Remove the rules first, or move
them.

```
$ verse charter             # the ordering, by level
$ verse c rules sdlc
$ verse c -r strata sdlc # refused while rules live there
```

## Corners

**A misspelled field is named, down to a depth.** gomap ignores what it does not recognise, so
an unknown field decodes silently. So `startum: sdlc` would install into the implicit stratum
and report success. It would discard the one field that says which guarantees apply.
`verse c -a` refuses it. It also refuses an unknown field in a `condition`, in an `action`, or
in a sequence element or completion hook below an action. The check stops where the shape is no
longer the charter's own. A `spawn` is a launch spec, and a `patch` or `derive` payload holds
the author's own keys.

**How a derived entity leaves is unspecified.** Nothing removes derived entities unless a
charter says to. The [subject-side protection](trigger-model.md#lifecycle-how-an-entity-leaves)
does not extend to them. A derive addresses its target and does not select it, so the vanishing
marker is not in its path.

**The throttle does not reset at a crossing.** It could, because a chain never returns to a
stratum that it has left. With a reset, the throttle would catch a loop confined to one stratum
after the rule count of that stratum, not after the count of the whole charter. That tightens
how fast verse catches a runaway, not the worst case. It is not built.

## Why it is this way

The reasons for this design are in [strata.md](design/strata.md), in the repository.

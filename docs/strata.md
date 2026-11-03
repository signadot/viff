# Strata

A `patch` in a [rule](glossary.md#rule) writes one place: the [subject](glossary.md#subject)
of its condition. With `patch` alone, a rule that wants an entity at another address must spawn
an agent and let the agent write it. Nothing at install can see that write or check it.

[`derive`](glossary.md#derive) is that write, said in the [charter](glossary.md#charter)
instead. A charter that can write anywhere can write in a circle, so `derive` comes with a
limit: the **stratum**. A [stratum](glossary.md#stratum) is a named part of the store with a
declared level. The [level of a stratum](glossary.md#level-of-a-stratum) is a number that
orders the strata of one charter. Each rule lives in one stratum, and a derive only writes to a
higher level.

```tony
strata:
- name: observe
  level: 0
  match: [ "gh", "linear" ]
- name: sdlc
  level: 10
  match: "signadot"
- name: effectuate
  level: 100
  match: "do"
rules:
- name: urgent-label
  stratum: observe
  condition:
    subject:
      path: "gh.issue.*"
      value: { label: urgent }
  action:
    patch: { triaged: true }
    derive:
    - path: "signadot.feature"
      id: .[subject.name]            # signadot.feature.1, for gh.issue.1
      value:
        priority: urgent

- name: request-approval
  stratum: sdlc
  condition:
    subject:
      path: "signadot.feature.*"
      value: { priority: urgent }
  action:
    patch: { stage: awaiting-approval }
    derive:
    - path: "do.approve"
      value:
        feature: .[subject.ref]
```

`verse c -a -f charter.tony` installs the strata and the rules. It installs the strata first,
whatever their order in the file.

The levels are numbers with gaps, so you can insert a level later without renumbering the others.

Verse does not work out the ordering from the rules; you declare it. The reason is in
[Why the ordering is declared rather than inferred](design/strata.md#why-the-ordering-is-declared-rather-than-inferred).

## Two rules, and they are the whole thing

- The **subject** of a rule must be inside the `match` of its stratum.
- The **derive** of an action must be inside the `match` of some **strictly higher** stratum.

Verse checks both at install, without reading the store. `verse c -a` refuses a charter that
breaks either, and names the rule and the level.

The `match` of a stratum does two jobs. It says **what subjects the stratum owns**, and what
derives it accepts from below.

## What a stratum may match

**An address**, with no `value` predicate. Write a union as a list:
`match: [ "gh", "linear" ]`. The address may have any length. It owns every address at it or
below it. Two of the strata of [comment drift](groundskeeper.md):

```tony
- name: range
  level: 20
  match:
    path: "repos.verse.drift.range"
- name: ledger
  level: 30
  match:
    path: "repos.verse.drift.finding"
```

`[repos, verse, drift, range]` is the address where the ranges are derived. A rule whose subject
is `[repos, verse, drift, range, "*"]` is inside it.

**Verse checks containment segment by segment.** The path of a stratum contains a subject when
both of these hold:

- The path of the stratum is no longer than the path of the subject.
- At each position, the segment of the stratum admits every name that the segment of the
  subject admits.

A `*` admits any name. A name admits only itself.

**A subject with an operator at a position is contained only by a `*` at that position.** For
example, `!glob "range*"` might select outside a segment named `range`, and no check at install
can say that it does not. So a rule whose subject globs a segment is not in a stratum that owns
one name at that segment. Strata do not overlap, so usually no other stratum owns the rule
either, and the rule is unstratified. That is fine as long as it does not derive. Put an operator
below the address a stratum names, not at one of its segments.

Two strata are disjoint when, at some position that both paths reach, their segments admit no
common name.

A stratum refuses a `value` predicate. With one, the level would depend on state that rules
change as they fire, and a level must not change.

## `derive`: identity, not state

Each entry of a derive names an address as `path`, and its content as `value`. The content goes
under the address:

```tony
derive:
- path: "repos.verse.drift.range"
  value: {from: .[subject.value.sha], to: .[subject.value.covering]}
```

**A derive may read its subject, and nothing else.** An expression in a derive may name
`subject` and its parts. Verse refuses an expression that reads any other name at install. A [path read](glossary.md#path-read) may
reach further, under the limit that [Path reads inside a derive](#path-reads-inside-a-derive)
states.

A rule may use `on` or `when` on anything at any level. The extra firings such a read causes
cannot change what the derive writes. The argument is in
[Why a derive reads only its subject](design/strata.md#why-a-derive-reads-only-its-subject).

**Verse refuses `subject.commit` in a derive.** Its value depends on when the firing happened,
not on the subject, so a read from above does change it.

A derive that writes something new on every firing wakes the level above, and that wakes the
rule again. That is a cycle across levels, the one thing the ordering is there to prevent. For
the same reason, the link the engine writes holds no commit.

`subject` and its parts are [bindings](reference/action.md#bindings) of an action.
`subject.value` reads at the [commit](glossary.md#commit) that caused the firing.

That read costs nothing when the commit changed the subject, which is the usual case: a rule
about the entity that changed. A member of a fan-out that the commit did not touch is read at
that [revision](glossary.md#revision), and the store answers it by replaying its log.

A completion [hook](glossary.md#hook) reads at the commit at which **its rule fired**. So a
`derive` in an `onDone` can name what the firing was about. "Derive a release note from what
shipped" needs this.

**The engine decides the identity.** The derived entity's address is a function of
(rule, subject). For a [fan-out](#each-when-one-answer-holds-many-facts) it is a function of
(rule, subject, key). The engine writes the link to the subject itself, at
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

The address reads `<target>.<rule>.<subject…>`
([derive](reference/action.md#derive)). The rule is in the address because the identity is
(rule, subject), not the subject alone. Without it, two rules that derive into one place about
one subject would write the same entity. Each derived entity has one writer.

The derived address carries the subject exactly. `derived.feature.raise.gh.issue.1` is the
entity `raise` derived about `gh.issue.1`, and it reads back as exactly those segments. So
`verse data derived feature raise -paths` lists everything this rule has derived.

The link works the same way for a spawn, for the same reason. A derive is about exactly one
subject. The condition decides which, and the charter does not have to write it in. So
`do.approve` can say "approve the thing this is about", and the link says which thing. A
charter may also write the subject into a field of its own (`pr: .[subject.ref]`), and a spawn
downstream can then use it (`prompt: "approve $[pr]"`).

Two things follow. A re-fire about the same subject writes the same address, so it changes
nothing. And **no two rules can address one derived entity**: rule names are unique in a
charter, and each subject gives its own address. So nothing derived has the problem of
[two triggers, one field](convergence.md#two-triggers-one-field).

The same property rules out rollups: fifty features cannot feed one `do.approve`. The level
above reads the *set* of features instead. That is the cost of one writer per entity.

### A key on a single derive

A derive may name a `key:` and not fan out:

```tony
action:
  derive:
  - path: "repos.verse.merge.train"
    key:  .[subject.value.sha]      # one train per tip of main
    value: {sha: .[subject.value.sha]}
```

The address becomes (rule, subject, key), like a fan-out with one element. The key is for a case
a plain derive cannot express: **the same subject, decided more than once.**

A merge train is that case. `git.verse.head` is one entity whose `sha` changes, so a rule that
derives a batch from it writes one address. The id of a
[gate](reference/gates.md#gates-whether-it-proceeds) is a function of (rule, gate, subject).
Without a key, the second batch lands at an address whose question is already settled, and it
takes the first batch's answer. Nobody is asked, and nothing on the docket says so. With the sha
in the address, a new tip is a new subject, and so a new question.

The two install checks are those of the fan-out, without the check on the element:

- The key must be **one expression and nothing else**, a whole `.[…]`. A `$[…]` inside text
  becomes a string, and an address is not built out of text.
- The key may read the **subject**, its state included, and nothing else.

Verse refuses `subject.commit` in a key, as in the content: a key that depends on when the rule
fired writes a new entity on every firing.

The key keeps the rule, not an exception to it. A re-fire about the same subject computes the
same key, and writes the same bytes to the same address. That write changes nothing, and that is
what lets a rule read a higher level safely.

`id:` takes a whole `.[…]` too, or a literal name, but puts the entity at the address plus that
value, with no rule and no subject. [derive](reference/action.md#derive) compares `id:` and `key:`.

## `!each`: when one answer holds many facts

One firing derives one entity. That is not enough when a task answers with twelve findings.
`!each` is the fan-out:

```tony
action:
  derive:
  - path: "repos.verse.drift.finding"
    value: !each
      over:  .[data.findings]     # a list to walk; each element binds item
      key:   .[item.symbol]       # the address, read out of the element
      value:
        symbol: .[item.symbol]
        says:   .[item.says]
        about:  .[subject.name]
```

**`!each` follows the same rule as a plain derive.** A plain derive carries no state, because its
address is (rule, subject), so a re-fire writes the same bytes and changes nothing. The address of
a fan-out is (rule, subject, key), with the key read out of the element, so the same answer still
gives the same addresses with the same bytes. One sentence covers both:

> **A derive may carry state exactly when its address is a function of that same state.**

Verse checks `over`, `key` and `value` at install. Each refusal applies that sentence:

- `over` and `key` must each be *one* expression and nothing else, a whole `.[…]`. A `$[…]`
  inside text becomes a string, and a string gives nothing to walk or to address.
- `key` must read the element, `item`, and nothing else. Otherwise every element shares one
  address. It may compose the element's fields, `'.[item.symbol + "-" + item.kind]'`.
- `value` may read `item` and the subject. `subject.commit` stays refused, for the reason it is
  refused in a plain derive.

The keys under the tag are a closed set: verse refuses any other, such as `sweep: true`.

Two refusals happen at run time, and both are loud on purpose:

- `over` must resolve to a **list**. A charter that wants a nested answer flattened says so in
  its answer contract, where the shape is already declared.
- An element whose `key` is empty **fails the whole firing**, and verse names the element.

Deriving the other elements would leave one out of the ledger, and a ledger that is short
without saying so is a failure nobody sees.

The address replaces characters an id may not hold (`io/github.Reflect` → `io_github.Reflect`).
When it does, it adds a hash of the original, so two keys that look alike after the change stay
two entities. Put the readable key in the payload too; the address is for the engine.

### It is append-only, and that is a decision

A finding that stops appearing in an answer **leaves its entity standing**. Nothing sweeps it
away.

A ledger needs **resolution to be a fact somebody wrote**: resolved, at this commit, by this
change. A finding a model did not mention this time is not that fact. A sweep would turn that
absence into a resolution without saying so, which is worse than a stale entity a reader can see
and delete.

A derive that *removes* is also a different thing from a derive that writes. A delete does not
repeat harmlessly the way a write does, so it would need the ordering argument again. It should
not arrive hidden inside a fan-out.

## Path reads inside a derive

[`!get-path` and `!list-paths`](reference/action.md#get-path-and-list-paths-reaching-other-data)
let an action copy an entity that is not its subject. They are the one thing the ordering must
limit, because the section above depends on this: nothing a derive writes is reached from a
level above it.

**A path read inside a derive must resolve at or below the stratum of the rule.** An entity at
or below that stratum is written only by rules at that level, or derived from below. Rules above
never write it. So the levels above still cannot change what a derive writes.

A read upward would break this: the entity above changes, the derive writes different bytes, and
the level above wakes. The cycle then crosses a level.

Verse checks this at install, with the same containment as the level checks. The address of the
read, up to its first segment that is not a literal name, must be inside the `match` of a
stratum. Verse also refuses a read of an address in no stratum: with no level, "at or below" has
no answer.

None of this applies to a plain `patch`, because nothing downstream depends on a patch writing
the same bytes each time. A rule may read any level and write the result to its own subject.

A read in a condition is not limited either. It decides which entities a rule is about, and
writes nothing, so there is nothing for a level above to change. A read in a condition is the one
read that counts toward what the rule *reads*: see
[the action of a rule](reference/action.md#a-condition-may-read-too-and-that-one-does-track).

## What this earns, and what it does not

**A cascade crosses at most (number of strata − 1) levels.** Each derive strictly increases the
level, nothing writes downward, and the ordering is finite.

The limit is on the number of *crossings*, not the number of steps. Between two crossings, the
rules of one stratum can patch their own subjects and wake each other freely.
[The cycle throttle](cycle-throttle.md) limits that, not the ordering.

**The ordering does not promise that a charter settles, and cannot.** A level ordering allows
recursion inside a stratum, as stratified Datalog does. So the `fwd`/`back` pair from
[convergence](convergence.md#the-cycle-and-how-it-is-caught) still loops if both rules are in one
stratum. The throttle catches that loop at run time.

**The loop that remains goes through the world outside verse.** A rule in the effectuate stratum
writes an approval to the github [iod](glossary.md#iod)'s out place, and the iod makes it on
GitHub. The iod also writes GitHub into the store, so the approval comes back at level 0. That
cycle closes outside the store. You can see it, because it passes through the iod, and real
latency sets its pace, not the speed of the store.

A strict ordering does not mean that nothing loops. When you watch GitHub come back around, that
is not a bug.

**Tasks are outside the ordering.** A spawned agent writes wherever its tools reach, at no
level. So *the ordering covers a charter as far as its writes are declared*. `derive` turns a
write across entities from "an agent did it, and nothing can check it" into "the charter said it,
and verse checks it". Every write that still goes through a spawn is outside the ordering.

## Declaring nothing is a complete answer

A charter with no `stratum:` anywhere runs in one implicit stratum. Verse checks nothing, and the
throttle catches recursion. Each stratum you declare adds checks, so you can add them a few at a
time.

The one thing an unstratified rule may not do is `derive`. "Strictly above" has no answer for a
rule with no level. A rule must declare a stratum to derive.

## Installing, and the order it does not depend on

A stratum is one entity, `verse.trigger.strata.<name>`, as a rule is one entity,
`verse.trigger.rule.<name>`. For a charter installed at an address, the names have the package as
a prefix. An install writes one entity and a removal deletes one, so two installs at once cannot
lose each other's writes.

The **name addresses** a stratum, and the level orders it. A rule says `stratum: sdlc`, which
says what it means; `10` does not. Two strata may not share a level, and verse refuses that at
install: the number is the whole ordering, and two equal numbers do not say which comes first.

A stratum and a rule are separate entities, so either can arrive first. **A rule that names a
stratum that is not installed is held, not refused.** A [held rule](glossary.md#held-rule) is
stored and does not fire. Verse arms it when its stratum arrives. The rule has passed every
other check, so only the ordering waits. A rule restored with a spawn no runner offers yet is
held the same way, for the runner, since a restart reads the charter before the runners answer.
An install refuses such a spawn.

**A file holds rules when it names a stratum it does not define.** A charter written to sit
beside another charter does this. `verse c -a` sends a file's strata first, so a self-contained
charter never holds a rule.

The install says which rules it held, and what each waits for. A misspelled stratum name is
held the same way, so read this line:

```
$ verse charter -a -f review.tony
baseline: installed 0 trigger(s)
  held: rule review is waiting for stratum sdcl. It fires on nothing until then.
```

The count is the number of rules accepted to fire. A rule whose watch is still opening counts,
and the install says `arming:` for it; a held rule does not. `verse charter` lists a held rule
with `held: <what it waits for>`.

`verse c <address> rules <name>` shows a held rule too, with the same `held` line.
`verse status` counts the rules as the install does, so a charter of four rules with two held
reports two, and names the two under `pending` with what each waits for. A rule that passed at
install and fails now, because the runner that offered its spawn is gone, say, is held the same
way: it stands, `pending` says `refused: …` for it, and it arms when the world lets it.

**To edit a level, apply the file again.** A stratum installed exactly as declared is left alone.
A stratum that differs is replaced, as a rule of the same name is.

Verse checks the change against every rule in that charter, because each was checked against the
old level. A rule that no longer fits refuses the *change*, and names itself. No rule stays at a
level verse did not check it against.

**A file is applied as one step.** `verse c -a` sends a file, or a directory of them, in one
request. A refusal anywhere restores what stood before. The declaration, the levels and the rules
all become the charter, or none of them do.

**Verse refuses to remove a stratum while rules still name it.** Those rules were checked against
a pattern that would no longer exist, and they would go on firing with no level. Remove or move
the rules first.

```
$ verse charter             # the ordering, by level
$ verse c rules sdlc
$ verse c -r strata sdlc # refused while rules live there
```

## Corners

**A misspelled field is named, down to a depth.** Without a check, `startum: sdlc` would install
into the implicit stratum and report success, losing the one field that says which checks apply.
`verse c -a` refuses it. It also refuses an unknown field in a `condition`, in an `action`, or in
a sequence element or completion hook below an action. The check stops where the shape is no
longer the charter's own: inside a `spawn`, and inside a `patch` or `derive` payload, which holds
the author's own keys ([what an install does not check](reference/charter.md#what-an-install-does-not-check)).

**Nothing removes a derived entity unless a charter says to.** The
[protection a subject has](trigger-model.md#lifecycle-how-data-leaves) does not extend to
it. A derive addresses its target and does not select it, so the vanishing marker is not in its
way.

**The throttle does not reset at a crossing.** It could, because a chain never returns to a
stratum it has left. With a reset, the throttle would catch a loop inside one stratum after that
stratum's rule count, not the whole charter's. That would make verse catch a runaway sooner, not
change the worst case. Verse does not do this.

## Why it is this way

The reasons for this design are in [strata.md](design/strata.md), in the repository.

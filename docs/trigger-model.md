# The trigger model

One noun and two grammars carry the whole model. An entity is a node at an address — a path in
the tree, as long as the thing writing it makes it: the git source reflects a commit under its
repository (`git.acme.commit.9fe1`), verse keeps a task under `verse.task.instance`, and a package
keeps its findings under its own address (`verse-dev.drift.finding.abc`) — carrying a small
payload. A match names entities at an instant;
a formula is a match asked of the verse as it moves. Every trigger concept, `when` and `subject`
and flows, lands on one of those two, and a gate on neither: it asks somebody. Knowing which is
the thing to be unambiguous about.

This is also the field-by-field reference for a charter rule. The [tutorial](tutorial.md) installs
one and drives it; here is what each field means.

The match vocabulary is [tony](https://signadot.github.io/tony-format/)'s rather than verse's. The
same operators serve matching, patching and diffing there, which is the argument made in
[Structured matching, patching and diffing done right](https://dev.to/scott_cotton_dc9ce3e7e632/structured-matching-patching-and-diffing-done-right-22ci).

## Two layers: point-in-time matches, and matches asked over time

A *match* is a point-in-time tony selector over the entity-verse: which entities satisfy this
right now. There is no time in it.

A *formula* is a match asked of the verse as it moves: at two revisions for a cause, at one for a
condition. Nothing composes it — there is no `until`, no `quorum`, no `deadline` in a condition.
A formula's whole content is its match.

| concept | grammar | what it expresses |
|---|---|---|
| `subject` | match — point-in-time | the entities a firing is *about*, selected at the commit. Required |
| flow | match — point-in-time | which live tasks to steer, by their subject |
| `on` | formula — a match at two revisions | the **cause**: fire at the commit where this first holds, having not held before |
| `when` | formula — a match at one revision | the **condition**: this must hold at that commit for the firing to proceed |
| gate | neither — a question posed to somebody | proceed when whoever answers settles it: one yes, a threshold of them, or a clock's default |

The cut is simple. Name entities at an instant and you want a match; ask whether one holds as
the verse moves and you want a formula. `subject` and flow-addressing are point-in-time, since a
subject is selected at the commit a firing happens at and never accumulated, so they are plain
matches.

`on` and `when` are asked of the verse's evolution, so they are formulas. A gate is neither: it
asks somebody, and what it says is how the answer settles ([gates](#gates--whether-it-proceeds)).
`on` and `when` are the same predicate asked at different moments — `on` at C-1 and C, firing on ¬P → P, and `when` at
C, gating — which is why they share a grammar and a type. Cause and condition, told apart by edge
and level.

## The trigger

A rule has four parts: a name, the condition that causes it, the gates that guard it, and the
action that fires. A fifth is optional: `stratum:`, the declared level it lives in, which
constrains what its subject may be and where a `derive` may land ([strata](strata.md)). Omitted,
it is unconstrained, which is what every rule here means.

```tony
- name: review
  # what it is about
  condition:
    subject: {path: [demo, pr, *], value: { stage: open }}
  # the guard
  gates:
  - { name: approval, mode: ask }
  # the consequence
  action:
    patch: { stage: reviewing }
    spawn: { driver: scripted, vector: auto, prompt: "echo reviewing $subject" }
    onDone:
      patch: { stage: reviewed }
```

```go
type Trigger struct {
    Name      string
    Condition Condition   // the cause: what it is about, and what fires it
    Gates     []Gate      // the guard — conjunctive: all must resolve to proceed
    Action    Action      // patch / spawn — declarative data, no Go closure
}

// Condition is the ECA "cause": what fires, what must hold, and what it is about.
type Condition struct {
    Subject Match     // REQUIRED, point-in-time — the entities this firing acts on
    On      *Formula  // optional — the CAUSE: a rising edge of a store predicate
    When    *Formula  // optional — the CONDITION: must hold at the same commit
}
```

## The rule

One sentence, and everything else in this document follows from it:

> At each commit **C**, for each trigger: if `on` is vacuous or **¬on(C-1) ∧ on(C)**, and
> `when` holds at **C**, fire once for **every entity `subject` selects at C**.

Three things are worth reading off it before anything else.

**A firing is about one entity.** N selected members are N firings, each with its own gate
question and its own spawn id. There is no firing "about a set".

**Everything is read at one snapshot.** The condition and the selection both come from the store as
it stood at C, so a firing cannot act on a member under a condition that had already stopped
holding. The edge is C-1 against C rather than "what the engine saw last time", which is what makes
a replayed commit — a catch-up, a restart — reach the same verdict as the live one.

**A vacuous `on` is the ordinary shape.** The rule above has no `on`, so it is considered at every
commit touching anything it reads. What ends it is the charter rather than the dispatcher. An action
whose write takes its subject out of the selection fires once and then selects nothing, and one that
writes the same value again produces no commit and so raises no next evaluation. Latch, or run at
every commit; a spawning action that cannot latch is refused at install.

A trigger is fully declarative, so a charter can be installed over the wire, persisted, or reflected
as an entity, and the engine interprets it directly. There is no compiled form.

### A charter can be installed at an address

A charter installed at an address is a **package**. It runs in an engine of its own, and it may
write only inside that address. The address is where the install points, either the positional
path or the file's own `at:` declaration, and installing takes it: the address gets an owner
record naming the installer, what the package derives beneath it is owned through that record,
and an install at an address somebody else owns is refused ([perms.md](perms.md)).

```sh
verse c -a notes -f notes-charter.tony    # its rules write under notes.*
verse c -a -f charter.tony                # baseline: may write anywhere
```

That is what lets two groups keep rules in one verse without arranging not to collide. Isolation by
convention holds until somebody edits a rule. A package's holds because the confinement is a
property of the *engine* rather than of the rules currently installed in it: a `derive` landing
outside does not install, and a write that resolves outside at fire time is refused, which is the
half that matters, since a derive's address can be templated.

**A rule may select anywhere it only reads.** The subject is a write when the action patches it and
a read otherwise, so a package's rule can select a source and derive from it, which is what most of
them do:

```tony
at: drift                        # where this package writes — its engine is confined to it
reads: [git]                   # …and what it may read besides
---
- name: reflected           # the level the source sits at
  level: 0
  match: {path: [git]}
- name: ours
  level: 10
  match: {path: [drift]}
- name: head-moved          # in the `drift` package
  stratum: reflected
  condition: {subject: git.verse.head}    # a source: read, never written
  action: {derive: [{path: [drift, head], value: {repo: $subject.value.repo}}]}
```

Two packages both reacting to `git.*` is the ordinary arrangement, not an exotic one. What each
may do about it is write in its own address.

**Reads are declared**, which is the `reads:` line above. A package reads inside its own address and
whatever it names, and nothing else. An undeclared read does not install, and the refusal says
which line to add:

```
rule "note-the-head" reads git.verse.*, which notes.* does not declare: a charter installed
at an address reads inside it and what it declares, so that the graph over packages can be
checked for a cycle. Add it: `reads: [git.verse]` (this package reads nothing)
```

An absent `reads:` is not "everything". It is nothing: a package with no declaration reads only
inside itself. That is the safe default and the strict one, since a package that reads nothing
outside cannot interfere with anybody, and it is what makes the graph total, because every edge is
written down.

One file may hold several packages. A `---` document carrying `at:` opens one, and everything after
it belongs to that package until the next declaration. `docs/working/comment-drift.tony` is two, a
`drift` package that reviews and a `merge` package that gates what the review delivers, installed
by one `verse c -a -file`, all of them or none. Each package goes in **as one step**: its
declaration, its levels and its rules install together or not at all, so a refusal anywhere puts
back what stood before it (see
[strata.md](strata.md#installing-and-the-order-it-does-not-depend-on)).

**Its levels are its own**, and so are its records. A package's rules and strata are addressed by
package (`verse.trigger.rule."verse-dev.drift:head-moved"`), so two packages may each declare a `reflected`
level without one overwriting the other, which is what comment-drift's two halves need, since both
read the git source and both want a bottom level for it. They are not confined: a package's lowest
level owns the place it *reads*, and its rules select from there and derive above it. What makes
per-package levels coherent is the write confinement rather than a second check on the strata. With
no cross-package write there is no cross-package derive, so there is no cross-package ordering to
define, and two packages may both declare a level 10 and mean nothing to each other.

**The reads graph is acyclic.** `a` reading what `b` writes while `b` reads what `a` writes is a
feedback loop neither charter's author can see in their own file, and nothing else catches it. The
level discipline is per package, so a cross-package derive has no ordering to violate, and the
cycle throttle bounds a chain within one engine. So the declaration is also a graph — P → Q when
what P reads meets what Q may write, which is Q's address — and a cycle is refused at install,
showing the loop:

```
reads: this would close a cycle between packages: drift → merge → drift — one reads what
the next writes, all the way round, so a write on any of them can wake the others and come
back. A package graph is acyclic: break the loop, or fold the two halves into one package
```

Declaring a read of an address no package is installed at adds no edge. `git` is a source's place,
and reading a source is not depending on a package, which is why two packages both reading it — the
arrangement above — is not a loop.

**What it does not cover** is baseline. The unpackaged charter may write anywhere, so a loop
through it is not a loop between packages and this cannot see it, which is the same reason baseline
is unconfined in what it writes.

`verse charter` reads the charter at an address, the way `verse data` reads the verse at one. The
charter is one document with a place for every package, so "what is installed here" is the same
question at every depth, and every node answers the same shape.

```sh
verse charter                  # the whole charter
verse charter drift            # the `drift` package and anything under it
verse charter demo pr          # …one deeper: the package at demo.pr
verse charter drift -depth 0   # …that address alone, without what is under it
```

```sh
verse charter -depth 1
# writes: anywhere            ← baseline: no address, so it may write anywhere
# rules: []
# children:
#   drift:
#     writes: drift.*
#     reads: [git]
#     rules: [ … the three rules, in full … ]
#     strata: [ … ]
#   merge:
#     writes: merge.*
#     reads: [git, drift]
#     rules: [ … ]
```

That is comment-drift installed: the review at `drift`, the gate at `merge` reading it, and a
baseline nobody has put anything in. A package at `demo.pr` nests under a `demo` that holds only
`children:`, since nothing is installed at `demo` itself.

It prints the rules themselves rather than their names. This is the program the verse is running,
and a listing of names would leave what it *does* to a second command per rule. `-depth 0` is how
it gets short. One rule by name is still one command:

```sh
verse charter merge             # that package alone, rules and all
verse charter merge -depth 0    # …without whatever is installed under it
verse c drift rules merge-proposal
```

A package comes off whole, too: its rules, its levels, what it reads, and the engine running them.
What it *wrote* stays, because a charter's writes are the world's, and `verse data -rm` is how
those go.

```sh
verse c -r merge
verse c -R demo                          # …and every charter under that address
```

**Two more things it does not confine**, stated because the boundary is easy to overstate. A spawn:
a task acts in the world under its `grant`, and store confinement is not world confinement (see
[provider selection](provider-selection.md)). And verse's own records — a task's mirror, a posed
question — which the engine writes into
[`verse`'s own part of the tree](reference/entities.md), where no charter may be installed and no
charter may write.

A package is the same shape as a `verse connect` scope, one axis over. A scope says whose *copy* of
the verse a charter acts in; an address says which *part* of the verse it acts on. Both stamp their
records so a restore takes only its own. Where a scope waits for its dev to reconnect, because its
layer cannot be enumerated, a package comes back at every start, because its address is a stamp
anyone can list.

### The charter is durable

Installing a trigger writes it into the verse as `verse.trigger.rule.<name>`, or
`verse.trigger.rule.<package>:<name>` for a package, so two of them may use one name. Removing one
deletes that entity, and a start reads them back before anything observes. So the program a verse
comes back up running is the one it went down with.

The record holds the rule as `spec`, the `scope` and the package address `at` it was installed in
when it has them, and `by`: who installed it — the principal the install acted as, and
`system:charter` for the charter the deployment names. A restore reinstalls the rule as `by`, and a
charter file refused part way puts a replaced rule back as its installer rather than as the system.

Everything else a restart needs is durable in the same way. A task is mirrored, a sequence's
position is a fact on its subject, a claim is a lease in the store, an open question is a record
with its ballots on it and an open selection a channel with its beside it, and resume re-poses the
one and rehydrates a chain's hooks and the other's awaiter from them.
The program has to be durable too, or a verse comes back with its work in progress intact and
nothing left to react to it.

One entity per rule, because that is what the operations already are. An install writes one, a
removal deletes one, and neither reads the others, so two installs cannot lose each other, and a
rule is addressable (`verse data verse trigger rule review`).

**A rule is stored as structure, under the `!raw` escape.** A write is a tony *patch*, the property
that makes the store good at everything else, so operator tags in a payload are *executed* by the
store rather than stored by it. A charter is full of them: `path: [demo, pr, !glob "hotfix-*"]`,
`patch: {tmp: !delete null}`. Stored as bare structure, a rule is not one that reads back wrong. It
is one that cannot be read back at all, because the write leaves an entity whose own patch log does
not apply (`glob patching "null" gave cannot patch with glob operation`).

`!raw` says *this tag is data*. The subtree is stored verbatim, nothing beneath it is interpreted
at any depth, and the escape itself is consumed, so the rule lands carrying its own tags.
`entity.Raw` is that wrap on the way in. A rule read back is ordinary data, which is why putting one
back means escaping it again — `entity.AsData`, which is what `entity.Modify` does for its callers,
since a record that holds a document would otherwise execute its own operators the moment anything
else about it changed. Raw also *replaces* rather than merging, so re-installing a rule cannot leave
a field of the version before it standing in the version after.

The gain is that the record is the rule. `verse data verse trigger rule review` shows a document to
read field by field, a charter change is an observable fact about a shape rather than about a
string, and nothing has to re-parse to ask a question about it.

**A rule keeps the comments it was written with**, so the record carries the reasons and not only
the shape — the example above reads back with all three of its notes in place.

A comment attaches to the *value* it is written about, above it or beside it on the same line, and
something has to carry it: the value's own type, the field holding it, or the codec of whatever it
sits inside. Between them they cover a charter:

| a comment on | kept by |
|---|---|
| the rule, `action:`, `gating:`, `spawn:`, a gate entry | the value's own type |
| a `patch:` or `derive:` body | nothing needed — the value is stored as-is |
| `name:`, `stratum:`, `condition:`, `gates:`, `seq:`, a gate's `name:`/`mode:` | the *field* holding it — which is how a value whose own type cannot carry one still keeps it |
| `subject:`, `on:`, `when:` | Condition's own codec, which is hand-written and so carries them by hand |

The two positions are one question asked twice, and both are carried: `name: review # the short
name` reads back as it was written, as `# what this rule is for` above it does.

The exception is the fields of a spawn: `driver:`, `prompt:`, `runner:` and the rest. A comment
above or beside `spawn:` is kept, and one written on a field inside it is not.

**A comment above a container and one above its first entry are the same comment.** A block list
begins at its first element and an object at its first field, so both head the container, they
merge, and they come back together above it. Everything after the first keeps its own. This is
why a comment above `subject:` reads back above the condition when `subject:` is written first,
and beside the field when it is not — the text survives either way, and only its carrier
changes.

```tony
gates:
# both of these merge onto the list…
- { name: approval, mode: ask }
# …while this one stays with its entry
- { name: security, mode: ask }
```

The same holds one level out, for a charter file that is a list of rules: a comment above the
first rule belongs to the file. Write it inside the item to attach it to the rule.

```tony
- # what this rule is for
  name: review
```

Restoring a rule is an ordinary install, which means it **replays**: a subtree watch is
level-triggered at the open, so a restored trigger acts on what already matches it — including
what arrived while verse was down. A restart is then just an install over a verse that has
moved on, which is the same property described above, and the same reason to write a latch
rather than an edge.

**There are no goals.** A goal would be a target over this substrate: a `doneWhen` witness, a
deadline fail-latch, a live `waitingOn`, and owned triggers reaped on a terminal outcome, durable
the same way a rule is. Every use case tried against that definition falls short of it, and it
would cost a top-level concept in a model whose whole claim is that it is small.

A **scoped charter** (`verse c -a -scope`) is restored the same way, when the scope's engine is
next built, which is on demand and so in practice when the dev reconnects. Its record carries
`scope: <name>`, which is what makes it restorable at all: a scoped store reads baseline layered
underneath, so a listing in a scope returns baseline's rules and the scope's own, and only the
record says which is which. Baseline's are passed over, because a scope runs what was installed
into it (see [connect](reference/connect.md#the-charter-a-scope-runs)).

What a restart still cannot do is bring back a scope nobody has reconnected to: enumerating
the scopes that have a charter needs a scope to be a listable thing, and it is not one yet.

The charter is an observable place like any other, so a trigger can watch it
(`subject: {path: [verse, trigger, rule, "*"]}`). It has to *name* it: `verse.trigger.*` joins
`verse.task.*` as the engine's own machinery, and a rule reaches either only with a path that begins
with `verse`, so the loop cannot ignite on itself. Meta-observation stays possible, and stays
deliberate. The perms mirror (`verse.perms`) is one
step further: it is the system's, held out to nobody, so only the system's own charter can
read it, and a rule of anyone else's naming it is refused at install rather than installed to
match nothing ([perms.md](perms.md)). A rule reads as its owner, all the way down.

## `subject` — what the firing is about, and required

`subject` is a point-in-time `Match` — never a formula. It says what a firing acts on, selected
once, from the store as it stood at the commit. There is no `until` or `deadline` here: a
subject is *selected*, not *awaited*.

It is the one field a condition cannot leave out, because nothing else in a trigger can answer for
it. The action has no target of its own, and a `when` need not pin one place: `{path: [demo, *, *]}`
spans everything two segments under `demo`, and a disjunction such as `!or [a.b.c, {path: [d, e, *]}]` makes it plain.
"The entity the when matched" answers *which entity fired* when the question is *what is this rule
about*, and a rule that acts on whichever disjunct happened to fire is not a rule anyone wrote
down. So the subject is always stated, and everything else in the trigger is read relative to it.

It resolves in one of two ways, both the same match grammar:

| subject | resolves to | example |
|---|---|---|
| an address naming one entity (`demo.pr.1`) | that entity, whether or not it exists — an action's `patch` *creates* it | publishing a fact nothing has written yet |
| a pattern — a path with `*` or an operator in it (`demo.pr.*`), with an optional payload predicate | every matching entity in the store, at that commit | every reviewed PR |

A subject is always a *selection*, never "the entity that changed". A rule is a statement about the
world, so which entities it is about cannot depend on who happened to be written last, and there is
no changed entity to inherit from anyway, since the rule is asked once per commit rather than once
per change.

"When a PR opens, review it" and "review all of them" therefore settle in the same place: the
action advances each PR out of the selection, so both end with every open PR reviewed. What
differs is how many firings it takes to get there, not where it ends up.

Naming is the exception that proves it. A glob-free address is a set of one *whether or not the
entity exists*, because naming is addressing by identity rather than selecting from what is there.
Read "every entity this selects" literally and creating an entity would be unspellable: the only
way to create one would be to already have it.

A subject that selects must begin with a literal segment, so that it selects out of a place, or
name one entity outright, since that is what the engine selects and spawns for. A pattern that
selects nothing is a no-op — it never conjures an entity called `*`.

The engine starts one firing per selected entity, each about exactly one subject, and that identity
is what every part of a firing keys on: its spawn id, its gate question, its selection channel,
its place in a causal chain. A re-evaluation about a subject already firing, or already running work it
started, is not a second firing, which is what keeps a rule waiting on a human from asking again at
every commit.

This is why `subject` is bounded where a `when` formula ranges freely. It pins a place and
predicates over the single entity's payload rather than composing over time. "All open PRs" is
`{path: [demo, pr, *], value: {stage: open}}`, and the same thing one level deeper is
`{path: [verse-dev, drift, head, "*"], value: {reviewed: pending}}`.

### The pattern says how deep its subjects are

How deep an entity is is a property of the question rather than of the store. A document holding
`{a: {b: {c: {stage: open}}}}` has an entity at `a.b.c`, or one at `a.b` whose payload happens to
be nested, and nothing in it says which.

So the pattern carries the depth. A `path:` of N segments identifies entities at depth N,
exactly: `{path: [demo, pr, "*"]}` selects three-segment entities and
`{path: [verse-dev, drift, head, "*"]}` selects four-segment ones. Each element is a match in
its own right and an operator acts within its segment, so a name, a `!glob` and a `*` all count
the same: one segment. Nothing means "at or below", and a short path is not a prefix: `{path: [demo, pr]}` names the
entity at `demo.pr` itself, and `{path: [demo, pr, "*"]}` the ones under it.

A pattern that names the wrong depth does not fail. It selects the node at the depth it asked for,
a node that exists holding the subtree below it as its payload, reads the fields it wanted, does
not find them, and writes its patches there. This is worth knowing wherever a rule reads what
another rule derived, since a derive's own depth follows from the subjects the producing rule
selects. See [`action`](#action--what-fires) below.

A write names the place it writes, and a listing walks: `verse data -paths` lists every address
under the one it was given, and `-depth` bounds how far it goes. Asking a listing for one level
answers with whatever happens to live there and loses anything deeper, which is why it walks
instead.

### The string form is an address, in one grammar

There is one string grammar for addressing entities by identity, and both the charter wire and the
CLI (`verse flow <addr>`) parse it: a kinded path, with `*` for any one segment. So
`"subject": "demo.pr.*"` in a charter and `verse flow demo.pr.*` on the command line mean the same
set. The string is the path it spells, at every length: `"demo.pr"` names the entity at `demo.pr`,
and `"verse-dev.drift.head.*"` selects four segments deep. Anything else in a segment is a name —
`hotfix-*` written in the string is the segment called `hotfix-*`, not a glob.

An operator on a segment needs the object form, `path: [verse-dev, drift, head, !glob "head-*"]`.
Every element is a match in its own right: `!glob "head-*"`, `!or [hot, cold]`, a bare name for
equality, `*` for any segment. Nothing here enumerates the operators, because `tony.Match` owns
them, so a segment is as expressive as a payload predicate.

There is no deferred `$var` form, and nothing in a payload can redirect a subject. A trigger
whose cause is a `verse.task.instance` change and which acts on PRs acts on the PRs its own
`subject` selects — every one of them — not on the one the completed task happened to name.

In particular, a `flow` field on the changed payload does not short-circuit that. Letting it
resolve the firing onto whatever ref it holds would advance the single PR a completed task was
about, at the price that whether a firing acts on one entity or on a whole set would turn on
whether some entity happens to carry a key by that name, which no charter says and no reader of one
could predict.

What that shortcut does well, advancing exactly the subject a finished task was about, is what
[completion hooks](#completion-hooks-one-trigger-chaining-visible) do exactly. The engine scopes a
hook to *that* spawn's task, a correlation the store cannot fake because the engine is what spawned
it. Reach for a hook when you mean "this task, this subject", and for an `on` when you mean "when
this becomes true, act on that set".

`flow` is a convention published by `mirror.Reflect` — the field *is* the task's subject — read by
the flow query, and by a rule whose subject carries it as `$subject.value.flow`. It is distinct
from the reserved `$subject` binding, which is the firing's own resolved subject.

The one distinction inside the address form is name vs select, and it is the one place
existence stops mattering. A glob-free address *names* its entity, so it is a set of one
whether or not that entity is there and an action's `patch` may bring it into existence — that
is how a trigger publishes a fact nothing has written yet. A glob anywhere *selects* over what
exists, so it can only act on entities that are really there.

### Acting on a set

A selector subject advances a whole set:

```tony
- name: ship-reviewed
  condition:
    when:    {path: [ci, status, *], value: { state: green }}
    subject: {path: [demo, pr, *], value: { stage: reviewed }}
  gates:
  - { name: ship, mode: ask }
  action:
    patch: { stage: shipped }
```

Every reviewed PR advances, while CI is green. Each is its own firing, with its own `ask` gate and,
with a `spawn`, its own task tagged with that member as its subject, so each is its own flow.

One gate per member is worth saying out loud, because one gate for the whole set is the easier thing
to assume. A firing is about one entity, so twelve reviewed PRs are twelve decisions, and presenting
them as one is a summary standing in for twelve. The honest arity costs an operator twelve answers
until `verse gate approve` grows a bulk form.

Note what the `when` does here and what it does not. It is the condition, while CI is green, and the
rule is re-evaluated at every commit touching either subtree, so a PR reaching `reviewed` afterwards
is picked up. Write `on` instead of `when` and you get the other sentence: fire once, at the moment
CI went green, over whatever was reviewed *then*. Both are useful. They are different rules, and the
engine warns at install when a narrow `on` leaves part of what the rule reads uncovered.

## `on` and `when` — the cause and the condition

They are one predicate asked at two moments. `on` is asked at C-1 and at C and fires on the
rising edge — ¬P → P. `when` is asked at C and gates. Both are optional; both are matches over
the store, with the same grammar.

**`when` is the condition.** With no `on`, the trigger is considered at every commit touching
anything it reads, and `when` says whether this is a moment to act. That is the reconciler
shape: *while* this holds, act on what the subject selects.

**`on` is the cause.** It narrows *when the rule is considered at all* to the instant its
predicate becomes true, and two properties follow that an author has to know:

- **An edge does not re-fire while it stays true.** `on: {path: [demo, pr, *], value:
  {stage: reviewed}}` rises when the first PR reaches `reviewed` and says nothing about the second,
  because the predicate never fell. There is no way to say "on each PR reaching reviewed"; that is
  what a subject plus a latch is for.
- **An `on` trigger does not act on the backlog.** Installed over a verse where its predicate
  already holds, there is no rising edge to observe. A subject-dispatched trigger does the
  opposite, acting on everything already standing (see [level at open](#the-backlog)).

A match is a predicate over the verse. It conjoins a `path` — which addresses it reaches — and a
payload predicate, `value`, over what stands at them; a match without a `value` asks only where.

There is no `op` key. Such a key would classify a *change*, `set` or `delete`, and a
match is asked only of *state*, at a revision, where nothing has one. A create is the rising edge
of "this exists"; a delete is the rising edge of the
[vanishing marker](#lifecycle-how-an-entity-leaves) that precedes removal. A charter carrying `op`
is refused with that explanation rather than an "unknown key", because the author is reaching for a
distinction the model deliberately does not carry, and "unknown key" would not say so.

The address string is the path written as one kinded path, so `demo.pr.*` means
`{path: [demo, pr, "*"]}`, and a match that is only a path of names and `*` reads back out of
`verse c` in the string form: `demo.pr.*`. Same match, shorter spelling.

Keep it small. This is where complexity hides. To act "only the first time", match a state field
your action then advances: a **latch**. That is not a nicety. A rule with no latch is considered at
every commit touching what it reads, so the latch is what gives it an end state, and a spawning
action that has none is refused at install.

<a id="the-backlog"></a>
**Installing a trigger fires it on what already matches.** A trigger's observation replays the
places it reads when it opens, so the backlog standing in the verse when you install the charter is
acted on immediately, and you do not have to re-touch each entity to announce a state it has held
all along. An `on` is the exception, for the same reason it does not re-fire: a predicate that
already holds has no rising edge. The same property means the latch above is what stops an action
re-running when the engine restarts over a verse still in the matching state. See
[change events](change-events.md#the-seed-is-replayed-a-subtree-watch-is-level-triggered-at-the-open).

Neither composes over time: a condition is a match asked at a revision, and the temporal
operators a gate once carried (`!until`, `!quorum`, `!deadline`) are refused by name wherever
a charter is read. See [Limits today](#limits-today).

## What a match can say

The leaf is a real tony match, evaluated by `tony.Match`, in each path segment and in the payload
predicate.

A path segment takes operators — the richer matching a bare name cannot spell:

```tony
# filepath globbing
subject: {path: [demo, pr, !glob "hotfix-*"]}
# everything but one
subject: {path: [demo, pr, !not "1"]}
# composed
subject: {path: [demo, pr, !or ["1", !glob "hf-*"]]}
```

An operator is a tag, in a segment and in `value` alike, and nothing else stands in for one.
Inside `value` that is not a matter of taste:

```tony
value: !not {reviewed: null}      # the absence latch
value: {not: {reviewed: null}}    # a payload EQUAL to that object — which is nothing, ever
```

Both lines are legal, and the second means what it says: `value` is a predicate over payload
*data*, where a field genuinely named `not` holding one key is an ordinary thing to match.

JSON has no tag syntax, so a charter written in JSON has no operators. It can write paths of names
and `*`, and plain equality predicates; a condition that needs an operator is written in tony.

**An operator on a segment does not make the watch a predicate.** A segment is matched by
`entity.SegMatches`, which sends a tagged one through the payload matcher, so
`path: [demo, !glob "pr-*", "*"]` says what it looks like it says. The read narrows to the LITERAL
PREFIX — `entity.Pattern.literalPrefix` stops at the first wildcard or operator — and the operator
filters below it. That is also why a path that selects begins with a literal: the literal prefix is
the place a watch opens on, and a watch cannot be opened on a wildcard at the top.

`verse` is a segment like any other. What verse writes about its own running stands under it
([entities](reference/entities.md)), and a rule about it names it first:

```tony
# a rule about PRs — the ordinary case, and it cannot see verse's own records
subject: {path: [demo, pr, "*"]}
# a rule about what verse writes: a posed question, a task's mirror, an installed rule
subject: {path: [verse, gate, question, "*"], value: {status: pending}}
```

Nothing has to be excluded by default for that to hold. A path begins with the place it is about,
so `{path: [task, "*", "*"]}` on a charter about the world's tasks reaches `task.*` and never the
mirror of a run at `verse.task.instance`.

The payload predicate takes operators too. It is `value`: the same tony match, evaluated against
the payload instead of a segment.

```tony
# a record's fields
when: {path: [demo, pr, *], value: { stage: !or [open, merged] }}
on:   {path: [demo, pr, *], value: { branch: !glob "hotfix-*" }}
# the payload root
when: {path: [demo, count, *], value: !or [10, 100]}
```

`value` is open in shape because a payload is. An entity's payload is any tony value — a record, a
count, a flag, a state name, a list — so a predicate over it may be any of those, or an operator
over one. Almost every charter writes the record form, and the rest are why the key is not called
`fields`.

Over a record it is a **subset** constraint, as a tony match over an object always is: the
fields you name must agree, and any others are ignored. Over a scalar there is nothing to be a
subset of, so `value: 42` means the payload *is* 42.

That cuts one way worth knowing. `value: "stage=open"` is a legal predicate, matching an entity
whose whole payload is the string `stage=open`, rather than an expression language. A separate
key that asserted an object would make that an install error, and it would still catch only
the wrong *shape* and never `{stge: open}`, the misspelled key that is the typo people actually
make. So there is one name, and no shape assertion.

Four semantics are worth knowing before you rely on one.

`!glob` is `filepath.Match`, so `*` does not cross `/` and `?` and `[…]` are metacharacters:
`!glob "a*"` does not match `a/b`. That is the point of it, since a name holding a `/` — a slack
message's `<workspace>/<ts>` — can then be matched a piece at a time, but it means `!glob "*"` and a
bare `*`, which is any one segment whatever it holds, are not interchangeable.

A `null` in a predicate is a presence test, not "is null". A null pattern matches any value,
so the only thing it constrains is that the field is there. The three questions have three
different spellings:

```tony
# stage is present, whatever its value
value: { stage: null }
# stage is absent
value: !not { stage: null }
# stage is present and its value is null
value: { stage: !irtype null }
```

An absent key is not a null one — the store records whether a field was cleared or never
written.

Numbers are type-exact. `2` and `2.0` are different values, so a predicate written `{n: 2}` does
not match a payload of `{n: 2.0}`. Integer widths do not matter, being an artifact of whoever wrote
the value rather than of the value.

Operators are tags, so they are written in tony. A JSON charter writes paths and plain equality
predicates.

## `gates` — whether it proceeds

A list of gates, all of which must resolve before the action fires. Gates are per *firing*, and a
firing is about one entity, so a rule selecting twelve PRs asks twelve questions. Each is
`{ name: …, mode: auto|ask|park }`. An empty list fires with no approval.

The question's id is a function of (trigger, gate, subject), which is what makes a re-evaluated
rule ask once. A firing that finds its question already standing joins it rather than posting a
copy, and one answer releases everyone waiting. A rule waiting on a human is therefore not a rule
filling the docket.

Multiple gates model multi-approval: a deploy needing both a security and a release-manager sign-off
is two `ask` gates. One gate that takes more than one voice says so on the question it poses:

```tony
  gates:
  - name: approvals
    need: 2                                    # proceed takes two ballots; one deny refuses
    deadline: {within: "24h", default: deny}   # and the clock decides silence
    answerers: [oscar@example.com, group:payments]   # and whose ballots
  - name: route
    options: [{id: ship, need: 1}, {id: hold, need: 2}, {id: drop, need: 1}]
```

Every gate poses the same record: the options it can settle on, each with the number of ballots
that settles it, and a deadline with the option it falls to. A gate that says nothing poses
`proceed` and `deny` at one each, which is the question a gate has always asked. `need` is the
short spelling of `options: [{id: proceed, need: N}, {id: deny, need: 1}]`, and the two are not
written together. `options` are ordered: the first to reach its threshold settles the question.
A gate that carries any of these and no `mode` is `ask`; with `mode: auto` it is refused, since
auto asks nobody.

`answerers` is who the question is held out to: grantees in the
[grammar](perms.md#principals-and-grantees) a grant names them in — a person (`user:<email>`, or
the bare email), a whole kind (`user:*`), `*`, a group (`group:<name>`), a binding
(`via:verse.resolvers.<kind>.<name>`). It is written as the question's
`read-write` grant when the question is posed, in place of the docket's default, and `need` over
it is what one-or-many means: one ballot per answerer, `need: 1` a race, `need` equal to their
number all of them. The charter's owner co-owns what their rule asked and answers it without
being named. A gate nobody could settle is refused at install: a threshold above the answerers
named, an answerer the grammar does not read, or a group or a binding that does not stand. [perms.md](perms.md) has the record it becomes.

The tally is whoever answers' to keep, not the engine's: an answer is a ballot on the record,
the question settles in the same write as the ballot that reaches a threshold, and one answerer
casting twice replaces their own ballot rather than counting twice. What the engine owes is to
ask exactly what was written, and to act on the one answer that comes back.

Waiting for a fact is not a gate. A rule that acts once CI is green says so in `when:`.

### `onDenied` — what a no means

A denied firing writes nothing: a firing that did not happen leaves no trace of having
happened. The consequence is that the subject is left in the exact state that asked the
question, so the next commit that touches it re-selects it, the rule fires again, and the gate
is re-posed under the same id — to somebody who already answered it.

The latch a well-formed rule carries cannot help, because it lives in the action and on a deny
the action never runs. So the gate says what its own refusal means:

```tony
  gates:
  - name: approval
    mode: ask
    onDenied: { patch: { stage: rejected } }
```

It is an ordinary action — `patch`, `derive`, `spawn`, its own completion hooks — applied about the
same subjects the firing was about. It hangs off the *gate* rather than the action, because a no is
the gate's outcome: two gates refuse for different reasons, and each says what its own refusal
means. It is not a completion hook and needs no spawn of its own.

Only a committed refusal runs it. A resolver that could not be reached, or a question withdrawn
because its subject left the selection, said nothing at all, and writing a rejection on their
behalf would put a decision in the store that nobody made. It applies to a no however it arrives:
one person's, a threshold of them, or the clock's default.

Omitting it is legitimate — "ask again next time somebody touches it" is a real policy — so a
gate without one earns an advisory at install rather than a refusal. To say a no is final
without changing the subject's own state, the vanishing-marker shape works:

```tony
  condition:
    subject: {path: [demo, pr, *], value: !and [ { stage: open }, !not { blocked: true } ]}
  gates:
  - { name: approval, mode: ask, onDenied: { patch: { blocked: true } } }
```

Removing the marker is then what re-opens the question.

### A deadline settles

`deadline: {within: "24h", default: deny}` settles the question on `deny` when the clock passes
with no option at its threshold, as `by: deadline`, and the firing sees it as a refusal. The
instant is stamped on the record once, when the question is first posed, and honoured by every
later posing: a restart arms against the instant the question was given, not against a fresh
duration, so a deadline that passed while the verse was down fires on the way back up.

The default is an ordinary option. A ballot for it counts toward its own threshold like any
other, so a `deny` that needs one settles on one deny, before the clock. A default left empty
settles on nothing (`verdict: none`), which the firing reads as a refusal nobody made.

### A question outlives the verse that asked it

The record, its options and every ballot cast into it are entities, so a restart finds the
question exactly where it stood. What does not survive is the *awaiter*, which lives inside the
firing. A question with no awaiter is the worst kind of docket entry: it lists a decision,
somebody makes it, and nothing moves.

Startup gives the awaiter back, by the route the axis makes available:

- A **gate's** question blocks the firing itself, and the firing comes back by being
  *evaluated again*: its action never ran — that is what the gate is holding — so a rule that
  still selects the subject re-poses the same question, joins the record already standing, and
  the ballots already cast still count. Re-evaluated, not resumed, which is the distinction the
  next paragraph turns on.
- A **selection's** question (a `spawn` with a `provider`) opens *after* the action has
  written its latch, so nothing re-fires it. It records the `chain` of the action it is
  blocking, and startup navigates that chain back into the charter and re-enters the
  selection, which re-poses the same question — the same lookup key a resumed task's hooks
  are rehydrated from.

A question nothing can answer any more is *settled* rather than left standing: its rule left the
charter, its action moved, its subject is gone — or, the case that is easy to miss, **the
subject moved out of what the rule selects**. A merge proposal swept to `state: stale` is no
longer picked by a rule that reads `!not.has-path state`, so that rule will never look at its
gate again however healthy the rule is. The record is stamped `status: orphaned` with a `why`,
keeping the trail while taking it off the list of things a person can act on. That is not a
verdict — nobody decided anything — so a rule that selects the subject again re-poses the same
question over the same ballots.

The re-pose is idempotent by construction, which is what lets it run beside a rule that also
re-fires: the question id is derived from the rule, the gate and the subject, the settle is
terminal-once, and the spawn id is deterministic per (chain, subject). One question, one task,
however many paths reach it.

**A scoped verse (`verse connect`) is not covered.** Its questions live in the scope's own store,
which nothing enumerates at startup — the same gap scoped charters and scoped claims have.

**A verse that dies between settling a question and starting the work it decided loses that
spawn.** The record is answered, so nothing re-poses it. The resume does not introduce this: it
is the window `patch` then `spawn` already has inside one action. An action is not atomic, and
a crash in the middle of one leaves the half that landed.

### A gate's answer applies to the world it was asked in

A question is not answered in a vacuum. While it stands, the firing watches the subject's own
subtree beside it, and asks at every change whether the rule would still select the subject. When
it would not, the question is moot: it is **withdrawn** — `status: orphaned` with a `why`, no
verdict, no action, and no `onDenied`, because a moot question is not a refusal. The ballots stay
on the record, and a rule that selects that subject again re-poses the same question over them.
An answer that arrives after the subject left the selection is the same case from the other
side: the firing withdraws rather than acts.

This is not hypothetical tidiness. Without it, a merge proposal swept to `state: stale` while its
gate stands open leaves a question that watches only its ballots: it reaches its threshold on the
approval already cast, settles `proceed`, and merges a branch the sweep retired, losing the work
on it. A gate that can only be
answered about a world that has moved on is a gate answered by reflex, which is the failure gates
exist to prevent.

**The withdrawal is prompt.** The firing watches the subject's own subtree while it waits, so a
question goes moot at the change that made it moot, with nobody having to answer and nothing
having to restart.

One limit is worth knowing. It asks only about the *subject*, not the cause: a rule fired by an
`on` edge may still select its subject and never see that edge again, so a question that stands is
not a promise the rule will re-fire. And when it cannot tell — an unreadable store, a predicate
whose read fails — it lets the question stand, because a stale docket entry costs a reader a
moment while a wrongly withdrawn one takes a live question away from the people it is in front of.

### An install re-fires, so a derive must own what it writes

Installing a charter evaluates its rules against what is already there, which is what makes an
install act on the world rather than only on what happens next. A `derive` is a patch merged at
depth, so it overwrites exactly the fields it names.

Put those together and a derive naming a field *another* rule owns rewrites that field **every time
the charter is installed**, over whatever the flow had reached. Measured on 131 records, a flow
sitting at `reviewed: baseline` comes back as 85 `pending` and 46 `gathered` after re-applying a
rule that has not changed. Nothing fails; the derive simply writes its own value again.

So a derive states the facts it owns, and the rule that follows it starts from an **absence**:

```tony
# the derive says what it knows, and nothing about the flow's state
derive: [{path: [notes, row], value: {sha: $subject.name}}]
# …and the next rule latches on the absence, which is nobody's to overwrite
condition: { subject: {path: [notes, row, *], value: !not.has-path reviewed} }
```

A charter that does it the other way earns a note at install naming both rules and the field.

## `action` — what fires

Data, not code. An action needs no target of its own: it acts on the condition's subject.

**Every rule has one, and a rule that would do nothing is refused at install.** `patch`, `derive`,
`spawn` or `seq`: any one is enough. A rule with none of them would install, report itself
installed, and then watch its subtree for the life of the verse without ever writing anything.
Completion hooks do not count, because a hook is what happens when something finishes and there is
nothing there to finish, which is also why an `onX` without a spawn or a seq is refused. A gate
does not count either: a gate on a rule with no action poses a question whose answer nothing
reads.

`patch` writes to the subject's payload — a state transition. It is a tony patch, not an
assignment: a nested write merges into the subtree it names rather than replacing it, so two
triggers writing under one key do not clobber each other, and the
[mergeop](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop) operators
apply. `patch: { tmp: !delete null }` removes a key, which is something no amount of setting
fields could say. Writing a scalar (`patch: 42`) replaces the whole payload.

A nested write merges into an OBJECT, and where the subject holds an array or a scalar under
that key there is nothing to merge into — so the firing is refused rather than replacing what
stands there:

```yaml
# subject: {stage: counting, votes: [{by: ana}, {by: bo}]}
patch:
  stage: counted
  votes: {bo: yes}      # refused: the patch merges an object into votes, where an array stands
```

The refusal fails the firing, which is what a patch that could not be established already does:
the subject is left exactly as it was, so the rule still selects it and the next commit tries
again. To replace a container deliberately, write the whole value at that key
(`patch: {votes: [...]}`), which is a value rather than a merge and says so. A key the subject
does not carry yet is not this case — a patch builds it, which is what every derive that grows
structure relies on.

`derive` writes an entity that is *not* the subject, the one thing `patch` cannot do. It is a list
of entries, each naming a place as a path and what goes there —
`- {path: [verse-dev, drift, head], value: …}` — and its content may reference its own subject and,
at or below its own level, other entities. It is only installable inside a declared level ordering, where it must land
strictly above the rule that writes it. See [strata](strata.md).

**Unnamed, the address is the engine's, and it is the provenance**: the place, then the rule, then
the subject's own segments. A rule selecting `git.verse.ref.*` and deriving into
`[verse-dev, merge, proposal]` writes

    verse-dev.merge.proposal.proposal-appeared.git.verse.ref."verse/auto/…"

which is **eight** segments — three for the place, one for the rule, four for the subject. No
two rules can address the same entity, which is what the composition is for.

**So a reader of a derivation says how deep one is**, and how deep follows from the depth of the
subjects the producing rule selects. `[verse-dev, merge, proposal, "*"]` is four segments and
selects the *rule* level, whose payload is the whole subtree of that rule's derivations rather than
any one of them. A selection of N segments identifies entities at depth N, exactly. A rule that selects the wrong depth here does not
fail: it selects a node that exists, reads fields that are not there, and writes its patches onto
it.

**Naming one is how a derive stays shallow.** `id:` (below) puts the entity at the place plus
that one segment — `verse-dev.merge.proposal."verse/auto/…"`, four — which is what a reader
already spells. A derive whose subjects stand under one place, read by rules that name the place
literally, generally wants one.

One firing derives one entity — until it says `!each`, which fans out over a list and derives
one per element, addressed by a key read out of the element. That is the same invariant with
the key in the address rather than an exception to it, and it is where a task's twelve findings
become twelve facts instead of eleven paragraphs: see
[strata](strata.md#each--when-one-answer-holds-many-facts).

A single derive may carry a `key:` too — `- {path: [verse-dev, merge, train], key:
$subject.value.sha, value: …}` — which makes its address a function of (rule, subject, key)
instead of (rule, subject). One entity per value of something, where the same subject keeps
moving: see [strata](strata.md#a-key-on-a-single-derive).

**`id:` is that key with the composition dropped**, and the difference is the reader's. A keyed
entity stands at `verse-dev.drift.head.head-moved.git.verse.head.aaaa1111` — the place, the rule,
the subject, the key — and nothing in the grammar composes that from a subject, so anything that
wants that record has to *describe* it — a `"*"` per segment and a predicate on a field it happens
to know — instead of naming it. With `id: $subject.value.sha` the record is
`verse-dev.drift.head.aaaa1111`, and a reader spells it:
`!get-path {at: [verse-dev, drift, head, $subject.value.sha], path: result.report}`.

What the composition buys is uniqueness, and `id:` hands that to the author. Two subjects of one
rule that compute the same value share one entity, where `key:` gives them one each. So `id:`
claims the value identifies the record **across subjects**, true when the subject is a singleton as
`git.verse.head` is, and `key:` claims it only within one. The *place* stays the engine's either
way, since a derive may only write inside its own stratum, checked at install. A derive carrying
both is refused, since which one stands would decide that question silently.

`!each` takes both too, and there the difference has teeth: keyed, every element lands under
(rule, subject, key), so a fan-out can only ever create its **own** copies. Named, it lands at
the place plus the value — which is how one rule's fan-out reaches entities another rule names,
and the only way to say "mark every one of those that is already there". A derive merges at
depth, so the mark lands beside the facts rather than over them.

`spawn` launches a task from a `launch.Spec`, or, instead of a fixed `driver`, a governed
`provider` selection of the doer. The spawned task is tagged with the subject, which is what makes
it part of that subject's flow.

A selection binds the whole doer rather than just its driver. The winning provider entity's
`driver`, `frame`, `tools` and `grant` overlay the spawn's own, so two doers on one driver can
differ in what they are told and what they may do. The overlay only ever narrows: a doer's grant
must be within the spawn's, which is why `grant:` is required on a spawn that selects, its tools
intersect rather than replace, and `vector` is not a doer's to set. A doer that would widen is
refused at *eligibility*, so it never appears on a ballot. See
[provider selection](provider-selection.md).

The spec's fields fall into three groups, and the difference decides what a charter can say.

*The request* is `prompt` and `frame`. Both are text you write. `prompt` is the one job; `frame` is
the standing instructions it is read in, the agent's system prompt. A frame is worth writing
whenever the charter reads the answer rather than a person: the default tells the agent its last
message *is* the result and not to summarise, but only the charter knows what shape it wants
back.

*The contract* is `answer`. This is the shape the final message must have, and unlike the frame it
is not advice: it is checked. See [the answer contract](#the-answer-contract) below.

*The envelope* is `vector`, `grant` and `tools`. These name things rather than spelling them out.
`vector: auto|ask|park` sets how much the task must confirm; a firing that already
answered its own `gates` can say `vector: auto` rather than re-ask per action. A spawn that
wants the task gated per action writes the task's own `gates:` beside it — the same shape as
the trigger's, each with the question it poses, run at every boundary of the task rather than
once for the firing; see [a gate's question](reference/tasks.md#a-gates-question).
`grant: read-only|no-delegate|none|all` is the hard authority: an action outside it is
*refused*, not asked about, because nobody at a console was delegated the authority to widen
what the charter granted. `tools` narrows what the driver offers at all — `tools: [read_file,
list_dir]` on a review agent means shell and delegation never come up. `fail` is always
offered whatever you list, since it is how a task reports it could not do the work.

An unknown `vector`, `grant`, or tool name fails the spawn rather than falling back to unbounded. A
field that reads back as written and enforces nothing is the failure this is built to avoid.
`resolver` names the binding that answers this run's gates, and a spawn naming one that is not
installed is refused rather than left unsupervised; see
[attached resolvers](attached-resolvers.md). `environment` is declared and unread.

```tony
spawn:
  driver: llm
  prompt:   "Review $subject for security defects."
  answer:
    severity: !or [low, high]
    note:     !irtype string
  grant:  read-only
  vector: auto
  tools:  [read_file, list_dir]
  retain: 4096
```

`retain` is how many of this task's read-plane events are kept for a reader that resumes: the
window below which a `?since=` gets a resync marker instead of the story. It is per spawn because
the right number is a property of the work. A task that narrates every token and one that reports
four boundaries an hour want windows two orders of magnitude apart, and no single default is right
for both. The charter is the party that knows which it is. Omit it and the runtime's own modest
default applies. A longer window is paid for in memory per live task, which is why the author
chooses it rather than inheriting it.

### `calls` — when the charter knows the actions

A spawn can carry the work itself instead of a prompt for something to work it out. A merge is
four git commands; paying a model to rediscover them each firing buys nothing but the chance of
doing something else.

```tony
spawn:
  vector: auto
  calls:
  - tool: run_bash
    input: {command: "git merge --ff-only origin/$branch"}
  - tool: push
    input: {destination: main}
```

Its presence **is** the driver — `driver: calls` may be written and must not contradict it — and
each entry becomes an ordinary action, derived, gated and narrated the same way, so a gate written
for an agent's push holds this one. Values are expanded per argument, so a `$branch` containing a
quote is an argument rather than something that has to survive being spliced into text.

An entry may narrow what ends the list:

```tony
  - tool: push
    input: {destination: retire}
    fatal: [err]        # a remote declining one ref is not a reason to stop
```

out of `err` (the action failed), `blocked` (refused, by a gate or by the far side) and `nonzero`
(a command exited non-zero). Absent means all three, since a list is ordered because its calls
depend on each other, and `fatal: []` means this call ends nothing, which is what a sweep says. The
two are different statements, so writing neither is not the same as writing the empty one. A list
with an unknown outcome, no tool, or a contradicting driver fails the *firing*, where its author
can read why. A tool name it cannot check belongs to the runner and is refused there.

What a call list does not get is what belongs to a model: no `answer` contract, since there is
nobody to repair one, so a spawn carrying both fails; no delegation; and no deciding what to do
next from what the last result said. That last one is the trade. It is right for four git commands
and wrong for anything that branches.

### Who answers a rule's own gates

```tony
gating:
  resolver: verse.resolvers.security.security-review
  gates:    [{name: ship, mode: ask}]
```

`gating` groups the two facts about a rule's gates: which they are, and who answers them. `gates:`
at the top level means `gating: {gates: …}`, one field read two ways rather than two that can
disagree, and writing both is refused rather than merged, because a rule that says which gates it
has twice has said it twice.

`resolver` names an installed binding (`verse resolver add`). It is the same word a spawn uses for
the same act: a spawn names who answers the run it starts, a rule names who answers the gates it
raises itself. Empty leaves the question to whatever standing binding covers its subject, and then
to the docket a person reads. A binding that is not installed is refused here rather than left to
own a question nothing can answer; see [attached resolvers](attached-resolvers.md).

`seq` runs a list of actions in order, each starting when the previous one completes. It is
the depth axis, and it has its own section below.

### Bindings

Every field of **the payload an action reads** is bound by name (`$stage`, `$path`, …), plus
`$subject` — the resolved subject. Which payload that is has exactly two answers, and which one
applies is a property of *why* the action is running.

A trigger firing reads **the subject's own** payload. The change that dispatched it is a doorbell
and may name an entirely different entity, whose fields would mean nothing on the subject being
acted on. So `review $subject` in a spawn's prompt names the PR the firing is about, even when the
entity that fired was a task rather than the PR, and `$stage` is that PR's stage rather than the
task's.

A completion hook, or a sequence element one drives, reads **the finished task's** payload
instead: `$status`, `$result`, `$data` and `$err` are the facts it exists to read. The engine
synthesises that payload rather than reading the store, because the mirror may not have written
the terminal facts yet — which is both why those bindings resolve at all and why they are flat.
They are facts about a run, with no entity to hang a path off.

`$subject` is a record, and the bare form is its address:

| `$subject` | the whole ref, `demo.pr.1` |
| `$subject.ref` `.name` | the address, and its last segment — the key the subject stands at |
| `$subject.value` | the subject's payload **at the commit that caused the firing**, not at the moment the action runs — the two differ by however long a gate took to answer |
| `$subject.commit` | that commit's revision |

A completion hook reads at **the same commit its rule fired at**. It is woken by a task finishing
rather than by a write, so it has no cause of its own. The one it takes is the firing's, carried on
the task's record as `occurrence`, which is what makes it survive a restart, since the engine that
runs the hook may never have seen the firing. So a rule is one statement about one moment.
`$subject.value` in an `onDone` is the subject as the condition selected it, not as the action left
it a step earlier, and a charter whose task does not write its own subject gets the reading its
author meant. A task the charter did not start has no such commit, and `.value` and `.commit` fail
there by name rather than resolving to a silent zero.

In a spawn's strings the identity parts bind flatly; `.value` does not, since a payload has no
text form and its top-level fields are already bound by name.

### `!get-path` and `!list-paths` — reaching another entity

Everything above reads the firing's own subject, or the task that just finished. These two
reach an entity neither of those is:

```tony
action:
  patch:
    repo:     !get-path {at: [git, repo, $subject.value.head]}
    branch:   !get-path {at: [git, repo, $subject.value.head], path: branch}
    branches: !list-paths {at: [git, repo, "*"], path: branch}
    # …and a predicate narrows the walk to the ones that look like this (list only)
    green:    !list-paths {at: [git, repo, "*"], value: {state: green}, path: branch}
    # the string form is the kinded path, for an address with nothing to expand in it
    head:     !get-path git.verse.head
    # and `at:` names the address as a path, at whatever depth the tree is
    says:     !get-path { at: [verse-dev, drift, head, aaaa1111], path: result.report }
    open:     !list-paths { at: [verse-dev, drift, finding, "*"], value: {status: open}, path: says }
```

The address is `at:` here and `path:` in a condition, which looks like an inconsistency and is not.
In a read, `path:` is already the field path inside the payload, and two things cannot both be
`path`.

**One grammar, split by cardinality, and the names say which.** A verse is one document, so an
address is a path to a node in it and `path:` continues it into the payload. `!get-path` answers with one node; `!list-paths` answers with a list, and is
where the globs live. They mirror go-tony's own split, where `GetKPath` returns one node and rejects
a wildcard outright while `ListKPath` collects.

Which one you get is legible in the charter rather than inferred from whether some segment happened
to be a `*`: `!get-path` **refuses** a wildcard in its last segment or in its `path`, and names the
tag that answers that question instead.

| | |
|---|---|
| a string | the address as a kinded path (`git.verse.head`), with nothing in it to expand; it may go on into the payload (`git.verse.head.sha`) |
| `at` | the address as a path of at least two segments. Every segment above the last is a literal or `*`; the last is a literal, a `$reference`, or (list only) a `*` or an operator |
| `path` | a kpath into the payload; wildcards are list-only. Omitted, you get the whole payload |
| `value` | a payload predicate, **list only** — of the entities the address selects, the ones that look like this |

Nothing above the last segment is a reference or a glob, because the level check asks whether a
read lands at or below the rule's own stratum, and it asks that of the literal prefix, which is all
that is known at install.

`!get-path` refuses a `value` for the reason it refuses a wildcard. A predicate can name several,
an entity that is not there does not satisfy one, and one that is there might not either. On the
list form a predicate is the *cheaper* read, because a `*` already walks everything under the place
and this is what narrows it. Neither form is a query that slides: both are answered from one
snapshot at the causing commit, so a predicate is evaluated against the same world the rule was.

The string form's dots are its segments, so a `$reference` — which has dots of its own — cannot be
written inside one. An address that expands a reference is written with `at:`.

**Read at the commit that caused the firing**, the same rule `$subject.value` follows and for the
same gate reason.

**In an action, they copy and do not track.** Whatever is read is read once. If it changes
afterwards nothing re-runs: this rule fires again only when its own subject changes, or when an `on`
or `when` it carries covers the target. Add the `on` if you want to follow it.

### A condition may read too, and that one does track

A read in `condition.subject`'s predicate is answered before the predicate is matched, so a rule
can select on how its subject compares to another entity — the thing a match alone cannot say,
since a match tests a field against a literal or an operator and never against a value read from
elsewhere. `!let` binds the read, and the body compares:

```tony
- name: proposal-outrun
  condition:
    subject:
      path: [merge, proposal, *]
      value: !let
        let: [{tip: !get-path {at: [git, verse, head], path: sha}}]
        in:  {state: open, base: !not .[tip]}
  action:
    patch: { state: stale }
```

Every open proposal cut from a commit that is no longer the tip of main — "which of these can
still land", with no ancestry walk and nothing to poll.

**It joins what the rule reads**, which is the difference from an action's read, and it is not a
detail: the rule above is re-evaluated when `git.verse.head` moves, over proposals that did not
change. A trigger is skipped at a commit that touched nothing it reads, and a condition comparing
against another entity reaches a different verdict the moment that entity moves, so the read counts
or the skip would be wrong.

**It cannot name `$subject`.** In an action the subject is settled and addressing by its fields is
the point. A condition is what *chooses* the subject, so there is no one entity to read from yet.
It is refused at install, saying that rather than "unknown reference".

**A missing `!get-path` target fails the firing**, as any unresolved reference does, so that
"nothing is there" cannot launder into "something is there, and it is nothing". An empty
`!list-paths` is an empty **list** — "which one" has an answer or it does not, while "all of them"
of nothing is legitimately nothing.

**Inside a `derive` they must resolve at or below the rule's own stratum** — see
[strata](strata.md), where that is the whole of what they cost. In a `patch` they are
unconstrained.

The tag is the whole spelling: a plain `{get-path: …}` key is ordinary data and stays that way,
since a magic key would turn a payload somebody wrote into a lookup nobody asked for. That does
mean a JSON-shaped charter cannot write one, JSON having no tags.

### The answer contract

A charter that patches `$data.class` onto its subject depends on the task producing a `class`.
Without a contract, that dependency is checked at patch time, after the work is done and the tokens
are spent, and what is left is a hook error on the task entity and a subject stranded at a stage no
rule matches. A frame asking for a shape does not prevent it: an agent that has just read nine files
narrates and fences before it answers, and a frame is advice a model may ignore.

`answer` is the same request made enforceable. It is the shape the final message must have, written
as a tony **match pattern** — the `accept` clause of a schema, the same vocabulary `when` is written
in, stored the same way under the same `!raw` escape:

```tony
spawn:
  driver: llm
  prompt:   "Review $subject."
  answer:
    class:    !or [bug, nit, risk]
    severity: !or [low, high]
    why:      !irtype string
```

It is checked when the model stops, which is the only moment it can be: a run ends by the model
ceasing to call tools, so stopping and reporting are one act. A failure goes back to the agent as a
**diagnosis** — the path, what was expected there, what was found, absent kept apart from
present-but-wrong, and every such failure rather than the first — and it is asked to fix it. That
is the match's own account of how it walked (`tony.Explaining`), so a failure inside an operator is
located as precisely as the operator knows it: `findings[1].severity`, not "findings". Repair is
bounded at two attempts. If a model cannot produce the shape with the complaint in front of it,
more turns will not teach it.

A run whose answer never satisfies its contract **fails**. That is the whole point:

- `patch: { class: $data.class }` cannot fail at patch time, because a task that
  could not produce `class` did not succeed. The failure lands not on a hook nobody watches
  but on `onFailed`, which every rule already declares a branch for.
- The check is on the answer, not the layout: a fenced answer is unwrapped, so the repair
  budget is spent on content.
- It is a guarantee about *shape*, not about content. A repair loop pressures a model toward
  compliance, so the agent is also told to call `fail` rather than invent a value it could not
  determine. An honest failure is worth more than a well-formed wrong answer.
- A sub-agent (`call_agent`) does not inherit it. A child answers a subtask its parent
  worded, and its result comes back as one more tool result; only the parent's own final
  message is what a charter derefs.

Writing a contract the driver cannot parse fails the firing where it is written, before a model
call, the same as an unknown tool name. A driver with no model to ask again, such as `scripted`,
fails the spawn rather than running it unchecked, because a contract silently dropped is worse than
none: the charter downstream was written believing it holds.

A `patch` resolves references too, and there it is a **deref by kpath**, not a template
substitution — it names a value, it does not compute one:

```tony
onDone:
  patch:
    stage:    reviewed
    severity: $result            # the task's own answer, as a fact on the subject
    step:     $verse.review.step # any depth, and list indices: $result.findings[0].id
```

The reference grammar is [tony](https://signadot.github.io/tony-format/)'s, not verse's:
`$result.findings[0].id` resolves by handing `result.findings[0].id` to the same kpath
navigation that addresses an action inside its trigger (`review.seq[2].onFailed`). The
familiar `$name` spelling *is* that kpath at depth one.

This is what closes the loop. A result patched onto the subject is an ordinary fact, and an
ordinary condition matches it, with no knowledge of tasks required:

```tony
subject: {path: [demo, pr, *], value: { stage: reviewed, severity: high }}
```

Four semantics are worth knowing.

*Whole value versus interpolation.* A value that is **exactly** one reference resolves to that
reference's node, so a number stays a number and an object stays an object. A reference **embedded
in text** interpolates and yields a string, because that is how it was written. The distinction is
load-bearing: payload predicates are type-exact, so a `3` flattened to `"3"` is a value
`value: {count: 3}` does not match.

```tony
# the number 3
patch: { count: $n }
# the string "saw 3"
patch: { note: "saw $n" }
```

*A composite has no text form.* `"saw $result"` where `$result` is an object is an error, not
a rendering — inventing one would make the same charter mean different things depending on
what a task happened to return. Reference it on its own to write the whole value.

*An absent reference fails the firing.* It is not dropped, and this follows from the store
recording an absent key as different from a null one: omitting it would launder "the task never
said" into "the task said nothing", which a match reads as a different fact. A field written null
resolves to null, as it should. The failure is the same one a failed write takes: the action stops,
and its spawn does not start.

*There is no escape.* `$$` is not a literal dollar, and `a$$b` references `$b`. Anything that is not
a well-formed reference is left exactly as written: `$5.00`, `100$`, a bare `$`.

A spawn's strings are a flatter substitution: top-level fields only, always as text, and an
unresolved `$var` left in place rather than failing. So `$verse.review.step` derefs in a `patch` and
does not in a `spawn.prompt`. Unifying the two halves onto one resolver is the obvious next step,
and it is not taken here because making a spawn fail on an unbound variable would change what
installed charters do.

A field holding a **document** renders as tony in a prompt, and that is the one place the two halves
disagree on purpose. A patch refuses to give a composite a text form, because `"saw $result"` would
make one charter mean different things depending on what a task returned. A prompt is text by
nature, with no whole-value form to reach for instead, so the only question is *which* text. A
charter that puts a ledger in front of an agent puts a document there:

```tony
prompt: "Outstanding findings:\n$findings\n\nThe diff:\n…"
```

Leaves are untouched by that, which matters more than the feature: `$stage` and `$count` bind
exactly their own bytes (`3`, never `"3"`). A null field binds empty.

### Completion hooks: one trigger, chaining visible

An `Action` carries one hook per terminal status — `onDone` (status `done`), `onFailed`,
`onCancelled`, `onOrphaned` — and each is itself a full `Action` (`patch` + `spawn` + its own
hooks), so a chain (`review → merge → approve`) nests `onDone`s and reads top-to-bottom in one
rule. When the action's `spawn` reaches a terminal status, the engine fires the matching hook
about the *same subject*, scoped to exactly that task: the hook rides the live task
registry (keyed by task id), so there is no `flow` match and no separate trigger.

```tony
action:
  patch:  { stage: reviewing }
  spawn:  { driver: scripted, vector: auto, prompt: "review $subject" }
  onDone: { patch: { stage: reviewed } }
```

The alternative is a *decoupled* completion trigger: a separate rule that fires on the
`verse.task.instance` change and names the PR by pattern (`reviewed`, below). That is the tool when
a completion should fan into several reactions, advance a different subject, or be authored
separately. The hook is the tool for a linear chain owned by one trigger, with the task step visible
inline.

The four hooks are named fields rather than an `on map[status]*Action`, which is what makes
an unknown status unwritable: there is no field to write it in, so nothing needs validating.
The cost is that a fifth terminal status would be a schema change, and that a misspelled
`onDoen` is dropped like any other unknown field.

Hooks survive a restart. Resume rehydrates them from the task's chain, so `done`, `failed` and
`cancelled` still fire. `onOrphaned` is inert, and not for want of rehydration: orphaning and
adopting are the two alternatives in reconcile, so a task that can be re-attached is adopted and
never marked orphaned, and one that cannot is orphaned and never tracked. No tracked task reaches
that status.

### `seq`: sequence along the chain, parallel across triggers

Nesting `onDone`s expresses a chain, but it right-drifts and, the reason that matters, has no
arity. The engine knows there is a next hook and never how many remain, so a sequence written that
way cannot say where it is. `seq` spells the depth axis directly:

```tony
- name: review
  condition:
    subject: {path: [demo, pr, *], value: { stage: open }}
  gates:
  - { name: review, mode: ask }
  action:
    seq:
    - spawn: { driver: scripted, vector: auto, prompt: "lint $subject" }
    - spawn:    { driver: scripted, vector: auto, prompt: "review $subject" }
      onFailed: { patch: { stage: rejected } }
    - patch: { stage: reviewed }
    onDone:
      patch: { stage: ready }
```

There is deliberately no `par`. Breadth is what independent triggers matching the same condition
already do, and a task id carries both directions: `review@demo.pr.1` and `hold@demo.pr.1` are two
roots about one subject, while `review.onDone@demo.pr.1` is an extension of one chain. Putting
breadth *inside* a chain would cost that reading.

The rules:

- `seq` and `spawn` are mutually exclusive on one action, or "does the sequence wait for the
  spawn?" is a coin flip. `patch` alongside `seq` is fine: the patch applies, then the sequence
  runs.
- The sequence owns the `done` slot of each element, so an explicit `onDone` on a non-last element
  is an install error, since the list already says what comes next. `onFailed`, `onCancelled` and
  `onOrphaned` stay per element as off-ramps, and firing one ends the sequence.
- `onDone` on the action *carrying* the seq means "after the whole sequence", so a sequence
  is a unit with its own completion and nesting composes.
- An element with no spawn completes immediately, so `seq: [{patch: …}, {patch: …}]` is two
  ordered writes rather than an error. `seq` sequences actions, not tasks.
- Gates gate the sequence, not the step: gates are per firing and a sequence is one firing,
  so one `ask` approves the whole thing.
- No chain may contain two `seq` segments. That is stated over chains rather than elements,
  because an element's `onFailed` holding a sequence nests just as much, only further away. With at
  most one `seq` per chain, "the sequence this task belongs to" is a unique ancestor rather than a
  stack, which keeps re-fire idempotence a predicate rather than a search.

### Chains and ids

A spawned task is identified by `<chain>@<subject>`, where the chain is the
[kpath](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath) of the action
inside its trigger — dotted field names, brackets for list positions. Hooks descend by field
name and sequence elements by index:

| the action | chain | id for `demo.pr.1` |
|---|---|---|
| the trigger's own | `review` | `review@demo.pr.1` |
| its done-hook | `review.onDone` | `review.onDone@demo.pr.1` |
| a nested done-hook | `review.onDone.onDone` | `review.onDone.onDone@demo.pr.1` |
| the third sequence step | `review.seq[2]` | `review.seq.2@demo.pr.1` |
| that step's failure branch | `review.seq[2].onFailed` | `review.seq.2.onFailed@demo.pr.1` |

Because the chain addresses the action rather than naming it privately, it is deterministic, which
is what makes a re-fire idempotent: a spawn dedups by id. It is also a lookup key back into the
charter, which is how resume rehydrates a task's hooks and how a sequence finds its next step. And
it is published on the task entity as `chain`, with its root as `trigger`, so "every task of this
trigger" is a payload predicate rather than a glob over ids.

The id drops the brackets because they are shell-hostile: `zsh` kills `review.seq[2]@demo.pr.1` with
`no matches found` before verse ever sees it, and ids get pasted onto command lines constantly. The
dotted form is lossless because `Action`'s field set is closed — a segment after `seq` is an
element, a segment matching a hook name is a field. The join is `@` rather than `-`, which could
not be decoded back, because trigger names spend `-` too.

A rule is its scope, its charter's address and its name — names are unique only within one charter
— and every charter in a verse shares one task runtime and one docket. So every id a firing mints
leads with the charter that minted it: a package's address and `:`, a scope's name and `::`, and
nothing for baseline's. The task, its gate questions and its provider selection all carry it:

| the charter | task id | gate question id |
|---|---|---|
| baseline's | `review@demo.pr.1` | `verse.trigger.review.ok~…` |
| the package at `verse-dev.drift` | `verse-dev.drift:review@demo.pr.1` | `verse-dev.drift:verse.trigger.review.ok~…` |
| scope `me`'s | `me::review@demo.pr.1` | `me::verse.trigger.review.ok~…` |

A rule's name therefore may not contain `:`, as it may not contain `@`.

### Progress facts

A parallel branch is woken by a condition, and a condition matches facts, so a sequence publishes
its position or the breadth axis cannot observe the depth axis at all. The engine writes it on the
subject under one reserved `verse` root as each element completes:

```tony
verse:
  charter:             # what the engine records about rules acting on this entity
    review:            # keyed by trigger
      step:  2         # how many elements have completed
      steps: 3         # len(seq), known at install
      shape: "a3f1…"   # hash of the declared shape, known at install
      done:  true      # latched when step reaches steps
      ended: failed    # …or the terminal status an off-ramp stopped it at
  meta:                # what verse records about the entity itself
    vanishing: "2026-07-29T13:04:05Z"   # the intent to remove it — see Lifecycle
```

A join reads it at whatever depth it cares about:

```tony
subject: {path: [demo, pr, *], value: { verse: { charter: { review: { done: true } } } }}
```

One reserved name, not a family. The alternative, flat per-trigger keys like `review.step`, reserves
an open-ended set of names out of a payload that is the author's own document. A single `verse` root
reserves one name and documents once, the way `flow` is one reserved key on task entities. Nested
predicates keep it readable, since `value` matches structurally and so matches a payload carrying
any amount of other content.

The two halves under it are separated because they grow differently — `charter` gains a key per
rule, `meta` a field per lifecycle concern — and because flat they would collide: a trigger *named*
`vanishing` would write its progress exactly where the lifecycle marker goes.

(The name is plain `verse` and that is not settled. A sigil would say "reserved" at a glance and
give the word back to authors — `@verse`, `~verse` and `$verse` all parse unquoted, address
through kpath and round-trip through the yaml renderer. `.verse` does not, and is the one to
avoid: kpath reads `.verse.meta.vanishing` back as `verse.meta.vanishing`, dropping the sigil
silently rather than refusing it.)

`done` is published rather than derived because a match compares a value against a pattern,
never one field of a payload against another. A join spelled `step == steps` is not
expressible; one spelled `{step: 3}` silently stops meaning "finished" the moment the
sequence gains an element. So the engine latches `done` when `step` reaches `steps`, and the
join matches that.

`ended` is the other way a run concludes: an element that reaches a terminal status other than
`done` is an off-ramp, so its own `onFailed`/`onCancelled`/`onOrphaned` runs and the sequence
stops there. Two fields rather than one, because they are two facts — a join asking for `done`
must not start matching a run that gave up — and both settle the same repeat policy: a sequence
that concluded either way does not run again at this shape. Editing the sequence changes the
shape, which is the one thing that frees it.

`shape` is a hash of the declared sequence, compared on each advance. A mismatch means the charter
moved underneath, and the sequence fails with a reason rather than silently running whatever now
sits at `seq[k+1]`. It is a hash rather than a pinned copy because these sequences can run for a
long time, and a retained snapshot per live sequence is a retention floor the store can never
compact past. Refusing to continue needs detection rather than reconstruction, so the error can say
that the shape changed and name the trigger, but not what changed.

## Flow — a subject-match over live tasks

A flow is not a stored key. It is a match evaluated over the subjects of *live* tasks:

```sh
verse flow -subject demo.pr.1       # tasks whose subject matches demo.pr.1
verse flow -subject 'demo.pr.*'     # …every pr
```

Relating tasks is what a match *does*, so "relational flows" are not a second primitive to add
later. They are broader subject-matches. `verse flow` lists the live tasks a selection names, and
a verb applies the same selection and acts on what it names. A query, not a maintained index.
Holding, suspending and cancelling that work is [control](control.md).

## Suspending

A suspension is one record naming a charter (or every charter), a rule of it (or all), and a
subject (or every subject), and it holds the work about that subject in every form until it is
lifted. What an operator does with one, and how it differs from holding a run, is
[control](control.md); this is what it does to rules.

```tony
# verse.trigger.suspension."verse-dev.drift.**:*@git.*.head"
charter: verse-dev.drift      # an installed charter's address; "*" for every charter; "" for baseline's
recursive: true               # …and every charter installed beneath it
rule: "*"                     # a rule's name in it, or "*"
subject: git.*.head           # an address, * open in any segment, covering at or beneath; "*" for every subject
held:
  user:walter@example.com:
    why: "reading the drift charter's range logic before it runs again"
    since: "2026-09-14T03:10:00Z"
    occurrence: 41872         # the store revision the hold landed at
```

The record is keyed by what it covers, addresses parsed and rendered by segment, so two spellings
of one selector are one record, and suspending the same thing twice adds a holder to it.

**A rule about to fire** is asked in three places. At the top of evaluation, before the rule's
watermark moves, a rule suspended about every subject is skipped; so the revision it skipped is
still one a re-arm's replay is admitted at. Per subject, a firing about a suspended subject is
refused before the cycle throttle sees it, so a firing that was not going to happen counts no
trip; and again on entry to a firing, since a paced firing waits out the throttle first. Every
spawn and every charter-authored write pass a backstop. Nothing asks in subject selection, which
is what decides a question has gone moot: a suspension there would moot work rather than hold it,
and a rule that stops selecting its subject during a suspension ends that firing as it would have.

**A firing at its gate** has its question withdrawn — orphaned, with the suspension as its `why`,
ballots and deadline stamp kept — and waits holding its flight. When the suspension lifts it asks
the same question under the same id if the rule still selects the subject, with the time its clock
had left.

**A completion hook or a `seq` step** lands its writes, since recording what a task did is not
progress, and parks what it would start before a provider selection could be posed, so a hook asks
nothing while suspended and the engine's reap loop never waits on one.

**A lift is ordered.** The record is deleted, and the suspension drains: new firings about its work
are still refused while what it held is released — tasks resume, parked launches start in order,
withdrawn questions are asked again — and only then is it removed and every rule it covered armed
again, its watermark first moved below the head, the path an install and a restart take. So a rule
acts on the world as it stands when the suspension lifts, and nothing that happened meanwhile is
replayed: a level rule still matching acts once, one whose condition came and went does nothing,
and an `on` edge that rose during the suspension is not seen, as an install over a verse where the
predicate already holds sees none.

**Whose it is.** Each scope's suspensions are one table in memory, written only by the route that
writes their records and loaded before the scope's tasks are re-attached or its charter armed;
until it has loaded it holds everything. A rule `derive` aimed at `verse.trigger.suspension` is
refused at install, since no table would see it. What a suspension covers is decided each time it
would apply, against the holder's `control` over the work's charter or at its subject, and a rule
the system installed (`by: system:charter` on its record) is the system's alone to suspend
([perms](perms.md#suspending)).

## Two rules, read out loud

```json
{ "name": "review",
  "condition": { "subject": { "path": ["demo", "pr", "*"], "value": { "stage": "open" } } },
  "gates":     [ { "name": "approval", "mode": "ask" } ],
  "action":    { "patch": { "stage": "reviewing" }, "spawn": { "driver": "scripted", "vector": "auto", "prompt": "review $subject" } } }
{ "name": "ship-green",
  "condition": { "on":      { "path": ["ci", "status", "*"], "value": { "state": "green" } },
                 "subject": { "path": ["demo", "pr", "*"], "value": { "stage": "reviewed" } } },
  "action":    { "patch": { "stage": "shipped" } } }
```

`review` reads as *while a PR is open, review it*. No cause is named, so it is considered at every
commit touching `demo.pr`, and its patch advances each PR past `open` so it settles. Its spawn is
allowed only *because* of that patch: a spawning action with nothing to latch on is refused.

`ship-green` reads as *at the moment CI goes green, ship everything reviewed*. It fires once,
at the edge, over the set as it then stood. Written with `when` instead of `on` it would say
*while* CI is green, and pick up a PR reviewed afterwards.

## The commit is the unit

A trigger's rule is a question about the store at a revision rather than about one entity, so it is
asked once per commit. A docd commit is atomic across many branches, and `LogdStore` reports it
whole: one `Commit`, carrying one change per entity it touched.

That framing has to come from the store, because it cannot be recovered downstream. Changes of
one commit share a revision, but a consumer reading them one at a time cannot tell whether the
next belongs to the same commit without waiting for the one after it — while the store, which
built them from a single delta, knows exactly.

What the engine does with the changes is decide whether asking is worth it. A trigger is skipped at
a commit that touched none of the subtrees it reads, and that skip is exact rather than a heuristic.
If nothing it reads changed then `on(C) = on(C-1)`, so there is no edge, and a vacuous rule would
evaluate the same predicates over the same state and write the same values, which produce no commit.
Either way it is indistinguishable from not running, which is what makes per-commit evaluation
affordable.

How the store builds a commit out of one delta — and why the per-entity grain sits inside the
transaction rather than instead of it — is in
[change events](change-events.md#one-commit-many-entities-the-entity-is-the-reactive-grain).

## Lifecycle: how an entity leaves

An entity does not simply disappear. The intent to remove it is written first, as
`verse.meta.vanishing` — the instant it was recorded — and the removal follows separately.

That order exists because of what a cause is. A trigger fires on the rising edge of a store
predicate: *something matching this now exists*. There is no way to say the negative — a match
asks what is there, so "this stopped existing" has no spelling. Writing the intent down turns
the negative into a positive, and a departure becomes an ordinary rising edge:

```tony
- name: note-departures
  condition:
    on:      {path: [demo, pr, *], value: { verse: { meta: { vanishing: null } } }}
    subject: audit.record.latest
  action:
    patch: { saw: "a departure" }
```

Match the marker's *presence*, where `vanishing: null` is tony's present-any-value idiom, rather
than its value. The value is a timestamp and may grow into a record — who asked, why, a grace
period — and a charter that matched on a particular value would have to be rewritten when it does.

Three things follow, and the first is the one to know:

**A vanishing entity is not a subject.** A rule acts on what it selects — patches it, spawns
about it, raises a gate on it — and doing any of that to something on its way out is work about
something that will not be there. So `subject: {path: [demo, pr, *]}` skips it, and naming
the marker is the opt-in that sees it again — which is how the reaper works.

A *named* subject (`demo.pr.1`) is skipped too, and this is the one place naming does not beat
selecting. You may name something that does not exist yet; you may not name something on its way
out. Otherwise a rule reacting to a departure acts on the departing entity, and since a patch over
an absent entity creates, a removal landing first means the rule resurrects it: back as an ordinary
live entity, stripped to whatever the rule wrote, with no marker on it. React to a departure by
writing somewhere else, to an audit record or an archive. The thing that is going is not a subject.

The raw read (`verse data`) still shows it, because making the intent visible is the whole point.
Only the rule-facing selection hides it.

**The removal itself is inert.** When the entity finally goes, a predicate over it falls from
true to false, and nothing fires on a falling edge. The reactive moment was the intent; the
removal is bookkeeping.

**The reaper is an ordinary trigger.** It selects what is leaving — by naming the marker — and
removes it. `!delete null` is the removal, at the root exactly as at a field:

```tony
- name: reap
  condition:
    subject: {path: [demo, pr, *], value: { verse: { meta: { vanishing: null } } }}
  action:
    patch: !delete null
```

A bare `patch: null` writes the payload `null`: an entity that stands there holding nothing, rather
than one that is gone. Removal is `!delete`, and it is the same operator at the root as at a field,
so `{tmp: !delete null}` removes a key and `patch: !delete null` removes the entity. One operator,
any depth.

That is what makes this a convention rather than a mechanism. Nothing in the engine knows how to
reap. A charter says when, and what grace period, and whether anything should happen first.

## Limits today

A condition does not compose over time. `on` and `when` are asked of the store at a revision,
so they have to be answerable at one: a match. The temporal grammar that once lived on gates
(`!until`, `!quorum`, `!fact`, `!deadline`) is refused by name wherever a charter is read; a
gate's threshold and deadline are the question's (`need:`, `deadline:`), and waiting for a
fact is the condition's own match.

A rule may declare the stratum it lives in, and then its subject must fall inside that stratum's
pattern and any `derive` must land strictly above it. Declaring nothing is a complete answer: a
charter with no `stratum:` anywhere is unconstrained. See [strata](strata.md).

A cycle across rules is caught at runtime rather than at install. Two triggers each writing what
the other selects on defeat every install-time protection here, and
[the cycle throttle](cycle-throttle.md) bounds them by causal depth instead. Everything else about
whether a charter settles is in [convergence](convergence.md), including the one rules-level
ambiguity that is warned about rather than refused.

A guard about **nothing existing** has no spelling. A match asks what is there, so "an entity exists
whose payload is not X" is sayable with `!not`, and "no entity matching M exists" — *ship when there
is no open blocker* — is not. The model is existence-positive throughout, which is why
[a departure is written down](#lifecycle-how-an-entity-leaves) rather than inferred. The same
discipline answers a `when` about absence, by making the absence a positive fact somebody writes: a
count, or a summary entity. That is a discipline rather than a grammar.

The substrate all of this leans on is [change events](change-events.md). Whether a charter written
on it settles, and what per-commit evaluation costs, is [convergence](convergence.md).

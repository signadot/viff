# The trigger model — entities, matches, and one selector grammar

One noun and two grammars carry the whole model. An entity is a node `system:kind:id`
with a small payload. A match names entities at an instant; a formula relates the
verse over time. Every trigger concept — `when`, `subject`, gates, flows — lands on
exactly one of those two, and knowing which is the thing to be unambiguous about.

This is also the field-by-field reference for a charter rule: the [tutorial](tutorial.md)
installs one and drives it, and here is what each field means.

The match vocabulary itself is [tony](https://signadot.github.io/tony-format/)'s, not
verse's: the same operators serve matching, patching and diffing there, which is the argument
made in [Structured matching, patching and diffing done
right](https://dev.to/scott_cotton_dc9ce3e7e632/structured-matching-patching-and-diffing-done-right-22ci).

## Two layers: point-in-time matches, temporal formulas

A *match* is a point-in-time tony selector over the entity-verse: which entities satisfy this
right now. There is no time in it — no `until`, no `deadline`.

A *formula* is a temporal composition of matches with operators (`quorum`, `fact`, `until`,
`deadline`, `eventually`, …), evaluated over the verse's evolution. A formula's leaves are
matches.

| concept | grammar | what it expresses |
|---|---|---|
| `subject` | match — point-in-time | the entities a firing is *about*, selected at the commit. Required |
| flow | match — point-in-time | which live tasks to steer, by their subject |
| `on` | formula — temporal | the **cause**: fire at the commit where this first holds, having not held before |
| `when` | formula — temporal | the **condition**: this must hold at that commit for the firing to proceed |
| gate | formula — temporal | proceed at a threshold — `quorum`/`fact`/`until`/`deadline` |

The cut is: name entities at an instant and you want a match; relate the verse over time and
you want a formula. `subject` and flow-addressing are point-in-time — a subject is selected at
the commit a firing happens at, never accumulated — so they are plain matches, and giving them
a `deadline` or an `until` would be a category error.

`on`, `when` and gates all relate the verse's evolution, so they are formulas. `on`
and `when` are the SAME predicate asked at different moments — `on` at C-1 and C, firing on
¬P → P; `when` at C, gating — which is why they share a grammar and a type. Cause and
condition, told apart by edge and level.

## The trigger

A rule has four parts: a name, the condition that causes it, the gates that guard
it, and the action that fires. A fifth is optional — `stratum:`, the declared level it lives
in, which constrains what its subject may be and where a `derive` may land ([strata](strata.md)).
Omitted, it is unconstrained, which is what every rule here means.

```tony
- name: review
  # what it is about
  condition:
    subject: { system: demo, kind: pr, value: { stage: open } }
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

**Everything is read at one snapshot.** The condition and the selection both come from the
store as it stood at C, so a firing cannot act on a member under a condition that had already
stopped holding. The edge is C-1 against C rather than "what the engine saw last time", which
is what makes a replayed commit — a catch-up, a restart — reach the same verdict as the live
one.

**A vacuous `on` is the ordinary shape.** The rule above has no `on`: it is considered at every
commit touching anything it reads. What ends it is not the dispatcher but the charter — an
action whose write takes its subject out of the selection fires once and then selects nothing,
and one that writes the same value again produces no commit and so raises no next evaluation.
Latch, or run at every commit; a spawning action that cannot latch is refused at install.

A trigger is fully declarative, so a charter can be installed over the wire, persisted, or
reflected as an entity, and the engine interprets it directly — there is no compiled form.

### A charter can be installed AT an address

A charter installed with `--at` is a **package**: it runs in an engine of its own, and it may
write only inside the address it was installed at.

```sh
verse trigger add --at notes notes-charter.tony    # its rules write under notes:*
verse trigger add charter.tony                     # baseline: may write anywhere
```

That is what lets two groups keep rules in one verse without arranging not to collide. Isolation
by convention holds until somebody edits a rule; a package's holds because the confinement is a
property of the ENGINE rather than of the rules currently installed in it — a `derive` landing
outside does not install, and a write that resolves outside at fire time is refused (which is
the half that matters, since a derive's address can be templated).

**A rule may SELECT anywhere it only reads.** The subject is a write when the action patches it,
and a read otherwise — so a package's rule can select a source and derive from it, which is what
most of them do:

```tony
- name: reflected           # the level the source sits at
  level: 0
  match: {system: git}
- name: ours
  level: 10
  match: {system: drift}
- name: head-moved          # in the `drift` package
  stratum: reflected
  condition: {subject: git:head:verse}    # a source: read, never written
  action: {derive: {drift: {head: {repo: $subject.value.repo}}}}
```

Two packages both reacting to `git:*` is the ordinary arrangement, not an exotic one. What each
may do about it is write in its own address.

**Reads are DECLARED.** A package reads inside its own address and whatever its `imports:` names,
and nothing else — an undeclared read does not install, and the refusal says which line to add:

```tony
at: drift                     # where this package writes
imports: [ !verse-path [git] ]  # …and what it reads besides
---
- name: head-moved
  condition: {subject: git:head:verse}
  action: {derive: {drift: {head: {repo: $subject.value.repo}}}}
```

An absent `imports:` is not "everything", it is nothing: a package with no declaration reads only
inside itself. That is the safe default and the strict one — a package that reads nothing outside
cannot interfere with anybody — and it is what makes the graph total, since every edge is written
down.

One file may hold SEVERAL packages: a `---` document carrying `at:` opens one, and everything
after it belongs to that package until the next declaration. `docs/working/comment-drift.tony` is
two — a `drift` package that reviews and a `merge` package that gates what the review delivers —
installed by one `verse trigger add --file`.

**Its levels are its own**, and so are its records: a package's rules and strata are addressed
by package (`verse:trigger:rule:drift:head-moved`), so two packages may each declare a
`reflected` level without one overwriting the other. They are not confined — a package's lowest
level owns the slice it READS. comment-drift's `reflected` owns `system: git`: its rules select from the source and
derive above it, which is the ordinary shape. What makes per-package levels coherent is the
write confinement rather than a second check on the strata: with no cross-package write there is
no cross-package derive, so there is no cross-package ordering to define, and two packages may
both declare a level 10 and mean nothing to each other.

**The import graph is acyclic.** `a` reading what `b` writes while `b` reads what `a` writes is a
feedback loop neither charter's author can see in their own file, and nothing else catches it:
the level discipline is per package, so a cross-package derive has no ordering to violate, and
the cycle throttle bounds a chain within one engine. So the declaration is also a graph — P → Q
when what P reads meets what Q may write, which is Q's address — and a cycle is refused at
install, showing the loop:

```
imports: this would close a cycle between packages: drift → merge → drift — one reads what the
next writes, all the way round …
```

Importing an address no package is installed at adds no edge: `git` is a source's slice, and
reading a source is not depending on a package. Two packages both reading `git:*` is the ordinary
arrangement, and it is not a loop.

**What it does not cover**: baseline. The unpackaged charter may write anywhere, so a loop
through it is not a loop between packages and this cannot see it — which is the same reason
baseline is unconfined in what it writes.

`verse trigger tree` is the whole charter at once — baseline and each package, what it writes,
what it imports, and how much it holds:

```sh
verse trigger tree
# - at: (baseline)   writes: anywhere   rules: 4
# - at: drift        writes: drift.*    imports: [git]         rules: 3
# - at: merge        writes: merge.*    imports: [git, drift]  rules: 5
```

**Two more things it does not confine**, stated because the boundary is easy to overstate. A SPAWN:
a task acts in the world under its `grant`, and store confinement is not world confinement (see
[provider selection](provider-selection.md)). And verse's own records — a task's mirror, a
motion, a posed question — which are written by the engine into
[verse's half of the tree](reference/entities.md#two-halves-one-tree), where no charter may be
installed and no charter may write.

A package is the same shape as a `verse connect` scope one axis over: a scope says whose COPY of
the verse a charter acts in, an address says which PART of the verse it acts on. Both stamp
their records so a restore takes only its own — and where a scope waits for its dev to
reconnect (its layer cannot be enumerated), a package comes back at every start, because its
address is a stamp anyone can list.

### The charter is durable

Installing a trigger writes it into the verse as `verse:trigger:rule:<name>` — or
`verse:trigger:rule:<package>:<name>` for a package, so two of them may use one name; removing one
deletes that entity; and a start reads them back before anything observes. So the program a
verse comes back up running is the one it went down with.

Everything else a restart needs is durable in the same way — a task is mirrored, a
sequence's position is a fact on its subject, a claim is a lease in the store, an open motion
is a channel with its ballots beside it, and resume rehydrates a chain's hooks and a motion's
awaiter from them. The program has to be too, or a verse comes back with its work in progress
intact and nothing left to react to it.

One entity per rule, because that is what the operations already are: an install writes one,
a removal deletes one, and neither reads the others — so two installs cannot lose each
other, and a rule is addressable (`verse state get verse:trigger:rule:review`).

**A rule is stored as structure, under the `!raw` escape.** The escape is what makes that
possible, and it is worth saying why it is needed. A write is a tony *patch* — the property
that makes the store good at everything else — so operator tags in a payload are EXECUTED by
the store rather than stored by it. A charter is full of them: `id: !glob "hotfix-*"`,
`patch: {tmp: !delete null}`. Stored as bare structure, a rule is therefore not one that reads
back wrong, it is one that cannot be read back at all: the write leaves an entity whose own
patch log no longer applies (`glob patching "null" gave cannot patch with glob operation`).

`!raw` says *this tag is data*: the subtree is stored verbatim, nothing
beneath it is interpreted at any depth, and the escape itself is consumed, so the rule lands
carrying its own tags. `entity.Raw` is that wrap on the way in; a rule read back is ordinary
data, which is why putting one back means escaping it again (`entity.AsData`, which is what
`entity.Modify` does for its callers — a record that holds a document would otherwise execute
its own operators the moment anything else about it changed). Raw also *replaces* rather than
merging, so re-installing a rule cannot leave a field of the version before it standing in the
version after.

The gain is that the record is the rule: `verse state get verse:trigger:rule:review` shows a
document to read field by field, a charter change is an observable fact about a shape rather
than about a string, and nothing has to re-parse to ask a question about it.

**A rule keeps the comments it was written with**, so the record carries the reasons and not only
the shape — the example above reads back with all three of its notes in place.

A comment attaches to the VALUE it is written about — above it, or beside it on the same line —
and something has to carry it: the value's own type, the field holding it, or the codec of
whatever it sits inside. Between them they cover a charter:

| a comment on | kept by |
|---|---|
| the rule, `action:`, `gating:`, `spawn:`, a gate entry | the value's own type |
| a `patch:` or `derive:` body | nothing needed — the value is stored as-is |
| `name:`, `stratum:`, `condition:`, `gates:`, `seq:`, a gate's `name:`/`mode:`/`formula:` | the FIELD holding it — which is how a value whose own type cannot carry one still keeps it |
| `subject:`, `on:`, `when:` | Condition's own codec, which is hand-written and so carries them by hand |

The two positions are one question asked twice, and both are carried: `name: review # the short
name` reads back as it was written, as `# what this rule is for` above it does.

The exception is the FIELDS OF A SPAWN — `driver:`, `prompt:`, `runner:` and the rest. A comment
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

**Goals are gone.** A goal was a target over this substrate — a `doneWhen` witness, a deadline
fail-latch, a live `waitingOn`, and owned triggers reaped on a terminal outcome — durable the
same way a rule is. Nothing installed one: every use case tried against the definition fell
short of it, and it cost a top-level concept in a model whose whole claim is that it is small.
So it was removed rather than carried. The finding and the three shapes a better definition
might take are in [Goals: what is left](working/goals-residual.md); where it is headed is
[a charter authored per change](roadmap/dynamic-charter.md).

A **scoped charter** (`verse trigger add --scope`) is restored the same way, when the scope's
engine is next built — which is on demand, so in practice when the dev reconnects. Its record
carries `scope: <name>`, which is what makes it restorable at all: a scoped store reads
baseline layered underneath, so a listing in a scope returns baseline's rules and the scope's
own, and only the record says which is which. Baseline's are passed over — a scope runs what
was installed into it (see [connect](reference/connect.md#the-charter-a-scope-runs)).

What a restart still cannot do is bring back a scope nobody has reconnected to: enumerating
the scopes that have a charter needs a scope to be a listable thing, and it is not one yet.

The charter is an observable slice like any other, so a trigger can watch the charter
(`subject: {system: trigger, kind: rule}`). It has to *name* it: `trigger:*` joins `task:*`
as the engine's own machinery, kept out of the default match set so the loop cannot ignite on
itself. Meta-observation stays possible, and stays deliberate.

## `subject` — what the firing is about, and required

`subject` is a point-in-time `Match` — never a formula. It says what a firing acts on, selected
once, from the store as it stood at the commit. There is no `until` or `deadline` here: a
subject is *selected*, not *awaited*.

It is the one field a condition cannot leave out, because nothing else in a trigger can answer
for it. The action has no target of its own, and a `when` need not pin one kind: `{system:
demo}` spans all of them, and a disjunction (`!or [a:b:c, {system: d, kind: e}]`) makes it
plain — "the entity the when matched" answers *which entity fired* when the question is *what
is this rule about*. A rule that acts on whichever disjunct happened to fire is not a rule
anyone wrote down. So the subject is always stated, and everything else in the trigger is read
relative to it.

It resolves in one of two ways, both the same match grammar:

| subject | resolves to | example |
|---|---|---|
| an address naming one entity (`demo:pr:1`) | that entity, whether or not it exists — an action's `patch` *creates* it | publishing a fact nothing has written yet |
| a pattern — a glob (`demo:pr:*`), or a `system`+`kind` selector with an optional payload predicate | every matching entity in the store, at that commit | every reviewed PR |

A subject is always a SELECTION, never "the entity that changed". A rule is a statement about
the world, so which entities it is about cannot depend on who happened to be written last —
and there is no changed entity to inherit from anyway, since the rule is asked once per commit
rather than once per change.

"When a PR opens, review it" and "review all of them" therefore settle in the same place: the
action advances each PR out of the selection, so both end with every open PR reviewed. What
differs is how many firings it takes to get there, not where it ends up.

The NAMING case is the exception that proves it. A glob-free address is a set of one *whether
or not the entity exists*, because naming is addressing by identity rather than selecting from
what is there. Read "every entity this selects" literally and creating an entity would be
unspellable: the only way to create one would be to already have it.

A subject must name a definite `system` and `kind`, or one entity outright, since that is what
the engine selects and spawns for. A pattern that selects nothing is a no-op — it never
conjures an entity called `*`.

The engine starts one firing per selected entity, each about exactly one subject, and that
identity is what every part of a firing keys on: its spawn id, its gate question, its motion
channel, its place in a causal chain. A re-evaluation about a subject already firing — or already
running work it started — is not a second firing, which is what keeps a rule waiting on a
human from asking again at every commit.

This is why `subject` is bounded where a `when` formula ranges freely: it pins `system:kind`
and predicates over the single entity's payload, rather than composing over time. "all open
PRs" is just `{system: demo, kind: pr, value: {stage: open}}`.

### The string form is an address, in one grammar

There is one string grammar for addressing entities by identity, and both the charter
wire and the CLI (`verse flow <addr>`) parse it with `ParseExpr`: three segments, `*` a
wildcard per segment, and an id that may glob (`hotfix-*`). So `"subject": "demo:pr:*"` in a
charter and `verse flow demo:pr:*` on the command line mean the same set, as they read like
they should.

There is no deferred `$var` form, and nothing in a payload can redirect a subject. A trigger
whose cause is a `verse:task:instance` change and which acts on PRs acts on the PRs its own
`subject` selects — every one of them — not on the one the completed task happened to name.

In particular, a `flow` field on the changed payload does not short-circuit that. Letting it
resolve the firing onto whatever ref it holds would advance the single PR a completed task
was about — at the price that whether a firing acts on one entity or on a whole set turns on
whether some entity happens to carry a key by that name, which no charter says and no reader
of one could predict.

What that shortcut does well — advancing exactly the subject a finished task was about —
is what [completion hooks](#completion-hooks-one-trigger-chaining-visible) do, and do exactly: the
engine scopes a hook to *that* spawn's task, a correlation the store cannot fake because the
engine is what spawned it. Reach for a hook when you mean "this task, this subject"; reach
for an `on` when you mean "when this becomes true, act on that set".

`flow` remains a convention published by `mirror.Reflect` — the field IS
the task's subject — read by the flow query, and by a rule whose subject carries it as
`$subject.value.flow`. It is kept distinct from the reserved `$subject` binding, which is the
firing's own resolved subject.

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
    when:    { system: ci, kind: status, value: { state: green } }
    subject: { system: demo, kind: pr,   value: { stage: reviewed } }
  gates:
  - { name: ship, mode: ask }
  action:
    patch: { stage: shipped }
```

Every reviewed PR advances, while CI is green. Each is its OWN firing: its own `ask` gate and —
with a `spawn` — its own task tagged with that member as its subject, so each is its own flow.

One gate per member is worth saying out loud, because one gate for the whole set is the easier
thing to assume. A firing is about one entity, so twelve reviewed PRs are twelve decisions;
presenting them as one is a summary standing in for twelve. The honest arity costs an operator
twelve answers until `verse gate approve` grows a bulk form.

Note what the `when` does here and what it does not. It is the CONDITION — while CI is green —
and the rule is re-evaluated at every commit touching either subtree, so a PR reaching
`reviewed` afterwards is picked up. Write `on` instead of `when` and you get the other
sentence: fire once, at the moment CI went green, over whatever was reviewed *then*. Both are
useful; they are different rules, and the engine warns at install when a narrow `on` leaves
part of what the rule reads uncovered.

## `on` and `when` — the cause and the condition

They are one predicate asked at two moments. `on` is asked at C-1 and at C and fires on the
rising edge — ¬P → P. `when` is asked at C and gates. Both are optional; both are formulas over
the store, with the same grammar.

**`when` is the condition.** With no `on`, the trigger is considered at every commit touching
anything it reads, and `when` says whether this is a moment to act. That is the reconciler
shape: *while* this holds, act on what the subject selects.

**`on` is the cause.** It narrows *when the rule is considered at all* to the instant its
predicate becomes true, and two properties follow that an author has to know:

- **An edge does not re-fire while it stays true.** `on: {system: demo, kind: pr, value:
  {stage: reviewed}}` rises when the FIRST PR reaches `reviewed` and says nothing about the
  second — the predicate never fell. There is no way to say "on each PR reaching reviewed";
  that is what a subject plus a latch is for.
- **An `on` trigger does not act on the backlog.** Installed over a verse where its predicate
  already holds, there is no rising edge to observe. A subject-dispatched trigger does the
  opposite, acting on everything already standing (see [level at open](#the-backlog)).

A match is a predicate over the verse. It ANDs its dimensions — `system` / `kind` / `id` / a
payload predicate. Empty dimensions are wildcards.

There is no `op` dimension. It classified a CHANGE — `set` or `delete` — and a match is asked
only of STATE now, at a revision, where nothing has one. A create is the rising edge of "this
exists"; a delete is the rising edge of the [vanishing marker](#lifecycle-how-an-entity-leaves)
that precedes removal. A charter carrying `op` is refused with that explanation rather than an
"unknown key", because the author is reaching for a distinction the model deliberately does not
carry, and "unknown key" would not say so.

The address string is sugar for the identity dimensions, so `demo:pr:*` means
`{system: demo, kind: pr}` — and since a trailing wildcard *is* an unset dimension, that is
what reads back when you `verse trigger show` it: `demo:pr`. Same match, shorter spelling.

Keep it small. This is where complexity hides. To act "only the first time", match a state
field your action then advances — a LATCH. That is not a nicety: a rule with no latch is
considered at every commit touching what it reads, so the latch is what gives it an end state,
and a spawning action that has none is refused at install.

<a id="the-backlog"></a>
**Installing a trigger fires it on what already matches.** A trigger's observation replays
the slices it reads when it opens, so the backlog standing in the verse when you install the
charter is acted on immediately; you do not have to re-touch each entity to announce a state it
has held all along. (An `on` is the exception, and for the same reason it does not re-fire: a
predicate that already holds has no rising edge.) The same property
means the latch above is what stops an action re-running when the engine restarts over a verse
still in the matching state; see
[change events](change-events.md#the-seed-is-replayed-a-subtree-watch-is-level-triggered-at-the-open).

`on` and `when` may also grow *temporal* operators (`until`, `deadline`, … — the same language
a gate formula uses), at which point they relate the verse's evolution rather than being asked
at a revision. That is not evaluated yet; see [Limits today](#limits-today).

## What a match can say

The leaf is a real tony match, evaluated by `tony.Match`, on every dimension a filter can
live on.

`id` takes operators — the richer matching a plain glob cannot spell:

```tony
# filepath globbing
subject: { system: demo, kind: pr, id: !glob "hotfix-*" }
# everything but one
subject: { system: demo, kind: pr, id: !not "1" }
# composed
subject: { system: demo, kind: pr, id: !or ["1", !glob "hf-*"] }
```

In a JSON-shaped charter, write the operator as a single-key object — `{"id": {"glob":
"hotfix-*"}}` — the same spelling a formula uses for its tags. The two are one grammar; only
tony can write the tag directly.

`system` and `kind` take no operator, and that is structural rather than unfinished: they
name the subtree verse watches and the set it lists — they become a docd watch path
(`Formula.Subtrees`) and the store's `List` prefilter — and a watch path cannot be a
predicate. Write the whole segment as `*` for "any", and put the operator on `id`.

`at` is the whole address prefix as ONE covering path, which is the same three dimensions
written once:

```tony
subject: { at: demo.pr, value: {stage: open} }        # {system: demo, kind: pr}
subject: { at: verse.gate.question }                   # …and in verse's own half
subject: { at: !verse-path [demo pr], value: {…} }     # the same, as steps
```

`!verse-path [demo pr 1]` is an address written as STEPS — the spelling `verse state --paths`
answers with and `verse state` takes, so an address read off a listing pastes into a charter
without being re-punctuated. It goes wherever the string form goes:

```tony
subject: !verse-path [demo pr 1]      # the same as "demo:pr:1"
when:    !verse-path [git head verse]
```

The tag is required on a list, so a list of steps cannot be read as anything else — and an
address is written one way or the other: `{at: demo.pr, system: other}` is refused rather than
merged.

`root` names which half of the verse the match is about, and an unwritten one is the world's:

```tony
# a rule about PRs — the ordinary case, and it cannot see verse's own records
subject: { system: demo, kind: pr }
# a rule about what verse writes: a posed question, a task's mirror, an installed rule
subject: { root: verse, system: gate, kind: question, value: {status: pending} }
```

The default is the point rather than a convenience. `verse:` holds what verse writes about its
own running ([entities](reference/entities.md#two-halves-one-tree)), and a rule that
did not ask for it must not select it by omission — otherwise `{system: task}` on a charter
about the world's tasks would quietly match the mirror of every run in the verse. Like `system`
and `kind`, it takes no operator: it is the first segment of a watch path.

The payload predicate takes operators too. It is `value`, one dimension: the same tony match,
evaluated against the payload instead of the id.

```tony
# a record's fields
when: { system: demo, kind: pr,    value: { stage: !or [open, merged] } }
on:   { system: demo, kind: pr,    value: { branch: !glob "hotfix-*" } }
# the payload ROOT
when: { system: demo, kind: count, value: !or [10, 100] }
```

`value` is open in shape because a payload is: an entity's payload is any tony value — a
record, a count, a flag, a state name, a list — so a predicate over it may be any of those,
or an operator over one. Almost every charter writes the record form; the rest are why the
dimension is not called `fields`.

Over a record it is a **subset** constraint, as a tony match over an object always is: the
fields you name must agree, and any others are ignored. Over a scalar there is nothing to be a
subset of, so `value: 42` means the payload *is* 42.

That cuts one way worth knowing: `value: "stage=open"` is a legal predicate — it matches an
entity whose whole payload is the string `stage=open` — not an expression language. There used
to be a `fields` dimension that asserted an object and made exactly that mistake an install
error; it went away with the collapse to one name, because it caught only the wrong *shape*
and never `{stge: open}`, the misspelled key that is the typo people actually make.

Four semantics are worth knowing before you rely on one.

`!glob` is stricter than a plain string. It is `filepath.Match`, so `*` does not cross `/`
and `?`/`[…]` are metacharacters: `id: "a*"` matches `a/b` and `id: !glob "a*"` does not.
That is the point of it — a path-shaped id can be matched a segment at a time — but it means
the two spellings are not interchangeable.

A `null` in a predicate is a presence test, not "is null". A null pattern matches any value,
so the only thing it constrains is that the field is there. The three questions have three
different spellings:

```tony
# stage is PRESENT, whatever its value
value: { stage: null }
# stage is ABSENT
value: !not { stage: null }
# stage is present AND its value is null
value: { stage: !irtype null }
```

An absent key is not a null one — the store records whether a field was cleared or never
written.

Numbers are type-exact: `2` and `2.0` are different values, so a predicate written `{n: 2}`
does not match a payload of `{n: 2.0}`. Integer widths do not matter — that is an artifact of
whoever wrote the value, not of the value.

Operators inside a payload predicate are tony-only. The id dimension accepts the JSON
single-key spelling because its keyspace is closed, but a payload is *your* document — a key
in it may itself be named `or` — so that sugar cannot be borrowed here without changing what
some charter already means. A JSON charter can still write plain equality predicates; an
operator over a payload needs tony.

## `gates` — whether it proceeds

A list of gates, all of which must resolve to proceed (conjunctive) before the action fires.
Gates are per *firing*, and a firing is about one entity — so a rule selecting twelve PRs asks
twelve questions. Each is `{ name: …, mode: auto|ask|park }`. An empty list fires with no
approval.

The question's id is a function of (trigger, gate, subject), which is what makes a re-evaluated
rule ask ONCE: a firing that finds its question already standing joins it rather than posting a
copy, and one answer releases everyone waiting. A rule waiting on a human is therefore not a
rule filling the docket.

Multiple gates model multi-approval — a deploy needing both a security and a release-manager
sign-off is two `ask` gates. A gate can also carry a `formula` the verse resolves itself — a
vote, a deadline, a threshold — which is the temporal layer of this document at work on the
guard.

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

It is an ordinary action — `patch`, `derive`, `spawn`, its own completion hooks — applied about
the same subjects the firing was about. It hangs off the GATE rather than the action because a
no is the gate's outcome: two gates refuse for different reasons, and each says what its own
refusal means. It is not a completion hook and needs no spawn of its own.

Only a committed refusal runs it. A resolver that could not be reached, or a monitor that ended
before its motion committed, said nothing at all — and writing a rejection on their behalf would
put a decision in the store that nobody made. It applies on both arms: a posed `ask` that is
answered no, and a `formula` whose motion commits a deny.

Omitting it is legitimate — "ask again next time somebody touches it" is a real policy — so a
gate without one earns an advisory at install rather than a refusal. To say a no is final
without changing the subject's own state, the vanishing-marker shape works:

```tony
  condition:
    subject: { system: demo, kind: pr, value: !and [ { stage: open }, !not { blocked: true } ] }
  gates:
  - { name: approval, mode: ask, onDenied: { patch: { blocked: true } } }
```

Removing the marker is then what re-opens the question.

### A motion outlives the verse that opened it

The channel, its options and every ballot cast into it are entities, so a restart finds the
motion exactly where it stood. What does not survive is the *awaiter* — the goroutine watching
the tally was inside the firing — and a motion with no awaiter is the worst kind of docket
entry: it lists a decision, somebody makes it, and nothing moves.

Startup gives the awaiter back, by the route the axis makes available:

- A **whether-gate** motion blocks the firing itself, and the firing comes back by being
  *evaluated again*: its action never ran — that is what the gate is holding — so a rule that
  still selects the subject re-monitors the same channel, and the ballots already cast still
  count. Re-evaluated, not resumed, which is the distinction the next paragraph turns on.
- A **who-select** motion (a `spawn` with a `provider`) opens *after* the action has written
  its latch, so nothing re-fires it. It records the `chain` of the action it is blocking, and
  startup navigates that chain back into the charter and re-enters the selection — the same
  lookup key a resumed task's hooks are rehydrated from.

A motion nothing can answer any more is *settled* rather than left standing: its rule left the
charter, its action moved, its subject is gone — or, the case that is easy to miss, **the
subject moved out of what the rule selects**. A merge proposal swept to `state: stale` is no
longer picked by a rule that reads `!not.has-path state`, so that rule will never look at its
gate again however healthy the rule is. The motion is stamped `status: orphaned` with a `why`
and a `when`, keeping the trail while taking it off the list of things a person can act on.
That is not a verdict — the channel carries no `outcome`, because nobody decided anything — so
a rule that selects the subject again reopens the same channel over the same ballots.

The resume is idempotent by construction, which is what lets it run beside a rule that also
re-fires: the channel id is derived from the subject and the spec, the commit is terminal-once
under a revoke guard, and the spawn id is deterministic per (chain, subject). One motion, one
task, however many paths reach it.

**A scoped verse (`verse connect`) is not covered.** Its motions live in the scope's own store,
which nothing enumerates at startup — the same gap scoped charters and scoped claims have.

**A verse that dies between committing a motion and starting the work it decided loses that
spawn.** The channel is closed, so nothing resumes it. The resume does not introduce this: it
is the window `patch` then `spawn` already has inside one action. An action is not atomic, and
a crash in the middle of one leaves the half that landed.

### A gate's answer applies to the world it was asked in

A motion is not settled the moment it is satisfied. Between reaching its threshold and
committing, the verdict is rechecked — and until this it was rechecked against the wrong thing.

The **revoke guard** covers the ballots: the witness that carried a `proceed` must still stand
at the commit, so a retracted approval aborts it and the motion goes back to monitoring. That
is the right rule for the answer. It says nothing about the *question*.

So the commit also asks whether the rule would still select the subject. If it would not, the
question is moot: the motion is **withdrawn** — `status: orphaned` with a `why`, no `outcome`,
no action, and no `onDenied`, because a moot question is not a refusal. The ballots stay in the
log, and a rule that selects that subject again reopens the same channel over them.

This is not a hypothetical tidiness. A merge proposal was swept to `state: stale` while its
gate stood open; the motion, watching only its vote log, reached quorum on the approval already
cast, committed `proceed`, and merged a branch the sweep had retired — losing the work on it
(see [merge-gate-residual](working/merge-gate-residual.md)). A gate that can only be answered
about a world that has moved on is a gate answered by reflex, which is the failure gates exist
to prevent.

Two limits worth knowing. The check runs **at the commit**, not continuously, so a motion whose
subject has already left the selection stays listed until somebody votes or the verse restarts
— it is honest about the answer, not yet prompt about the listing. And it asks only about the
SUBJECT, not the cause: a rule fired by an `on` edge may still select its subject and never see
that edge again. When it cannot tell — an unreadable store, a predicate whose read fails — it
lets the motion stand, because a stale docket entry costs a reader a moment while a wrongly
withdrawn one takes a live question away from the people it is in front of.

## `action` — what fires

Data, not code. An action needs no target of its own: it acts on the condition's subject.

`patch` writes to the subject's payload — a state transition. It is a tony patch, not an
assignment: a nested write merges into the subtree it names rather than replacing it, so two
triggers writing under one key do not clobber each other, and the
[mergeop](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop) operators
apply. `patch: { tmp: !delete null }` removes a key, which is something no amount of setting
fields could say. Writing a scalar (`patch: 42`) replaces the whole payload.

`derive` writes an entity that is NOT the subject — the one thing `patch` cannot do. It is
addressed `<system>.<kind>`, its content may reference its own subject and — at or below its
own level — other entities, and the derived entity's
id is the engine's, a function of (rule, subject). It is only installable inside a declared
level ordering, where it must land strictly above the rule that writes it: see
[strata](strata.md).

One firing derives one entity — until it says `!each`, which fans out over a list and derives
one per element, addressed by a key read out of the element. That is the same invariant with
the key in the address rather than an exception to it, and it is where a task's twelve findings
become twelve facts instead of eleven paragraphs: see
[strata](strata.md#each--when-one-answer-holds-many-facts).

`spawn` launches a task from a `launch.Spec` — or, instead of a fixed `driver`, a governed
`provider` selection of the doer. The spawned task is tagged with the subject, which is
what makes it part of that subject's flow.

A selection binds the whole doer, not just its driver: the winning provider entity's `driver`,
`frame`, `tools` and `grant` overlay the spawn's own, so two doers on one driver can differ in
what they are told and what they may do. The overlay only ever narrows — a doer's grant must
be within the spawn's (which is why `grant:` is required on a spawn that selects), its tools
intersect rather than replace, and `vector` is not a doer's to set. A doer that would widen is
refused at *eligibility*, so it never appears on a ballot. See
[provider selection](provider-selection.md).

The spec's fields fall into three groups, and the difference decides what a charter can say.

*The request* — `prompt` and `frame`. Both are text you write. `prompt` is the one job; `frame`
is the standing instructions it is read in (the agent's system prompt). A frame is worth
writing whenever the charter reads the answer rather than a person: the default tells the
agent its last message *is* the result and not to summarise, but only the charter knows what
shape it wants back.

*The contract* — `answer`. This is the shape the final message must have, and unlike the
frame it is not advice: it is checked. See [the answer contract](#the-answer-contract) below.

*The envelope* — `vector`, `grant`, `tools`. These name things rather than spelling them out.
`vector: auto|ask|park` sets how much the task must confirm; a firing that already
answered its own `gates` can say `vector: auto` rather than re-ask per action.
`grant: read-only|no-delegate|none|all` is the hard authority: an action outside it is
*refused*, not asked about, because nobody at a console was delegated the authority to widen
what the charter granted. `tools` narrows what the driver offers at all — `tools: [read_file,
list_dir]` on a review agent means shell and delegation never come up. `fail` is always
offered whatever you list, since it is how a task reports it could not do the work.

An unknown `vector`, `grant`, or tool name fails the spawn rather than falling back to
unbounded: a field that reads back as written and enforces nothing is the failure this is
built to avoid. `resolver` names the binding that answers this run's gates, and a spawn naming
one that is not installed is refused rather than left unsupervised — see
[attached resolvers](attached-resolvers.md). `environment` remains declared and unread — see
[authority](roadmap/authority.md).

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

`retain` is how many of this task's read-plane events are kept for a reader that resumes —
the window a `?since=` below which gets a resync marker instead of the story. It is per spawn
because the right number is a property of the work: a task that narrates every token and
one that reports four boundaries an hour want windows two orders of magnitude apart, and no
single default is right for both. The charter is the party that knows which it is. Omit it and
the runtime's own modest default applies; a longer window is paid for in memory per live
task, which is why the author chooses it rather than inheriting it.

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
each entry becomes an ordinary action: derived, gated, and narrated the same way, so a gate
written for an agent's push holds this one. Values are expanded per argument, so a `$branch`
containing a quote is an argument rather than something that has to survive being spliced into
text.

An entry may narrow what ends the list:

```tony
  - tool: push
    input: {destination: retire}
    fatal: [err]        # a remote declining ONE ref is not a reason to stop
```

out of `err` (the action failed), `blocked` (refused, by a gate or by the far side) and `nonzero`
(a command exited non-zero). Absent means all three — a list is ordered because its calls depend
on each other — and `fatal: []` means this call ends nothing, which is what a sweep says. The two
are different statements, so writing neither is not the same as writing the empty one. A list with an
unknown outcome, no tool, or a contradicting driver fails the FIRING, where its author can read
why; a tool name it cannot check belongs to the runner and is refused there.

What a call list does not get is what belongs to a model: no `answer` contract (there is nobody
to repair one, so a spawn carrying both fails), no delegation, and no deciding what to do next
from what the last result said. That last one is the trade: it is right for four git commands
and wrong for anything that branches.

### Who answers a rule's own gates

```tony
gating:
  resolver: verse:resolvers:security:security-review
  gates:    [{name: ship, mode: ask}]
```

`gating` groups the two facts about a rule's gates: which they are, and who answers them.
`gates:` at the top level still works and means `gating: {gates: …}` — one field read two
ways, not two that can disagree — and writing both is refused rather than merged, because a
rule that says which gates it has twice has said it twice.

`resolver` names an installed binding (`verse resolver add`). It is the same word a spawn
uses for the same act: a spawn names who answers the run it starts, a rule names who answers
the gates it raises itself. Empty leaves the question to whatever standing binding covers its
subject, and then to the docket a person reads. A binding that is not installed is refused
here rather than left to own a question nothing can answer — see [attached
resolvers](attached-resolvers.md).

`seq` runs a list of actions in order, each starting when the previous one completes. It is
the depth axis, and it has its own section below.

### Bindings

Every field of **the payload an action reads** is bound by name (`$stage`, `$path`, …), plus
`$subject` — the resolved subject. Which payload that is has exactly two answers, and which one
applies is a property of *why* the action is running.

A trigger firing reads **the subject's own** payload. The change that dispatched it is a
doorbell and may name an entirely different entity, whose fields would mean nothing on the
subject being acted on. So `review $subject` in a spawn's prompt names the PR the firing is
about, even when the entity that fired was a task, not the PR, and `$stage` is that PR's
stage rather than the task's.

A completion hook, or a sequence element one drives, reads **the finished task's** payload
instead: `$status`, `$result`, `$data` and `$err` are the facts it exists to read. The engine
synthesises that payload rather than reading the store, because the mirror may not have written
the terminal facts yet — which is both why those bindings resolve at all and why they are flat.
They are facts about a run, with no entity to hang a path off.

`$subject` is a record, and the bare form is its address — so everything written before it had
parts still means what it meant:

| `$subject` | the whole ref, `demo:pr:1` |
| `$subject.ref` `.system` `.kind` `.id` | the address, and its three segments |
| `$subject.value` | the subject's payload **at the commit that caused the firing**, not at the moment the action runs — the two differ by however long a gate took to answer |
| `$subject.commit` | that commit's revision |

A completion hook has no causing commit — it is woken by a task finishing rather than by a
write — so `.value` and `.commit` fail there by name rather than resolving to a silent zero. In a
spawn's strings the identity parts bind flatly; `.value` does not, since a payload has no text
form and its top-level fields are already bound by name.

### `!get-path` and `!list-paths` — reaching another entity

Everything above reads the firing's own subject, or the task that just finished. These two
reach an entity neither of those is:

```tony
action:
  patch:
    repo:     !get-path { system: git, kind: repo, id: $subject.value.head }
    branch:   !get-path { system: git, kind: repo, id: $subject.value.head, path: branch }
    branches: !list-paths { system: git, kind: repo, id: "*", path: branch }
    # …and a predicate narrows the walk to the ones that look like this (list only)
    green:    !list-paths { system: git, kind: repo, id: "*", value: {state: green}, path: branch }
    # the string form addresses an entity and stops there
    also:     !get-path "git:repo:$subject.value.head"
```

**One grammar, split by cardinality, and the names say which.** A verse is one document —
`{system: {kind: {id: payload}}}` — so an entity address is a path prefix, and `path:` continues
it into the payload. `!get-path` answers with one node; `!list-paths` answers with a list, and is
where the globs live. They mirror go-tony's own split, where `GetKPath` returns one node and
rejects a wildcard outright while `ListKPath` collects.

Which one you get is legible in the charter rather than inferred from whether some id happened to
contain a `*`: `!get-path` **refuses** a globbing id or a wildcard path, and names the tag that
answers that question instead.

| | |
|---|---|
| `system`, `kind` | always literal — no reference, no glob |
| `id` | literal, a `$reference`, or (list only) a glob |
| `path` | a kpath into the payload; wildcards are list-only. Omitted, you get the whole payload |
| `value` | a payload predicate, **list only** — of the ids the address selects, the ones that look like this |

`!get-path` refuses a `value` for the reason it refuses a globbing id: a predicate can name
several, and an entity that is not there does not satisfy one — while one that is there might not
either. On the list form a predicate is the *cheaper* read, because `id: "*"` already walks the
whole kind and this is what narrows it. Neither form is a query that slides: both are answered
from one snapshot at the causing commit, so a predicate is evaluated against the same world the
rule was.

The string form stops at the entity, because an id may itself contain dots and
`"git:repo:abc.branch"` has no unambiguous split. Going deeper is the object form's `path:`.

**Read at the commit that caused the firing**, the same rule `$subject.value` follows and for the
same gate reason.

**They copy, and do not track — in an ACTION.** Whatever is read is read once. If it changes
afterwards nothing re-runs; this rule fires again only when its own subject changes, or when an
`on`/`when` it carries covers the target. Add the `on` if you want to follow it.

### A condition may read too, and that one DOES track

A read in `condition.subject`'s predicate is answered before the predicate is matched, so a rule
can select on how its subject compares to another entity — the thing a match alone cannot say,
since a match tests a field against a literal or an operator and never against a value read from
elsewhere. `!let` binds the read, and the body compares:

```tony
- name: proposal-outrun
  condition:
    subject:
      system: merge
      kind: proposal
      value: !let
        let: [{tip: !get-path {system: git, kind: head, id: verse, path: sha}}]
        in:  {state: open, base: !not .[tip]}
  action:
    patch: { state: stale }
```

Every open proposal cut from a commit that is no longer the tip of main — "which of these can
still land", with no ancestry walk and nothing to poll.

**It joins what the rule reads**, which is the difference from an action's read and is not a
detail: the rule above is re-evaluated when `git:head:verse` moves, over proposals that did not
change. A trigger is skipped at a commit that touched nothing it reads, and a condition comparing
against another entity reaches a different verdict the moment that entity moves — so the read
counts, or the skip would be wrong.

**It cannot name `$subject`.** In an action the subject is settled and addressing by its fields is
the point; a condition is what CHOOSES the subject, so there is no one entity to read from yet.
Refused at install, saying that rather than "unknown reference".

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

That failure is late. `$data.class` is missing at PATCH time — after the work is done and
the tokens are spent — and what is left is a hook error on the task entity and a subject
stranded at a stage no rule matches. A frame asking for a shape does not prevent it: an
agent that has just read nine files narrates and fences before it answers, and the frame is
advice a model may ignore.

`answer` is the same request made enforceable — the shape the final message must have,
written as a tony **match pattern** (the `accept` clause of a schema, which is the same
vocabulary `when` is written in, stored the same way, under the same `!raw` escape):

```tony
spawn:
  driver: llm
  prompt:   "Review $subject."
  answer:
    class:    !or [bug, nit, risk]
    severity: !or [low, high]
    why:      !irtype string
```

It is checked when the model stops, which is the only moment it can be: a run ends by the
model ceasing to call tools, so stopping and reporting are one act. A failure goes back to
the agent as a **diagnosis** — the path, what was expected there, what was found, absent kept
apart from present-but-wrong, and every such failure rather than the first — and it is asked
to fix it. That is the match's own account of how it walked (`tony.Explaining`), so a failure
inside an operator is located as precisely as the operator knows it: `findings[1].severity`,
not "findings". That is bounded at two
attempts: if a model cannot produce the shape with the complaint in front of it, more turns
will not teach it.

A run whose answer never satisfies its contract **fails**. That is the whole point:

- `patch: { class: $data.class }` cannot fail at patch time, because a task that
  could not produce `class` did not succeed. The failure lands not on a hook nobody watches
  but on `onFailed`, which every rule already declares a branch for.
- The check is on the answer, not the layout: a fenced answer is unwrapped, so the repair
  budget is spent on content.
- It is a guarantee about SHAPE, not about content. A repair loop pressures a model toward
  compliance, so the agent is also told to call `fail` rather than invent a value it could
  not determine — an honest failure is worth more than a well-formed wrong answer.
- A sub-agent (`call_agent`) does not inherit it. A child answers a subtask its parent
  worded, and its result comes back as one more tool result; only the parent's own final
  message is what a charter derefs.

Writing a contract the driver cannot parse fails the firing where it is written, before a
model call — same as an unknown tool name. A driver with no model to ask again (`scripted`)
fails the spawn rather than running it unchecked, because a contract silently dropped is
worse than none: the charter downstream was written believing it holds.

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

This is what closes the loop. Before it, a task's output could reach the next *prompt* and
nothing else, so a flow only ever advanced through state the charter wrote by hand. A result
patched onto the subject is an ordinary fact, and an ordinary condition matches it — no
knowledge of tasks required:

```tony
subject: { system: demo, kind: pr, value: { stage: reviewed, severity: high } }
```

Four semantics are worth knowing.

*Whole value versus interpolation.* A value that is **exactly** one reference resolves to
that reference's node, so a number stays a number and an object stays an object. A reference
**embedded in text** interpolates and yields a string, because that is how it was written.
The distinction is load-bearing: payload predicates are type-exact, so a `3` flattened to
`"3"` is a value `value: {count: 3}` does not match.

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
recording an absent key as different from a null one: omitting it would launder "the task
never said" into "the task said nothing", which a match reads as a different fact. A field
written null resolves to null, as it should. The failure is the same one a failed write takes
— the action stops, and its spawn does not start.

*There is no escape.* `$$` is not a literal dollar; `a$$b` references `$b`. Anything that is
not a well-formed reference is left exactly as written (`$5.00`, `100$`, a bare `$`), which
is what keeps a charter that predates patch expansion meaning what it did.

A spawn's strings are still the older, flatter substitution: top-level fields only, always as
text, and an unresolved `$var` is left in place rather than failing. So `$verse.review.step`
derefs in a `patch` and does not in a `spawn.prompt`. Unifying the two halves onto one
resolver is the obvious next step and deliberately not taken here, because making a spawn
fail on an unbound variable would change what already-installed charters do.

A field holding a **document** renders as tony there, and that is the one place the two halves
disagree on purpose. A patch refuses to give a composite a text form, because `"saw $result"`
would make one charter mean different things depending on what a task returned. A prompt is text
by nature — there is no whole-value form to reach for instead — so the question is only *which*
text, and the answer used to be Go's formatting of a decoded map. A charter that puts a ledger in
front of an agent now puts a document there:

```tony
prompt: "Outstanding findings:\n$findings\n\nThe diff:\n…"
```

Leaves are unchanged, which matters more than the feature: `$stage` and `$count` bind exactly the
bytes they always did (`3`, never `"3"`). A null field is the single exception — it used to bind
as `<nil>`, which was a zero value leaking rather than anything anybody chose, and is now empty.

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

The alternative is a *decoupled* completion trigger — a separate rule that fires on the
`verse:task:instance` change and names the PR by pattern (`reviewed`, below). That is the tool
when a completion should fan into several reactions, advance a different subject, or be
authored separately; the hook is the tool for a linear chain owned by one trigger, with the
task step visible inline.

The four hooks are named fields rather than an `on map[status]*Action`, which is what makes
an unknown status unwritable: there is no field to write it in, so nothing needs validating.
The cost is that a fifth terminal status would be a schema change, and that a misspelled
`onDoen` is dropped like any other unknown field.

Hooks survive a restart: resume rehydrates them from the task's chain, so `done`,
`failed` and `cancelled` still fire. `onOrphaned` is inert, and not for want of
rehydration — orphaning and adopting are the two alternatives in reconcile, so a task
that can be re-attached is adopted and never marked orphaned, and one that cannot is
orphaned and never tracked. No tracked task reaches that status.

### `seq`: sequence along the chain, parallel across triggers

Nesting `onDone`s expresses a chain, but it right-drifts and — the reason that matters — has
no arity. The engine knows there is a next hook; it never knows how many remain, so a
sequence written that way cannot say where it is. `seq` spells the depth axis directly:

```tony
- name: review
  condition:
    subject: { system: demo, kind: pr, value: { stage: open } }
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

There is deliberately no `par`. Breadth is what independent triggers matching the same
condition already do, and a task id already carries both directions: `review@demo:pr:1`
and `hold@demo:pr:1` are two roots about one subject, while `review.onDone@demo:pr:1` is an
extension of one chain. Putting breadth *inside* a chain would cost that reading.

The rules:

- `seq` and `spawn` are mutually exclusive on one action, or "does the sequence wait for the
  spawn?" is a coin flip. `patch` alongside `seq` is fine: the patch applies, then the
  sequence runs.
- The sequence owns the `done` slot of each element, so an explicit `onDone` on a non-last
  element is an install error — the list already says what comes next. `onFailed` /
  `onCancelled` / `onOrphaned` remain per element as off-ramps, and firing one ends the
  sequence.
- `onDone` on the action *carrying* the seq means "after the whole sequence", so a sequence
  is a unit with its own completion and nesting composes.
- An element with no spawn completes immediately, so `seq: [{patch: …}, {patch: …}]` is two
  ordered writes rather than an error. `seq` sequences actions, not tasks.
- Gates gate the sequence, not the step: gates are per firing and a sequence is one firing,
  so one `ask` approves the whole thing.
- No chain may contain two `seq` segments — stated over chains rather than elements, because
  an element's `onFailed` holding a sequence nests just as much, only further away. With at
  most one `seq` per chain, "the sequence this task belongs to" is a unique ancestor
  rather than a stack, which is what keeps re-fire idempotence a predicate rather than a
  search.

### Chains and ids

A spawned task is identified by `<chain>@<subject>`, where the chain is the
[kpath](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath) of the action
inside its trigger — dotted field names, brackets for list positions. Hooks descend by field
name and sequence elements by index:

| the action | chain | id for `demo:pr:1` |
|---|---|---|
| the trigger's own | `review` | `review@demo:pr:1` |
| its done-hook | `review.onDone` | `review.onDone@demo:pr:1` |
| a nested done-hook | `review.onDone.onDone` | `review.onDone.onDone@demo:pr:1` |
| the third sequence step | `review.seq[2]` | `review.seq.2@demo:pr:1` |
| that step's failure branch | `review.seq[2].onFailed` | `review.seq.2.onFailed@demo:pr:1` |

Because the chain addresses the action rather than naming it privately, it is deterministic,
which is what makes a re-fire idempotent — a spawn dedups by id. It is also a lookup key back
into the charter, which is how resume rehydrates a task's hooks and how a sequence finds
its next step. And it is published on the task entity as `chain`, with its root as
`trigger`, so "every task of this trigger" is a payload predicate rather than a glob over
ids.

The id drops the brackets because they are shell-hostile: `zsh` kills
`review.seq[2]@demo:pr:1` with `no matches found` before verse ever sees it, and ids get
pasted onto command lines constantly. The dotted form is lossless because `Action`'s field
set is closed — a segment after `seq` is an element, a segment matching a hook name is a
field. The `@` join replaced an earlier `-`, which could not be decoded back because trigger
names spend `-` too.

### Progress facts

A parallel branch is woken by a condition, and a condition matches facts — so a sequence
publishes its position, or the breadth axis cannot observe the depth axis at all. The engine writes it
on the subject under one reserved `verse` root as each element completes:

```tony
verse:
  charter:             # what the engine records about RULES acting on this entity
    review:            # keyed by trigger
      step:  2         # how many elements have completed
      steps: 3         # len(seq), known at install
      shape: "a3f1…"   # hash of the declared shape, known at install
      done:  true      # latched when step reaches steps
      ended: failed    # …or the terminal status an off-ramp stopped it at
  meta:                # what verse records about the ENTITY itself
    vanishing: "2026-07-29T13:04:05Z"   # the intent to remove it — see Lifecycle
```

A join reads it at whatever depth it cares about:

```tony
subject: { system: demo, kind: pr, value: { verse: { charter: { review: { done: true } } } } }
```

One reserved name, not a family. The alternative — flat per-trigger keys like `review.step` —
reserves an open-ended set of names out of a payload that is the author's own document. A
single `verse` root reserves one name and documents once, the way `flow` is one reserved key
on task entities. Nested predicates keep it readable, since `value` matches structurally
and so matches a payload carrying any amount of other content.

The two halves under it are separated because they grow differently — `charter` gains a key per
rule, `meta` a field per lifecycle concern — and because flat they collided: a trigger *named*
`vanishing` would have written its progress exactly where the lifecycle marker goes.

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

`shape` is a hash of the declared sequence, compared on each advance. A mismatch means the
charter moved underneath, and the sequence fails with a reason rather than silently running
whatever now sits at `seq[k+1]`. It is a hash rather than a pinned copy because these
sequences can run for a long time, and a retained snapshot per live sequence is a retention
floor the store can never compact past. Refusing to continue needs detection, not
reconstruction — so the error can say that the shape changed and name the trigger, but not
what changed.

## Flow — a subject-match over live tasks

A flow is not a stored key. It is a match evaluated over the subjects of *live* tasks:

```sh
verse flow pause demo:pr:1      # tasks whose subject matches demo:pr:1
verse flow pause demo:pr:*      # …the whole kind
```

Relating tasks is what a match *does*, so "relational flows" are not a second primitive
to add later — they are broader subject-matches. `verse flow` is the distinct subjects with
live tasks; steering applies the match and acts on the matched tasks. A query, not a
maintained index.

## Two rules, read out loud

```json
{ "name": "review",
  "condition": { "subject": { "system": "demo", "kind": "pr", "value": { "stage": "open" } } },
  "gates":     [ { "name": "approval", "mode": "ask" } ],
  "action":    { "patch": { "stage": "reviewing" }, "spawn": { "driver": "scripted", "vector": "auto", "prompt": "review $subject" } } }
{ "name": "ship-green",
  "condition": { "on":      { "system": "ci",   "kind": "status", "value": { "state": "green" } },
                 "subject": { "system": "demo", "kind": "pr", "value": { "stage": "reviewed" } } },
  "action":    { "patch": { "stage": "shipped" } } }
```

`review` reads as *while a PR is open, review it*: no cause named, so it is considered at every
commit touching `demo:pr`, and its patch advances each PR past `open` so it settles. Note that
its spawn is allowed only BECAUSE of that patch — a spawning action with nothing to latch on is
refused.

`ship-green` reads as *at the moment CI goes green, ship everything reviewed*. It fires once,
at the edge, over the set as it then stood. Written with `when` instead of `on` it would say
*while* CI is green, and pick up a PR reviewed afterwards.

## The commit is the unit

A trigger's rule is a question about the store at a revision, not about one entity, so it is
asked once per COMMIT. A docd commit is atomic across many branches, and `LogdStore` reports
it whole: one `Commit`, carrying one change per entity at path-depth `system/kind/id`.

That framing has to come from the store, because it cannot be recovered downstream. Changes of
one commit share a revision, but a consumer reading them one at a time cannot tell whether the
next belongs to the same commit without waiting for the one after it — while the store, which
built them from a single delta, knows exactly.

What the engine does with the changes is decide whether asking is worth it: a trigger is
skipped at a commit that touched none of the subtrees it reads. That skip is exact rather than
a heuristic. If nothing it reads changed then `on(C) = on(C-1)` — no edge — and a vacuous rule
would evaluate the same predicates over the same state and write the same values, which produce
no commit. Either way, indistinguishable from not running, which is what makes per-commit
evaluation affordable.

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
    on:      { system: demo, kind: pr, value: { verse: { meta: { vanishing: null } } } }
    subject: audit:record:latest
  action:
    patch: { saw: "a departure" }
```

Match the marker's PRESENCE (`vanishing: null` is tony's present-any-value idiom), not its
value. The value is a timestamp today and may grow into a record — who asked, why, a grace
period — and a charter that matched on a particular value would have to be rewritten when it
does.

Three things follow, and the first is the one to know:

**A vanishing entity is not a subject.** A rule acts on what it selects — patches it, spawns
about it, raises a gate on it — and doing any of that to something on its way out is work about
something that will not be there. So `subject: {system: demo, kind: pr}` skips it, and naming
the marker is the opt-in that sees it again — which is how the reaper works.

A NAMED subject (`demo:pr:1`) is skipped too, and this is the one place naming does not beat
selecting. You may name something that does not exist yet; you may not name something on its
way out. Otherwise a rule reacting to a departure acts on the departing entity, and since a
patch over an absent entity CREATES, a removal landing first means the rule RESURRECTS it —
back as an ordinary live entity, stripped to whatever the rule wrote, with no marker on it.
React to a departure by writing somewhere else: an audit record, an archive. The thing that is
going is not a subject.

The raw read (`verse state`) still shows it — making the intent visible is the whole
point; only the rule-facing selection hides it.

**The removal itself is inert.** When the entity finally goes, a predicate over it falls from
true to false, and nothing fires on a falling edge. The reactive moment was the intent; the
removal is bookkeeping.

**The reaper is an ordinary trigger.** It selects what is leaving — by naming the marker — and
removes it. `!delete null` is the removal, at the root exactly as at a field:

```tony
- name: reap
  condition:
    subject: { system: demo, kind: pr, value: { verse: { meta: { vanishing: null } } } }
  action:
    patch: !delete null
```

A bare `patch: null` used to remove too, and now writes the payload `null` — an entity that
stands there holding nothing, rather than one that is gone. That was never a second way of
saying removal so much as the store having no way to hold a null, and it cost `!delete` its
generality: `{tmp: !delete null}` removed a key while removing the whole entity was spelled
another way entirely. One operator, any depth.

Which is what makes this a convention rather than a mechanism: nothing in the engine knows how
to reap. A charter says when, and what grace period, and whether anything should happen first.

## Limits today

A temporal `on` or `when` is not evaluated yet, and is rejected at install with an error that
says so: both are asked of the store at a revision, so they have to be answerable at one. Use a
point-in-time match; temporal composition works today in a gate's `formula`, which the motion
compiler reads (`quorum`, `fact`, `until`, `deadline`).

A rule may declare the STRATUM it lives in, and then its subject must fall inside that stratum's
pattern and any `derive` must land strictly above it. Declaring nothing is a complete answer:
a charter with no `stratum:` anywhere behaves exactly as it always has. See [strata](strata.md).

A CYCLE across rules is caught at runtime, not at install. Two triggers each writing what the
other selects on (`{stage: a} → b` beside `{stage: b} → a`) defeat every install-time
protection — every write is a real change, so the substrate's no-op suppression never engages;
every firing completes, so the in-flight guards never engage; and the latch check passes, because
both rules do advance their subject out of their own selection, into each other's. What catches
it is [the cycle throttle](cycle-throttle.md): a write made during a firing carries the causal
depth of the chain that produced it, and a chain deeper than the installed trigger count has
necessarily repeated a rule. A *paced* cycle — one passing through a task, the world, or a
decision somebody answers — is a control loop rather than a bug, and never trips. Everything else
about whether a charter settles is in [convergence](convergence.md), including the one
rules-level ambiguity that is warned about rather than refused.

A guard about **nothing existing** has no spelling. A match asks what is there, so "an entity
exists whose payload is not X" is sayable (`!not`) and "no entity matching M exists" — *ship when
there is no open blocker* — is not. The model is existence-positive throughout, which is why
[a departure is written down](#lifecycle-how-an-entity-leaves) rather than inferred; the same
discipline answers a `when` about absence, by making the absence a positive fact somebody writes
(a count, a summary entity). That is a discipline rather than a grammar.

An operator on `system` or `kind` is refused for the structural reason given above, and
recovering it needs either a full scan or expansion into N literal subtrees — a design
question rather than a port. Keyed sequence steps (`review.seq(lint)`, addressing an element
by name so it survives an insertion) are the better form on their own terms, but
[kpath](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath)'s `!key` segment
has no defined semantics upstream yet.

The substrate all of this leans on is [change events](change-events.md); whether a charter
written on it settles, and what per-commit evaluation costs, is [convergence](convergence.md).

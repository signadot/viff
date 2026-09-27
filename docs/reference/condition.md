# The condition of a rule

This page holds the fields of a rule's condition: `subject`, `on` and `when`. All three are
written as matches, so the page also says what a match can say. The model that these fields
belong to is in [the trigger model](../trigger-model.md).

## `subject` — what the firing is about, and required

`subject` is a point-in-time `Match` — never a formula. It says what a firing acts on, selected
once, from the store as it stood at the commit. There is no `until` or `deadline` here: a
subject is *selected*, not *awaited*.

It is the one field a condition cannot leave out, because nothing else in a trigger can answer for
it. The action has no target of its own, and a `when` need not pin one place: `{path: "demo.*.*"}`
spans everything two segments under `demo`, and a disjunction such as `!or [a.b.c, {path: "d.e.*"}]` makes it plain.
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
`{path: "demo.pr.*", value: {stage: open}}`, and the same thing one level deeper is
`{path: "verse-dev.drift.head.*", value: {reviewed: pending}}`.

### The pattern says how deep its subjects are

How deep an entity is is a property of the question rather than of the store. A document holding
`{a: {b: {c: {stage: open}}}}` has an entity at `a.b.c`, or one at `a.b` whose payload happens to
be nested, and nothing in it says which.

So the pattern carries the depth. A `path:` of N segments identifies the nodes N segments down,
exactly: `{path: "demo.pr.*"}` selects the nodes standing under `demo.pr` and
`{path: "verse-dev.drift.head.*"}` those under `verse-dev.drift.head`. Each element is a match in
its own right and an operator acts within its segment, so a name, a `!glob` and a `*` all count
the same: one segment. Nothing means "at or below", and a short path is not a prefix: `{path: "demo.pr"}` names the
entity at `demo.pr` itself, and `{path: "demo.pr.*"}` the ones under it.

A pattern that names the wrong depth does not fail. It selects the node at the depth it asked for,
a node that exists holding the subtree below it as its payload, reads the fields it wanted, does
not find them, and writes its patches there. This is worth knowing wherever a rule reads what
another rule derived, since a derive's own depth follows from the subjects the producing rule
selects. See [`action`](action.md#action--what-fires).

A write names the place it writes, and a listing walks: `verse data -paths` lists every address
under the one it was given, and `-depth` bounds how far it goes beneath that address. Asking a
listing for one level answers with whatever happens to live there and loses anything deeper,
which is why it walks instead.

### The string form is an address, in one grammar

There is one string grammar for addressing entities by identity, and both the charter wire and the
CLI (`verse flow show <addr>`) parse it: a kinded path, with `*` for any one segment. So
`"subject": "demo.pr.*"` in a charter and `verse flow show 'demo.pr.*'` on the command line mean
the same set. The string is the path it spells, at every length: `"demo.pr"` names the entity at `demo.pr`,
and `"verse-dev.drift.head.*"` selects four segments deep. Anything else in a segment is a name —
`hotfix-*` written in the string is the segment called `hotfix-*`, not a glob.

The object form says the same path and adds a predicate: `{path: "verse-dev.drift.head.*", value:
{…}}`. There is no list of segments and no operator in a path — a match on a segment was the one
thing the list carried, and what it said is said by a predicate on the node, where a reader looks
for a condition on what a node holds.

There is no deferred `$var` form, and nothing in a payload can redirect a subject. A trigger
whose cause is a `verse.task.instance` change and which acts on PRs acts on the PRs its own
`subject` selects — every one of them — not on the one the completed task happened to name.

In particular, a `flow` field on the changed payload does not short-circuit that. Letting it
resolve the firing onto whatever ref it holds would advance the single PR a completed task was
about, at the price that whether a firing acts on one entity or on a whole set would turn on
whether some entity happens to carry a key by that name, which no charter says and no reader of one
could predict.

What that shortcut does well, advancing exactly the subject a finished task was about, is what
[completion hooks](action.md#completion-hooks-one-trigger-chaining-visible) do exactly. The engine scopes a
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
    when:    {path: "ci.status.*", value: { state: green }}
    subject: {path: "demo.pr.*", value: { stage: reviewed }}
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

- **An edge does not re-fire while it stays true.** `on: {path: "demo.pr.*", value:
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
[vanishing marker](../trigger-model.md#lifecycle-how-an-entity-leaves) that precedes removal. A charter carrying `op`
is refused with that explanation rather than an "unknown key", because the author is reaching for a
distinction the model deliberately does not carry, and "unknown key" would not say so.

The address string is the path written as one kinded path, so `demo.pr.*` means
`{path: "demo.pr.*"}`, and a match that is only a path of names and `*` reads back out of
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
[change events](../change-events.md#the-seed-is-replayed-a-subtree-watch-is-level-triggered-at-the-open).

Neither composes over time: a condition is a match asked at a revision, and the temporal
operators a gate once carried (`!until`, `!quorum`, `!deadline`) are refused by name wherever
a charter is read. See [Limits today](../trigger-model.md#limits-today).

## What a match can say

At the bottom it is a real tony match, evaluated by `tony.Match`, in each path segment and in the
payload predicate.

A path takes no operator: it is a kinded path, the same one `verse data -at` reads, and a segment
is a name, `*` or `..`. What a family of names needs is a predicate on what the node HOLDS, which
is where both live charters say it:

```tony
# every pr whose branch is a hotfix
subject: {path: "demo.pr.*", value: {branch: !glob "hotfix-*"}}
# everything but one
subject: {path: "demo.pr.*", value: {id: !not "1"}}
```

An operator is a tag, and nothing else stands in for one. Inside `value` that is not a matter of
taste:

```tony
value: !not {reviewed: null}      # the absence latch
value: {not: {reviewed: null}}    # a payload EQUAL to that object — which is nothing, ever
```

Both lines are legal, and the second means what it says: `value` is a predicate over payload
*data*, where a field genuinely named `not` holding one key is an ordinary thing to match.

JSON has no tag syntax, so a charter written in JSON has no operators. It can write paths and
plain equality predicates; a condition that needs an operator is written in tony.

**A path decides where a rule watches.** The read narrows to the LITERAL PREFIX —
`entity.Pattern.literalPrefix` stops at the first `*` or `..` — and the rest of the path picks
from what stands under it. That is also why a path that selects begins with a name: the literal
prefix is the place a watch opens on, and a watch cannot be opened on a wildcard at the top.

`verse` is a segment like any other. What verse writes about its own running stands under it
([entities](entities.md)), and a rule about it names it first:

```tony
# a rule about PRs — the ordinary case, and it cannot see verse's own records
subject: {path: "demo.pr.*"}
# a rule about what verse writes: a posed question, a task's mirror, an installed rule
subject: {path: "verse.gate.question.*", value: {status: pending}}
```

Nothing has to be excluded by default for that to hold. A path begins with the place it is about,
so `{path: "task.*.*"}` on a charter about the world's tasks reaches `task.*` and never the
mirror of a run at `verse.task.instance`.

A `..` segment reaches every depth beneath where it stands, the node included — the subject for a
rule about things that do not all stand at one depth, which is what derived entities are:

```tony
# every finding beneath verse-dev, wherever the rule that derived it put one
subject: {path: "verse-dev..finding", value: {stage: open}}
subject: "verse-dev..finding"   # the same, as a kinded path
```

A subject that descends selects by reading the subtree at its literal prefix and walking it, and
its watch is opened with the pattern rather than with a depth, so a write at any depth it reaches
wakes the rule. It names a SET, so it is never "one entity named": a rule with a descending
subject selects.

The payload predicate takes operators too. It is `value`: the same tony match, evaluated against
the payload instead of a segment.

```tony
# a record's fields
when: {path: "demo.pr.*", value: { stage: !or [open, merged] }}
on:   {path: "demo.pr.*", value: { branch: !glob "hotfix-*" }}
# the payload root
when: {path: "demo.count.*", value: !or [10, 100]}
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

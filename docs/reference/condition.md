# The condition of a rule

This page describes the fields of the [condition](../glossary.md#condition) of a
[rule](../glossary.md#rule): `subject`, `on` and `when`. All three are written as matches, so
the page also says what a [match](../glossary.md#match) can say.
[The charter reference](../trigger-model.md) is the model that these fields belong to.

A condition reads data in the store. People write it, rules write it, verse writes its own, and
an [iod](../glossary.md#iod) writes what a system outside verse holds. The condition
does not care which.

## `subject`: what the firing is about, and required

`subject` says what a [firing](../glossary.md#firing) acts on. It is a `Match` at one point in
time, and never a formula. Verse selects the subject once, from the
[store](../glossary.md#store) as it stood at the commit. A subject has no `until` and no
`deadline`: verse *selects* a subject, and does not *await* it.

`subject` is the one field that a condition cannot leave out. The rest of the rule is read
relative to it. [The subject is required](../design/reference-condition.md#the-subject-is-required) gives the reason.

A subject resolves in one of two ways. Both use the same match grammar.

| subject | resolves to | example |
|---|---|---|
| an [address](../glossary.md#address) with no wildcard (`demo.pr.1`) | that address, whether or not [data](../glossary.md#data) stands there. An action's `patch` *creates* it. | publishing a fact nothing has written yet |
| a [pattern](../glossary.md#pattern), which is a path with `*` or `..` in it (`demo.pr.*`), with an optional payload predicate | every matching address in the store, at that commit | every reviewed PR |

A subject is always a *selection*. It is never "the data that changed".
[A subject is a selection](../design/reference-condition.md#a-subject-is-a-selection) gives the reason.

"When a PR opens, review it" and "review all of them" therefore settle in the same state. The
action moves each PR out of the selection, so both end with every open PR reviewed. The two
differ in the number of firings, and not in the state they end in.

An address with no wildcard is a set of one, *whether or not anything stands there*. To name an
address is to name it by identity. It is not to select from what is there.
[A name is a set of one](../design/reference-condition.md#a-name-is-a-set-of-one) gives the reason.

A subject that selects must begin with a literal segment, so that it selects out of a place.
Or the subject names one address outright. The engine selects and spawns for these two shapes.
A pattern that selects nothing does nothing. It never creates data at a segment called `*`.

The engine starts one firing for each selected address, and each firing is about exactly one
subject. Every part of a firing uses that identity as its key:

- its spawn id
- its gate question
- its selection channel
- its place in a causal chain

A re-evaluation about a subject is not a second firing in two cases: when the subject is
already firing, and when work that the firing started is already running. So a rule that waits
for a person does not ask again at every commit.

For this reason `subject` is bounded, where a `when` formula ranges freely. `subject` names a
place, and its predicate is over the [payload](../glossary.md#payload) at one address.
"All open PRs" is `{path: "demo.pr.*", value: {stage: open}}`. The same thing deeper in the tree
is `{path: "repos.verse.drift.head.*", value: {reviewed: pending}}`.

### The pattern says how deep its subjects are

The depth of a piece of data is a property of the question, and not of the store. A document
that holds `{a: {b: {c: {stage: open}}}}` has data at `a.b.c`. Or it has data at `a.b` whose
payload is nested. Nothing in the document says which.

The pattern therefore carries the depth. A `path:` of N segments identifies the nodes exactly
N segments down. A name and a `*` count the same: each is one segment.

| `path` | What it selects |
|---|---|
| `{path: "demo.pr.*"}` | the nodes that stand under `demo.pr` |
| `{path: "repos.verse.drift.head.*"}` | the nodes under `repos.verse.drift.head` |
| `{path: "demo.pr"}` | the node at `demo.pr` itself |

A short path is not a prefix. In a path with no `..` segment, nothing means "at or below".

A pattern that names the wrong depth does not fail. It selects the node at the depth it asked
for. That node exists, and its payload is the subtree beneath it. The rule reads the fields
that it wanted, does not find them, and writes its patches at that node.

Check the depth wherever a rule reads what another rule derived. See
[`action`](action.md#action-what-fires).

A write names the place that it writes. A listing walks: `verse data -paths` lists every
address under the one it was given, and `-depth` bounds how far it goes beneath that address.

A listing walks for this reason. A listing of one level would answer with whatever stands at
that level, and would lose anything deeper.

### The string form is an address, in one grammar

One string grammar names addresses by identity. Both the charter wire and the CLI
(`verse flow show <addr>`) parse it. The string is a
[kinded path](../glossary.md#kinded-path), with `*` for any one segment. So
`"subject": "demo.pr.*"` in a charter and `verse flow show 'demo.pr.*'` on the command line
mean the same set.

The string is the path that it spells, at every length.

| String | What it means |
|---|---|
| `"demo.pr"` | names the address `demo.pr` |
| `"repos.verse.drift.head.*"` | selects five segments deep |

Anything else in a segment is a name. `hotfix-*` in the string is the segment called
`hotfix-*`, and not a glob.

The object form says the same path and adds a predicate:
`{path: "repos.verse.drift.head.*", value: {…}}`. There is no list of segments, and a path holds
no operator. A condition on what a node holds is a predicate on the node, where a reader looks
for it.

There is no deferred reference form, and nothing in a payload can redirect a subject. Take a rule
whose cause is a change to `verse.task.instance`, and which acts on PRs. The rule acts on the
PRs that its own `subject` selects, every one of them. It does not act on the one PR that the
completed task names.

A `flow` field on the changed payload does not change this.
[A `flow` field does not redirect a subject](../design/reference-condition.md#a-flow-field-does-not-redirect-a-subject) gives
the reason.

[Completion hooks](action.md#completion-hooks-one-trigger-chaining-visible) advance exactly
the subject that a finished task was about. The store cannot fake this correlation, because the engine spawned the task.

| You mean | Use |
|---|---|
| "this task, this subject" | a [hook](../glossary.md#hook) |
| "when this becomes true, act on that set" | an `on` |

`flow` is a field that verse writes on the record of each task: the field *is* the subject of
the task. The flow query reads it. A rule whose subject carries the field reads it as
`subject.value.flow`. The field is distinct from the reserved `subject` binding, which is
the resolved subject of the firing itself.

### Acting on a set

A subject that selects advances a whole set. The team's CI records the checks on `main` at
`ci.status.main`, and this rule ships every reviewed pull request while those checks pass:

```tony
- name: ship-reviewed
  condition:
    when:
      path: "ci.status.main"
      value: { state: green }
    subject:
      path: "demo.pr.*"
      value: { stage: reviewed }
  gates:
  - name: ship
    mode: ask
  action:
    patch: { stage: shipped }
```

Every reviewed PR advances, while CI on `main` is green. Each PR is a firing of its own, with its
own `ask` gate. With a `spawn`, each PR has its own task, tagged with that PR as its subject. So
each PR is a [flow](../glossary.md#flow) of its own.

**There is one gate for each member**, and not one gate for the whole set. A firing is about
one address, so twelve reviewed PRs are twelve questions
([gates](gates.md#gates-whether-it-proceeds)). An operator gives twelve answers, because `verse question approve` has
no bulk form.

**The `when` is the condition**: while CI on `main` is green. Verse evaluates the rule again at
every commit that touches either subtree. So the rule also acts on a PR that reaches `reviewed`
later.

[Two rules, read out loud](../trigger-model.md#two-rules-read-out-loud) says what an `on` in
place of `when` means. The engine warns at install when a narrow `on` leaves uncovered a part of what the rule reads.

## `on` and `when`: the cause and the condition

`on` and `when` are one predicate asked at two moments. Both are optional. Both are matches
over the store, with the same grammar.

| Field | Asked at | What it does |
|---|---|---|
| [`on`](../glossary.md#on) | C-1 and C | fires on the rising edge, ¬P → P |
| [`when`](../glossary.md#when) | C | gates |

**`when` is the condition.** With no `on`, verse considers the rule at every commit that
touches anything the rule reads. `when` says whether to act at that commit. This shape is a
reconciler: *while* this holds, act on what the subject selects.

**`on` is the cause.** It narrows *when verse considers the rule at all* to the instant at
which its predicate becomes true. Two properties follow, and an author must know both:

- **An edge does not fire again while it stays true.**
  `on: {path: "demo.pr.*", value: {stage: reviewed}}` rises when the first PR reaches
  `reviewed`. It says nothing about the second PR, because the predicate never fell. "On each
  PR reaching reviewed" has no spelling. A subject and a latch say it.
- **A rule with an `on` does not act on the backlog.** When the rule is installed over a store
  where its predicate already holds, there is no rising edge to observe. A
  [level rule](../glossary.md#level-rule) does the opposite: it acts on everything that
  already stands (see [level at open](#the-backlog)).

A match is a predicate over the store. It joins a `path` and a `value`. The `path` says which
addresses the match reaches. The `value` is a [predicate](../glossary.md#predicate) over the
payload that stands at those addresses. A match with no `value` asks only where.

**A match has no `op` key.** Verse refuses a charter that carries `op`, and the refusal
explains why. It does not say "unknown key". A match says a create and a delete in this way:

| Change | How a match says it |
|---|---|
| a create | the rising edge of "this exists" |
| a delete | the rising edge of the [vanishing marker](../trigger-model.md#lifecycle-how-data-leaves) that precedes removal |

[A match has no `op` key](../design/reference-condition.md#a-match-has-no-op-key) gives the reason.

A match that is only a path of names and `*` reads back from `verse c`
in the string form: `demo.pr.*`. It is the same match in a shorter spelling.

**Keep a condition small.** A large condition is hard to read and hard to check. To act "only
the first time", match a state field that your action then advances. That field is a
**[latch](../glossary.md#latch)**. [The rule](../trigger-model.md#the-rule) says why a rule
needs one.

<a id="the-backlog"></a>
**Installing a rule fires it on what already matches.** When a rule opens, its observation
replays the places that the rule reads. So the rule acts at once on the backlog that stands in
the store at the install. You do not write the data again to announce a state that it already
has.

An `on` is the exception, for the same reason that it does not fire again: a predicate that
already holds has no rising edge.

The latch also stops an action from running again when the engine restarts over a store that
is still in the matching state. See
[change events](../change-events.md#the-seed-is-replayed-a-subtree-watch-is-level-triggered-at-the-open).

**Neither `on` nor `when` composes over time.** See
[Limits today](../trigger-model.md#limits-today).

## What a match can say

A match is a tony match, and `tony.Match` evaluates it.

**A path takes no operator.** A path is a kinded path, the same one that `verse data -at`
reads. A segment is a name, `*` or `..`. `**` is no segment and is refused: `..` is every depth,
`*.*` exactly two. To select a family of names, write a predicate on what the node holds:

```tony
# every pr whose branch is a hotfix
subject:
  path: "demo.pr.*"
  value: {branch: !glob "hotfix-*"}
# everything but one
subject:
  path: "demo.pr.*"
  value: {id: !not "1"}
```

**An [operator](../glossary.md#operator) is a tag**, and nothing else acts as one. Inside
`value` the difference changes the meaning:

```tony
value: !not {reviewed: null}      # the absence latch
value: {not: {reviewed: null}}    # a payload EQUAL to that object — which is nothing, ever
```

Both lines are legal, and the second means what it says. `value` is a predicate over payload
*data*, and a payload may have a field named `not` that holds one key.

JSON has no syntax for tags, so a charter written in JSON has no operators. It can write paths
and plain equality predicates. Write a condition that needs an operator in tony.

**A path decides where a rule watches.** The read narrows to the literal prefix of the path:
`entity.Pattern.literalPrefix` stops at the first `*` or `..`. The rest of the path picks from
what stands under the prefix.

A path that selects begins with a name for the same reason. A watch opens on the literal
prefix, and a watch cannot open on a wildcard at the top.

`verse` is a segment like any other. What verse writes about its own running stands under it
([entities](entities.md)). A rule about those records names `verse` first:

```tony
# a rule about PRs — the ordinary case, and it cannot see verse's own records
subject: {path: "demo.pr.*"}
# a rule about what verse writes: an ask on the docket, a task's mirror, an installed rule
subject:
  path: "verse.ask.*"
  value: {status: pending}
```

Nothing is excluded by default to make this hold. A path begins with the place that it is
about. So `{path: "task.*.*"}`, in a charter about the tasks of the world, reaches `task.*`.
It never reaches the mirror of a run at `verse.task.instance`.

A `..` segment reaches every depth beneath the place where it stands, and the node at that
place too. Use it for a rule about things that do not all stand at one depth. Derived records
are such things:

```tony
# every finding beneath repos.verse, wherever the rule that derived it put one
subject:
  path: "repos.verse..finding"
  value: {stage: open}
subject: "repos.verse..finding"   # the same, as a kinded path
```

A subject that descends selects by reading the subtree at its literal prefix and walking it.
Verse opens its watch with the pattern, and not with a depth. So a write at any depth that the
pattern reaches wakes the rule.

A subject that descends names a set. So it is never "one address named": a rule with a
descending subject selects.

**The payload predicate takes operators.** The predicate is `value`: a tony match, evaluated
against the payload.

```tony
# a record's fields
when:
  path: "demo.pr.*"
  value: { stage: !or [open, merged] }
on:
  path: "demo.pr.*"
  value: { branch: !glob "hotfix-*" }
# the payload root
when:
  path: "demo.count.*"
  value: !or [10, 100]
# an order: one field against another in the same payload, scaled (!lt, !le, !gt, !ge)
subject:
  path: "quota.status.*"
  value: { used: !ge(0.8).get-path(root) cap }
```

`value` is open in shape because a payload is. A payload is any tony value: a
record, a count, a flag, a state name, a list. So a predicate over it may be any of those, or
an operator over one. Almost every charter writes the record form. The other forms are why the
key is not called `fields`.

| Payload | What `value` means |
|---|---|
| a record | a **subset** constraint, as a tony match over an object always is. The fields you name must agree, and any others are ignored. |
| a scalar | equality. There is nothing to be a subset of, so `value: 42` means the payload *is* 42. |

`value: "stage=open"` is a legal predicate. It matches data whose whole payload is the
string `stage=open`. It is not an expression language. There is one name for the predicate,
and no assertion about its shape. [`value` asserts no shape](../design/reference-condition.md#value-asserts-no-shape) gives
the reason.

Know three things about a predicate before you rely on one.

**`!glob` is `filepath.Match`.** So `*` does not cross `/`, and `?` and `[…]` are special
characters: `!glob "a*"` does not match `a/b`. This is intended: a name that holds a `/` can be
matched one piece at a time. The `<workspace>/<ts>` of a slack message is such a
name. But `!glob "*"` and a bare `*` are not interchangeable: a bare `*` is any one segment,
whatever it holds.

**A `null` in a predicate is a presence test**, and not "is null". A null pattern matches any
value, so it constrains only that the field is there. The three questions have three
spellings:

```tony
# stage is present, whatever its value
value: { stage: null }
# stage is absent
value: !not { stage: null }
# stage is present and its value is null
value: { stage: !irtype null }
```

An absent key is not a null key. The store records whether a field was cleared or never
written.

**Numbers are type-exact.** `2` and `2.0` are different values, so a predicate written
`{n: 2}` does not match a payload of `{n: 2.0}`. Integer widths do not matter. A width comes
from whoever wrote the value, and not from the value.

## Why it is this way

The reasons for this design are in [reference-condition.md](../design/reference-condition.md), in the repository.

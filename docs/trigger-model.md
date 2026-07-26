# The trigger model — entities, matches, and one selector grammar

One noun and two grammars carry the whole model. An **entity** is a node `system:kind:id`
with a small payload. A **match** names entities at an instant; a **formula** relates the
verse over time. Every trigger concept — `when`, `subject`, gates, goals, flows — lands on
exactly one of those two, and knowing which is the thing to be unambiguous about.

This is also the field-by-field reference for a charter rule: the [tutorial](tutorial.md)
installs one and drives it, and here is what each field means.

*The match vocabulary itself is [tony](https://signadot.github.io/tony-format/)'s, not
verse's: the same operators serve matching, patching and diffing there, which is the argument
made in [Structured matching, patching and diffing done
right](https://dev.to/scott_cotton_dc9ce3e7e632/structured-matching-patching-and-diffing-done-right-22ci).*

## Two layers: point-in-time matches, temporal formulas

- **Match** — a *point-in-time* tony selector over the entity-verse: "which entities satisfy
  this **right now**." No time in it — no `until`, no `deadline`.
- **Formula** — a *temporal* composition of matches with operators (`quorum`, `fact`,
  `until`, `deadline`, `eventually`, …), evaluated over the verse's **evolution**. A
  formula's leaves are matches.

| concept | grammar | what it expresses |
|---|---|---|
| **Subject** | **Match** — point-in-time | the entities a firing is *about* — const system/kind + an entity predicate; **no** temporal operators |
| **Flow** | **Match** — point-in-time | which *live processes* to steer, by their subject |
| **When** | **Formula** — temporal | *fire* when the verse's evolution satisfies this — an edge on a match, or `until`/`deadline`/… |
| **Gate** | **Formula** — temporal | *proceed* at a threshold — `quorum`/`fact`/`until`/`deadline` |
| **Goal `doneWhen`** | **Formula** — temporal | *done*, latched, when the witness appears |

The cut: **name entities at an instant → a `Match`; relate the verse over time → a
`Formula`.** `Subject` and flow-addressing are point-in-time — a subject is *named at the
instant a firing happens*, never accumulated — so they are plain matches (const system/kind
preferred), and giving them a `deadline` or an `until` would be a category error. `When`,
gates, and `doneWhen` all relate the verse's *evolution*, so they are formulas — and `When`
and a gate are the **same** grammar, differing only in consequence: a satisfied `When`
*fires*, a satisfied gate *proceeds*. A `When` that watches a single change is just its
*atom* form.

## The trigger

A rule has four parts: a name, the **condition** that causes it, the **gates** that guard
it, and the **action** that fires.

```tony
- name: review
  condition:
    when: { system: demo, fields: { stage: open } }   # the cause
  gates:
  - { name: approval, mode: ask }                     # the guard
  action:                                             # the consequence
    patch: { stage: reviewing }
    spawn: { driver: scripted, task: "echo reviewing $subject" }
    onDone:
      patch: { stage: reviewed }
```

```go
type Trigger struct {
    Name      string
    Condition Condition   // the cause: what fires it, and what it is about
    Gates     []Gate      // the guard — conjunctive: all must resolve to proceed
    Action    Action      // patch / spawn — declarative data, no Go closure
}

// Condition is the ECA "cause". When and Subject are grouped, not nested, because the
// Subject is resolved at the instant When fires — there is no subject before the match —
// yet neither field owns the other. Two halves of "when X, about Y", in two DIFFERENT
// grammars: When is temporal, Subject is point-in-time.
type Condition struct {
    When    *Formula  // temporal — fires when the verse's evolution satisfies this
    Subject Match     // point-in-time — the entities this firing is about, at the fire instant
}
```

A trigger is fully declarative, so a charter can be installed over the wire, persisted, or
reflected as an entity, and the engine interprets it directly — there is no compiled form.

## `when` — what fires it

A `when` is a **match**: a point-in-time predicate over the verse. It ANDs its dimensions —
`system` / `kind` / `id` / `op` (`set` for created-or-updated, or `delete`) / a payload
predicate. Empty dimensions are wildcards, and `{}` matches everything.

The address string is sugar for the identity dimensions, so `demo:pr:*` means
`{system: demo, kind: pr}` — and since a trailing wildcard *is* an unset dimension, that is
what reads back when you `verse trigger show` it: `demo:pr`. Same match, shorter spelling.

*Keep it small.* The "when" is where complexity hides. Create and update are one `set`, so
to fire "only the first time", match a state field your action then advances — a latch,
rather than an edge (see `change-events.md`).

A `when` may also grow *temporal* operators (`until`, `deadline`, … — the same language a
gate formula uses), at which point it is watched over the verse's evolution rather than
matched against one change. That is not evaluated yet; see [Limits today](#limits-today).

## `subject` — what the firing is about

`Subject` is a **point-in-time `Match`** — never a formula. It *selects* the entities a
firing acts on, evaluated once, at the instant `when` fires. There is no `until` or
`deadline` here: a subject is *named*, not *awaited*. It resolves in one of three ways, all
the same match grammar:

| `subject` | resolves to | example |
|---|---|---|
| *(omitted)* | the matched entity | a change to `demo:pr:1` is about `demo:pr:1` |
| an **address naming one entity** (`demo:pr:1`) | that one entity, **named** — an action's `patch` *creates* it if absent | publishing a fact nothing has written yet |
| a **pattern** — a glob (`demo:pr:*`, `demo:*:*`) or a `system`+`kind` selector with an optional payload predicate | the firing's OWN subject if it carries one (a reflected process's `flow`); **else** the set it selects from the store | the PR a completed process was about; or every reviewed PR |

**Omitted is one entity, always — even when `when` matches everything.** A `when` in its
atom form is dispatched per change *event*, and the change stream is per-entity, so
`when: {}` fires once per change, each firing about the entity that changed. It does not
sweep the store. Fan-out comes only from a *pattern* subject, which is the one dispatch path
that resolves a set.

A pattern that names nothing the firing is about is a no-op — it never conjures an entity
called `*`, nor misfires onto a flow it does not name.

The engine **fans out one action/process per resolved entity**, and **each is about exactly
one subject**. A *firing* can yield 0, 1, or N; a *process / flow / goal* always has one
subject. A fan-out's spawn id is derived from its subject, so a re-fire is idempotent per
member, and the runaway backstop meters *entities acted on*, truncating a too-wide fan-out
rather than dropping it. Gates stay per firing — a broadcast asks once.

This is why `subject` is bounded where a `when` formula ranges freely: it pins `system:kind`
(it must name a definite kind to select and spawn for) and predicates over the single
entity's payload. "all open PRs" is just `{system: demo, kind: pr, entity: {stage: open}}`.

### The string form is an address, in one grammar

There is **one** string grammar for addressing entities by identity, and both the charter
wire and the CLI (`verse flow <addr>`) parse it with `ParseExpr`: three segments, `*` a
wildcard per segment, and an id that may glob (`hotfix-*`). So `"subject": "demo:pr:*"` in a
charter and `verse flow demo:pr:*` on the command line mean the same set, as they read like
they should.

There is no deferred `$var` form. A completion trigger — one that fires on a `process:instance`
change but acts on the PR — does not reach back into the payload for a reference: its `subject`
is a plain pattern (`"demo:pr:*"`) that the firing matches against the process's OWN subject (its
`flow`). The pattern both picks the one flow it names and filters out the flows it does not — the
same address grammar as everything else, no back-reference, no second string form. `flow` is a
convention published by `mirror.Reflect` and by a goal latch — the field IS the process's
subject — kept distinct from the reserved `$subject` binding, which is the firing's own
resolved subject.

The one distinction inside the address form is **name vs select**. A glob-free address
*names* its entity, so an action's `patch` may bring it into existence — that is how a trigger
publishes a fact nothing has written yet. A glob anywhere *selects* over what exists, so it
can only act on entities that are really there (and can never conjure one called `*`).

### Broadcast: one firing, N subjects

A selector subject is how one change advances a whole set:

```tony
- name: ship-reviewed
  condition:
    when:    { system: ci, kind: status }
    subject: { system: demo, kind: pr, fields: { stage: reviewed } }
  gates:
  - { name: ship, mode: ask }
  action:
    patch: { stage: shipped }
```

One CI change fires this once, one `ask` gate is raised for the *firing* (not one per PR),
and then every reviewed PR advances. A `spawn` would give each member its own process,
tagged with that member as its subject — so each is its own flow. Broadcast is a wider
setting of the same selector, not a new mechanism.

## What a match can say

The leaf is a real tony match, evaluated by `tony.Match`, on every dimension a filter can
live on.

**`id` and `op` take operators** — the richer matching a plain glob cannot spell:

```tony
subject: { system: demo, kind: pr, id: !glob "hotfix-*" }        # filepath globbing
subject: { system: demo, kind: pr, id: !not "1" }                # everything but one
subject: { system: demo, kind: pr, id: !or ["1", !glob "hf-*"] } # composed
```

In a JSON-shaped charter, write the operator as a single-key object — `{"id": {"glob":
"hotfix-*"}}` — the same spelling a formula uses for its tags. The two are one grammar; only
tony can write the tag directly.

**`system` and `kind` take no operator**, and that is structural rather than unfinished:
they name the subtree verse watches and the set it lists — they become a docd watch path
(`Formula.Subtrees`) and the store's `List` prefilter — and a watch path cannot be a
predicate. Write the whole segment as `*` for "any", and put the operator on `id`.

**The payload predicate takes operators too.** It is the same tony match, evaluated against
the payload instead of the id, and it has **two spellings, one meaning**:

```tony
when: { system: demo, kind: pr,    fields:  { stage: !or [open, merged] } }  # a record's fields
when: { system: demo, kind: pr,    fields:  { branch: !glob "hotfix-*" } }
when: { system: demo, kind: count, payload: !or [10, 100] }                  # the payload ROOT
```

`fields` is the object-shaped spelling — a predicate over a record's fields, which is what
almost every charter matches. `payload` is the general one, a predicate on the payload
*root*, for the entities whose payload is not a record at all (an entity's payload is any
tony value: a count, a flag, a state name, a list). `entity` is accepted as another name for
`fields`.

Set **one** of them, not both — two predicates on the same value have no stated way to
combine. `fields` must be an object; a scalar there is an install error pointing you at
`payload`, which is what keeps a typo like `fields: "stage=open"` from becoming a predicate
that quietly matches nothing.

Four semantics are worth knowing before you rely on one.

**`!glob` is stricter than a plain string.** It is `filepath.Match`, so `*` does not cross
`/` and `?`/`[…]` are metacharacters: `id: "a*"` matches `a/b` and `id: !glob "a*"` does not.
That is the point of it — a path-shaped id can be matched a segment at a time — but it means
the two spellings are not interchangeable.

**A `null` in a predicate is a PRESENCE test, not "is null".** A null pattern matches any
value, so the only thing it constrains is that the field is there. The three questions have
three different spellings:

```tony
fields: { stage: null }            # stage is PRESENT, whatever its value
fields: !not { stage: null }       # stage is ABSENT
fields: { stage: !irtype null }    # stage is present AND its value is null
```

An absent key is not a null one — the store records whether a field was cleared or never
written.

**Numbers are type-exact**: `2` and `2.0` are different values, so a predicate written
`{n: 2}` does not match a payload of `{n: 2.0}`. Integer widths do not matter — that is an
artifact of whoever wrote the value, not of the value.

**Operators inside a payload predicate are tony-only.** The id dimension accepts the JSON
single-key spelling because its keyspace is closed, but a payload is *your* document — a key
in it may itself be named `or` — so that sugar cannot be borrowed here without changing what
some charter already means. A JSON charter can still write plain equality predicates; an
operator over a payload needs tony.

## `gates` — whether it proceeds

A list of gates, **all** of which must resolve to proceed (conjunctive) before the action
fires. Gates are per *firing*, so a broadcast asks once, not once per subject. Each is
`{ name: …, mode: auto|ask|park }`. An empty list fires with no approval.

Multiple gates model multi-approval — a deploy needing both a security and a release-manager
sign-off is two `ask` gates. A gate can also carry a **`formula`** the verse resolves itself
— a vote, a deadline, a threshold — which is the temporal layer of this document at work on
the guard.

## `action` — what fires

Data, not code. An action needs no target of its own: it acts on the condition's
**subject**.

- **`patch`** writes to the subject's payload (a state transition). It is a **tony patch**,
  not an assignment: a nested write merges into the subtree it names rather than replacing
  it, so two triggers writing under one key do not clobber each other, and the mergeop
  operators apply — `patch: { tmp: !delete null }` removes a key, which is something no
  amount of setting fields could say. Writing a scalar (`patch: 42`) replaces the whole
  payload.
- **`spawn`** launches a process from a `launch.Spec` (`driver`, `task`, and later
  `policy`/`scope`/`environment`) — or, instead of a fixed `driver`, a governed
  **`provider`** selection of the doer. The spawned process is tagged with the subject,
  which is what makes it part of that subject's flow.

### Bindings

Every field of the changed payload is bound by name (`$stage`, `$path`, …), plus `$subject`
— the *resolved* subject. So `review $subject` in a spawn's task names the PR the firing is
about, even when the entity that fired was a process, not the PR. A completion hook can also
read the finished process's payload via `$result` and `$status`.

### Completion hooks: one trigger, chaining visible

An `Action` carries one hook per terminal status — `onDone` (status `done`), `onFailed`,
`onCancelled`, `onOrphaned` — and each is itself a full `Action` (`patch` + `spawn` + its own
hooks), so a chain (`review → merge → approve`) nests `onDone`s and reads top-to-bottom in one
rule. When the action's `spawn` reaches a terminal status, the engine fires the matching hook
about the *same subject*, scoped to exactly that process: the hook rides the live process
registry (keyed by process id), so there is no `flow` match and no separate trigger.

```tony
action:
  patch:  { stage: reviewing }
  spawn:  { driver: scripted, task: "review $subject" }
  onDone: { patch: { stage: reviewed } }
```

The alternative is a *decoupled* completion trigger — a separate rule that fires on the
`process:instance` change and names the PR by pattern (`reviewed`, below). That is the tool
when a completion should fan into several reactions, advance a different subject, or be
authored separately; the hook is the tool for a linear chain owned by one trigger, with the
process step visible inline.

Hooks survive a restart: resume rehydrates them from the process's chain, so `done` /
`failed` / `cancelled` still fire. (`onOrphaned` is inert — a process that can be re-attached
is adopted and never marked orphaned, one that cannot is orphaned and never tracked, so no
tracked process reaches that status.) See `completion-hooks.md`.

## Flow — a subject-match over live processes

A flow is **not a stored key.** It is a `Match` evaluated over the subjects of *live*
processes:

```sh
verse flow pause demo:pr:1      # processes whose subject matches demo:pr:1
verse flow pause demo:pr:*      # …the whole kind
```

Relating processes is what a match *does*, so "relational flows" are not a second primitive
to add later — they are broader subject-matches. `verse flows` is the distinct subjects with
live processes; steering applies the match and acts on the matched processes. A query, not a
maintained index.

Goals sit here too, and split the same way: a goal is a `Subject` (a point-in-time `Match` —
the flow it owns and steers) plus a `doneWhen` (a temporal `Formula` that latches). One of
each layer, no new machinery.

## Two rules, read out loud

```json
{ "name": "review",
  "condition": { "when": { "system": "demo", "fields": { "stage": "open" } } },
  "gates":     [ { "name": "approval", "mode": "ask" } ],
  "action":    { "patch": { "stage": "reviewing" }, "spawn": { "driver": "scripted", "task": "review $subject" } } }
{ "name": "reviewed",
  "condition": { "when":    { "system": "process", "kind": "instance", "fields": { "status": "done" } },
                 "subject": "demo:pr:*" },
  "action":    { "patch": { "stage": "reviewed" } } }
```

`reviewed` reads as it means: *when a process completes, about the PR it carried, set
reviewed*. Its `subject` is a plain pattern — `demo:pr:*` matched against the process's own
subject — in the same address grammar as everywhere else.

## One event per entity

None of this needs a match to "select one entity" — it needs the **change stream to be
per-entity**, and it is. A docd commit is atomic across many branches, but `LogdStore`
normalises each commit into one `Event` per entity at path-depth `system/kind/id`. So a
firing is driven by one event → one `Ref` → `when`'s matched entity is exactly one: the
subject is well-defined *structurally*, not by constraining the pattern. One commit that
changes `…:a` and `…:b` yields two events, two firings, and subjects `a` and `b`, with no
fan-out logic. The known cost, recorded in `change-events.md`: transaction atomicity is
flattened — the reactive grain is the entity, not the commit.

## Limits today

A **temporal `when` is not evaluated yet**, and is rejected at install with an error that
says so: a temporal when has no change to dispatch on, so it needs a long-lived monitor over
the subtrees it reads. Use a point-in-time match in a `when`; temporal composition works
today in a gate's `formula` and a goal's `doneWhen`. The implementation plan is
`trigger-model-plan.md`; the substrate this leans on is `change-events.md`.

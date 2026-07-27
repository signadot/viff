# The trigger model — entities, matches, and one selector grammar

One noun and two grammars carry the whole model. An entity is a node `system:kind:id`
with a small payload. A match names entities at an instant; a formula relates the
verse over time. Every trigger concept — `when`, `subject`, gates, goals, flows — lands on
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
| `subject` | match — point-in-time | the entities a firing is *about* — const system/kind plus an entity predicate, no temporal operators |
| flow | match — point-in-time | which live processes to steer, by their subject |
| `when` | formula — temporal | fire when the verse's evolution satisfies this — an edge on a match, or `until`/`deadline`/… |
| gate | formula — temporal | proceed at a threshold — `quorum`/`fact`/`until`/`deadline` |
| goal `doneWhen` | formula — temporal | done, latched, when the witness appears |

The cut is: name entities at an instant and you want a match; relate the verse over time and
you want a formula. `subject` and flow-addressing are point-in-time — a subject is named at the
instant a firing happens, never accumulated — so they are plain matches (const system/kind
preferred), and giving them a `deadline` or an `until` would be a category error. `when`,
gates, and `doneWhen` all relate the verse's evolution, so they are formulas. `when` and a gate
are the same grammar, differing only in consequence: a satisfied `when` fires, a satisfied gate
proceeds. A `when` that watches a single change is just its atom form.

## The trigger

A rule has four parts: a name, the condition that causes it, the gates that guard
it, and the action that fires.

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

### The charter is durable

Installing a trigger writes it into the verse as `trigger:rule:<name>`; removing one deletes
that entity; and a start reads them back before anything observes. So the program a verse
comes back up running is the one it went down with.

Everything else a restart needs was already durable — a process is mirrored, a sequence's
position is a fact on its subject, a claim is a lease in the store, and resume rehydrates a
chain's hooks from it. The one thing that did not survive was the program itself, so a verse
came back with its work in progress intact and nothing left to react to it.

One entity per rule, because that is what the operations already are: an install writes one,
a removal deletes one, and neither reads the others — so two installs cannot lose each
other, and a rule is addressable (`verse entity get trigger:rule:review`).

**A rule is stored as structure, under the `!raw` escape.** The escape is what makes that
possible, and it is worth saying why it was needed. A write is a tony *patch* — the property
that makes the store good at everything else — so operator tags in a payload are EXECUTED by
the store rather than stored by it. A charter is full of them: `id: !glob "hotfix-*"`,
`patch: {tmp: !delete null}`. A structurally-stored rule was therefore not one that read back
wrong, it was one that could not be read back at all — the write left an entity whose own
patch log no longer applied (`glob patching "null" gave cannot patch with glob operation`) —
and the rule was kept as its tony *text* instead.

`!raw` (go-tony v0.0.100) says *this tag is data*: the subtree is stored verbatim, nothing
beneath it is interpreted at any depth, and the escape itself is consumed, so the rule lands
carrying its own tags. `entity.Raw` is that wrap on the way in; a rule read back is ordinary
data, which is why putting one back means escaping it again (`entity.AsData`, which is what
`entity.Modify` does for its callers — a record that holds a document would otherwise execute
its own operators the moment anything else about it changed). Raw also *replaces* rather than
merging, so re-installing a rule cannot leave a field of the version before it standing in the
version after.

The gain is that the record is the rule: `verse entity get trigger:rule:review` shows a
document to read field by field, a charter change is an observable fact about a shape rather
than about a string, and nothing has to re-parse to ask a question about it.

Restoring a rule is an ordinary install, which means it **replays**: a subtree watch is
level-triggered at the open, so a restored trigger acts on what already matches it — including
what arrived while verse was down. A restart is then just an install over a verse that has
moved on, which is the same property described above, and the same reason to write a latch
rather than an edge.

A **goal** is durable the same way — the same `spec` field, the same escape — and its record
is the goal: `goal:instance:<name>` holds the definition it was installed from, next to the
live status and the terminal latch it already carried. A start reinstalls every goal still being pursued, which brings back both
halves — the doing (its owned triggers) and the watching (its monitor). Three properties make
that a re-install rather than a reconstruction: the deadline's absolute instant is stamped on
the record, so it does not slide forward; `doneWhen` is evaluated before the monitor waits, so
a goal *reached while verse was down* latches on the way back up; and the terminal commit is a
CAS on `terminal=false`, so nothing can latch twice. A **settled** goal is not restored — its
outcome stands and its charter was reaped, so reinstalling it would recreate the doing for a
goal that is over.

This is why a goal's owned triggers are not *also* written out as standalone rules: they
travel in the goal's record and come back with it. Written separately they would have two
owners and two lifetimes, and the standalone copy would survive the reap that ends the goal —
still firing for something nobody is pursuing.

One thing is deliberately not restored. A **scoped charter** (`verse trigger add --scope`) is
written into its scope, but a scope's engine is created on demand by `verse connect`, and a
scoped store reads baseline layered underneath — so restoring one means enumerating scopes and
reading only the scope's own layer. That is the same gap scoped resume has.

The charter is now an observable slice like any other, so a trigger can watch the charter
(`when: {system: trigger, kind: rule}`). It has to *name* it: `trigger:*` joins `process:*`
as the engine's own machinery, kept out of the default match set so the loop cannot ignite on
itself. Meta-observation stays possible, and stays deliberate.

## `when` — what fires it

A `when` is a match: a point-in-time predicate over the verse. It ANDs its dimensions —
`system` / `kind` / `id` / `op` (`set` for created-or-updated, or `delete`) / a payload
predicate. Empty dimensions are wildcards, and `{}` matches everything.

The address string is sugar for the identity dimensions, so `demo:pr:*` means
`{system: demo, kind: pr}` — and since a trailing wildcard *is* an unset dimension, that is
what reads back when you `verse trigger show` it: `demo:pr`. Same match, shorter spelling.

Keep it small. The "when" is where complexity hides. Create and update are one `set`, so
to fire "only the first time", match a state field your action then advances — a latch,
rather than an edge (see [change events](change-events.md)).

**Installing a trigger fires it on what already matches.** A trigger's observation replays
the slice it watches when it opens, so the backlog standing in the verse when you install the
charter is acted on immediately — you do not have to re-touch each entity to announce a state
it has held all along. The same property means the latch above is what stops an action
re-running when the engine restarts over a verse still in the matching state; see
[change events](change-events.md#the-seed-is-replayed-a-subtree-watch-is-level-triggered-at-the-open).

A `when` may also grow *temporal* operators (`until`, `deadline`, … — the same language a
gate formula uses), at which point it is watched over the verse's evolution rather than
matched against one change. That is not evaluated yet; see [Limits today](#limits-today).

## `subject` — what the firing is about

`Subject` is a point-in-time `Match` — never a formula. It *selects* the entities a
firing acts on, evaluated once, at the instant `when` fires. There is no `until` or
`deadline` here: a subject is *named*, not *awaited*. It resolves in one of three ways, all
the same match grammar:

| `subject` | resolves to | example |
|---|---|---|
| *(omitted)* | the matched entity | a change to `demo:pr:1` is about `demo:pr:1` |
| an address naming one entity (`demo:pr:1`) | that one entity, named — an action's `patch` *creates* it if absent | publishing a fact nothing has written yet |
| a pattern — a glob (`demo:pr:*`, `demo:*:*`) or a `system`+`kind` selector with an optional payload predicate | the firing's OWN subject if it carries one (a reflected process's `flow`); else the set it selects from the store | the PR a completed process was about; or every reviewed PR |

Omitted is one entity, always — even when `when` matches everything. A `when` in its atom
form is dispatched per change *event*, and the change stream is per-entity, so `when: {}`
fires once per change, each firing about the entity that changed. It does not sweep the
store. Fan-out comes only from a *pattern* subject, which is the one dispatch path that
resolves a set.

A pattern that names nothing the firing is about is a no-op — it never conjures an entity
called `*`, nor misfires onto a flow it does not name.

The engine fans out one action, and one process, per resolved entity, and each is about
exactly one subject. A *firing* can yield 0, 1, or N; a process, flow, or goal always has one
subject. A fan-out's spawn id is derived from its subject, so a re-fire is idempotent per
member, and the runaway backstop meters *entities acted on*, truncating a too-wide fan-out
rather than dropping it. Gates stay per firing — a broadcast asks once.

This is why `subject` is bounded where a `when` formula ranges freely: it pins `system:kind`
(it must name a definite kind to select and spawn for) and predicates over the single
entity's payload. "all open PRs" is just `{system: demo, kind: pr, entity: {stage: open}}`.

### The string form is an address, in one grammar

There is one string grammar for addressing entities by identity, and both the charter
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

The one distinction inside the address form is name vs select. A glob-free address
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

`id` and `op` take operators — the richer matching a plain glob cannot spell:

```tony
subject: { system: demo, kind: pr, id: !glob "hotfix-*" }        # filepath globbing
subject: { system: demo, kind: pr, id: !not "1" }                # everything but one
subject: { system: demo, kind: pr, id: !or ["1", !glob "hf-*"] } # composed
```

In a JSON-shaped charter, write the operator as a single-key object — `{"id": {"glob":
"hotfix-*"}}` — the same spelling a formula uses for its tags. The two are one grammar; only
tony can write the tag directly.

`system` and `kind` take no operator, and that is structural rather than unfinished: they
name the subtree verse watches and the set it lists — they become a docd watch path
(`Formula.Subtrees`) and the store's `List` prefilter — and a watch path cannot be a
predicate. Write the whole segment as `*` for "any", and put the operator on `id`.

The payload predicate takes operators too. It is the same tony match, evaluated against the
payload instead of the id, and it has two spellings with one meaning:

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

Set one of them, not both — two predicates on the same value have no stated way to combine.
`fields` must be an object; a scalar there is an install error pointing you at `payload`,
which is what keeps a typo like `fields: "stage=open"` from becoming a predicate that quietly
matches nothing.

Four semantics are worth knowing before you rely on one.

`!glob` is stricter than a plain string. It is `filepath.Match`, so `*` does not cross `/`
and `?`/`[…]` are metacharacters: `id: "a*"` matches `a/b` and `id: !glob "a*"` does not.
That is the point of it — a path-shaped id can be matched a segment at a time — but it means
the two spellings are not interchangeable.

A `null` in a predicate is a presence test, not "is null". A null pattern matches any value,
so the only thing it constrains is that the field is there. The three questions have three
different spellings:

```tony
fields: { stage: null }            # stage is PRESENT, whatever its value
fields: !not { stage: null }       # stage is ABSENT
fields: { stage: !irtype null }    # stage is present AND its value is null
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
Gates are per *firing*, so a broadcast asks once, not once per subject. Each is
`{ name: …, mode: auto|ask|park }`. An empty list fires with no approval.

Multiple gates model multi-approval — a deploy needing both a security and a release-manager
sign-off is two `ask` gates. A gate can also carry a `formula` the verse resolves itself — a
vote, a deadline, a threshold — which is the temporal layer of this document at work on the
guard.

## `action` — what fires

Data, not code. An action needs no target of its own: it acts on the condition's subject.

`patch` writes to the subject's payload — a state transition. It is a tony patch, not an
assignment: a nested write merges into the subtree it names rather than replacing it, so two
triggers writing under one key do not clobber each other, and the
[mergeop](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop) operators
apply. `patch: { tmp: !delete null }` removes a key, which is something no amount of setting
fields could say. Writing a scalar (`patch: 42`) replaces the whole payload.

`spawn` launches a process from a `launch.Spec` — or, instead of a fixed `driver`, a governed
`provider` selection of the doer. The spawned process is tagged with the subject, which is
what makes it part of that subject's flow.

The spec's fields fall into three groups, and the difference decides what a charter can say.

*The request* — `task` and `frame`. Both are text you write. `task` is the one job; `frame`
is the standing instructions it is read in (the agent's system prompt). A frame is worth
writing whenever the charter reads the answer rather than a person: the default tells the
agent its last message *is* the result and not to summarise, but only the charter knows what
shape it wants back.

*The contract* — `answer`. This is the shape the final message must have, and unlike the
frame it is not advice: it is checked. See [the answer contract](#the-answer-contract) below.

*The envelope* — `vector`, `grant`, `tools`. These name things rather than spelling them out.
`vector: auto|ask|park` sets how much the process must confirm; a firing that already
answered its own `gates` can say `vector: auto` rather than re-ask per action.
`grant: read-only|no-delegate|none|all` is the hard authority: an action outside it is
*refused*, not asked about, because nobody at a console was delegated the authority to widen
what the charter granted. `tools` narrows what the driver offers at all — `tools: [read_file,
list_dir]` on a review agent means shell and delegation never come up. `fail` is always
offered whatever you list, since it is how a process reports it could not do the work.

An unknown `vector`, `grant`, or tool name fails the spawn rather than falling back to
unbounded: a field that reads back as written and enforces nothing is the failure this is
built to avoid. `resolver` and `environment` remain declared and unread — see
[authority](roadmap/authority.md).

```tony
spawn:
  driver: llm
  task:   "Review $subject for security defects."
  answer: |
    severity: !or [low, high]
    note:     !irtype string
  grant:  read-only
  vector: auto
  tools:  [read_file, list_dir]
```

`seq` runs a list of actions in order, each starting when the previous one completes. It is
the depth axis, and it has its own section below.

### Bindings

Every field of the changed payload is bound by name (`$stage`, `$path`, …), plus `$subject`
— the *resolved* subject. So `review $subject` in a spawn's task names the PR the firing is
about, even when the entity that fired was a process, not the PR. A completion hook also
reads the finished process's payload: `$status`, `$result`, `$data`, `$err`, `$flow`,
`$chain`.

Those last four are what a process leaves behind, and they answer different questions:

| field | what it is |
|---|---|
| `status` | that the process **stopped** — `done`, `failed`, `cancelled`, `orphaned` |
| `result` | the driver's final answer, as text, always present |
| `data` | that answer parsed, **only when it is an object or a list** — absent otherwise |
| `err` | why it failed: infrastructure, a driver error, a declared `fail` |

`status: done` means the process stopped, not that the work succeeded. An agent asked to
review a file it cannot open will stop and say so in prose, and that is an ordinary
completion — its only structural exits are "keep going" and "stop". The `fail` tool gives it
a third, which is what `err` records, but a model can still stop without reaching for it.

So do not read success off `status`. **A hook that advances state should name evidence**:

```tony
onDone: { patch: { stage: reviewed, severity: $data.severity } }
```

An agent that failed cannot produce `$data.severity`, so the firing fails and the subject
does not advance — no detection of the lie required, and no reliance on the agent reporting
its own failure honestly. A hook spelled `patch: {stage: reviewed}` advances on a bare claim.
This is the same discipline a goal already follows: `doneWhen` latches on a witness in the
store, never on a process saying it finished.

A hook's failure is recorded against the process whose completion fired it, under `hook`:

```tony
hook: { chain: review.onDone, status: failed, err: "$data.severity is not present …" }
```

It goes there rather than on the subject because it is a fact about that completion, and
because putting an error into a field a charter matches on is exactly what makes
`fields: {severity: high}` quietly stop matching.

### The answer contract

That failure is late. `$data.class` is missing at PATCH time — after the work is done and
the tokens are spent — and what is left is a hook error on the process entity and a subject
stranded at a stage no rule matches. A frame asking for a shape does not prevent it: an
agent that has just read nine files narrates and fences before it answers, and the frame is
advice a model may ignore.

`answer` is the same request made enforceable — the shape the final message must have,
written as a tony **match pattern** (the `accept` clause of a schema, which is the same
vocabulary `when` is written in):

```tony
spawn:
  driver: llm
  task:   "Review $subject."
  answer: |
    class:    !or [bug, nit, risk]
    severity: !or [low, high]
    why:      !irtype string
```

It is checked when the model stops, which is the only moment it can be: a run ends by the
model ceasing to call tools, so stopping and reporting are one act. A failure goes back to
the agent as a **diagnosis** — the path, what was expected there, what was found, and every
such failure rather than the first — and it is asked to fix it. That is bounded at two
attempts: if a model cannot produce the shape with the complaint in front of it, more turns
will not teach it.

A run whose answer never satisfies its contract **fails**. That is the whole point:

- `patch: { class: $data.class }` can no longer fail at patch time, because a process that
  could not produce `class` did not succeed. The failure moves from a hook nobody watches to
  `onFailed`, which every rule already declares a branch for.
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
    severity: $result            # the process's own answer, as a fact on the subject
    step:     $verse.review.step # any depth, and list indices: $result.findings[0].id
```

The reference grammar is [tony](https://signadot.github.io/tony-format/)'s, not verse's:
`$result.findings[0].id` resolves by handing `result.findings[0].id` to the same kpath
navigation that addresses an action inside its trigger (`review.seq[2].onFailed`). The
familiar `$name` spelling *is* that kpath at depth one.

This is what closes the loop. Before it, a process's output could reach the next *prompt* and
nothing else, so a flow only ever advanced through state the charter wrote by hand. A result
patched onto the subject is an ordinary fact, and an ordinary `when` matches it — no
knowledge of processes required:

```tony
when: { system: demo, kind: pr, fields: { stage: reviewed, severity: high } }
```

Four semantics are worth knowing.

*Whole value versus interpolation.* A value that is **exactly** one reference resolves to
that reference's node, so a number stays a number and an object stays an object. A reference
**embedded in text** interpolates and yields a string, because that is how it was written.
The distinction is load-bearing: payload predicates are type-exact, so a `3` flattened to
`"3"` is a value `fields: {count: 3}` does not match.

```tony
patch: { count: $n }          # the number 3
patch: { note: "saw $n" }     # the string "saw 3"
```

*A composite has no text form.* `"saw $result"` where `$result` is an object is an error, not
a rendering — inventing one would make the same charter mean different things depending on
what a process happened to return. Reference it on its own to write the whole value.

*An absent reference fails the firing.* It is not dropped, and this follows from the store
recording an absent key as different from a null one: omitting it would launder "the process
never said" into "the process said nothing", which a match reads as a different fact. A field
written null resolves to null, as it should. The failure is the same one a failed write takes
— the action stops, and its spawn does not start.

*There is no escape.* `$$` is not a literal dollar; `a$$b` references `$b`. Anything that is
not a well-formed reference is left exactly as written (`$5.00`, `100$`, a bare `$`), which
is what keeps a charter that predates patch expansion meaning what it did.

A spawn's strings are still the older, flatter substitution: top-level fields only, always as
text, and an unresolved `$var` is left in place rather than failing. So `$verse.review.step`
derefs in a `patch` and does not in a `spawn.task`. Unifying the two halves onto one
resolver is the obvious next step and deliberately not taken here, because making a spawn
fail on an unbound variable would change what already-installed charters do.

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

The four hooks are named fields rather than an `on map[status]*Action`, which is what makes
an unknown status unwritable: there is no field to write it in, so nothing needs validating.
The cost is that a fifth terminal status would be a schema change, and that a misspelled
`onDoen` is dropped like any other unknown field.

Hooks survive a restart: resume rehydrates them from the process's chain, so `done`,
`failed` and `cancelled` still fire. `onOrphaned` is inert, and not for want of
rehydration — orphaning and adopting are the two alternatives in reconcile, so a process
that can be re-attached is adopted and never marked orphaned, and one that cannot is
orphaned and never tracked. No tracked process reaches that status.

### `seq`: sequence along the chain, parallel across triggers

Nesting `onDone`s expresses a chain, but it right-drifts and — the reason that matters — has
no arity. The engine knows there is a next hook; it never knows how many remain, so a
sequence written that way cannot say where it is. `seq` spells the depth axis directly:

```tony
- name: review
  condition:
    when: { system: demo, kind: pr, fields: { stage: open } }
  gates:
  - { name: review, mode: ask }
  action:
    seq:
    - spawn: { driver: scripted, task: "lint $subject" }
    - spawn:    { driver: scripted, task: "review $subject" }
      onFailed: { patch: { stage: rejected } }
    - patch: { stage: reviewed }
    onDone:
      patch: { stage: ready }
```

There is deliberately no `par`. Breadth is what independent triggers matching the same
condition already do, and a process id already carries both directions: `review@demo:pr:1`
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
  ordered writes rather than an error. `seq` sequences actions, not processes.
- Gates gate the sequence, not the step: gates are per firing and a sequence is one firing,
  so one `ask` approves the whole thing.
- No chain may contain two `seq` segments — stated over chains rather than elements, because
  an element's `onFailed` holding a sequence nests just as much, only further away. With at
  most one `seq` per chain, "the sequence this process belongs to" is a unique ancestor
  rather than a stack, which is what keeps re-fire idempotence a predicate rather than a
  search.

### Chains and ids

A spawned process is identified by `<chain>@<subject>`, where the chain is the
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
into the charter, which is how resume rehydrates a process's hooks and how a sequence finds
its next step. And it is published on the process entity as `chain`, with its root as
`trigger`, so "every process of this trigger" is a payload predicate rather than a glob over
ids.

The id drops the brackets because they are shell-hostile: `zsh` kills
`review.seq[2]@demo:pr:1` with `no matches found` before verse ever sees it, and ids get
pasted onto command lines constantly. The dotted form is lossless because `Action`'s field
set is closed — a segment after `seq` is an element, a segment matching a hook name is a
field. The `@` join replaced an earlier `-`, which could not be decoded back because trigger
names spend `-` too.

### Progress facts

A parallel branch is woken by a `when`, and a `when` matches facts — so a sequence publishes
its position, or the breadth axis cannot observe the depth axis at all. The engine writes it
on the subject under one reserved `verse` root as each element completes:

```tony
verse:
  review:            # keyed by trigger
    step:  2         # how many elements have completed
    steps: 3         # len(seq), known at install
    shape: "a3f1…"   # hash of the declared shape, known at install
    done:  true      # latched when step reaches steps
```

A join reads it at whatever depth it cares about:

```tony
when: { system: demo, kind: pr, fields: { verse: { review: { done: true } } } }
```

One reserved name, not a family. The alternative — flat per-trigger keys like `review.step` —
reserves an open-ended set of names out of a payload that is the author's own document. A
single `verse` root reserves one name and documents once, the way `flow` is one reserved key
on process entities. Nested predicates keep it readable, since `fields` matches structurally
and so matches a payload carrying any amount of other content.

`done` is published rather than derived because a match compares a value against a pattern,
never one field of a payload against another. A join spelled `step == steps` is not
expressible; one spelled `{step: 3}` silently stops meaning "finished" the moment the
sequence gains an element. So the engine latches `done` when `step` reaches `steps`, and the
join matches that.

`shape` is a hash of the declared sequence, compared on each advance. A mismatch means the
charter moved underneath, and the sequence fails with a reason rather than silently running
whatever now sits at `seq[k+1]`. It is a hash rather than a pinned copy because these
sequences can run for a long time, and a retained snapshot per live sequence is a retention
floor the store can never compact past. Refusing to continue needs detection, not
reconstruction — so the error can say that the shape changed and name the trigger, but not
what changed.

## Flow — a subject-match over live processes

A flow is not a stored key. It is a match evaluated over the subjects of *live* processes:

```sh
verse flow pause demo:pr:1      # processes whose subject matches demo:pr:1
verse flow pause demo:pr:*      # …the whole kind
```

Relating processes is what a match *does*, so "relational flows" are not a second primitive
to add later — they are broader subject-matches. `verse flows` is the distinct subjects with
live processes; steering applies the match and acts on the matched processes. A query, not a
maintained index.

Goals sit here too, and split the same way: a goal is a subject (a point-in-time match — the
flow it owns and steers) plus a `doneWhen` (a temporal formula that latches). One of each
layer, no new machinery.

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

None of this needs a match to "select one entity" — it needs the change stream to be
per-entity, and it is. A docd commit is atomic across many branches, but `LogdStore`
normalises each commit into one `Event` per entity at path-depth `system/kind/id`. So a
firing is driven by one event → one `Ref` → `when`'s matched entity is exactly one: the
subject is well-defined *structurally*, not by constraining the pattern. One commit that
changes `…:a` and `…:b` yields two events, two firings, and subjects `a` and `b`, with no
fan-out logic. The known cost, recorded in [change events](change-events.md): transaction
atomicity is flattened — the reactive grain is the entity, not the commit.

## Limits today

A temporal `when` is not evaluated yet, and is rejected at install with an error that says
so: a temporal when has no change to dispatch on, so it needs a long-lived monitor over the
subtrees it reads. Use a point-in-time match in a `when`; temporal composition works today in
a gate's `formula` and a goal's `doneWhen`.

An operator on `system` or `kind` is refused for the structural reason given above, and
recovering it needs either a full scan or expansion into N literal subtrees — a design
question rather than a port. Keyed sequence steps (`review.seq(lint)`, addressing an element
by name so it survives an insertion) are the better form on their own terms, but
[kpath](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath)'s `!key` segment
has no defined semantics upstream yet.

The substrate all of this leans on is [change events](change-events.md).

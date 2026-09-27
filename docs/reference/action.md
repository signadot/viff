# The action of a rule

This page holds the `action` field of a rule: `patch`, `derive`, `spawn`, `calls` and `seq`.
It also holds what an action can read, the answer contract, completion hooks, and the ids and
progress facts that a firing writes. The model that the action belongs to is in
[the trigger model](../trigger-model.md).

## `action` — what fires

Data, not code. An action needs no target of its own: it acts on the condition's subject.

**Every rule has one, and a rule that would do nothing is refused at install.** `patch`, `derive`,
`spawn` or `seq`: any one is enough. A rule with none of them would install, report itself
installed, and then watch its subtree for the life of the verse without ever writing anything.
Completion hooks do not count, because a hook is what happens when something finishes and there is
nothing there to finish, which is also why an `onX` without a spawn or a seq is refused. A gate
does not count either: a gate on a rule with no action poses a question whose answer nothing
reads.

**An action resolves everything, then writes.** The order is: resolve the `patch` and every
`derive`, write the patch, write each derived entity, then `spawn`. A reference that does not
resolve, or a read of an entity that is not there, fails the firing before anything is
written. The subject stays where it was, so the rule fires again on the next commit that
touches it, and the failure is kept on the rule
([when a rule does nothing](../trigger-model.md#when-a-rule-does-nothing)). The same holds for a completion hook.

The writes are separate commits. If the store fails between two of them, what landed stays.

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
`- {path: "verse-dev.drift.head", value: …}` — and its content may reference its own subject and,
at or below its own level, other entities. It is only installable inside a declared level ordering, where it must land
strictly above the rule that writes it. See [strata](../strata.md).

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
[strata](../strata.md#each--when-one-answer-holds-many-facts).

A single derive may carry a `key:` too — `- {path: "verse-dev.merge.train", key:
$subject.value.sha, value: …}` — which makes its address a function of (rule, subject, key)
instead of (rule, subject). One entity per value of something, where the same subject keeps
moving: see [strata](../strata.md#a-key-on-a-single-derive).

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
[provider selection](../provider-selection.md).

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
once for the firing; see [a gate's question](tasks.md#a-gates-question).
`grant: read-only|no-delegate|none|all` is the hard authority: an action outside it is
*refused*, not asked about, because nobody at a console was delegated the authority to widen
what the charter granted. `tools` narrows what the driver offers at all — `tools: [read_file,
list_dir]` on a review agent means shell and delegation never come up. `fail` is always
offered whatever you list, since it is how a task reports it could not do the work.

An unknown `vector`, `grant`, or tool name fails the spawn rather than falling back to unbounded. A
field that reads back as written and enforces nothing is the failure this is built to avoid.
`resolver` names the resolver that answers this run's gates, and a spawn naming one that is not
installed is refused rather than left unsupervised; see
[attached resolvers](../attached-resolvers.md). `environment` names the workspace the task runs
in. verse carries the name to the runner, which resolves it and refuses one it does not have.

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
derive: [{path: "notes.row", value: {sha: $subject.name}}]
# …and the next rule latches on the absence, which is nobody's to overwrite
condition: { subject: {path: "notes.row.*", value: !not.has-path reviewed} }
```

A charter that does it the other way earns a note at install naming both rules and the field.

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
    branches: !list-paths {at: "git.repo.*", path: branch}
    # …and a predicate narrows the walk to the ones that look like this (list only)
    green:    !list-paths {at: "git.repo.*", value: {state: green}, path: branch}
    # the string form is the kinded path, for an address with nothing to expand in it
    head:     !get-path git.verse.head
    # and `at:` names the address as a path, at whatever depth the tree is
    says:     !get-path { at: "verse-dev.drift.head.aaaa1111", path: result.report }
    open:     !list-paths { at: "verse-dev.drift.finding.*", value: {status: open}, path: says }
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
      path: "merge.proposal.*"
      value: !let
        let: [{tip: !get-path {at: "git.verse.head", path: sha}}]
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
[strata](../strata.md), where that is the whole of what they cost. In a `patch` they are
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
subject: {path: "demo.pr.*", value: { stage: reviewed, severity: high }}
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
    subject: {path: "demo.pr.*", value: { stage: open }}
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
subject: {path: "demo.pr.*", value: { verse: { charter: { review: { done: true } } } }}
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

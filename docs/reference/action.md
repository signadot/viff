# The action of a rule

This page describes the `action` field of a [rule](../glossary.md#rule): `patch`, `derive`,
`spawn`, `calls` and `seq`. It also describes what an action can read, the answer contract,
completion hooks, and the ids and progress facts that a firing writes. The model that the
action belongs to is in [the trigger model](../trigger-model.md).

## `action` — what fires

The [action](../glossary.md#action) of a rule is data, not code. An action needs no target of
its own. It acts on the [subject](../glossary.md#subject) of the condition.

| Field | What it does |
|---|---|
| `patch` | changes the payload of the subject ([patch](#patch)) |
| `derive` | writes an entity that is not the subject ([derive](#derive)) |
| `spawn` | starts a task ([spawn](#spawn)) |
| `calls` | inside a `spawn`: the list of actions the task runs, written by the charter ([calls](#calls--when-the-charter-knows-the-actions)) |
| `seq` | runs a list of actions in order ([seq](#seq-sequence-along-the-chain-parallel-across-triggers)) |
| `onDone` | an action that fires when the task ends `done` ([completion hooks](#completion-hooks-one-trigger-chaining-visible)) |
| `onFailed` | an action that fires when the task ends `failed` ([completion hooks](#completion-hooks-one-trigger-chaining-visible)) |
| `onCancelled` | an action that fires when the task ends `cancelled` ([completion hooks](#completion-hooks-one-trigger-chaining-visible)) |

**Every rule has an action, and verse refuses at install a rule that would do nothing.** Any one
of `patch`, `derive`, `spawn` or `seq` is enough. A rule with none of them would install and
report itself installed. It would then watch its subtree for the life of the store and never
write anything.

Completion hooks do not count as an action. A hook is what happens when something finishes, and
without a spawn or a seq nothing is there to finish. For the same reason, verse refuses an `onX`
without a spawn or a seq. A gate does not count either. A gate on a rule with no action poses a
question whose answer nothing reads.

**An action resolves everything, then writes.** The order is:

1. resolve the `patch` and every `derive`;
2. write the patch;
3. write each derived entity;
4. `spawn`.

A reference that does not resolve fails the firing before anything is written. So does a read
of an entity that is not there. The subject stays where it was, so the rule fires again on the
next commit that touches it. The failure is kept on the rule
([when a rule does nothing](../trigger-model.md#when-a-rule-does-nothing)). The same holds for a
completion hook.

The writes are separate commits. If the store fails between two of them, what landed stays.

### `patch`

`patch` writes to the payload of the subject. It is a state transition.

A [patch](../glossary.md#patch) is a tony patch, not an assignment. A nested write merges into
the subtree it names, and does not replace it. So two rules that write under one key do not
overwrite each other's fields. The
[mergeop](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop) operators apply.
`patch: { tmp: !delete null }` removes a key. Setting fields cannot say that. Writing a scalar
(`patch: 42`) replaces the whole payload.

A nested write merges into an object. The subject may hold an array or a scalar under that key.
Then nothing is there to merge into, and verse refuses the firing. It does not replace what
stands there:

```yaml
# subject: {stage: counting, votes: [{by: ana}, {by: bo}]}
patch:
  stage: counted
  votes: {bo: yes}      # refused: the patch merges an object into votes, where an array stands
```

The refusal fails the firing, as any patch does that could not be established.

To replace a container on purpose, write the whole value at that key (`patch: {votes: [...]}`).
That is a value and not a merge, and it says so.

A key that the subject does not carry yet is a different case. A patch builds it. Every derive
that grows structure depends on this.

### `derive`

`derive` writes an entity that is *not* the subject. A `patch` cannot do this. A
[derive](../glossary.md#derive) is a list of entries. Each entry names an address as a path, and
what goes there: `- {path: "verse-dev.drift.head", value: …}`. Its content may reference its own
subject. It may also reference other entities, at or below its own level.

A derive is installable only inside a declared order of levels, and it must land strictly above
the rule that writes it. See [strata](../strata.md).

**Unnamed, the address is the engine's, and it is the provenance.** The address is the place,
then the rule, then the segments of the subject. A rule that selects `git.verse.ref.*` and derives
into `[verse-dev, merge, proposal]` writes

    verse-dev.merge.proposal.proposal-appeared.git.verse.ref."verse/auto/…"

This address has **eight** segments: three for the place, one for the rule, four for the
subject. No two rules can address the same entity.

**So a reader of a derivation says how deep one is.** The depth follows from the depth of the
subjects that the producing rule selects. `[verse-dev, merge, proposal, "*"]` has four segments.
It selects the *rule* level, whose payload is the whole subtree of the derivations of that rule,
and not any one of them.
[The pattern says how deep its subjects are](condition.md#the-pattern-says-how-deep-its-subjects-are)
says what a wrong depth does.

**Naming one is how a derive stays shallow.** `id:` puts the entity at the place plus that one
segment: `verse-dev.merge.proposal."verse/auto/…"`, which has four segments. A reader already
spells that address. A derive usually wants an `id:` when its subjects stand under one place and
the rules that read them name the place literally.

One firing derives one entity, unless it says `!each`. `!each` fans out over a list and derives
one entity per element. The address of each takes a key read out of the element. This is the
same invariant, with the key in the address. With it, the twelve findings of a task become
twelve facts, and not eleven paragraphs. See
[strata](../strata.md#each--when-one-answer-holds-many-facts).

A single derive may carry a `key:` too:
`- {path: "verse-dev.merge.train", key: $subject.value.sha, value: …}`. Its address is then a
function of (rule, subject, key), and not of (rule, subject). It gives one entity per value of
something, where the same subject keeps moving. See
[strata](../strata.md#a-key-on-a-single-derive).

**`id:` is that key with the composition dropped.** The difference is for the reader. A keyed
entity stands at `verse-dev.drift.head.head-moved.git.verse.head.aaaa1111`: the place, the rule,
the subject, the key. Nothing in the grammar composes that address from a subject. So a reader
that wants the record must *describe* it, with a `"*"` per segment and a predicate on a field it
happens to know. It cannot name it.

With `id: $subject.value.sha` the record is `verse-dev.drift.head.aaaa1111`, and a reader spells
it: `!get-path {at: [verse-dev, drift, head, $subject.value.sha], path: result.report}`.

`id:` gives the author the job of keeping addresses unique.

| Field | Two subjects of one rule that compute the same value | What it claims |
|---|---|---|
| `id:` | share one entity | the value identifies the record **across subjects**. This is true when the subject is a singleton, as `git.verse.head` is. |
| `key:` | get one entity each | the value identifies the record within one subject only |

The *place* stays the engine's in both cases, because a derive may write only inside its own
stratum. Verse checks this at install. Verse refuses a derive that carries both `id:` and `key:`.

`!each` takes both too, and there the difference matters more.

- Keyed, every element lands under (rule, subject, key). A fan-out can only create its **own**
  copies.
- Named, every element lands at the place plus the value. In this way the fan-out of one rule
  reaches entities that another rule names. It is the only way to say "mark every one of those
  that is already there".

A derive merges at depth, so the mark lands beside the facts, and not over them.

### An install re-fires, so a derive must own what it writes

Installing a charter evaluates its rules against what is already in the store
([the backlog](condition.md#the-backlog)). So an install
acts on the world, and not only on what happens next. A `derive` is a patch merged at depth, so
it overwrites exactly the fields it names.

Together, these two facts mean: a derive that names a field that *another* rule owns rewrites
that field **every time the charter is installed**. It writes over whatever state the flow had
reached. A rule that has not changed, applied again, sets a flow at `reviewed: baseline` back to
`pending` and `gathered`. Nothing fails. The derive writes its own value again.

So a derive states the facts it owns. The rule that follows it starts from an **absence**:

```tony
# the derive says what it knows, and nothing about the flow's state
derive:
- path: "notes.row"
  value: {sha: $subject.name}
# …and the next rule latches on the absence, which is nobody's to overwrite
condition:
  subject:
    path: "notes.row.*"
    value: !not.has-path reviewed
```

For a charter that does it the other way, the install prints a note that names both rules and
the field.

### `spawn`

`spawn` launches a [task](../glossary.md#task) from a `launch.Spec`. Instead of a fixed
`driver`, a spawn may carry a `provider` selection, which chooses the doer and is governed. Verse
tags the spawned task with the subject. The tag makes the task part of the flow of that subject.

A [provider selection](../glossary.md#provider-selection) binds the whole doer, and not only its
driver. The `driver`, `frame`, `tools` and `grant` of the winning provider entity overlay those of
the spawn. So two doers on one driver can differ in what they are told and what they may do.

The overlay only narrows:

- the grant of a doer must be within the grant of the spawn, so `grant:` is required on a spawn
  that selects;
- the tools of a doer intersect the tools of the spawn, and do not replace them;
- `vector` is not for a doer to set.

Verse refuses a doer that would widen the spawn at *eligibility*, so it never appears on a
ballot. See [provider selection](../provider-selection.md).

The fields of the spec fall into three groups. The group of a field decides what a charter can
say with it.

| Group | Fields | What they are |
|---|---|---|
| *The request* | `prompt`, `frame` | text you write |
| *The contract* | `answer` | the shape the final message must have. Verse checks it. |
| *The envelope* | `vector`, `grant`, `tools` | names of things, not text |

**The request.** `prompt` is the one job. `frame` is the standing instructions under which the
task reads the prompt. For an agent it is the system prompt. The default frame tells the agent
that its last message *is* the result, and not to summarise. Only the charter knows what shape
it wants back. So write a frame whenever the charter, and not a person, reads the answer.

**The contract.** `answer` is not advice, as the frame is. Verse checks it. See
[the answer contract](#the-answer-contract).

**The envelope.**

| Field | What it does |
|---|---|
| `vector` | sets how much the task must confirm. A firing that already answered its own `gates` can say `vector: auto`, and does not ask again for each action. |
| `grant` | the hard authority. Verse *refuses* an action outside it, and asks nobody. |
| `tools` | narrows what the driver offers at all. `tools: [read_file, list_dir]` on a review agent means that shell and delegation never come up. |

The values are `vector: auto|ask|park` and `grant: read-only|no-delegate|none|all`.

A spawn that wants the task gated for each action writes the task's own `gates:` beside it.
These gates have the same shape as the gates of the rule, and each poses its question. They run
at every boundary of the task, and not once for the firing. See
[a gate's question](tasks.md#a-gates-question).

Nobody at a console was given the authority to widen what the charter granted. For this reason
verse refuses an action outside the `grant`, and does not ask about it.

`yield` is always offered, whatever `tools` lists. It is how a task hands control back. With the
reason `cannot`, it is how a task reports that it could not do the work.

An unknown `vector`, `grant` or tool name fails the spawn. Verse does not fall back to
unbounded. A field that reads back as written and enforces nothing is the failure that this rule
prevents.

| Field | What it names |
|---|---|
| `resolver` | the resolver that answers the gates of this run. Verse refuses a spawn that names a resolver that is not installed, and does not leave the run unsupervised. See [attached resolvers](../attached-resolvers.md). |
| `environment` | the workspace that the task runs in. Verse carries the name to the runner. The runner resolves it, and refuses a name it does not have. |

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

`retain` is how many events of the task's event stream verse keeps for a reader that resumes.
A `?since=` below that window gets a resync marker instead of the events. Omit it, and the
default of the runtime applies. That default is small. A longer window costs memory for each
live task. The reason `retain` is set per spawn is in
[Why it is this way](../design/reference-action.md#retain-is-per-spawn).

### `as: subject` — a task for whoever wrote the record

The task of a rule acts as the installer of the rule. `as: subject` makes it act for the owner of
the record that fired it as well:

```tony
spawn:
  driver: llm
  prompt: $subject.value.then
  as: subject
```

The task acts as the pair of them, `pair:<owner> <installer>`, on the owner's authority. A
consequence belongs to whoever raised it: asked "may I merge this?", an answerer's yes lets the
owner merge what the owner may. Neither the answerer nor the installer needs that right, and the
installer's reach is not lent to the owner.

A rule may say `as: subject` only where its subject lies in a place the installer owns: the
settled part of the subject's path, the last segment left out. Anywhere else, the installer
could select somebody's records and run a task of the installer's design as them, so verse
refuses the charter at install.

The task record is the owner's, and says so as `asker:`. Its own gates ask the owner. Where the
owner is the system, the task acts as the rule alone.

`subject` is the only value that `as` takes. Verse refuses any other at install. It also refuses
a charter that writes `asker:` itself, because the engine sets it.

### `calls` — when the charter knows the actions

A spawn can carry the work itself, instead of a prompt for something to work the work out. A
merge is four git commands. A model that finds them again at each firing adds nothing but the
chance of doing something else.

```tony
spawn:
  vector: auto
  calls:
  - tool: run_bash
    input: {command: "git merge --ff-only origin/$branch"}
  - tool: push
    input: {destination: main}
```

The presence of `calls` **is** the driver. `driver: calls` may be written, and it must not
contradict the list. Each entry becomes an ordinary action. It is derived, gated and narrated in
the same way as any other. So a gate written for the push of an agent holds this push too.

Verse expands values for each argument separately. So a `$branch` that contains a quote is one
argument. It is not text that has to survive being spliced into a command.

An entry may narrow what ends the list:

```tony
  - tool: push
    input: {destination: retire}
    fatal: [err]        # a remote declining one ref is not a reason to stop
```

`fatal` lists outcomes from these three:

| Outcome | Meaning |
|---|---|
| `err` | the action failed |
| `blocked` | the action was refused, by a gate or by the far side |
| `nonzero` | a command exited non-zero |

An absent `fatal` means all three, because a list is ordered when its calls depend on each other.
`fatal: []` means that this call ends nothing, which is what a sweep says. The two are different
statements, so writing neither is not the same as writing the empty one.

A list with an unknown outcome, no tool, or a driver that contradicts it fails the *firing*.
There its author can read why. A tool name that verse cannot check belongs to the runner, and
the runner refuses it.

A call list does not get what belongs to a model:

- no `answer` contract, because nothing can repair one. A spawn that carries both fails.
- no delegation.
- no choice of the next action from what the last result said.

The last one is the trade. It is right for four git commands, and wrong for anything that
branches.

### Bindings

A [binding](../glossary.md#binding) is a name that begins with `$`. Every field of **the payload
an action reads** is bound by name (`$stage`, `$path`, …). `$subject` is bound too, to the
resolved subject. Which payload the action reads has exactly two answers. The answer depends on
*why* the action runs.

**A firing of a rule reads the payload of the subject itself.** The change that dispatched the
firing may name a different entity. The fields of that entity would mean nothing on the subject
being acted on. So `review $subject` in the prompt of a spawn names the PR the firing is about,
even when the entity that fired was a task and not the PR. `$stage` is the stage of that PR, and
not of the task.

**A completion hook reads the payload of the finished task.** So does an element of a sequence
that a hook drives. `$status`, `$result`, `$data` and `$err` are the facts it exists to read. The
engine makes that payload itself, and does not read it from the store. The mirror may not have
written the terminal facts yet. This is why those bindings resolve at all, and why they are
flat. They are facts about a run, and no entity holds them at a path.

`$subject` is a record. The bare form is its address.

| Binding | Value |
|---|---|
| `$subject` | the whole ref, `demo.pr.1` |
| `$subject.ref` `.name` | the address, and its last segment: the key that the subject stands at |
| `$subject.value` | the payload of the subject **at the commit that caused the firing**, and not at the moment the action runs. The two differ by however long a gate took to be answered. |
| `$subject.commit` | the revision of that commit |

**A completion hook reads at the same commit that its rule fired at.** A task that finishes wakes
the hook, and no write does. So the hook has no cause of its own. It takes the cause of the
firing, which the record of the task carries as `occurrence`. So the hook survives a restart,
even when the engine that runs the hook never saw the firing.

In this way a rule is one statement about one moment. `$subject.value` in an `onDone` is the
subject as the condition selected it. It is not the subject as the action left it one step
earlier. A charter whose task does not write its own subject gets the reading its author meant.

A task that the charter did not start has no such commit. There `.value` and `.commit` fail by
name. They do not resolve to a silent zero.

In the strings of a spawn, the parts of the identity bind flatly. `.value` does not bind there,
because a payload has no text form and its top-level fields are already bound by name.

### `!get-path` and `!list-paths` — reaching another entity

Everything in [Bindings](#bindings) reads the subject of the firing, or the task that just
finished. A [path read](../glossary.md#path-read) reaches an entity that is neither of those:

```tony
action:
  patch:
    repo: !get-path
      at: [git, repo, $subject.value.head]
    branch: !get-path
      at: [git, repo, $subject.value.head]
      path: branch
    branches: !list-paths
      at: "git.repo.*"
      path: branch
    # …and a predicate narrows the walk to the ones that look like this (list only)
    green: !list-paths
      at: "git.repo.*"
      value: {state: green}
      path: branch
    # the string form is the kinded path, for an address with nothing to expand in it
    head:     !get-path git.verse.head
    # and `at:` names the address as a path, at whatever depth the tree is
    says: !get-path
      at: "verse-dev.drift.head.aaaa1111"
      path: result.report
    open: !list-paths
      at: "verse-dev.drift.finding.*"
      value: {status: open}
      path: says
```

The address is `at:` in a read, and `path:` in a condition. In a read, `path:` is the field path
inside the payload.

**One grammar, split by cardinality, and the names say which.** `!get-path` answers with one
node. `!list-paths` answers with a list, and it is where the globs are.

The charter shows which one you get. It does not depend on whether a segment is a `*`.
`!get-path` **refuses** a wildcard in its last segment or in its `path`. The refusal names the
tag that answers that question instead.

| Form | What it is |
|---|---|
| a string | the address as a kinded path (`git.verse.head`), with nothing in it to expand. It may go on into the payload (`git.verse.head.sha`). |
| `at` | the address as a path of at least two segments. Every segment above the last is a literal or `*`. The last is a literal, a `$reference`, or (list only) a `*` or an operator. |
| `path` | a kpath into the payload. Wildcards are list-only. Omitted, you get the whole payload. |
| `value` | a predicate on the payload, **list only**: of the entities the address selects, the ones that look like this |

No segment above the last is a reference or a glob. The level check asks whether a read lands at
or below the stratum of the rule. It asks this of the literal prefix, because that is all that is
known at install.

`!get-path` refuses a `value`, for the same reason it refuses a wildcard. A predicate can name
several entities. An entity that is not there does not satisfy one, and an entity that is there
might not either.

On the list form, a predicate makes the read *cheaper*. A `*` already walks everything under the
place, and the predicate narrows it.

Neither form is a query that moves with time. Both are answered from one snapshot at the causing
commit. So verse evaluates a predicate against the same world that the rule saw.

The dots of the string form are its segments. A `$reference` has dots of its own, so it cannot
be written inside one. Write an address that expands a reference with `at:`.

**In an action, they copy and do not track.** Verse reads the value once. If it changes
afterwards, nothing runs again. This rule fires again only when its own subject changes, or when
an `on` or `when` that it carries covers the target. Add the `on` if you want to follow the
target.

### A condition may read too, and that one does track

A read in the predicate of `condition.subject` is answered before the predicate is matched. So a
rule can select on how its subject compares to another entity. A match alone cannot say that. A
match tests a field against a literal or an operator, and never against a value read from
elsewhere. `!let` binds the read, and the body compares:

```tony
- name: proposal-outrun
  condition:
    subject:
      path: "merge.proposal.*"
      value: !let
        let:
        - tip: !get-path
            at: "git.verse.head"
            path: sha
        in:
          state: open
          base: !not .[tip]
  action:
    patch: { state: stale }
```

This rule selects every open proposal cut from a commit that is not the tip of main. It answers
"which of these can still land", with no walk of ancestry and nothing to poll.

**The read joins what the rule reads.** This is how it differs from a read in an action. Verse
evaluates the rule `proposal-outrun` again when `git.verse.head` moves, over proposals that did
not change.

Verse skips a rule at a commit that touched nothing the rule reads. A condition that compares
against another entity reaches a different verdict as soon as that entity moves. So the read must
count, or the skip would be wrong.

**The read cannot name `$subject`.** In an action the subject is settled, and the action
addresses other entities by its fields. A condition *chooses* the subject, so no one entity is
there to read from yet. Verse refuses it at install, and says so. It does not say "unknown
reference".

**A missing `!get-path` target fails the firing**, as any unresolved reference does. So "nothing
is there" cannot turn into "something is there, and it is nothing". An empty `!list-paths` is an
empty **list**. "Which one" has an answer or does not. "All of them", of nothing, is correctly
nothing.

**Inside a `derive`, the reads must resolve at or below the stratum of the rule.** See
[strata](../strata.md), where that is the whole of what they cost. In a `patch` they have no
such limit.

**The tag is the whole spelling.** A plain `{get-path: …}` key is ordinary data, and stays data.
So a charter written as JSON cannot write a path read, because JSON has no tags.

### The answer contract

`answer` is the shape that the final message of the task must have. The
[answer contract](../glossary.md#answer-contract) is written as a tony **match pattern**: the
`accept` clause of a schema. It uses the vocabulary that `when` uses, and it is stored in the same
way, under the same `!raw` escape:

```tony
spawn:
  driver: llm
  prompt:   "Review $subject."
  answer:
    class:    !or [bug, nit, risk]
    severity: !or [low, high]
    why:      !irtype string
```

Verse checks the contract when the model stops. That is the only moment it can check it. A run
ends when the model stops calling tools, so stopping and reporting are one act.

A failure goes back to the agent as a **diagnosis**, and the agent is asked to fix it. The
diagnosis says:

- the path;
- what was expected there;
- what was found, keeping absent apart from present but wrong;
- every such failure, and not only the first.

The diagnosis is the match's own account of how it walked (`tony.Explaining`). So a failure
inside an operator is located as precisely as the operator knows it: `findings[1].severity`, and
not "findings". Repair is limited to two attempts. A model that cannot produce the shape with the
complaint in front of it will not learn it in more turns.

A run whose answer never satisfies its contract **fails**. This has four consequences.

- `patch: { class: $data.class }` cannot fail at patch time, because a task that could not
  produce `class` did not succeed. The failure lands on `onFailed`, for which every rule already
  declares a branch. It does not land on a hook that nobody watches.
- The check is on the answer, not the layout. Verse unwraps a fenced answer, so the repair
  attempts go to the content.
- The contract is a guarantee about *shape*, not about content. A repair loop pushes a model to
  comply. So the agent is also told to call `yield` with the reason `cannot`, and not to invent a
  value it could not determine. An honest failure is worth more than a well-formed wrong answer.
- A sub-agent (`call_agent`) does not inherit the contract. A child answers a subtask that its
  parent worded, and its result comes back as one more tool result. Only the final message of the
  parent is what a charter reads.

A contract that the driver cannot parse fails the firing where it is written, before any call to
a model. An unknown tool name does the same. A driver with no model to ask again, such as
`scripted`, fails the spawn. It does not run the spawn unchecked. The charter downstream was
written in the belief that the contract holds, so a contract dropped without a word is worse than
none.

A `patch` resolves references too. There a reference is a **deref by kpath**, not a template
substitution. It names a value, and does not compute one:

```tony
onDone:
  patch:
    stage:    reviewed
    severity: $result            # the task's own answer, as a fact on the subject
    step:     $verse.review.step # any depth, and list indices: $result.findings[0].id
```

The grammar of references is that of [tony](https://signadot.github.io/tony-format/), not of
verse. `$result.findings[0].id` resolves by handing `result.findings[0].id` to the same kpath
navigation that addresses an action inside its rule (`review.seq[2].onFailed`). The familiar
`$name` spelling *is* that kpath at depth one.

A result patched onto the subject is an ordinary fact, and an ordinary condition matches it. The
condition needs no knowledge of tasks. This closes the loop:

```tony
subject:
  path: "demo.pr.*"
  value: { stage: reviewed, severity: high }
```

References in a patch follow four rules.

**Whole value against interpolation.** A value that is **exactly** one reference resolves to the
node of that reference. So a number stays a number, and an object stays an object. A reference
**embedded in text** interpolates and gives a string, because that is how it was written.
Predicates on the payload are exact about type. So a `3` flattened to `"3"` is a value that
`value: {count: 3}` does not match.

```tony
# the number 3
patch: { count: $n }
# the string "saw 3"
patch: { note: "saw $n" }
```

**A composite has no text form.** `"saw $result"`, where `$result` is an object, is an error. It
is not a rendering. With a rendering, the same charter would mean different things, depending on
what a task returned. Reference the object on its own to write the whole value.

**An absent reference fails the firing.** Verse does not drop it. The store records an absent key
as different from a null one. Omitting the reference would turn "the task never said" into "the
task said nothing", and a match reads those as different facts. A field written null resolves to
null. The failure is the same one that a failed write takes: the action stops, and its spawn does
not start.

**There is no escape.** `$$` is not a literal dollar, and `a$$b` references `$b`. Anything that
is not a well-formed reference is left exactly as written: `$5.00`, `100$`, a bare `$`.

The strings of a spawn use a flatter substitution:

- top-level fields only;
- always as text;
- an unresolved `$var` stays in place, and does not fail.

So `$verse.review.step` derefs in a `patch`, and does not in a `spawn.prompt`.

A field that holds a **document** renders as tony in a prompt. A charter that puts a ledger in
front of an agent puts a document there:

```tony
prompt: "Outstanding findings:\n$findings\n\nThe diff:\n…"
```

Leaves do not change. `$stage` and `$count` bind exactly their own bytes (`3`, never `"3"`). A
null field binds empty.

### Completion hooks: one trigger, chaining visible

An `Action` carries one [hook](../glossary.md#hook) for each terminal status:

| Hook | Status |
|---|---|
| `onDone` | `done` |
| `onFailed` | `failed` |
| `onCancelled` | `cancelled` |
| `onOrphaned` | `orphaned` |

Each hook is itself a full `Action` (`patch` + `spawn` + its own hooks). So a chain
(`review → merge → approve`) nests `onDone`s, and reads from top to bottom in one rule.

When the `spawn` of the action reaches a terminal status, the engine fires the matching hook
about the *same subject*, for exactly that task. The engine finds the hook through the registry
of live tasks, keyed by task id. So a hook needs no `flow` match and no separate rule.

```tony
action:
  patch:  { stage: reviewing }
  spawn:
    driver: scripted
    vector: auto
    prompt: "review $subject"
  onDone:
    patch: { stage: reviewed }
```

The alternative is a *decoupled* completion rule. That is a separate rule that fires on the
change to `verse.task.instance` and names the PR by pattern. Use it when a completion should fan
out into several reactions, advance a different subject, or be written separately. Use the hook
for a linear chain that one rule owns, with the task step visible in the rule.

The four hooks are named fields. So an unknown status cannot be written: no field exists to write
it in. A misspelled `onDoen` is dropped, like any other unknown field.

Hooks survive a restart. Resume restores them from the chain of the task, so `done`, `failed`
and `cancelled` still fire.

`onOrphaned` never fires. Orphaning and adopting are the two alternatives in reconcile. A task
that can be attached again is adopted, and never marked orphaned. A task that cannot is
orphaned, and never tracked. So no tracked task reaches that status.

### `seq`: sequence along the chain, parallel across triggers

`seq` spells the depth axis directly:

```tony
- name: review
  condition:
    subject:
      path: "demo.pr.*"
      value: { stage: open }
  gates:
  - name: review
    mode: ask
  action:
    seq:
    - spawn:
        driver: scripted
        vector: auto
        prompt: "lint $subject"
    - spawn:
        driver: scripted
        vector: auto
        prompt: "review $subject"
      onFailed:
        patch: { stage: rejected }
    - patch: { stage: reviewed }
    onDone:
      patch: { stage: ready }
```

There is no `par`. Breadth is what independent rules that match the same condition do.

A [seq](../glossary.md#seq) follows these rules.

- `seq` and `spawn` are mutually exclusive on one action. `patch` beside `seq` is allowed: the
  patch applies, then the sequence runs.
- The sequence owns the `done` slot of each element. So verse refuses at install an explicit
  `onDone` on any element, the last element too. The list already says what comes next.
  `onFailed`, `onCancelled` and `onOrphaned` stay on each element as ways out, and firing one ends
  the sequence.
- `onDone` on the action that *carries* the seq means "after the whole sequence". So a sequence is
  a unit with its own completion, and nesting composes.
- An element with no spawn completes at once. So `seq: [{patch: …}, {patch: …}]` is two ordered
  writes, and not an error. `seq` sequences actions, not tasks.
- Gates gate the sequence, not the step. Gates are per firing, and a sequence is one firing, so
  one `ask` approves the whole sequence.
- No chain may contain two `seq` segments. The rule is stated over chains, and not over elements,
  because a sequence inside the `onFailed` of an element nests just as much.

### Chains and ids

Verse identifies a spawned task by `<chain>@<subject>`. The
[chain](../glossary.md#chain-of-an-action) is the
[kpath](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath) of the action inside
its rule: dotted field names, and brackets for positions in a list. Hooks descend by field name,
and elements of a sequence by index:

| the action | chain | id for `demo.pr.1` |
|---|---|---|
| the trigger's own | `review` | `review@demo.pr.1` |
| its done-hook | `review.onDone` | `review.onDone@demo.pr.1` |
| a nested done-hook | `review.onDone.onDone` | `review.onDone.onDone@demo.pr.1` |
| the third sequence step | `review.seq[2]` | `review.seq.2@demo.pr.1` |
| that step's failure branch | `review.seq[2].onFailed` | `review.seq.2.onFailed@demo.pr.1` |

The chain addresses the action, and does not name it privately. So the chain is deterministic.

| Use | What the deterministic chain gives |
|---|---|
| a re-fire | is idempotent, because a spawn dedups by id |
| a lookup back into the charter | is how resume restores the hooks of a task, and how a sequence finds its next step |
| the task entity | publishes it as `chain`, with its root as `trigger`. So "every task of this rule" is a predicate on the payload, and not a glob over ids. |

The id drops the brackets, and joins with `@`. The dotted form is lossless, because the field set
of `Action` is closed. A segment after `seq` is an element, and a segment that matches a hook
name is a field.

A rule is identified by its scope, the address of its charter, and its name. Names are unique only
within one charter. Every charter in a store shares one task runtime and one docket. So every id
that a firing makes begins with the charter that made it:

- the address of a package, and `:`;
- the name of a scope, and `::`;
- nothing, for the charter of baseline.

The task, its gate questions and its provider selection all carry it:

| the charter | task id | gate question id |
|---|---|---|
| baseline's | `review@demo.pr.1` | `verse.trigger.review.ok~…` |
| the package at `verse-dev.drift` | `verse-dev.drift:review@demo.pr.1` | `verse-dev.drift:verse.trigger.review.ok~…` |
| scope `me`'s | `me::review@demo.pr.1` | `me::verse.trigger.review.ok~…` |

For this reason the name of a rule may not contain `:`, as it may not contain `@`.

### Progress facts

A parallel branch is woken by a condition, and a condition matches facts. So a sequence publishes
its position. Without it, the breadth axis could not observe the depth axis at all. The engine
writes the position on the subject, under one reserved `verse` root, as each element completes:

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

`meta.vanishing` is described in
[Lifecycle](../trigger-model.md#lifecycle-how-an-entity-leaves).

A join reads the position at whatever depth it needs:

```tony
subject:
  path: "demo.pr.*"
  value:
    verse:
      charter:
        review: { done: true }
```

`value` matches structurally, so it matches a payload that carries any amount of other content.

| Field | What it says |
|---|---|
| `done` | the engine latches it when `step` reaches `steps` |
| `ended` | the terminal status other than `done` at which an element stopped the sequence. Its own `onFailed`/`onCancelled`/`onOrphaned` runs, and the sequence stops there. |
| `shape` | a hash of the declared sequence. Verse compares it at each advance. |

A join that asks for `done` does not match a run that gave up. A sequence that concluded either
way does not run again at this shape. Editing the sequence changes the shape, and only that frees
it.

When `shape` does not match, the charter changed under the sequence. The sequence fails with a
reason. It does not run whatever now stands at `seq[k+1]`. The error says that the shape changed
and names the rule. It cannot say what changed.

## Why it is this way

The reasons for this design are in [reference-action.md](../design/reference-action.md), in the repository.

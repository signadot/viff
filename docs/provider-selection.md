# Provider selection

Every `spawn` answers two questions:

- *What* is the work? This is the prompt, the frame and the contract.
- *Who* does it?

This page is about the second question.

A [spawn](glossary.md#spawn) can answer it directly: it names a `driver`. Or it can leave the
answer to verse. For that, the spawn declares a **provider selection**. A
[provider selection](glossary.md#provider-selection) makes verse pick a
[doer](glossary.md#doer) when the rule fires. Verse picks from a registry of entities, and it
uses the same [question](glossary.md#question) that a gate poses. The doer that wins gives the
spawn what the doer *is*:

- its [driver](glossary.md#driver);
- its standing instructions;
- the tools it works with;
- the authority it runs under.

Verse lays these over the spawn that selected the doer.

For a walk through an example, see [tutorial §5](tutorial.md#the-same-machinery-answers-who).
For the place of `spawn` inside a rule, see [the trigger model](trigger-model.md).

## The registry

A doer in the registry is a [provider](glossary.md#provider). A provider is an entity at
`provider.<capability>.<id>`. The capability says what it can do. The id says which one it is.

```sh
verse data provider treat fixer -put '{
  driver: llm, risk: low,
  tools: [read_file, list_dir, write_file, run_bash],
  grant: no-delegate,
  frame: "You apply a confirmed one-line fix, then build and run the named test."
}'
verse provider
# treat      fixer          driver=llm risk=low grant=no-delegate tools=[read_file …]
```

The payload of a provider is one namespace. It holds two kinds of key, and verse does not
separate them:

| Kind | Keys | What it is for |
|---|---|---|
| **attributes** | `risk`, `tier`, `cost`, or any other key | what a selection *matches on* |
| **fragment fields** | `driver`, `frame`, `tools`, `grant`, `virtualKey` | what the winner *gives* to the spawn |

`driver` is both kinds. A selection can match on it, and the winner gives it.

Keeping one namespace means a provider entity is free-form, not a schema. So you can invent a
new attribute without a change to the code.

## Selecting

```tony
action:
  spawn:
    prompt: "treat $subject"
    grant: no-delegate
    provider: { capability: treat, match: { risk: low }, select: auto }
```

The fields of a provider selection:

| Field | What it does |
|---|---|
| `capability` | names the subtree of providers |
| `match` | a verse match pattern over the attributes of each provider. "Premium reviewers" or "anything under $5" is a pattern. |
| `select` | says who chooses (see the table below) |
| `within` | the deadline of the question, for `ask` and `vote` |

Verse finds the eligible doers when the rule fires. So you add and retire doers by writing and
deleting entities. The charter does not change.

An inline `eligible: [gpt, claude]` is the simpler form. It is a list of names. Each name is
the driver, and it gives nothing else, because a name has no attributes.

`select` is the same setting that a gate has:

| `select` | Who decides | What appears |
|---|---|---|
| `auto` (default) | a rule: the first eligible doer, by id | nothing |
| `ask` | one party | a question on the [docket](glossary.md#docket), every option at one |
| `vote` | the group, at `need` | a question on the docket, every option at `need` |

`within` has no default. If nobody picks by the deadline, the question settles on `none` and
nothing is spawned.

A gate follows the same rule: verse poses a question for `ask`, and never for `mode: auto`.

Verse refuses a `vote` under an `exclusive` resolver at install. An exclusive resolver answers
alone, and a vote needs a group. `ask` works with an exclusive resolver.

Verse does not store an `auto` pick. It picks again at each firing. The pick is deterministic
for the same registry, because the eligible set is sorted. When the registry changes, the pick
follows it.

## What the winner contributes

Verse binds the winner as a `launch.Fragment`: `driver`, `frame`, `tools`, `grant`,
`virtualKey`, and the id of the doer. The spawn is the base, and the fragment lies over it.

**The spawn block of the charter is what a doer varies from.** It is not a set of fields that
every doer must satisfy. So one rule can route to a fixer and to a memo-writer. The two share
`driver: llm` and differ in everything else.

The overlay only narrows the spawn.

| field | rule |
|---|---|
| `driver` | the doer's replaces |
| `frame` | replaces when set; absent inherits the spawn's |
| `tools` | **intersect** — never replace |
| `grant` | replaces when set and must be within the spawn's; absent inherits the spawn's |
| `virtualKey` | replaces when set; absent inherits the spawn's |
| `prompt`, `vector`, everything else | the charter's, untouched |

Two of these rules are not the obvious ones.

**A doer that sets `tools` and not `grant` keeps the grant of the spawn.** Absent and empty are
different values, on both sides. For this reason `task.NamedGrant` returns an `ok`, not a zero
`Grant`. The zero grant permits nothing. It would refuse every action of a task that never
named a grant.

**`tools` intersects, because an empty list means *all* the tools of the driver.** Suppose the
spawn offers only `[read_file]` and a doer offers `[read_file, run_bash]`. A replace would add
`run_bash`. A tool list narrows when you read it alone. It does not narrow when you lay it over
another list.

The empty list has one more effect. Verse cannot combine two tool lists that share no tool:

1. The intersection is the empty set.
2. The empty set is written as an empty list.
3. An empty list means all the tools.

So the answer to "these two agree on nothing" would be "have everything". Verse refuses such a
pair instead. The pair is a contradiction in any case: a doer that may use no tool that the
spawn permits cannot do the work.

## Which credential it runs under

`virtualKey` is the address of the model credential that the work runs under. This credential
is the [virtual key](glossary.md#virtual-key).

```sh
verse data acme keys review -put '{key: "vk-abc123"}'      # the node IS the credential
verse perms acme keys review --grants '[{read: [group:reviewers]}]'

verse data provider review careful -put '{
  driver: llm, risk: low,
  virtualKey: acme.keys.review,
  frame: "You review the change against the tests it claims to pass."
}'
```

The field holds an address, not a name. Verse has one namespace, and it is the tree.

**The read is the authority.** Before the task starts, versed reads the address **as whoever
named it**. If the owner of a charter may not read the node that the charter names, verse
refuses the spawn and does not run it. Verse has no group, flag or list for keys. Put the
permissions you want on the node. Where credentials live is your convention, not verse's.

For the same reason, a provider entity may set this field. A provider is an ordinary entity.
Anyone who can write entities can write one. The address in it is not authority. Verse checks
the read against the namer, whoever wrote the provider.

**The node holds the key.** So a rotation is one write: `verse data acme keys review -put …`.
Every charter and every provider that names the address follows it, and none of them changes.
Nothing is declared twice, and no config is deployed again.

As a result, the store holds bearer strings. This is a real cost, and verse accepts it. A
permitted reader can copy the key out of the store.

### What protects a key here, and what does not

**Permissions protect a credential in verse, and nothing else does.** The store keeps the
credential as written. The store has no encryption at rest, no envelope and no key management.
So the protection is a convention that the permissions model enforces. Whoever may read the
node may use the key. Whoever may read the node is set by the record of the node.

This has two consequences to know before you put a real key in.

**You must fence the place.** A verse with an open root grants `read` to everyone. So everyone
can read a credential written into it, as soon as it exists, and so can every rule. Set up the
place once. Then everything under it is fenced, including nodes that are already there:

```sh
verse perms acme keys --grants '[]' --child-grants '{}'
```

Both halves are needed. `--child-grants '{}'` alone passes nothing down, but the place itself
stays readable, and a child inherits that.

**A rule that may read a key may also copy it.** Fencing decides *who* reads. It cannot decide
what they do next. A rule runs as its owner. Suppose the owner of a rule may read a credential.
Then the rule can derive the contents of that node to any place where it may write. That
includes the place of an [io](glossary.md#io), where the sink sends it out of verse. Verse has
no check against this, and will not have one.

So the guarantee is this: **verse keeps a key from people the record excludes, and keeps
nothing from people the record admits.** That is enough for a Portkey virtual key. Its own
budget and route limit it, so the worst case is spending inside a limit that somebody set. It
is not enough for a provider key. A provider key has no limit, and does not belong in verse at
all.

If you need more than permissions, do not keep the secret in verse. You have two choices:

- Keep the secret in the environment of the runtime, and let the spawn name no credential.
- Store something that verse can hold safely: an encrypted value that your runner can open, or
  a reference into a secret manager. Rotation then happens where that value lives, not in a
  write.

So verse separates two things that one mechanism would join:

| Question | Who answers it |
|---|---|
| **who may spend** | the permissions on the node |
| **what it costs** | Portkey, which attaches budget, rate and routing to the virtual key |

Because Portkey answers the second question, spending has an owner and verse needs no
accounting system. For the same reason, cheap-model triage and expensive review are one line
in a charter, not two runner deployments.

## The ceiling

A `provider.*` entity is an ordinary entity. **Anyone who can write one can create a doer.**

So verse applies the rule that delegation uses. That rule is `task.Grant.Subset`, the check
that `NewChild` makes at a call_agent boundary. The charter sets a ceiling, and a doer may only
narrow it.

**A spawn that declares `provider:` must declare `grant:`, or verse does not install it.**
`task.Launch` treats a missing grant as unbounded. So without this requirement, "a doer may
only narrow" would check against everything, in the common case. You can write `grant: all`.
Then the spawn says "unbounded" and does not fall into it by default.

```
spawn declares `provider:` but no `grant:`: a selected doer may carry its own grant, and
that is only checkable against a ceiling the charter states. Add one (`grant: read-only`,
`no-delegate`, `none`) — or `grant: all` to say the selection is deliberately unbounded
```

### Refused at eligibility, not at the bind

Verse does not trim a doer that would widen the grant, and it does not fail it at launch. The
doer is **ineligible**. It never enters the set of options, so nobody can pick it and nobody
can vote for it.

An empty set of options comes with a reason, because each cause has its fix in a different
place:

| why nothing is eligible | where the fix is |
|---|---|
| the spawn names no doers and no capability | the charter |
| no `provider.<cap>.*` is registered | register one |
| all registered were excluded by `match` | loosen the match |
| they matched and may not run under this spawn | narrow the doer, **or** widen the spawn |

When no doer is eligible, verse spawns nothing. The last cause names both sides, because the
message cannot judge which side is right:

```
provider selection has no eligible doer … why="1 matching doer(s) may not run under this
spawn: greedy: its grant \"all\" is not within the spawn's \"no-delegate\" — a doer may
narrow authority, never widen it"
```

That line is in the log of `verse up`. Verse also puts the same reason on the rule, under
[`trouble`](glossary.md#trouble) (see
[when a rule does nothing](trigger-model.md#when-a-rule-does-nothing)).

Verse also refuses a doer whose `grant` names something outside the vocabulary. It does not
read the name as nothing. A typo must not let a task run without a grant.

### What a doer may not set

A provider entity that sets `vector`, `resolver` or `environment` is ineligible. Verse refuses
the doer and does not ignore the field. So the entity of a doer and the task it produces cannot
disagree.

`vector` is not in the fragment. It is the one axis that widens without a change to authority.
`vector: auto` means "this firing has already answered its gates". A doer that set it would
take every action boundary off the docket, and its grant would look unchanged. Whether a
person sees an action is for the charter to decide.

An ordering over `auto|ask|park` that would let a doer *tighten* autonomy is an open question.
A spawn may set `resolver` or `environment`. Verse refuses the fragment of a provider that sets
either.

## What you can see

A selection changes what a task may do. So three places show what the selection did.

**The task entity** carries `doer`, `grant` and `tools` for every spawn. This includes `auto`,
which poses no question and so shows in no other place. `doer` is separate from `driver`,
because a driver does not identify a doer.

```sh
verse data verse task instance <id>
# doer: provider.treat.fixer   driver: llm   grant: no-delegate   tools: [read_file, …]
```

**The docket** carries the grant and tools of each option. So the authority that an answer
would give is beside the name that the answer names:

```sh
verse gate
```

```tony
- chain: treat.onDone
  gate: provider
  id: verse.trigger.treat.provider~aa92f8cc
  mode: ASK
  options:
  - grant: no-delegate
    id: fixer
    need: 2
    tools: [read_file, list_dir, write_file, run_bash]
  - grant: read-only
    id: memo
    need: 2
    tools: [read_file, list_dir]
  origin: treat
  subject: demo.finding.7
  where: engine
```

```sh
verse gate verse.trigger.treat.provider~aa92f8cc -answer fixer -as alice@example.com     # 1 of 2 for fixer
```

A doer that adds nothing to the spawn writes neither field. So a [ballot](glossary.md#ballot)
between doers that differ only by model reads as a plain list of names.

**`verse provider`** lists the registry, with every attribute beside the driver. An operator
reads it to answer "what would this doer be allowed to do" before voting.

## Across a restart

A `vote` or `ask` selection without `within` is a question with no deadline. The spawn waits
until somebody picks. So the question must survive a restart of verse.

At start, verse follows the `chain` of the question back into the charter and poses the same
question again, with the ballots already cast
([a question outlives the verse](reference/gates.md#a-question-outlives-the-verse-that-asked-it)).
The chain is the address of the action that the answer holds up: `review`, `review.onDone` or
`review.seq[2]`.

The chain is on the docket entry too. A selection names a subject and a capability, and
neither says "which rule is waiting on this".

The resumed spawn cannot recover two things:

- `$references` resolve against the subject as it stands *now*. This is a new read, not a
  guess.
- The run names no `occurrence`. A restart is the world coming back, not a commit that causes
  something.

Verse settles a selection as `orphaned`, with a `why`, in two cases: its rule or action has
left the charter, or its subject is gone.

## Corners

- **A person cannot be selected.** A brokered doer is not a driver name. A spawn names its
  runner in its own `runner` field (see [runners](runners.md)), and a provider cannot set that
  field. So "this needs a person" is still a gate or a separate rule. It is not a selection
  whose winner is a person. This is the largest missing piece.
- **`tools` is one flat allow-list.** You cannot say "whatever this driver normally offers,
  minus `run_bash`". You cannot say "this work requires `write_file`, so a doer without it is
  not eligible".
- **`Grant.Subset` is sound but incomplete.** It accepts identity, predicate for predicate, or
  a wildcard parent. This costs nothing. The grant vocabulary is four names, built from the
  same atoms for each verb, so identity finds the real subset relations.
  `trigger/providerceiling_test.go` tests all sixteen pairs. No test can reach the gap either,
  because the grant of a doer is a *name*, and nobody can write a structured one. Both facts
  change together.
- **A grant name is resolved twice**: once by the engine, to check the ceiling, and once by
  `versed.launchOf`, to build the launch. This does no harm, and the two could be combined.

## Why it is this way

The reasons for this design are in [provider-selection.md](design/provider-selection.md), in the repository.

# The gates of a rule

This page describes the `gates` field of a [rule](../glossary.md#rule). It says what a gate
asks, who answers it, and what a refusal, a deadline and a restart do to the question. The
model that gates belong to is in [the trigger model](../trigger-model.md).

## `gates` — whether it proceeds

`gates` is a list of [gates](../glossary.md#gate-of-a-rule). Every gate must resolve before the
action fires. An empty list fires with no approval. Each gate is
`{ name: …, mode: auto|ask|park }`.

Gates are per *firing*. A [firing](../glossary.md#firing) is about one entity, so a rule that
selects twelve PRs asks twelve [questions](../glossary.md#question).

The id of a question is a function of (rule, gate, subject). So a rule that is evaluated again
asks once. A firing that finds its question already standing joins it, and does not post a copy.
One answer releases every firing that waits on it. So a rule that waits on a person does not
fill the [docket](../glossary.md#docket).

Several gates model several approvals. A deploy that needs a sign-off from security and from a
release manager is two `ask` gates. A single gate that takes more than one voice says so on the
question it poses:

```tony
  gates:
  - name: approvals
    pose: "Deploy *$[subject.value.title]* ($[subject.value.changes] changes) to staging?"
    need: 2                                    # proceed takes two ballots; one deny refuses
    deadline: {within: "24h", default: deny}   # and the clock decides silence
    answerers: [oscar@example.com, group:payments]   # and whose ballots
  - name: route
    options:
    - id: ship
      need: 1
      says: "merge it now"
    - id: hold
      need: 2
      says: "wait for the release call"
    - id: drop
      need: 1
```

### What a question says

The record of a question holds `about` beside the gate's own fields. `about` is the one line
that the subject says about itself. Verse copies it onto the question when it poses the question.
An entity opts in to this by carrying a `says`
([the question record](tasks.md#the-question-record)). With `pose` and `about`, a question
says what is asked and what it is about. A reader needs no grant on the subject to read either.

`pose` is the question in words. It is the only field of a gate written for a person, and not
for the engine. The other fields say who asked and how the question settles.

Verse expands `pose` at the firing, against the subject that the firing settled on. `$[…]` is
[go-tony's expression](https://github.com/signadot/tony-format). Only `subject.name` and
`subject.value` are in scope. The question carries the result as words.

A sentence that does not expand does not stop the question. Verse drops the words, and a reader
falls back to `reason`.

### Options and their thresholds

Every gate poses the same record:

- the options it can settle on;
- for each option, the number of ballots that settles it;
- a deadline, with the option that the question falls to.

A gate that says none of these poses `proceed` and `deny`, at one ballot each.

| Field | What it says |
|---|---|
| `says` of an option | what choosing the option *means*, for whoever is asked. The `id` is how an answer spells the option, and the name the rule reads. |
| `need` | the short spelling of `options: [{id: proceed, need: N}, {id: deny, need: 1}]`. Do not write `need` and `options` together. |
| `options` | an ordered list. The first option to reach its threshold settles the question. |

A gate that carries any of these, or a `pose`, and no `mode` has the mode `ask`. Verse refuses
such a gate with `mode: auto`, because `auto` asks nobody.

### Who may answer

`answerers` names who the question is held out to. It holds grantees, in the
[grammar](../perms.md#principals-and-grantees) that a grant uses:

| Grantee | Who |
|---|---|
| `user:<email>`, or the bare email | a person |
| `user:*` | a whole kind |
| `*` | everyone |
| `group:<name>` | a group |
| `via:verse.resolvers.<name>` | a resolver |
| `$principal` | whom a task acts for: on a task's own gate only (below) |

A gate that names nobody asks the docket.

**`$principal`** is whom a task acts for: whoever ran it as themselves, the asker of an ask whose
`then` it runs, or the subject's owner for a spawn `as: subject`. Verse puts that person in its
place when it poses the question, so the record names them. It is one answerer with one ballot:
`answerers: [$principal, group:payments], need: 2` takes the person and one ballot from payments.
A rule's own gate is asked by the rule, which acts for nobody, so verse refuses `$principal`
there at install. It also refuses it on a spawn's gates unless the spawn says `as: subject`, and
it refuses the spawn when the subject's owner turns out to be the system.

Verse writes `answerers` as the `read-write` grant of the question when it poses the question.
It replaces the default of the docket. Each answerer has one ballot. `need` over the answerers
says how many must answer:

- `need: 1` is a race;
- `need` equal to the number of answerers is all of them.

The owner of the charter co-owns what its rule asked, and answers it without being named.

Verse refuses at install a gate that nobody could settle:

- a threshold above the number of answerers named;
- an answerer that the grammar does not read;
- a group or a resolver that does not stand.

[perms.md](../perms.md) has the record that `answerers` becomes.

### `answerersFrom` — whom the record names

`answerersFrom: answerers` reads the answerers from a field of the subject at the firing, and
not from the rule. So one standing rule can pose the question of every record to whoever that
record names.

```tony
  gates:
  - name: answer
    pose: "$[subject.value.question]"
    answerersFrom: answerers      # {question: …, answerers: oscar@example.com}
```

The field holds one name or a list, in the same grammar as `answerers`. It is dotted for a field
deeper in the record (`review.by`).

A record without the field says nothing. The gate's own `answerers` stand, or the default of the
docket where the gate names none.

What a record does name is held to what `answerers` is held to at install. An empty list, a name
that the grammar does not read, or fewer people than `need` is a question that nobody could
settle. So verse asks nobody.

### `labels` — what the ask says about itself

`labels: {team: payments, kind: deploy}` rides on the gate's ask, for whoever watches asks to
match on. A label is a name and a value, both words the charter's author and the resolvers agree
on. Verse reads nothing into one and routes nothing by one. A name holding a space is refused at
install. A vector's gates take the same field.

### The tally

The answerers keep the tally, and the engine does not.

- An answer is a ballot on the record.
- The question settles in the same write as the ballot that reaches a threshold.
- An answerer who casts twice replaces their own ballot. The ballot does not count twice.
- A ballot says how its caster was authenticated: `authenticated` is the identity provider that
  verified them, or `asserted` for one who was whoever their request said
  ([who a request is from](perms.md#who-a-request-is-from)). A ballot that a resolver cast for
  somebody names the resolver in `via`, and has no `authenticated`. A ballot a trusted io relayed
  for a person names the io in `via` and the io's provider in `authenticated`
  ([a person relayed by a trusted io](perms.md#a-person-relayed-by-a-trusted-io)).

The engine asks exactly what was written, and acts on the one answer that comes back.

Waiting for a fact is not a gate. A rule that acts once CI is green says so in `when:`.

### `onDenied` — what a no means

A denied firing writes nothing. A firing that did not happen leaves no trace. So the subject
stays in the exact state that asked the question. The next commit that touches the subject
selects it again, and the rule fires again. The gate asks again: a new question, with an id of
its own, to somebody who already answered the last one.

The [latch](../glossary.md#latch) of a well-formed rule cannot help. The latch is in the action,
and on a deny the action never runs. So the gate says what its own refusal means:

```tony
  gates:
  - name: approval
    mode: ask
    onDenied:
      patch: { stage: rejected }
```

`onDenied` is an ordinary action: `patch`, `derive`, `spawn`, its own completion hooks. Verse
applies it about the same subjects that the firing was about. It is a field of the *gate*, not of
the action. It is not a completion hook, and it needs no spawn of its own.

Only a committed refusal runs `onDenied`. It applies to a no however the no arrives: from one
person, from a threshold of people, or from the default of the clock. It does not run when:

- an answerer could not be reached;
- a question was withdrawn because its subject left the selection.

In those cases nobody said anything. A rejection written for them would put a decision in the
store that nobody made.

A gate may omit `onDenied`. "Ask again the next time somebody touches it" is a real policy. So a
gate without `onDenied` gets an advisory at install, and not a refusal. The install prints the
advisory under the line that says what was installed, as `note: rule <name>: …`.

To make a no final without changing the state of the subject itself, use a marker:

```tony
  condition:
    subject:
      path: "demo.pr.*"
      value: !and
      - { stage: open }
      - !not { blocked: true }
  gates:
  - name: approval
    mode: ask
    onDenied:
      patch: { blocked: true }
```

Removing the marker then opens the question again.

### A deadline settles

`deadline: {within: "24h", default: deny}` settles the question on `deny` when the time passes
and no option is at its threshold. The question records `by: deadline`, and the firing sees a
refusal.

Verse stamps the instant on the record once, when it first poses the question. Every later
posing uses that instant. A restart arms the clock against the stamped instant, and not against a
new duration. So a deadline that passed while verse was down fires as verse starts again.

The default is an ordinary option. A ballot for it counts toward its own threshold, like any
other ballot. So a `deny` that needs one ballot settles on one deny, before the clock.

A default left empty settles on nothing (`verdict: none`). The firing reads that as a refusal
that nobody made.

`deadline: {atFrom: by, default: proceed}` takes the instant from a field of the subject
instead. The field holds an RFC 3339 date. That is how a record says "by Friday", and not a
duration from whenever verse happened to pose it. Verse reads it at the firing, and again when it
posts the question.

- A record without the field has no deadline.
- A record whose date does not parse is a question that nobody is asked.
- While the question stands, editing the date moves its deadline. Verse does not ask the question
  again.
- A settled question keeps the instant it settled under.

Whoever may write the record may move the date.

Do not write `within` and `atFrom` together. `at` is the instant that the engine resolved and
stamped, and a rule never writes it. The gates of a spawn stand `within` only, because there is
no record for them to read a date from.

### A question outlives the verse that asked it

The record, its options and every ballot cast into it are entities. So a restart finds the
question exactly where it stood.

The *awaiter* does not survive a restart. The awaiter is the part of the firing that waits for
the answer. A question with no awaiter is the worst kind of entry on the docket: it lists a
decision, somebody makes it, and nothing moves.

Startup gives the awaiter back. The route depends on what asked the question:

| Question of | How startup gives the awaiter back |
|---|---|
| a **gate** | The question blocks the firing itself, and the action never ran. So the firing comes back by being *evaluated again*. A rule that still selects the subject poses the same question again, and joins the record already standing. The ballots already cast still count. The firing is evaluated again, not resumed. |
| a **selection** (a `spawn` with a `provider`) | The question opens *after* the action has written its latch, so nothing fires it again. The question records the `chain` of the action it blocks. Startup follows that chain back into the charter, and enters the selection again. The selection poses the same question again. A resumed task restores its hooks from the same lookup key. |

Verse *settles* a question that nothing can answer any more, and does not leave it standing.
This happens when:

- its rule left the charter;
- its action moved;
- its subject is gone;
- **the subject moved out of what the rule selects.** This case is easy to miss.

For example, a merge proposal set to `state: stale` is not selected by a rule that reads
`!not.has-path state`. That rule never looks at its gate again, however healthy the rule is.

Verse stamps the record `status: orphaned`, with a `why`. The record keeps the trail, and leaves
the list of things a person can act on. This is not a verdict, because nobody decided anything.
So a rule that selects the subject again poses the same question again, over the same ballots.

Posing again is idempotent by construction, so it can run beside a rule that also fires again:

- the question id is derived from the rule, the gate and the subject, and a firing joins the
  question that stands open under it;
- a question settles once, and a firing after an answer asks a new question under
  `<id>-2`, `<id>-3`, and so on;
- the spawn id is deterministic per (chain, subject).

One question, and one task, however many paths reach it.

**A scope is not covered.** Its questions are in the scope's own store,
and nothing lists them at startup. Scoped charters and scoped claims have the same gap.

**When verse stops between settling a question and starting the work it decided, the spawn
is lost.** The record is answered, so nothing poses it again. The same window exists between
`patch` and `spawn` inside one action. An action is not atomic. A crash in the middle of one
leaves the half that landed.

### A gate's answer applies to the world it was asked in

While a question stands, the firing watches the subtree of the subject. At every change it asks
whether the rule would still select the subject. When the rule would not, the question is moot,
and verse **withdraws** it:

- the record says `status: orphaned`, with a `why`;
- there is no verdict and no action;
- `onDenied` does not run, because a moot question is not a refusal.

An answer that arrives after the subject left the selection is the same case. The firing
withdraws, and does not act.

**The withdrawal is prompt.** A question goes moot at the change that made it moot. Nobody has to answer, and nothing has to
restart.

The check has two limits.

- It asks only about the *subject*, not the cause. A rule fired by an `on` edge may still select
  its subject and never see that edge again. So a question that stands does not promise that the
  rule will fire again.
- When it cannot tell, it lets the question stand. It cannot tell when the store is unreadable,
  or when a read of the predicate fails.

### A credential for what a gate let through

A gate stops verse's own work. Work that reaches a system verse does not own, such as a cloud
account or a cluster, is stopped only if that system refuses what no gate approved. A
**credential** is how it can. It is a short-lived token that verse signs for the task of a rule
whose gate passed. The outside system is set up to accept such a token and nothing else.

```tony
- name: deploy
  condition: {subject: {path: "release.*", value: {stage: ready}}}
  gates: [{name: go, mode: ask}]
  action:
    patch: {stage: deploying}
    spawn:
      driver: scripted
      vector: auto
      prompt: "AWS_WEB_IDENTITY_TOKEN_FILE=$VERSE_CREDENTIAL_FILE aws s3 sync build s3://releases"
      credential: {audience: sts.amazonaws.com}
```

**The verse is an issuer.** Its config names the address and the signing key
([`auth.issuer`](up.md#configuration)). The outside system reads the verse's public keys from
that address, and checks each token's signature for itself.

**`sub` is what the outside system matches on.** It is the rule's address, then how the gate was
passed:

| `sub` | the gate was passed |
|---|---|
| `verse.trigger.rule.deploy:ballots` | on the ballots that people cast |
| `verse.trigger.rule.deploy:deadline` | on its deadline's default, with nobody's answer deciding it |
| `verse.trigger.rule.deploy:auto` | as a gate written `mode: auto`, which asks nobody |

- `sub` names no task and no subject. So access is granted once for a rule, and holds for every
  firing of it.
- A system that requires a person's answer grants only the `:ballots` form. If the rule's gate
  is later changed to `auto`, its tokens say `:auto` and that system refuses them.
- A rule with several gates says what its questions came to. It is `:deadline` if any gate that
  asked was settled by its deadline, and `:ballots` if every gate that asked was answered. It is
  `:auto` only where no gate asked.
- A rule in a package has the package in its address:
  `verse.trigger.rule."<package>:<name>":ballots`.

**Unattended work says so.** A rule whose work should reach the outside system with nobody
asked writes a gate that says that:

```tony
- name: deploy-staging
  condition: {subject: {path: "release.*", value: {stage: built}}}
  gates: [{name: unattended, mode: auto}]
  action:
    patch: {stage: staging}
    spawn:
      driver: scripted
      vector: auto
      prompt: "AWS_WEB_IDENTITY_TOKEN_FILE=$VERSE_CREDENTIAL_FILE aws s3 sync build s3://staging"
      credential: {audience: sts.amazonaws.com}
```

Its token says `:auto`. What that guarantees is that this rule fired and verse started this
task. Nobody approved it. So whoever may write the subject can cause it, and the outside system
grants `:auto` only what that is safe for.

**The other claims are for the outside system's audit log.**

| Claim | What it holds |
|---|---|
| `iss` | the verse's address as an issuer |
| `aud` | the `audience` that the spawn declared |
| `iat`, `nbf`, `exp` | when it was signed, and when it stops being good: ten minutes later |
| `jti` | an id of this token alone |
| `task` | the task's id |
| `rule`, `charter` | the rule's address, and its package's where it is in one |
| `subject` | the address the firing was about |
| `principal` | who the task acts as |
| `gates` | for each gate of the rule: `gate` (its name) and `passed` (`ballots`, `deadline` or `auto`). A gate that asked also has `question` (the question's address) and `answerers`. |

Each of `answerers` is `{principal, authenticated}`: who cast a ballot for the option the gate
settled on, and how they were authenticated. An answer that a resolver gave for somebody has
`via` and no `authenticated`; one a trusted io relayed for a person has both. A gate settled by
its deadline may have none.

**The task's record says a credential was minted.** It has
`credential: {audience, sub, jti, expires}`, and never the token. `jti` is the token's own id,
which the outside system's log has too.

**The task gets the token as a file.** The runner writes it outside the task's workspace, for
the task's user alone, and sets `VERSE_CREDENTIAL_FILE` in the task's shell to the file's path.
The token is never a value in the environment. AWS's and GCP's tools take such a token from a
file. The file is removed when the task ends.

**Who gets no credential.** Verse refuses the charter at install where it can tell, and fails the
spawn where it cannot. Either way it says why.

- A spawn that declares none gets none.
- A rule that writes no gate. A credential says which gate let the work through, so there has
  to be one, and `mode: auto` is one.
- A completion hook's spawn, or a step of a `seq`. They start after the answer was given. Make
  the promotion a rule of its own, with its own gate.
- A verse with no `auth.issuer`.
- An `audience` with a `$reference` in it. The audience is declared in the charter, and the
  subject's text cannot choose it.

**A token is minted once, when the task is spawned.** Verse cannot take a token back, so ten
minutes is how long a cancelled task could still act. A task that reaches for its credential
after that is refused by the outside system. So keep the promotion short, and gate it as its
own rule.

### `gating`

```tony
gating:
  gates:
  - name: ship
    mode: ask
```

`gating` holds the gates of a rule. `gates:` at the top level means `gating: {gates: …}`. Verse
refuses a rule that writes both, and does not merge them. A `resolver:` under `gating`, or on a
spawn, is refused as retired: a gate names its `answerers`, and whoever is asked grants the
[resolver](../attached-resolvers.md) that answers for them.

## Why it is this way

The reasons for this design are in [reference-gates.md](../design/reference-gates.md), in the repository.

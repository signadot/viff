# The gates of a rule

This page describes the `gates` field of a [rule](../glossary.md#rule): what a gate asks, who
answers it, and what a refusal, a deadline and a restart do to the question.
[The charter reference](../trigger-model.md) has the model that gates belong to.

The examples on this page are a team's releases of `acme/app`. Each release is a record at
`release.<n>`, with a `title`, a `stage` and the number of `changes` it carries. Rules deploy a
release when people say yes.

## `gates`: whether it proceeds

`gates` is a list of [gates](../glossary.md#gate-of-a-rule). Every gate must pass before the
action fires. A rule with no gates fires without asking anybody. Each gate is
`{name: …, mode: auto|ask}`: `auto` passes at once, and `ask` waits for an answer.

Gates are per *firing*. A [firing](../glossary.md#firing) is about one address, so a rule that
selects twelve releases asks twelve [questions](../glossary.md#question).

The id of a question is made from the rule, the gate and the subject. So a rule that is
evaluated again still asks once. A firing that finds its question already standing joins it,
and does not post a copy. One answer releases every firing that waits on it. So a rule that
waits on a person does not fill the [docket](../glossary.md#docket).

Several gates are several approvals. A deploy that needs a sign-off from security and from a
release manager is two `ask` gates. One gate that needs more than one person says so on the
question it poses. A gate with more to say than yes or no names its own options:

```tony
  gates:
  - name: approvals
    pose: "Deploy *$[subject.value.title]* ($[subject.value.changes] changes) to staging?"
    need: 2                                    # proceed takes two ballots; one deny refuses
    deadline: {within: "24h", default: deny}   # and the clock decides silence
    answerers: [dana@example.com, group:payments]   # and whose ballots
  - name: timing
    pose: "Deploy *$[subject.value.title]* now?"
    options:
    - id: proceed
      need: 1
      says: "deploy it now"
    - id: hold
      need: 2
      says: "wait for the release call"
    - id: deny
      need: 1
      says: "drop this release"
```

### What a question says

A question says two things in words, for a person:

- `pose` is the question, written on the gate.
- `about` is the one line the subject says about itself. A subject whose data carries a `says`
  field has one, and verse copies it onto the question when it poses it
  ([the question record](tasks.md#the-question-record)).

A reader needs no grant on the subject to read either. The other fields of a gate are for the
engine: they say who asked and how the question settles.

Verse expands `pose` at the firing, against the firing's subject, as it expands
[the strings of a spawn](action.md#in-the-strings-of-a-spawn) in the same firing: the subject's
fields by name, and `subject` as `ref`, `name` and `value`. A pose and a prompt that say
`$[subject.value.title]` say the same words. The question carries the result as words. If the
expression does not expand, the question is still asked: verse drops the words, and a reader
reads `reason` instead.

### Options and their thresholds

Every gate poses the same record:

- the options it can settle on;
- for each option, the number of ballots that settles it;
- a deadline, with the option that the question falls to.

A gate that says none of these poses `proceed` and `deny`, at one ballot each.

| Field | What it says |
|---|---|
| `says` of an option | what choosing the option *means*, for whoever is asked. The `id` is how an answer names the option, and the name the rule reads. |
| `need` | the short spelling of `options: [{id: proceed, need: N}, {id: deny, need: 1}]`. Do not write `need` and `options` together. |
| `options` | an ordered list. The first option to reach its threshold settles the question. |

**Only `proceed` lets the firing go on.** Any other option is a no: the action does not run, and
the gate's [`onDenied`](#ondenied-what-a-no-means) runs instead. In the example above, `hold` and `deny`
both stop the deploy. A gate whose options leave out `proceed` can never let its action run.

A gate that carries any of these, or a `pose`, and no `mode` has the mode `ask`. Verse refuses
such a gate with `mode: auto`, because `auto` asks nobody.

### Who may answer

`answerers` names who is asked. It holds grantees, in the
[grammar](../perms.md#principals-and-grantees) that a grant uses:

| Grantee | Who |
|---|---|
| `user:<email>`, or the bare email | a person |
| `user:*` | a whole kind |
| `*` | everyone |
| `group:<name>` | a group |
| `via:verse.resolvers.<name>` | a resolver |
| `$principal` | whom a task acts for: in an `ask` row of a task's autonomy only (below) |

A gate that names nobody asks the docket.

**`$principal`** is whom a task acts for: whoever ran it as themselves, the asker of an ask whose
`then` it runs, or the subject's owner for a spawn `as: subject`. It goes in an `ask` row of
the task's [autonomy](tasks.md#the-autonomy):
`{match: {system: git, verb: push}, then: {ask: {answerers: [$principal]}}}`. Verse puts that
person in its place when it poses the question, so the record names them. It is one answerer
with one ballot: `answerers: [$principal, group:payments], need: 2` takes the person and one
ballot from payments.

A rule's own gate is asked by the rule, and a rule acts for nobody. So verse refuses
`$principal` on a rule's gate at install. It also refuses it in a spawn's autonomy unless the spawn
says `as: subject`. And it refuses the spawn when the subject's owner is the system.

When verse poses the question, it writes `answerers` as the question's `read-write` grant, in
place of the docket's default. Each answerer has one ballot. `need` says how many must answer:

- `need: 1` is a race;
- `need` equal to the number of answerers is all of them.

The owner of the charter co-owns what its rule asked, so the owner may answer without being
named.

Verse refuses at install a gate that nobody could settle:

- a threshold above the number of answerers named;
- an answerer that the grammar does not read;
- a group or a resolver that does not stand.

[Tasks and gates](../perms.md#tasks-and-gates) says who may answer, and
[questions](perms.md#questions) has the record that `answerers` becomes.

### `answerersFrom`: whom the record names

`answerersFrom: answerers` reads the answerers from a field of the subject at the firing, not
from the rule. So one rule can ask, for each record, whoever that record names.

```tony
  gates:
  - name: approval
    pose: "Deploy *$[subject.value.title]*?"
    answerersFrom: approvers      # {title: …, approvers: dana@example.com}
```

So each release names who approves it. The field holds one name or a list, in the same grammar
as `answerers`. For a field deeper in the record, write it dotted (`review.by`).

A record without the field changes nothing: the gate's own `answerers` apply, or the docket's
default where the gate names none.

What a record names is checked as `answerers` is checked at install. An empty list, a name the
grammar does not read, or fewer people than `need` makes a question nobody could settle. Verse
asks nobody in that case.

### `labels`: what the ask says about itself

`labels: {team: payments, kind: deploy}` goes onto the gate's question. Whoever watches
questions can match on them: a resolver, or a charter that posts each question carrying a
`slack: <channel>` label to that Slack channel. A label is a name and a value that the charter's author and the reader
agree on. Verse itself reads nothing into a label and routes nothing by one. A name holding a
space is refused at install. An `ask` row of a task's autonomy takes the same field.

### The tally

The question's record keeps the tally. The engine does not count.

- An answer is a ballot on the record.
- The question settles in the same write as the ballot that reaches a threshold.
- An answerer who casts twice replaces their own ballot. The ballot does not count twice.
- A ballot says how its caster was authenticated: `authenticated` is the identity provider that
  verified them, or `asserted` for one who was whoever their request said
  ([who a request is from](perms.md#who-a-request-is-from)). A ballot that a resolver cast for
  somebody names the resolver in `via`, and has no `authenticated`. A ballot a trusted iod relayed
  for a person names the iod in `via` and the iod's provider in `authenticated`
  ([a person relayed by a trusted iod](perms.md#a-person-relayed-by-a-trusted-iod)).

The engine asks exactly what was written, and acts on the one answer that comes back.

### The answer is written first

The answer that settles a question is written on its record as `followed` before anything else
happens: before an ask runs the row of its `on` that the answer meets, and before the firing or
the task hears it. It holds the answer as the answered record spells it (`verdict`, `by`, `at`, and `via`,
`why`, `say`, `edit` and `authenticated` where the answer has them). The first one
written settles the question, so two answers landing together cannot disagree. The second is
refused with 409, and the refusal names whose answer stands. The same answer, by the same
answerer, again is a retry of the first.

A delivery that fails leaves the question pending and says why on the answer, as
`undelivered`. The next answer delivers it. So does posing the question again. An answer that
nothing is left to hear, the awaiter or the task's decision having gone, is answered 202 with
`the answer stands`. It is delivered when the question is next asked.

Waiting for a fact is not a gate. A rule that acts once CI is green says so in `when:`
([the condition](condition.md)).

### `onDenied`: what a no means

A denied firing writes nothing. So the subject stays in the exact state that asked the
question. The next commit that touches the subject selects it again, and the rule fires again.
The gate asks again: a new question, with an id of its own, to somebody who already answered.

The [latch](../glossary.md#latch) of a well-formed rule cannot help. The latch is in the action,
and on a deny the action never runs. So the gate says what its own refusal means:

```tony
  gates:
  - name: approval
    mode: ask
    onDenied:
      patch: { stage: rejected }
```

`onDenied` is an ordinary action: `patch`, `derive`, `spawn`, and their completion hooks. Verse
applies it to the same subject the firing was about. It is a field of the *gate*, not of the
action. It is not a completion hook, and it needs no spawn of its own.

Only a refusal that settled the question runs `onDenied`. The no may come from one person, from
a threshold of people, or from the clock's default. `onDenied` does not run when:

- an answerer could not be reached;
- a question was withdrawn because its subject left the selection.

In those cases nobody said anything. A rejection written for them would put a decision in the
store that nobody made.

A gate may leave out `onDenied`: "ask again the next time somebody touches it" is a real
policy. So a gate without `onDenied` gets a note at install, not a refusal. The install prints
it under the line that says what was installed, as `note: rule <name>: …`.

To make a no final without changing the state of the subject itself, use a marker:

```tony
  condition:
    subject:
      path: "release.*"
      value: !and
      - { stage: ready }
      - !not { blocked: true }
  gates:
  - name: approval
    mode: ask
    onDenied:
      patch: { blocked: true }
```

Removing the marker then opens the question again.

A gate starts nothing of its own. What a rule does about a yes is its action, and what a no
means for the subject is `onDenied`. The `on` of an [ask](../asking.md) is a person's
alone.

### A deadline settles

`deadline: {within: "24h", default: deny}` settles the question on `deny` when the time passes
and no option is at its threshold. The question records `by: deadline`, and the firing sees a
refusal.

Verse writes the instant on the record once, when it first poses the question. Every later
posing uses that instant. A restart sets the clock to the written instant, not to a new
duration. So a deadline that passed while verse was down fires as verse starts again.

The default is an ordinary option. A ballot for it counts toward its own threshold, like any
other ballot. So a `deny` that needs one ballot settles on one deny, before the clock.

A default left empty settles on nothing (`verdict: none`). The firing reads that as a refusal
nobody made.

`deadline: {atFrom: by, default: proceed}` takes the instant from a field of the subject
instead. The field holds an RFC 3339 date. That is how a record says "by Friday", rather than a
duration from whenever verse posed the question. Verse reads it at the firing, and again when
it posts the question.

- A record without the field has no deadline.
- A record whose date does not parse is a question that nobody is asked.
- While the question stands, editing the date moves its deadline. Verse does not ask the question
  again.
- A settled question keeps the instant it settled under.

Whoever may write the record may move the date.

Do not write `within` and `atFrom` together. `at` is the instant the engine worked out and
wrote; a rule never writes it. An `ask` row of a task's autonomy has `within` only, because there
is no record for it to read a date from.

### A question outlives the verse that asked it

The question's record, its options and every ballot are data in the store. So a restart finds
the question exactly where it stood.

The *awaiter* does not survive a restart. The awaiter is the part of the firing that waits for
the answer. A question with no awaiter would be the worst entry on the docket: somebody answers
it, and nothing moves.

So at startup verse *evaluates each rule again*. The action never ran, so a rule that still
selects the subject poses the same question again. It joins the record that stands, with its
options and every ballot already cast. A rule that no longer selects the subject poses nothing.
While verse runs, a question whose subject leaves what its rule reads is withdrawn, and an
answer to it decides nothing.

Posing again cannot make a second question, so it is safe beside a rule that also fires
again:

- the question id is derived from the rule, the gate and the subject, and a firing joins the
  question that stands open under it;
- a question settles once, and a firing after an answer asks a new question under
  `<id>-2`, `<id>-3`, and so on;
- the spawn id is deterministic per (chain, subject).

One question, and one task, however many paths reach it. A question posed again while an answer
stands on it delivers that answer ([the answer is written first](#the-answer-is-written-first)),
and the firing hears it.

**A scope is not covered.** Its questions are in the scope's own store, and nothing lists them
at startup. Charters and claims in a scope have the same gap.

**If verse stops between settling a question and starting the work, the spawn is lost.** The
record is answered, so nothing poses it again. The same gap exists between `patch` and `spawn`
inside one action. An action is not atomic: a crash in the middle leaves the half that landed.

### A gate's answer applies to the world it was asked in

While a question stands, the firing watches the subject and what is beneath it. At every change
it asks whether the rule would still select the subject. When the rule would not, the question
no longer matters, and verse **withdraws** it:

- the record says `status: orphaned`, with a `why`;
- there is no verdict and no action;
- `onDenied` does not run, because a withdrawn question is not a refusal.

An answer that arrives after the subject left the selection is the same case. The firing
withdraws, and does not act.

**The withdrawal happens at once**, at the change that made the question not matter. Nobody has
to answer, and nothing has to restart.

The check has two limits.

- It asks only about the *subject*, not the cause. A rule fired by an `on` edge may still select
  its subject and never see that edge again. So a question that stands does not mean the rule
  will fire again.
- When it cannot tell, it lets the question stand. It cannot tell when the store is unreadable,
  or when a read of the predicate fails.

### A credential for what a gate let through

A gate stops verse's own work. Work that reaches a system verse does not own, such as a cloud
account or a cluster, is stopped only if that system refuses what no gate approved. A
**credential** makes that possible. It is a short-lived token that verse signs for the task of a
rule whose gate passed. You set up the outside system to accept such a token and nothing else.

```tony
- name: deploy
  condition: {subject: {path: "release.*", value: {stage: ready}}}
  gates: [{name: go, mode: ask}]
  action:
    patch: {stage: deploying}
    spawn:
      script: |
        set -e
        cd app && make build
        AWS_WEB_IDENTITY_TOKEN_FILE=$VERSE_CREDENTIAL_FILE aws s3 sync build s3://releases
      environment:
        vars: {AWS_ROLE_ARN: "arn:aws:iam::123456789012:role/releases"}
        systems:
          network: {mode: open}
          git: {repo: acme/app, fetch: {}}
      credential: {audience: sts.amazonaws.com}
```

The runner checks out `acme/app`, and the script builds it and syncs the build to S3. AWS takes
the token from the file and the role from `AWS_ROLE_ARN`.

**The verse is an issuer.** Its config names the address and the signing key
([`auth.issuer`](up.md#configuration)). The outside system reads the verse's public keys from
that address, and checks each token's signature for itself.

**The audience is a system outside verse.** A credential whose audience is a registered runner's
subject is refused at the spawn. A runner admits only the token verse signs as itself, `sub:
verse`, so a credential would not open its door.

**`sub` is what the outside system matches on.** It is the rule's address, then how the gate was
passed:

| `sub` | the gate was passed |
|---|---|
| `verse.trigger.rule.deploy:ballots` | on the ballots that people cast |
| `verse.trigger.rule.deploy:deadline` | on its deadline's default, with nobody's answer deciding it |
| `verse.trigger.rule.deploy:auto` | as a gate written `mode: auto`, which asks nobody |

- `sub` names no task and no subject. So you grant access once for a rule, and it holds for every
  firing of it.
- A system that requires a person's answer grants only the `:ballots` form. If the rule's gate
  is later changed to `auto`, its tokens say `:auto` and that system refuses them.
- A rule with several gates says what its questions came to. It is `:deadline` if any gate that
  asked was settled by its deadline, and `:ballots` if every gate that asked was answered. It is
  `:auto` only where no gate asked.
- A rule in a package has the package in its address:
  `verse.trigger.rule."<package>:<name>":ballots`.

**Work nobody approves says so.** A rule whose work should reach the outside system without
asking anybody writes a gate that says that:

```tony
- name: deploy-staging
  condition: {subject: {path: "release.*", value: {stage: built}}}
  gates: [{name: unattended, mode: auto}]
  action:
    patch: {stage: staging}
    spawn:
      script: |
        set -e
        cd app && make build
        AWS_WEB_IDENTITY_TOKEN_FILE=$VERSE_CREDENTIAL_FILE aws s3 sync build s3://staging
      environment:
        vars: {AWS_ROLE_ARN: "arn:aws:iam::123456789012:role/staging"}
        systems:
          network: {mode: open}
          git: {repo: acme/app, fetch: {}}
      credential: {audience: sts.amazonaws.com}
```

Its token says `:auto`. It promises only that this rule fired and verse started this task.
Nobody approved it. So whoever may write the subject can cause it. Grant `:auto` in the outside
system only what that is safe for.

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
`via` and no `authenticated`; one a trusted iod relayed for a person has both. A gate settled by
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
  that step a rule of its own, with its own gate.
- A verse with no `auth.issuer`.
- An `audience` with a `$reference` in it. The audience is declared in the charter, and the
  subject's text cannot choose it.

**A token is minted once, when the task is spawned.** Verse cannot take a token back, so a
cancelled task could still act for up to ten minutes. After that, the outside system refuses the
token. So keep the gated work short, and gate it as its own rule.

### `gating`

```tony
gating:
  gates:
  - name: ship
    mode: ask
```

`gating` holds the gates of a rule. `gates:` at the top level means `gating: {gates: …}`. Verse
refuses a rule that writes both; it does not merge them. Verse refuses a `resolver:` under
`gating` or on a spawn. A gate names its `answerers`, and whoever is asked grants the
[resolver](../attached-resolvers.md) that answers for them.

## Why it is this way

The reasons for this design are in [reference-gates.md](../design/reference-gates.md), in the repository.

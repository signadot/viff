# Permissions, walked

This page shows the permissions model by example, in the order you meet it. The model itself is
[perms.md](perms.md): one owner for each node, grants on the records, and one check on every
write and read. The detail is in [reference/perms.md](reference/perms.md).

Most of what you own in a verse, you never wrote by hand: a task record that the runtime
mirrored, a question that a [rule](glossary.md#rule) posed, a branch that an
[iod](glossary.md#iod) reflected. The permissions on those things follow from who asked for the
work, not from who typed. This surprises people, so the page walks it in five steps:

1. your own entities;
2. a task you run, and its gates;
3. a [charter](glossary.md#charter), and what it owns;
4. a gate with no resolver behind it;
5. a gate with a resolver behind it.

## Before the first step: who you are

Every request that the CLI makes carries a [principal](glossary.md#principal): a name. Verse
checks every write and read against what its place permits *for that name*. So first, settle
what the name is.

The CLI reads its config in layers
([reference/up.md](reference/up.md#configuration)). Each layer is patched over the one before:

1. `/etc/verse.tony`;
2. `~/.config/verse.tony`;
3. the environment;
4. the flag on the command.

`user:` in the file is who every command acts as:

```tony
# ~/.config/verse.tony
addr: "localhost:9797"
user: "you@example.com"
```

| Setting | What it overrides |
|---|---|
| `VERSE_USER` in the environment | the file, for one shell |
| `-as` | both, on the commands that take it. Use it to act as somebody else for one command and see what they see. |
| `VERSE_CONFIG=<file>` | both file layers, outright. So a second verse gets a second file, and not a second identity in one file. |

With no `user` anywhere, who the CLI acts as depends on where the verse is:

- **A verse on this machine** (`localhost`, `127.0.0.1`, `::1`): the CLI acts as the
  [local user](glossary.md#local-user), `user:<login>@localhost`. `<login>` is your login name.
  This is who you are in the [tutorial](tutorial/README.md) before you configure anything.
- **Any other verse:** the request names nobody. It reads what `*` may read, and it writes
  nothing. Verse refuses every request that is not a read with
  `400 Verse-Principal is required`, because a write by nobody would be recorded as the
  system's.

The server refuses a write that names nobody, wherever it comes from. The local user is only
the CLI's default. So a program of your own that writes must name somebody itself.

```sh
verse data spaces you notes -put '{todo: [buy milk]}'
verse perms spaces you notes | grep owner             # owner: user:you@example.com
VERSE_USER=ana@example.com verse perms spaces you notes | grep owner   # the same record, read as ana
```

A name carries its kind before the colon. A request presents one of three kinds:

| Kind | Who | How it is set |
|---|---|---|
| `user:you@example.com` | a person | `user:` or `-as`. A bare email is read as this kind. |
| `agent:<name>` | a harness that drives verse from outside | `agent:` in its config, instead of `user:` |
| `runner:<identity>` | a [worker](glossary.md#worker) | it claims work as `-as runner:<identity>`. A runner reached by url acts as the `-identity` on its entity. |

Verse refuses a bare word such as `-as verse-runner`, and the refusal gives the grammar.
[reference/perms.md](reference/perms.md#the-grammar) has the whole grammar. The name is
[self-asserted](perms.md#principals-and-grantees): everything below is about what a name may do,
not about who holds it.

**You are also somebody in Slack and on GitHub.** When you answer a verse question in a Slack
thread or in a pull request comment, the iod that heard you asks verse to count the answer as
yours. Verse does that only through an **account binding**: a record that says this Slack
member, or this GitHub login, is this principal. You make the binding yourself, once for each
system, by proving to the app that you are the verse user:

```sh
verse account -link slack                       # prints: /verse link <nonce>
verse account -link github -account your-login
verse account                                   # the bindings that are you
```

Say the line it prints from your own account: to the Slack app as `/verse link <nonce>`, or in
a comment on a pull request where the github iod is asking a question. The iod reports that
your account said it, and verse binds the account to you. From then on, your replies there
count as you. The ballot records that you answered through Slack or GitHub, and through which
iod. Nobody but you can make the binding: a nonce said by anyone else binds nothing.

**A verse can verify a person instead of taking their word.** Where its config names an
identity provider, `verse account login` logs you in there. The CLI then sends the token it got
in place of a name. Verse checks the token, and acts as the person it proves. `verse whoami`
says how verse knows you: the provider's name, or `asserted` for a name it took as given. `-as`,
and `agent:` in the config, still send a bare name. A verse with `mode: required` refuses a
person who carries no token, with `401` and a line that says to log in. People are verified, and
so are the deployment's own iods, which present their own tokens. An agent or a runner is not
verified: it is whoever it says it is.

[Who a request is from](reference/perms.md#who-a-request-is-from) has the rules.

Two things to know before the first step. Each is a reasonable guess, and wrong:

- **A group is not a principal.** `group:<name>` is a [grantee](glossary.md#grantee). It
  appears on a grant line, in a gate's `answerers`, or behind a resolver. It admits the group's
  members, and each member acts as themselves. Nothing ever acts *as* a group. No request can
  present a group, and a group owns nothing.
- **The `agent` kind exists, and grants treat it as a person.** A harness that drives verse
  from outside can present itself as an agent (`agent:` in the CLI's config). Verse lists it
  under its own kind, and grants it exactly what a person is granted. So read
  `[user:*, agent:*]` as "people".

## Step 1: your own entities, and who wrote them

With `user:` set, everything you type is yours. The seeded entities from
[the tutorial](tutorial/data.md) are a good place to start. So is an address of your own:

```sh
verse data spaces you notes -put '{todo: [buy milk]}'
verse perms spaces you notes
```

```tony
at: spaces.you.notes
grants:
- read: ["*"]
owner: user:you@example.com
```

`spaces.you.notes` is yours because you created it, and it holds out the root's
`child-grants`, `read: ["*"]` ([perms.md](perms.md#ownership)).

Verse refuses the write of another user, and says why:

```
VERSE_USER=ana@example.com verse data spaces you notes -put '{todo: []}'
→ 403 refused: user:ana@example.com holds read at spaces.you.notes and writing it needs
  read-write; the nearest owner on record is user:you@example.com at spaces.you.notes
```

A refusal always says the three things that [perms.md](perms.md#what-you-see) lists. The last
is the record that decided. When verse refuses something you expected to pass, read that
record.

**Take a place.** `verse perms` on a node you may create under gives the node a record of its
own. What you set there governs what is made beneath it
([where a place can be taken](reference/perms.md#creating-and-taking-a-place)).

```sh
verse perms spaces you -child-grants '{}' -grants '[]'  # nothing held out beneath, nothing on it
verse data spaces you private -put '{…}'              # yours alone: no read for anyone
verse perms spaces you -grant '{read: [ana@example.com]}'
verse perms spaces you -revoke '{read: [ana@example.com]}'
```

[reference/perms.md](reference/perms.md#verse-perms) lists the flags.

A withdrawn grant stops future acts. It changes nothing that exists.

The store also knows who wrote every commit. How it keeps the author is in
[perms.md](platform/perms.md#the-author-of-a-commit), in the repository.

**What is not yours to write.** Verse refuses `verse data -put` under `verse.`, whatever you
hold. You write there through the verbs that own it: `verse c -a`, `verse task run`,
`verse question approve`, `verse perms`. Under each verb, the store checks the write against its
place. So nothing that reaches the store can make a task or answer a question for you.

Verse refuses `verse data verse perms` too. A person reads records with `verse perms`, never
from the mirror ([perms.md](perms.md#verses-own-half)).

## Step 2: a task you run, and what it may do for its gates

A [task](glossary.md#task) is an entity that the runtime mirrors. The mirror writes the task
*as whoever asked for it*:

```sh
verse task run -f check.tony                 # a spawn: prompt, subject, environment, autonomy
verse perms verse task instance <id>
```

The task is yours. The record beneath `verse.task.instance` names you as owner for two reasons:

- the seed holds `child-owner` out to everyone there;
- the spawn wrote as you.

Verse writes the task's record as whoever runs it, and that write is the check for `run`
([levels](reference/perms.md#levels)). The task's `run` and `claim` records follow it. Everyone
may read the task, by the default it was created with. Only you and the runtime write it.

**The runner that works the task holds the task and nothing else.** A runner reached by url
acts as `runner:` with the `identity` on its entity, and that is the claim.

When a worker claims the task, verse grants it `read-write` and `control` on the task and on
this run. Every report after that is written as the worker. That grant stops at the task's
record.

A [question](glossary.md#question) that the task raises is an ask, at its own address,
`verse.ask.<id>`. A grant on the task does not reach the ask. So the worker may not answer its
own question ([perms.md](perms.md#grants)).

**What you may say about its questions.** Two things, and they answer different questions:

- The spawn's `autonomy` is *which* of its steps are refused, asked about or allowed, and what
  each question takes to settle. It is a list of rows: a `match` over a step's system, verb and
  params, and a `then`. For an ask, the `then` carries `need`, `within`, `default`,
  `answerers` and `labels` ([a row's question](reference/tasks.md#a-rows-question)).

  ```tony
  # check.tony
  prompt: check the migration
  subject: demo.pr.1
  environment: {systems: {network: {mode: open}, commands: {}}}
  autonomy:
  - name: shell
    match: {system: commands, verb: run}
    then: {ask: {need: 1, within: "4h", default: deny, answerers: [ana@example.com]}}
  - then: allow
  ```

  `answerers` is *who* may answer. When the question is posed, `answerers` becomes the
  question's `read-write` line, in place of the docket's default. Leave it off, and the default
  stands. In the seed, the default is `[user:*, agent:*]`: any person.
- `-as <who>` is who the run is. So it is who owns the run, and who co-owns what the run asks.

What the task may *do* in the world is a different question: push, open a pull request, call a
sub-agent, reach a host. The spawn's `environment` says that, system by system, fixed when it
starts, and the runner refuses a launch whose environment goes past what it offers
([who does the work, and with what](reference/tasks.md#who-does-the-work-and-with-what)). A permissions grant says who may touch an
entity. A task's environment says what the task may do, and its autonomy what it does unasked.

**Answering.** You answer your task's questions as their co-owner, and so does whoever the
line names. Verse refuses the runner ([perms.md](perms.md#tasks-and-gates)).

Try it as somebody else, and read the refusal:

```
VERSE_USER=x@example.com verse question <id> --answer proceed
→ 403 refused: user:x@example.com holds read at verse.ask."<id>" and answering it
  needs read-write; the nearest owner on record is system:docket at verse.ask."<id>"
```

`x` holds `read`, because the docket's default gives every question `read: ["*"]`. `x` does
not hold `read-write`, because `x` is not on the line.

A worker may meet something that the task cannot decide. It asks with `question-ask`, and may
say whom ([a question for the person](reference/tasks.md#a-question-for-the-person)).

## Step 3: a charter, and what it owns

A rule acts as whoever installed its charter. So the installer co-owns every question that its
rules pose, and may answer them without being named on any line.

[perms.md](perms.md#what-acts-on-its-own) states the rule, and
[reference/perms.md](reference/perms.md#installed-things) has the detail. How this step looks on
a deployed verse is in [platform/perms.md](platform/perms.md#step-3-a-charter-and-what-it-owns),
in the repository.

## Step 4: a gate with nobody behind it

A charter gate raises the same record as a task's question: an ask, at `verse.ask.<id>`.
`verse perms verse ask <id>` shows who may see it, who may answer it, and who owns it. If the
gate names no `answerers:`, whoever may write what the question is about may answer, and whoever
may read it sees it. The answerers a gate names must be able to read what it is about, or the
gate is refused when it fires.

- To narrow the questions posed later, name `answerers` on the gate in the charter, and install
  the charter again. The install refuses a gate that nobody could settle.
- To narrow a question that stands, write its record with `verse perms`. You co-own it through
  the rule.
- To bring somebody else in, ask them: an ask of your own, about the same thing.

[reference/perms.md](reference/perms.md#questions) has the rules. How this step looks on a
deployed verse is in [platform/perms.md](platform/perms.md#step-4-a-gate-with-nobody-behind-it),
in the repository.

## Step 5: a resolver that answers for somebody

A [resolver](glossary.md#resolver) is an installed service that answers asks for whoever
grants it ([resolvers](attached-resolvers.md)). It owns nothing, and nothing is moved to it. A
party lets it answer for them with an `answer` grant, and may still answer for themselves.

```sh
verse resolver add - <<'EOF'
name: ben-desk
match: {about: "verse-dev.merge.train.*"}
method: {driver: service}
EOF
verse resolver grant ben-desk -as user:ben@example.com
```

The grant is Ben's, at `verse.for.user:ben@example.com`. From then on, `ben-desk` serves an
ask put to Ben about a merge train. It may read the ask (`answering:` on the ask's `read`). Its
answer, made as the resolver, is Ben's ballot, with `via` naming the resolver. For the asks about
a place that name nobody, the place's owner on record grants at the place. Two resolvers never
answer for one party on one ask: verse refuses a grant that would let them.

**What a service holds.** A resolver's service acts as whoever installed it
([platform/perms.md](platform/perms.md#what-a-resolver-service-holds), in the repository).

**Groups, here and on a gate.** A [group](glossary.md#group) is a named set of grantees, and
`group:<name>` is how anything names it:

```sh
verse group add release you@example.com group:qa 'runner:*'   # stored at verse.principal.group.release
```

A member is any [grantee](reference/perms.md#the-grammar), another group included.
`verse group add` refuses a member it cannot read, such as a group's address or a bare word,
and prints the grammar. Only the group's maker changes it. How verse reads the members when it
checks a grant is in [perms.md](perms.md#grants).

A gate's `answerers: [group:release]` puts its question to the group's members, each answering
as themselves, or through a resolver they grant.

## What to check when something surprises you

- **A refusal names the record that decided.** Read it with `verse perms <path>`. If you may
  not read it, you are outside the node's `read`, and that is the first fact.
- **A question is not under its task.** It is an ask: look at `verse.ask.<id>`, not at the
  task. Verse refuses the runner there on purpose.
- **A rule acts as its installer.** A firing that verse refuses, or a rule that never wakes, is
  usually the installer's own grants, seen through the rule.
- **`verse status` counts.** `perms: {refused, unreadable, hidden, unparsed}` is the check's
  count since the process started. [reference/perms.md](reference/perms.md#verse-perms) says
  what each field counts.

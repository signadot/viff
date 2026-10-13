# Permissions, walked: from a write of your own to a gate somebody else answers

This page is the walk. It shows the permissions model by example, in the order a person meets
it. The model is [perms.md](perms.md): one owner per node, grants on the records, and one check
on every write and read. The detail is [reference/perms.md](reference/perms.md).

Most of what you own in a verse you never wrote by hand. Examples are a task record that the
runtime mirrored, a question that a [rule](glossary.md#rule) posed, and a branch that a source
reflected. The permissions on those things follow from who asked for the work. They do not
follow from who typed. This part surprises people, so the page walks it in five steps:

1. your own entities;
2. a task you run, and its gates;
3. a [charter](glossary.md#charter), and what it owns;
4. a gate with no resolver behind it;
5. a gate with a resolver behind it.

## Before the first step: who you are

Every request that the CLI makes carries a [principal](glossary.md#principal). The check holds
every write and read to what its place permits *for that name*. So the first thing to settle is
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
user: "walter@example.com"
```

| Setting | What it overrides |
|---|---|
| `VERSE_USER` in the environment | the file, for one shell |
| `-as` | both, on the commands that take it. Use it to act as somebody else for one command and see what they see. |
| `VERSE_CONFIG=<file>` | both file layers, outright. So a second verse gets a second file, and not a second identity in one file. |

With no `user` anywhere, who the CLI acts as depends on where the verse is:

- **A verse on this machine** (`localhost`, `127.0.0.1`, `::1`): the CLI acts as the
  [local user](glossary.md#local-user), `user:<login>@localhost`. `<login>` is your login name.
  This is who you are in the [tutorial](tutorial.md) before you configure anything.
- **Any other verse:** the request names nobody. It reads what `*` may read, and it writes
  nothing. Verse refuses every request that is not a read with
  `400 Verse-Principal is required`. A write by nobody would be recorded as the system's.

The server refuses a request that names nobody, wherever the request comes from. The local user
is only the CLI's default. So a program of your own that writes must name somebody itself.

```sh
verse data spaces scott notes -put '{todo: [buy milk]}'
verse perms spaces scott notes | grep owner             # owner: user:walter@example.com
VERSE_USER=ana@example.com verse perms spaces scott notes | grep owner   # the same record, read as ana
```

A name carries its kind before the colon. A request presents one of three kinds:

| Kind | Who | How it is set |
|---|---|---|
| `user:walter@example.com` | a person | `user:` or `-as`. A bare email is read as this kind. |
| `agent:<name>` | a harness that drives verse from outside | `agent:` in its config, instead of `user:` |
| `runner:<identity>` | a [worker](glossary.md#worker) | it claims work as `-as runner:<identity>`. A runner reached by url acts as the `-identity` on its entity. |

Verse refuses a bare word such as `-as coderunner`, and the refusal gives the grammar.
[reference/perms.md](reference/perms.md#the-grammar) has the whole grammar. The name is
[self-asserted](perms.md#principals-and-grantees): everything below is about what a name may do,
not about who holds it.

**You are also somebody in Slack and on GitHub.** When you answer a verse question from a Slack
thread or a pull request comment, the io that heard you asks the verse to count it as you. The
verse does that only through an **account binding**: a record that says this Slack member, or
this GitHub login, is this principal. You make the binding yourself, once per system, by proving
to the app that you are the verse user:

```sh
verse account -link slack                       # prints: /verse link <nonce>
verse account -link github -account scott-cotton
verse account                                   # the bindings that are you
```

Say the line it prints from your own account: to the Slack app as `/verse link <nonce>`, or in
a comment on a pull request the github io is asking a question on. The io reports that your
account said it, the verse binds the account to you, and from then on your replies there count
as you. The ballot records that you answered through Slack or GitHub, and through which io.
Nobody but you can make the binding, and a nonce said by anyone else binds nothing.

**A verse can verify a person instead of taking their word.** Where its config names an identity
provider, `verse account login` logs you in there, and the CLI sends the token it is given in
place of a name. Verse checks the token, and the request is made as the person it proves.
`verse whoami` then says how verse knows you: the provider's name, or `asserted` for a name it
took as given. `-as`, and `agent:` in the config, still send a bare name. A verse that insists
(`mode: required`) refuses a person who carries no token, with `401` and a line that says to log
in. People are verified first, and so are the deployment's own ios, which present their own
tokens. Agents and runners are next. Until that is built, an agent or a runner is whoever it
says it is.

[Who a request is from](reference/perms.md#who-a-request-is-from) has the rules.

Know two things before the first step. Each is a reasonable guess that is wrong:

- **A group is not a principal.** `group:<name>` is a [grantee](glossary.md#grantee). It
  appears on a grant line, in a gate's `answerers`, or behind a resolver. It admits the group's
  members, and each member acts as themselves. Nothing ever acts *as* a group. No request can
  present a group, and a group owns nothing.
- **The `agent` principal exists, and nothing uses it.** A harness that drives verse from
  outside can present itself as an agent (`agent:` in the CLI's config). Verse lists it under
  its own kind, and grants it exactly what a person is granted. The name lets later grants
  narrow against it. Until they do, read `[user:*, agent:*]` as "people".

## Step 1: your own entities, and who wrote them

With `user:` set, everything you type is yours. The seeded entities from
[the tutorial](tutorial.md#2-entities) are a good place to start. So is an address of your own:

```sh
verse data spaces scott notes -put '{todo: [buy milk]}'
verse perms spaces scott notes
```

```tony
at: spaces.scott.notes
grants:
- read: ["*"]
owner: user:walter@example.com
```

`spaces.scott.notes` is scott's because scott created it, and it holds out the root's
`child-grants`, `read: ["*"]` ([perms.md](perms.md#ownership)).

Verse refuses the write of another user, and says why:

```
VERSE_USER=ana@example.com verse data spaces scott notes -put '{todo: []}'
→ 403 refused: user:ana@example.com holds read at spaces.scott.notes and writing it needs
  read-write; the nearest owner on record is user:walter@example.com at spaces.scott.notes
```

The refusal is the check's own words. It always says the three things that
[perms.md](perms.md#what-you-see) lists, and the last of them is which record decided.

When verse refuses something you expected to pass, look at the record that decided.

**Take a place.** `verse perms` on a node you may create under gives the node a record of its
own. What you set there governs what is made beneath it. [reference/perms.md](reference/perms.md#creating-and-taking-a-place)
says where a place can be taken.

```sh
verse perms spaces scott -child-grants '{}' -grants '[]'  # nothing held out beneath, nothing on it
verse data spaces scott private -put '{…}'              # scott's alone: no read for anyone
verse perms spaces scott -grant '{read: [ana@example.com]}'
verse perms spaces scott -revoke '{read: [ana@example.com]}'
```

[reference/perms.md](reference/perms.md#verse-perms) lists the flags.

A withdrawn grant stops future acts. It changes nothing that exists.

The store also knows who wrote every commit. How it keeps the author is in
[perms.md](platform/perms.md#the-author-of-a-commit), in the repository.

**What is not yours to write.** Verse refuses `verse data -put` under `verse.`, whatever you
hold. You write `verse` through the verbs that own it (`verse c -a`, `verse task run`,
`verse question approve`, `verse perms`). Beneath each verb, the store checks the write against its
place. So nothing that reaches the store can stamp a task or answer a question for you.

Verse refuses `verse data verse perms` too. A person reads records with `verse perms`, never
from the mirror ([perms.md](perms.md#verses-own-half)).

## Step 2: a task you run, and what it may do for its gates

A [task](glossary.md#task) is an entity that the runtime mirrors. The mirror writes the task
*as whoever asked for it*:

```sh
verse task run 'check the migration' -subject demo.pr.1 -vector ask
verse perms verse task instance <id>
```

The task is yours. The record beneath `verse.task.instance` names you as owner for two reasons:

- the seed holds `child-owner` out to everyone there;
- the spawn wrote as you.

Verse writes the task's record as whoever runs it, and that write is the check for run
([reference/perms.md](reference/perms.md#levels)). The task's `run` and `claim` records follow it.
Reads of the task follow the default the task was created with: everyone. Writes to the task
are yours and the runtime's.

**The runner that works the task holds the task and nothing else.** A runner reached by url
acts as `runner:` with the `identity` on its entity, and that is the claim.

When a worker claims the task, versed grants it `read-write` and `control` on the task and on
this run. Every report after that is written as the worker. That grant stops at the task's
record.

A [question](glossary.md#question) that the task raises is an ask, at its own address,
`verse.ask.<id>`. A grant on the task does not reach through the task's record to the
ask. So the executor may not answer its own gate ([perms.md](perms.md#grants)).

**What you may say about its gates.** You may say four things, and they answer different
questions:

- `-vector auto|ask|park` is *whether* the run stops to ask at all. `auto` asks nobody.
- `-gates <file>` is *which* actions ask, and what each question takes to settle. The file is a
  list in the shape that a charter's `gates:` uses: `match` over verbs and tools, `need`,
  `options`, `deadline`, and `answerers`.

  ```tony
  - name: shell
    mode: ask
    match: {verbs: [exec]}
    need: 1
    deadline: {within: "4h", default: deny}
    answerers: [ana@example.com]
  ```

  `answerers` is *who* may answer. When the question is posed, `answerers` becomes the
  question's `read-write` line, in place of the docket's default. If you leave it off, the
  default stands. In the seed the default is `[user:*,
  agent:*]`, which is any person.
- `-as <who>` is who the run is. So it is who owns the run and co-owns what the run asks.

What the task may *do* in the world is a different axis. Examples are to write files, to push,
and to delegate. The `grant` on a spawn sets it: `all`, `read-only`, `no-delegate` or `none`
([reference/action.md](reference/action.md#action--what-fires)). That grant is a hard limit.
The runtime refuses anything outside it. It is not a line on a record. A manual run takes the
runtime's default, and a charter's spawn names one. The two kinds of grant do not meet. A
permissions grant says who may touch an entity. A spawn's grant says what a task may attempt.

**Answering.** You answer your task's questions as their co-owner, and so does whoever the line
names. Verse refuses the runner ([perms.md](perms.md#tasks-and-gates)).

Try it as somebody else, and read the refusal:

```
VERSE_USER=x@example.com verse question <id> --answer proceed
→ 403 refused: user:x@example.com holds read at verse.ask."<id>" and answering it
  needs read-write; the nearest owner on record is system:docket at verse.ask."<id>"
```

`x` holds `read` because the docket's default gave every question `read: ["*"]`. `x` does not
hold `read-write` because `x` is not on the line.

A worker may hit something that the task cannot decide. Then the worker raises a gate itself,
and may say to whom. If the spawn has a gate of the same name that names answerers, that gate
stands over what the worker says.

## Step 3: a charter, and what it owns

A rule acts as whoever installed its charter. So the installer co-owns every question that its
rules pose, and answers them without being named on any line.

[perms.md](perms.md#what-acts-on-its-own) states the rule, and
[reference/perms.md](reference/perms.md#installed-things) has the detail. How this step reads on
a deployed verse is in [perms.md](platform/perms.md#step-3-a-charter-and-what-it-owns), in the
repository.

## Step 4: a gate with nobody behind it

A charter gate raises the same record as a task's gate: an ask, at `verse.ask.<id>`.
`verse perms verse ask <id>` shows who may see it, who may answer it, and who owns it.
Where the gate names no `answerers:`, whoever may write what the question is about may answer,
and whoever may read it sees it. The `answerers:` a gate names must be able to read what it is
about, or the gate is refused when it fires.

- To narrow the questions posed later, name `answerers` on the gate in the charter, and install
  the charter again. The install refuses a gate that nobody could settle.
- To narrow a question that stands, write its record with `verse perms`. You co-own it through
  the rule.
- To bring somebody else in, ask them: an ask of your own, about the same thing.

[reference/perms.md](reference/perms.md#questions) has the rules. How this step reads on a
deployed verse is in [perms.md](platform/perms.md#step-4-a-gate-with-nobody-behind-it), in the
repository.

## Step 5: a resolver that answers for somebody

A [resolver](glossary.md#resolver) ([Resolvers](attached-resolvers.md)) is an installed service
that answers asks for whoever grants it. It owns nothing, and nothing is moved to it: a party lets
it answer for them with an `answer` grant, and may still answer for themselves.

```sh
verse resolver add - <<'EOF'
name: ani-desk
match: {about: "verse-dev.merge.train.*"}
method: {driver: service}
EOF
verse resolver grant ani-desk -as user:peggy@example.com
```

The grant is ani's, at `verse.for.user:peggy@example.com`. From then on, an ask put to ani about a
merge train is one `ani-desk` serves: it reads it (`answering:` on the ask's `read`), and its
answer, made as the resolver, is ani's ballot, with `via` naming the resolver. A place's owner
on record grants at the place, for the asks about it that name nobody. Two resolvers never answer for one
party on one ask; a grant that would let them is refused.

**What a service holds.** A resolver's service is whoever installed it, acting as it. How is in
[perms.md](platform/perms.md#what-a-resolver-service-holds), in the repository.

**Groups, here and on a gate.** A [group](glossary.md#group) is a named set of grantees, and
`group:<name>` is how anything names it:

```sh
verse group add release walter@example.com group:qa 'runner:*'   # stored at verse.principal.group.release
```

A member is any [grantee](reference/perms.md#the-grammar), another group included.
`verse group add` refuses a member it cannot read, such as a group's address or a bare word, and
gives the grammar. The group is its maker's to change. How verse reads the members when it
checks a grant is in [perms.md](perms.md#grants).

A gate's `answerers: [group:release]` puts its question to the group's members, each answering
as themselves, or through a resolver they grant.

## What to check when something surprises you

- **A refusal names the record that decided.** Read that record with `verse perms <path>`. If you
  may not read it, you are outside the node's `read`. That is the first fact.
- **A question is not under the task.** It is an ask: look at `verse.ask.<id>`, not at the task.
  Verse refuses the runner there by design.
- **A rule acts as its installer.** A firing that verse refuses, or a rule that never wakes, is
  usually the installer's own grants seen through the rule.
- **`verse status` counts.** `perms: {refused, unreadable, hidden, unparsed}` is the check's
  count since the process started. [reference/perms.md](reference/perms.md#verse-perms) says
  what each field counts.

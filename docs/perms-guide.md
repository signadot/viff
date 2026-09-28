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
[reference/perms.md](reference/perms.md#the-grammar) has the whole grammar. The name is self-asserted:
verse has no login. Everything below is about what a name may do. It is not about proof of who
holds the name.

Know three things before the first step. Each is a reasonable guess that is wrong:

- **A group is not a principal.** `group:<name>` is a [grantee](glossary.md#grantee). It
  appears on a grant line, in a gate's `answerers`, or behind a resolver. It admits the group's
  members, and each member acts as themselves. Nothing ever acts *as* a group. No request can
  present a group, and a group owns nothing.
- **The `agent` principal exists, and nothing uses it.** A harness that drives verse from
  outside can present itself as an agent (`agent:` in the CLI's config). Verse lists it under
  its own kind, and grants it exactly what a person is granted. The name lets later grants
  narrow against it. Until they do, read `[user:*, agent:*]` as "people".
- **Identity is self-asserted.** A request is whoever the config, `VERSE_USER` or `-as` says,
  and the server trusts that. The check holds every write to what its place permits *for that
  name*. Who is behind the name is a different question, and verse does not ask it.

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

Two rules made that record:

- Outside `verse`, the tree is open to *create* until a record says otherwise. So a write where
  nothing stood makes the writer the owner. `spaces.scott.notes` is scott's because scott wrote
  it first.
- A new node holds out what the nearest `child-grants:` above it says. At the root that is
  `read: ["*"]`: everyone may read the node, and only its owner may write it.

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
own. What you set there governs what is made beneath it. In the world's half, you take a place
before anything goes in it. A node that already stands has an owner: the nearest record above
it. Nobody takes the node from that owner by arriving. To hold `child-owner` somewhere is the
right to create there. It says nothing about what stands there.

Under `verse`, a place is taken only where something stands, and only by its owner. The verb
that owns a thing makes it (`charter -a`, `io add`, `task run`), and the thing belongs to its
maker.

```sh
verse perms spaces scott -child-grants '{}' -grants '[]'  # nothing held out beneath, nothing on it
verse data spaces scott private -put '{…}'              # scott's alone: no read for anyone
verse perms spaces scott -grant '{read: [ana@example.com]}'
verse perms spaces scott -revoke '{read: [ana@example.com]}'
```

| Flag | What it does |
|---|---|
| `-child-grants` | sets what a node created *beneath* starts out holding out. Verse copies it onto each new record at creation. |
| `-grants` | replaces what the node itself holds out |
| `-grant` | adds a line |
| `-revoke` | takes a line off |

A withdrawn grant stops future acts. It changes nothing that exists. Ownership changes only by
create and delete. Verse has no transfer. To hand something over, make it under a place that the
other party owns.

The store also knows who wrote every commit. How it keeps the author is in
[perms.md](platform/perms.md#the-author-of-a-commit), in the repository.

**What is not yours to write.** Verse refuses `verse data -put` under `verse.`, whatever you
hold. You write `verse` through the verbs that own it (`verse c -a`, `verse task run`,
`verse gate approve`, `verse perms`). Beneath each verb, the store checks the write against its
place. So nothing that reaches the store can stamp a task or answer a question for you.

Verse refuses `verse data verse perms` too. A person reads records with `verse perms`, never
from the mirror. With no argument, `verse perms` lists the records of every node you may read.
With a path, it reads one.

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

**The runner that works the task holds the task and nothing else.** A worker claims the task in
one of two ways:

- a brokered claim, as `runner:<identity>`;
- a runner reached by url, which acts as `runner:` with the `identity` on its entity.

When a worker claims the task, versed grants it `read-write` and `control` on the task and on
this run. Every report after that is written as the worker. That grant stops at the task's
record.

A [question](glossary.md#question) that the task raises stands at its own address,
`verse.gate.question.<id>`. A grant on the task does not reach through the task's record to the
question. So the executor may not answer its own gate. No special case does this. It is what
"a grant does not reach through an owner record" means.

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
- `-resolver <resolver>` holds the run's questions out to a resolver instead ([step 5](#step-5-a-gate-with-a-resolver-behind-it)).
- `-as <who>` is who the run is. So it is who owns the run and co-owns what the run asks.

What the task may *do* in the world is a different axis. Examples are to write files, to push,
and to delegate. The `grant` on a spawn sets it: `all`, `read-only`, `no-delegate` or `none`
([reference/action.md](reference/action.md#action--what-fires)). That grant is a hard limit.
The runtime refuses anything outside it. It is not a line on a record. A manual run takes the
runtime's default, and a charter's spawn names one. The two kinds of grant do not meet. A
permissions grant says who may touch an entity. A spawn's grant says what a task may attempt.

**Answering.** Three rules decide who answers your task's questions:

- you answer them, because you co-own them, whether or not you are named;
- whoever is named on the line answers too;
- verse refuses the runner.

Try it as somebody else, and read the refusal:

```
VERSE_USER=x@example.com verse gate <id> -answer proceed
→ 403 refused: user:x@example.com holds read at verse.gate.question."<id>" and answering it
  needs read-write; the nearest owner on record is system:docket at verse.gate.question."<id>"
```

`x` holds `read` because the docket's default gave every question `read: ["*"]`. `x` does not
hold `read-write` because `x` is not on the line.

A worker may hit something that the task cannot decide. Then the worker raises a gate itself,
and may say to whom: `verse task do gate <id> <name> -as
runner:<identity> -to ana@example.com`. If the spawn has a gate of the same name that names
answerers, that gate stands over what the worker says.

## Step 3: a charter, and what it owns

A rule acts as whoever installed its charter. Its writes, what it spawns and what it reads are
the installer's. The installer co-owns every question that its rules pose, and answers them
without being named on any line. Installing a charter at an address takes that address for the
installer. A rule may not watch the records under `verse.perms`, and verse refuses such a rule at
install.

[perms.md](perms.md#what-acts-on-its-own) states the rule, and
[reference/perms.md](reference/perms.md#installed-things) has the detail. How this step reads on
a deployed verse is in [perms.md](platform/perms.md#step-3-a-charter-and-what-it-owns), in the
repository.

## Step 4: a gate with nobody behind it

A charter gate poses the same record as a task's gate, at `verse.gate.question.<id>`.
`verse perms verse gate question <id>` shows who may see it, who may answer it, and who owns it.
Where the gate names no `answerers:`, any person or agent may answer.

- To narrow the questions posed later, name `answerers` on the gate in the charter, and install
  the charter again. The install refuses a gate that nobody could settle.
- To narrow a question that stands, write its record with `verse perms`. You co-own it through
  the rule.
- To hand a question on, run `verse gate escalate <id>`. It goes to the next rung of the
  [ladder](glossary.md#ladder).

[reference/perms.md](reference/perms.md#questions) has the rules. How this step reads on a
deployed verse is in [perms.md](platform/perms.md#step-4-a-gate-with-nobody-behind-it), in the
repository.

## Step 5: a gate with a resolver behind it

A [resolver](glossary.md#resolver) ([reference/resolvers.md](reference/resolvers.md)) is the
installed record of who answers questions about a subject. Its answerer is a service that dials
in, a person, or a group.

A resolver is a grant with a condition in front of it:

- The condition is `condition.subject`, a match over the entities whose questions the resolver
  takes. It can also be `byRef`, so that a spawn's `-resolver` or a rule's `gating.resolver`
  can name the resolver.
- The grant is what holding a question out to the resolver does to the record:

```sh
verse resolver add - <<'EOF'
name: ani
condition:
  subject: "verse-dev.merge.train.*"
method: {driver: person, who: peggy@example.com}
deadline: {within: "4h", then: escalate}
EOF
verse gate escalate verse.trigger.train-run.merge-to-main~adaa5e99 -as user:walter@example.com
```

Holding a question out to a resolver **replaces the `read-write` line** with whoever answers as
the resolver:

| Driver | Who is on the line |
|---|---|
| `url` service | `via:` the resolver's address. The service's cover answers as it. |
| `person` | the person |
| `group` | `group:<name>`. Its members answer, each as themselves. |

The default `[user:*, agent:*]` line goes with it. So nobody outside the resolver answers any
more, except the co-owners. They hold the right by ownership, not by the line. `read` stays as
the default gave it.

Verse releases the question when the resolver is removed or the ladder runs out. The release
gives the line back to whoever the gate named in `answerers`. Where the gate named nobody, it
gives the line back to the default.

**The ladder.** Escalation walks the standing resolvers that match the subject, **by name**. It
goes from the resolver the question is on to the next, and past the last one to the open docket.
A resolver's `deadline: {within, then: escalate}` walks the same rung by itself when the
question stands unanswered that long. So a person who is away does not hold a question forever.

A question's own `deadline`, from the gate, is a different clock. It *settles* the question on
its default. Both deadlines are on the record, and the earlier one fires first.

**What a service holds.** A `url` resolver answers as `via:verse.resolvers.<name>`, and that
name is what the question's line holds. How a service answers through a
[cover](glossary.md#cover-of-a-resolver) is in
[perms.md](platform/perms.md#what-a-resolver-service-holds), in the repository.

`exclusive` on a resolver decides whose answer *counts* when several may answer. It is not a
security boundary. The line is the boundary.

**Groups, here and on a gate.** A [group](glossary.md#group) is a named set of grantees, and
`group:<name>` is how anything names it:

```sh
verse group add release walter@example.com group:qa 'runner:*'   # stored at verse.principal.group.release
```

A member is any grantee:

- a person (`user:<email>`, or the bare email);
- an agent;
- a runner;
- a place that acts (`via:<address>`);
- a whole kind (`user:*`);
- everyone (`*`);
- another group (`group:qa`).

`verse group add` refuses a member it cannot read, such as a group's address or a bare word, and
gives the grammar. The group is its maker's to change.

Verse reads the membership of a group when it checks the group. It never copies the membership.
So a group is the one grantee whose meaning changes after the grant is made. This is
deliberate: change who is in the group, and every line that names the group follows.

Verse walks the members exactly. It skips a group that it already met on the walk. So a group
nested in a group nested in a group admits its members at any depth. A cycle admits nobody,
because the walk ends.

A group resolver, `method: {driver: group, group: group:release}`, and a gate's
`answerers: [group:release]` hold a question out to the same people. The difference: the
resolver is a rung on the ladder, and the gate's line is not.

## What to check when something surprises you

- **A refusal names the record that decided.** Read that record with `verse perms <path>`. If you
  may not read it, you are outside the node's `read`. That is the first fact.
- **A question is not under the task.** Look at `verse.gate.question.<id>`, not at the task.
  Verse refuses the runner there by design.
- **A rule acts as its installer.** A firing that verse refuses, or a rule that never wakes, is
  usually the installer's own grants seen through the rule.
- **`verse status` counts.** `perms: {refused, unreadable, hidden, unparsed}` is the check's
  count since the process started. [reference/perms.md](reference/perms.md#verse-perms) says
  what each field counts.
- **`[user:*, agent:*]` means people.** Until the agent principal has grants of its own, a line
  that holds both is the docket's openness, and no more.

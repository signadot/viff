# Permissions

This page is the model of permissions. It defines the words and states the rules. The walk
is [perms-guide.md](perms-guide.md). It shows the model by example, from an entity of your own
to a gate that a resolver answers. The detail is [reference/perms.md](reference/perms.md). It
has every field, rule and table.

The model has three rules:

- Every [node](glossary.md#node) in the store has one **owner**.
- The owner, and the owners of the nodes above it, may do anything to the node.
- Everybody else may do what a **grant** lets them do.

The rest of this page says what those words mean, in the order you need them.

## The words

| Word | Meaning |
|---|---|
| **node** | anything with an [address](glossary.md#address): `demo.pr.1`, a field of it (`demo.pr.1.stage`), a rule (`verse.trigger.rule.review`). An address has steps, so every node has nodes above it, up to the root. |
| **principal** | who is acting: a person, an agent, a runner, or verse itself |
| **record** | what verse keeps about a node: its owner, the grants on it, and the grants that a node created beneath it starts with. Only verse writes records. You read and set your own records with `verse perms`. |
| **permission level** | what somebody may do at a node: `read`, `read-write` or `owner`. Each of the three includes the one before it. `control` stands beside them and admits steering work: run, cancel, pause, resume, say, a change of vector, and suspend. |
| **grant** | a line on a node's record. It gives a permission level to somebody who is not the node's owner. |

On this page, "level" means a permission level. It does not mean the level of a
[stratum](glossary.md#level-of-a-stratum).

`control` includes run ([reference/perms.md](reference/perms.md#levels)).

A refusal may say that somebody *holds* `read` at a node. This means that `read` is the most
the records admit them to at that node, as owner or by grant. The word *holds* says nothing
more.

## Principals and grantees

A [principal](glossary.md#principal) is a name with its kind in front, such as
`user:ana@example.com`. A person is `user:`, an agent is `agent:`, and a runner is `runner:`.
Verse itself is `system:`, and something installed that acts is `via:<address>` (see below).

A [grantee](glossary.md#grantee) is who a grant is to. A grantee is a principal, or a set of
principals: `*` for everyone, `<kind>:*` for every principal of a kind, or `group:<name>` for a
group's members. [The grammar](reference/perms.md#the-grammar) is in the reference.

Every request names who makes it. The CLI sends the `user:` or `agent:` in its config, and
`-as` names somebody else. Verse refuses a request that writes and names nobody.

**A principal is self-asserted.** Verse has no login. Verse trusts a name as much as it trusts
`-as`, and no more. The model does give one thing: every act names a party, and verse checks
the act against what that party may do. When verse verifies names, nothing else has to change.

## Ownership

**The owner of a node is the nearest record at or above it that names one.** Most nodes have no
record of their own. For example, `demo.pr.1.stage` is owned by whoever owns `demo.pr.1`, or
`demo.pr`, or `demo`.

**You own what you create.** A write that brings nodes into being leaves one record. The record
names the writer, and it stands at the topmost node that the write made. Write `demo.pr.1` into
an empty store, and `demo` is yours, with everything beneath it. You may create where you own
the node above. You may also create where a grant says you may (`child-owner`, below).

**Owners above co-own.** Whoever owns `demo` may do anything to `demo.pr.1`, whoever made it.

**Ownership moves only by creating and deleting.** Verse has no transfer. To hand something
over, the other party makes it. Deleting a node forgets its record and every record beneath it.
Whoever creates there next starts with no record.

**A place can be taken before anything goes in it.** Run
`verse perms spaces scott -grants '[]'` where nothing stands yet, and the place is yours. With
no flag, the command only reads the record. A node that already stands belongs to its owner,
and the command does not take it from them.

Places go to whoever takes them first. A grant says who may create beneath a node. It does not
say which name belongs to whom. Suppose `spaces` is open to everyone. Then anybody may take
`spaces.sally` before sally does. The owner of `spaces` corrects this by removing
`spaces.sally` (`b737wbpb`).

## Grants

```tony
# the record of spaces.scott, as `verse perms spaces scott` shows it
owner: user:walter@example.com
grants:
- read: [user:ana@example.com]        # this node and everything beneath it
- below: true                         # beneath it only, not the node itself
  read-write: [group:payments]
- child-owner: [user:*]               # may create beneath it, and own what they create
child-grants: {read: ["*"]}           # what a node created beneath starts out holding out
```

Know three things about grants before the rest:

- **A grant stops at the next owner's record.** A grant on `spaces` reaches everything beneath
  `spaces` that the owner of `spaces` still owns. The grant does not reach into `spaces.scott`
  once scott owns it. What is granted there is for scott to decide.
- **`child-owner` is the right to create, and nothing else.** It lets somebody make new nodes
  beneath a node and own them. It gives them nothing over what already stands there. It never
  lets anybody take the node above a place that somebody else has made.
- **`child-grants` is copied when a node is created.** It is what the new node's own record
  starts with. A later change to `child-grants` changes what is created later. It changes
  nothing that exists.

A **[group](glossary.md#group)** is a named set of grantees with an owner
(`verse group add payments ana@…`). Verse reads the members of a group when it checks a grant.
So membership changes without a change to the grants that name the group.

## What acts on its own

Four things act when nobody typed anything:

- a [rule](glossary.md#rule) fires;
- an [io](glossary.md#io) reflects;
- a [resolver](glossary.md#resolver) answers a gate;
- a [schedule](glossary.md#schedule) ticks.

One rule covers all four: **an installed thing acts as whoever installed it.** Its writes
appear as `via:<its address>`. The check treats that principal as the owner of the installed
thing's record.

A rule can do exactly what its installer can do, and it sees exactly what its installer can
see. An io writes what it reflects as its installer, into places its installer owns. Verse
refuses an install over somebody else's place. Verse also refuses an install that would leave
no owner on record. A thing with no owner acts as nobody, so it would stand there and do
nothing.

The charter a deployment ships is installed as the system and acts as the system. How
that works is in [perms.md](platform/perms.md#installed-things-and-their-records), in the
repository.

## The top of the tree

The root is a node with a record, like any other node. `verse perms --root` shows it. In an
open verse, anybody may make a top-level place and owns what they make, and everyone may read
it. A deployment may allow only admins to make a top-level place. Then everybody else works in
places that an admin opens to them.

How a deployment sets the root is in [perms.md](platform/perms.md#the-root-and-the-config), in
the repository.

## Verse's own half

Everything under `verse` belongs to the system. This includes rules, tasks, runners, ios,
resolvers, schedules, open questions, and the records themselves. You do not write there
directly. You make things with the verbs that own them (`charter -a`, `task run`, `io add`,
`gate approve`), and what a verb makes for you is yours.

In an open verse, anybody may use those verbs. Anybody may read what stands under `verse`,
except the records, which you read with `verse perms`. A rule may watch verse's own half and
react to it. A rule may not write into it.

## Tasks and gates

A [task](glossary.md#task) belongs to its spawner. The runner that works the task is granted
`read-write` and `control` on that task and nothing else.

These principals may answer a gate's [question](glossary.md#question):

- whoever owns what asked it: the task's owner, or the charter's owner;
- whoever the gate names as `answerers`;
- any person or agent, where the gate names nobody.

Whoever answers as a resolver answers a question held out to that resolver. The runner that
works a task cannot answer that task's gate. The runner's grant is on the task, and a question
is not part of the task.

Verse records an answer, a `say`, a cancel or a change of autonomy as *by* whoever made the
request. A remote worker reports back about its own task: its status, its output, the
decisions it saw. That report never decides who answered a question or what they chose.

## What you see

A refused write or read is a 403. The refusal says three things:

- what the requester holds;
- what the act needed;
- which record decided.

```
refused: user:bob@example.com holds read at demo.pr.1 and writing it needs read-write;
the nearest owner on record is user:alice@example.com at demo
```

Verse refuses a `get` of something you may not read. A listing, a watch or a replay leaves it
out. So you see the same set whichever way you ask.

| Command | What it does |
|---|---|
| `verse perms` | lists the records of every node you can read |
| `verse perms <path>` | shows one record. Its owner sets it with `-grant`, `-revoke`, `-grants` and `-child-grants`. |
| `verse status` | counts refusals under `perms` |

<a id="suspending"></a>
## Suspending

Suspending work (`verse flow suspend`) needs `control` over the charter it names, or at the
subject it names. A suspension covers only work that its holder has `control` over at the
moment the work would run. So a wide selector does not reach into places that somebody else
comes to own. [control.md](control.md) describes the commands.
[The reference](reference/perms.md#suspending) has the rule in full.

<a id="scopes"></a>
## Scopes

Baseline's records govern a [scope](glossary.md#scope). Opening a scope is not a way around a
grant. A grant made *in* a scope is copy-on-write, like anything else written there. It applies
in that scope, baseline never sees it, and it goes when the scope goes.
[The reference](reference/perms.md#scopes) says why grants layer this way and tasks do not.

## Where the rest is

| | |
|---|---|
| [perms-guide.md](perms-guide.md) | the model by example, in the order a person meets it |
| [reference/perms.md](reference/perms.md) | the grammar and how a grantee matches; a record's fields; what each level admits; creating and taking a place; what is checked on a write and on a read; installed things; questions; what each verb writes; the root; `verse perms`; suspending; scopes |
| [platform/perms.md](platform/perms.md) | in the repository: the `system:` parts, the root and the config, the seed, and how installed things get their records |

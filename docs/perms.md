# Permissions

This page is the model of permissions. It defines the words and states the rules. Two other
pages go with it:

- [Permissions, walked](perms-guide.md) shows the model by example, from data of your own
  to a gate that a resolver answers.
- [The permissions reference](reference/perms.md) has every field, rule and table.

The model has three rules:

- Every [node](glossary.md#node) in the store has one **owner**.
- The owner, and the owners of the nodes above it, may do anything to the node.
- Everybody else may do what a **grant** lets them do.

The rest of this page says what those words mean, in the order you need them.

## The words

| Word | Meaning |
|---|---|
| **node** | anything with an [address](glossary.md#address): `demo.pr.1`, a field of it (`demo.pr.1.stage`), or a rule (`verse.trigger.rule.review`). An address has steps, so every node has nodes above it, up to the root. |
| **principal** | who is acting: a person, an agent, a runner, or verse itself |
| **record** | what verse keeps about a node: its owner, the grants on it, and the grants that a node created beneath it starts with. Only verse writes records. You read and set your own records with `verse perms`. |
| **permission level** | what somebody may do at a node: `read`, `read-write` or `owner`. Each of the three includes the one before it. `control` is a fourth level beside them. It lets somebody steer work: run, cancel, pause, resume, say, change an autonomy, and suspend. |
| **grant** | a line on a node's record. It gives a permission level to somebody who is not the node's owner. |

On this page, "level" means a permission level. It does not mean the level of a
[stratum](glossary.md#level-of-a-stratum).

A refusal may say that somebody *holds* `read` at a node. This means that `read` is the most
the records give them at that node, as owner or by grant. The word *holds* means nothing more.

## Principals and grantees

A [principal](glossary.md#principal) is a name with its kind in front, such as
`user:ana@example.com`. A person is `user:`, an agent is `agent:`, and a runner is `runner:`.
Verse itself is `system:`. Something installed that acts, such as a rule, is `via:<address>`
([what acts on its own](#what-acts-on-its-own)).

A [grantee](glossary.md#grantee) is who a grant is to. A grantee is a principal, or a set of
principals: `*` for everyone, `<kind>:*` for every principal of a kind, `group:<name>` for a
group's members, `audience:<address>` for whoever holds the same level at that address, or
`answering:<address>` for the resolvers answering for it.
[The grammar](reference/perms.md#the-grammar) is in the reference.

Every request names who makes it. The CLI sends the `user:` or `agent:` in its config. On the
commands that take it, the flag `-as` names somebody else. Verse refuses a request that writes
and names nobody.

**A principal is what the request says, unless the verse verifies it.** By default, verse
trusts a name as much as it trusts `-as`, and no more. A deployment can name the identity
providers its people sign in with. A request that carries a token from one of them is then made
as the person the token proves. Verse refuses a token that proves nobody. The rest of the model
is the same either way: every act names a party, and verse checks the act against what that
party may do.
[Who a request is from](reference/perms.md#who-a-request-is-from) is in the reference.

## Ownership

**The owner of a node is the nearest record at or above it that names one.** Most nodes have no
record of their own. For example, `demo.pr.1.stage` is owned by whoever owns `demo.pr.1`, or
`demo.pr`, or `demo`.

**You own what you create.** A write that brings nodes into being leaves one record. The record
names the writer, and it stands at the topmost node that the write made. Write `demo.pr.1` into
an empty store, and `demo` is yours, with everything beneath it. You may create where you own
the node above. You may also create where a grant says you may (`child-owner`, below).

**A node's first grants are `child-grants`, or its creator's.** A node you create starts with
the grants that the nearest `child-grants` above it names. You can also give it grants of your
own as you create it. `verse data notes ana todo -put '{…}' -grants '[]'` makes a note that
nobody else reads, from its first commit. The node and its record land together, so the node is
never readable more widely than you said
([creating with grants](reference/entities.md#creating-with-grants)).

**Owners above co-own.** Whoever owns `demo` may do anything to `demo.pr.1`, whoever made it.

**Ownership moves only by creating and deleting.** Verse has no transfer. To give something to
somebody, they make it. Deleting a node forgets its record and every record beneath it.
Whoever creates there next starts with no record.

**A place can be taken before anything goes in it.** Run
`verse perms spaces you -grants '[]'` where nothing stands yet, and the place is yours. Without
`-grants`, the command only reads the record. A node that already stands belongs to its owner,
and the command does not take it from them. A place goes to whoever takes it first
([creating and taking a place](reference/perms.md#creating-and-taking-a-place)).

## Grants

```tony
# the record of spaces.you, as `verse perms spaces you` shows it
owner: user:you@example.com
grants:
- read: [user:ana@example.com]        # this node and everything beneath it
- below: true                         # beneath it only, not the node itself
  read-write: [group:payments]
- child-owner: [user:*]               # may create beneath it, and own what they create
child-grants: {read: ["*"]}           # the grants a node created beneath starts with
```

Know three things about grants before the rest:

- **A grant stops at the next owner's record.** A grant on `spaces` reaches everything beneath
  `spaces` that the owner of `spaces` still owns. It does not reach into `spaces.you` once you
  own it. You decide what is granted there.
- **`child-owner` is the right to create, and nothing else.** It lets somebody make new nodes
  beneath a node and own them. It gives them nothing over what already stands there. It never
  lets anybody take the node above a place that somebody else has made.
- **`child-grants` is copied when a node is created.** The new node's record starts with it,
  unless the creator gives grants of their own. A later change to `child-grants` changes what is
  created later. It changes nothing that exists.

A **[group](glossary.md#group)** is a named set of grantees with an owner
(`verse group add payments ana@…`). Verse reads the members of a group when it checks a grant.
So membership changes without a change to the grants that name the group.

## What acts on its own

Four things act when nobody typed anything:

- a [rule](glossary.md#rule) fires;
- an [iod](glossary.md#iod) writes what a system holds, or carries a rule's write back out to it;
- a [resolver](glossary.md#resolver) answers a gate;
- a [schedule](glossary.md#schedule) ticks.

One rule covers all four: **an installed thing acts as whoever installed it.** Its writes
appear as `via:<its address>`. The check treats that principal as the owner of the installed
thing's record.

A rule can do exactly what its installer can do, and it sees exactly what its installer can
see. An iod writes into the store as its installer, into places its installer owns.

Verse refuses an install over somebody else's place. Verse also refuses an install that would
leave no owner on record. A thing with no owner acts as nobody, so it would stand there and do
nothing.

The charter a deployment ships is installed as the system, and acts as the system
([installed things and their records](platform/perms.md#installed-things-and-their-records), in
the repository).

**A spawn with `as: subject`** acts for the owner of the record that fired it. Its task acts as
`pair:<owner> via:<the rule's address>`, and may do what the owner may. The pair names the
rule so that the record says what the task acted through. It adds no authority and takes none
away. A rule may say `as: subject` only over a place its installer owns. So a rule cannot act as somebody
whose records it only selects
([`as: subject`](reference/action.md#as-subject--a-task-for-whoever-wrote-the-record)). A pair
is a principal and never a grantee: nothing is granted to one.

## The top of the tree

The root is a node with a record, like any other node. `verse perms --root` shows it. In an
open verse, anybody may make a top-level place, and owns what they make. Everyone may read it. A
deployment may allow only admins to make a top-level place. Then everybody else works in places
that an admin opens to them
([the root and the config](platform/perms.md#the-root-and-the-config), in the repository).

## Verse's own half

Everything under `verse` belongs to the system: rules, tasks, runners, iods, resolvers,
schedules, open questions, and the records themselves. You do not write there directly. You
make things with the verbs that own them (`charter -a`, `task run`, `iod add`,
`question approve`). What a verb makes for you is yours.

In an open verse, anybody may use those verbs. Anybody may read what stands under `verse`,
except the records, which you read with `verse perms`. A rule may watch verse's own half and
react to it. A rule may not write into it.

## Tasks and gates

A [task](glossary.md#task) belongs to its spawner. The runner that works the task is granted
`read-write` and `control` on that task and nothing else.

A task may act on verse itself, as its owner would, through the
[verse system](systems/verse.md). The runner carries each such request with its own token, and
names the task. Verse admits it as the task's owner, and only while the task runs
([a task carried by its runner](reference/perms.md#a-task-carried-by-its-runner)). The runner
gains nothing by it: the request may do what the owner may, and no more.

These principals may answer a gate's [question](glossary.md#question):

- whoever owns what asked it: the task's owner, or the charter's owner;
- whoever the gate names as `answerers`, each of whom must be able to read what the question is
  about;
- where the gate names nobody, whoever may write what the question is about, or any person or
  agent where it is about nothing.

Nobody answers a question about something they cannot read. A question about something is read
by whoever may read that thing.

A resolver answers a question only when the question is held out to that resolver. The runner
that works a task cannot answer that task's questions. The runner's grant is on the task, and a
question is not part of the task.

Verse records an answer, a `say`, a cancel or a change of autonomy as made *by* whoever made the
request. A remote worker reports on its own task: its status, its output, the decisions it saw.
That report never decides who answered a question or what they chose.

## What you see

A refused write or read is a 403. The refusal says three things:

- what the requester holds;
- what the act needed;
- which record decided.

```
refused: user:bob@example.com holds read at demo.pr.1 and writing it needs read-write;
the nearest owner on record is user:alice@example.com at demo
```

Verse refuses a read of one thing you may not read. A listing, a watch or a replay leaves it
out. So you see the same set whichever way you ask.

You read and set records with [`verse perms`](reference/perms.md#verse-perms).

<a id="suspending"></a>
## Suspending

Suspending work (`verse flow suspend`) needs `control` over the charter it names, or at the
subject it names. A suspension covers only the work that its holder has `control` over when the
work would run. So a wide selector does not reach into places that somebody else comes to own.
[Control](control.md) has the commands, and [the reference](reference/perms.md#suspending) has
the rule in full.

<a id="scopes"></a>
## Scopes

Baseline's records also apply in a [scope](glossary.md#scope). Opening a scope is not a way
around a grant. A grant made *in* a scope is copy-on-write, like anything else written there. It
applies in that scope, baseline never sees it, and it goes when the scope goes.
[The reference](reference/perms.md#scopes) says why grants layer this way and tasks do not.

## Where the rest is

[platform/perms.md](platform/perms.md), in the repository, has the `system:` parts, the root
and the config, the seed, and how installed things get their records.

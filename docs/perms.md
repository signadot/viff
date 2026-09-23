# Permissions

Every node in the verse has one **owner**. The owner, and the owners of the nodes above it, may
do anything to it. Everybody else may do what a **grant** lets them. That is the model, and the
rest of this page says what those words mean, in the order you need them.

[perms-guide.md](perms-guide.md) walks it by example, from an entity of your own to a gate a
resolver answers. [reference/perms.md](reference/perms.md) has every field, rule and table.

## The words

| | |
|---|---|
| **node** | anything with an address: `demo.pr.1`, a field of it (`demo.pr.1.stage`), a rule (`verse.trigger.rule.review`). Addresses are paths, so every node has nodes above it, up to the root |
| **principal** | who is acting: a person, an agent, a runner, or verse itself |
| **record** | what verse keeps about a node: its owner, the grants on it, and the grants a node made beneath it starts with. Only verse writes records; you read and set your own with `verse perms` |
| **level** | what somebody may do at a node: `read`, `read-write`, or `owner`, each including the one before; and `control`, beside them, for steering work — run, cancel, pause, say, suspend |
| **grant** | a line on a node's record giving a level to somebody who is not its owner |

When a refusal says somebody *holds* `read` at a node, it means that is the most the records
admit them to there, as owner or by grant. It is the refusal's word and nothing more.

## Principals and grantees

A principal is a name with its kind in front. A grantee — who a grant is to — is a principal or
a set of them.

```
principal ::= user:<email>          # a person
            | agent:<name>          # an agent driving verse through the api
            | runner:<identity>     # a runner doing work
            | system:<part>         # verse itself
            | via:<address>         # something installed, acting: see below
grantee   ::= principal
            | *                     # everyone
            | <kind>:*              # every principal of a kind
            | group:<name>          # a group's members
            | <email>               # shorthand for user:<email>
```

Every request names who makes it: the CLI sends the `user:` or `agent:` in its config, and `-as`
names somebody else. A request that writes and names nobody is refused.

**A principal is self-asserted.** There is no login, and verse trusts a name no harder than it
trusts `-as`. What the model gives today is that every act names a party and is checked against
what that party may do, so that when names are verified nothing else has to change.

## Ownership

**The owner of a node is the nearest record at or above it that names one.** Most nodes have no
record of their own: `demo.pr.1.stage` is owned by whoever owns `demo.pr.1`, or `demo.pr`, or
`demo`.

**You own what you create.** A write that brings nodes into being leaves one record, naming the
writer, at the topmost node it made: write `demo.pr.1` into an empty verse and `demo` is yours,
with everything beneath it. You may create where you own the place above, or where a grant says
you may (`child-owner`, below).

**Owners above co-own.** Whoever owns `demo` may do anything to `demo.pr.1`, whoever made it.

**Ownership moves only by creating and deleting.** There is no transfer. To hand something
over, the other party makes it. Deleting a node forgets its record and every record beneath it,
so whoever creates there next starts clean.

**A place can be taken ahead of what goes in it**: `verse perms spaces scott`, where nothing
stands yet, makes it yours. A node that already stands is its owner's and is not taken from them.
First come, first served: a grant says who may create beneath a node and not which name is
whose, so where `spaces` is held out to everyone, anybody may take `spaces.sally` before sally
does, and it is the owner of `spaces` who puts that right, by removing it (`b737wbpb`).

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

Three things about grants are worth knowing before the rest:

- **A grant stops at the next owner's record.** A grant on `spaces` reaches everything beneath
  it that is still `spaces`' owner's. It does not reach into `spaces.scott` once scott owns that:
  what is granted there is scott's to say.
- **`child-owner` is the right to create, and nothing else.** It lets somebody make new nodes
  beneath and own them. It gives them nothing over what already stands there, and it is never a
  way to take the node above a place somebody else has made.
- **`child-grants` is copied when a node is created.** It is what the new node's own record
  starts out holding out. Changing it later changes what is created later, and nothing that
  exists.

A **group** is a named set of grantees with an owner (`verse group add payments ana@…`). Who is in
it is read when it is checked, so membership changes without touching the grants that name it.

## What acts on its own

A rule fires, an io reflects, a resolver answers a gate, a schedule ticks — and nobody typed
any of it. One rule covers all four: **an installed thing acts as whoever installed it.** Its
writes appear as `via:<its address>`, and the check treats that as the owner of its record.

So a rule can do exactly what its installer can do, and sees exactly what they can see. An io
writes what it reflects as its installer, into places its installer owns. Installing over
somebody else's place is refused. And an install that would leave no owner on record is refused
outright, since a thing with no owner acts as nobody and would sit there doing nothing.

The charter a deployment ships (`systemCharter`, and verse's built-in clock and calendar) is
installed as the system and acts as the system.

## The top of the tree

The root is a node with a record like any other, and that record is where a deployment says how
open it is:

```tony
# the root's record in an open verse
owner:        user:admin@verse
grants:       [{child-owner: ["*"]}, {read: ["*"]}]
child-grants: {read: ["*"]}
```

Open, anybody may make a top-level place and owns what they make, and what anybody creates is
readable by everyone until its owner says otherwise. With `perms: {admins: […]}` in the config
the first line reads `child-owner: [group:admin]` instead: only an admin makes a top-level
place, and everybody else works in places an admin holds out to them. `user:admin@verse` owns
the root in both, and so co-owns everything.

## Verse's own half

Everything under `verse` — rules, tasks, runners, ios, resolvers, schedules, open questions,
these records themselves — is the system's. You do not write there directly: you make things
with the verbs that own them (`charter -a`, `task run`, `io add`, `gate approve`), and what
a verb makes for you is yours. Anybody may use those verbs in an open verse, and anybody may
read what stands there, except the records, which you read with `verse perms`. A rule may watch
verse's half and react to it; it may not write into it.

## Tasks and gates

A task is its spawner's. The runner that works it is granted `read-write` and `control` on that
task and nothing else.

A gate's question may be answered by whoever owns what asked it — the task's owner, or the
charter's — and by whoever the gate names as `answerers`; where it names nobody, by any person
or agent. A question held out to a resolver is answered by whoever answers as that resolver. The
runner working a task cannot answer that task's gate: its grant is on the task, and a question
is not part of the task.

An answer, a `say`, a cancel or a change of autonomy is recorded as *by* whoever made the
request. What a remote worker reports back about its own task — its status, its output, the
decisions it saw — never decides who answered a question or what they chose.

## What you see

A refused write or read is a 403 that says three things: what the requester holds, what the act
needed, and which record decided.

```
refused: user:bob@example.com holds read at demo.pr.1 and writing it needs read-write;
the nearest owner on record is user:alice@example.com at demo
```

A `get` of something you may not read is refused; a listing, a watch or a replay simply leaves
it out, so you see the same set whichever way you ask. `verse perms` lists the records of every
node you can read, `verse perms <path>` shows one, and its owner sets it (`-grant`, `-revoke`,
`-grants`, `-child-grants`). `verse status` counts refusals under `perms`.

<a id="suspending"></a>
## Suspending

Suspending work (`verse flow suspend`) needs `control` over the charter it names or at the
subject it names. A suspension covers only work its holder has `control` over at the moment the
work would run, so a wide selector does not reach into places somebody else comes to own.
[control.md](control.md) is the surface; [the reference](reference/perms.md#suspending) has the
rule in full.

<a id="scopes"></a>
## Scopes

A scope is governed by baseline's records, and opening one is not a way around a grant. A grant
made *in* a scope is copy-on-write like anything else written there: it applies in that scope,
baseline never sees it, and it goes when the scope does.
[The reference](reference/perms.md#scopes) says why grants layer this way and tasks do not.

## Where the rest is

| | |
|---|---|
| [perms-guide.md](perms-guide.md) | the model by example, in the order a person meets it |
| [reference/perms.md](reference/perms.md) | the grammar and how a grantee matches; a record's fields; what each level admits; creating and taking a place; what is checked on a write and on a read; installed things; questions; what each verb writes; the root and the config; the seed; `verse perms`; suspending; scopes |
| [reference/up.md](reference/up.md#configuration) | `perms.admins`, `perms.child-grants`, `--reseed-perms` |

# Permissions reference

This page is the detail of permissions. The model is [perms.md](../perms.md). The walk, by
example, is [perms-guide.md](../perms-guide.md).

This page has these parts:

- the grammar;
- a record's fields;
- what verse checks, and when;
- what each verb writes.

The reasons for each rule are in the source, beside the code that enforces the rule
(`entity/attribution.go`, `versed/perms.go`, `versed/place.go`).

## The grammar

```
principal ::= user:<email>          # a person
            | agent:<name>          # an agent driving verse through the api
            | runner:<identity>     # a runner
            | system:<part>         # verse itself
            | via:<address>         # something installed, acting as whoever installed it
grantee   ::= principal
            | *                     # everyone, a read that names nobody included
            | <kind>:*              # every principal of the kind
            | group:<name>          # the group's members, each as themselves
            | <email>               # shorthand for user:<email>, at the keyboard only
```

Every surface that takes a name reads this grammar, and refuses what it does not read. The
surfaces are:

- `-as`;
- the config's `user:` and `agent:`;
- a grant line and a `child-grants`;
- a group's members;
- a gate's `answerers`;
- a resolver's `who` and `group`.

The rules of the grammar:

- A bare email is read as `user:<email>` and stored with its kind. Any other bare word names
  nothing. A bare `verse.…` address names nothing too.
- **A request presents `user:`, `agent:` or `runner:`.** `via:` and `system:` are names that
  verse makes. Verse refuses a request that carries one of them with 400. It refuses a request
  that carries a name the grammar does not read in the same way.
- **A request that is not a read presents one.** Verse refuses it with 400 when it names
  nobody. The routes that name their own party are in
  [perms.md](../platform/perms.md#routes-that-name-their-own-party), in the repository.
- Verse lists a [principal](../glossary.md#principal) the first time it presents itself, under
  `verse.principal.<kind>.<name>`.
- `system:<part>` names the part of verse that wrote, such as `system:docket` for questions.
  The parts are one owner. They are in [perms.md](../platform/perms.md#the-system-parts), in the
  repository.

**How a grantee matches.**

| grantee | admits |
|---|---|
| `<kind>:<id>` | the principal named exactly that, and anything installed that acts as them |
| `<kind>:*` | any principal of the kind, and anything installed that acts as one |
| `via:<address>` | the installed thing at that address and nothing else. It does not admit the thing's owner. A trailing `*` covers what acts beneath (`via:verse.io.*`). Only a rule, an io or a resolver is named this way. A runner acts as `runner:<identity>`. |
| `group:<name>` | the group's members, each matched as above. A member may be another group, and verse follows it. Verse does not enter a group again that it already met on the walk. So groups that name each other end the walk instead of looping. They admit exactly the members found along the walk. |
| `*` | everyone |

A stored name that the grammar does not read admits nobody. Nothing rewrites it. `verse status`
counts each check that meets one under `perms.unparsed`, and versed logs it once per record.

## Records

Records live at `verse.perms.node.<address>`. The root's record is at `verse.perms.root`. Only
verse writes records, and only the system reads them. Anybody else reads and sets them with
`verse perms`.

| field | meaning |
|---|---|
| `owner` | the principal who owns the node. It is absent on a record that only holds grants out (a seed row). |
| `grants` | a list of lines. `[]` holds out nothing. |
| `child-grants` | permission levels mapped to grantees. It is what a record made beneath this node starts out holding out. Verse copies it when that record is written, unless the creation gives grants of its own. It is not a fallback: a change to it touches nothing that exists. The nearest `child-grants` up the tree applies, and the root's is the last. |
| `under` | an address. Whoever co-owns that node co-owns this one. Verse writes it on a question (what asked it) and on a cover (its resolver). |

A grant line:

| field | meaning |
|---|---|
| `read`, `read-write`, `control` | grantees of that level |
| `child-owner` | grantees who may create beneath, and own what they create. It is always about what is beneath, so it takes no `below`. There is no grant of `owner` at a node. |
| `below: true` | the line covers what is beneath the node, and not the node |
| `at: [segs…]` | the line is about another address, with at most a trailing `*`. When `at` is absent, the line is about the node the record is on. Nearly every line is. |

**A grant does not reach through an owner record.** Verse walks up from a node. The grants on
each record apply until the walk passes a record that names an owner. The grants on that record
apply, and the grants above it do not.

**A group** is `verse.principal.group.<name>`. It has `members`, a list of grantees, and whoever
made it owns it. Verse reads the membership when it checks the group.

## Levels

A permission level is what a principal may do at a node.

| level | admits |
|---|---|
| `read` | reading the node and what is beneath it |
| `read-write` | writing into the node, with fields removed as well as set |
| `owner` | everything: deleting the node (which takes what is beneath it), replacing it whole, and setting its record. The owner on record and the owners above hold it. `child-owner` confers it only on a creation. |
| `control` | steering work: run, cancel, pause, resume, say, a change of vector, suspend. An owner by record holds it, and so does whoever it is granted to. |

A spawn carries the identity of whoever runs it, and verse writes the task's record as that
identity. That write is the check for run.

A refusal that says "needs owner" about a creation means that no `child-owner` line reaches
there.

## Creating, and taking a place

- **Who owns a created node.** A writer may hold `owner` where they write, by owning above or by
  a `child-owner` line. That writer owns what they create. Verse writes a record if the writer
  differs from the owner above, or gives the node grants (`verse data … -put … -grants`). A
  writer who holds `read-write` creates the node too, but the node belongs to the owner of the
  place, verse writes no record, and it refuses grants on the write.
- **Where the record goes.** At the topmost node that the write brought into being. The walk up
  stops at anything already there, as a node or as a place somebody took. A node created with
  grants has its own record too, written in the same commit as the node.
- **`child-owner` applies to a creation only.** Verse commits a write that only that line admits
  on one condition: nothing stands where the write lands. The condition holds at the node and at
  each node that its value reaches. Of two people who make one node at once, one owns it and
  verse refuses the other. Where something does stand, verse admits the write on whatever else
  the writer holds there, or refuses it and names the owner.
- **Taking a place** (`verse perms <path>` where no record stands) needs `owner` there. By a
  `child-owner` line, this means that nothing may stand at the node yet. Under `verse`, a place
  is taken only where something stands, and then only by its owner. So nobody can reserve a
  name that the system has not used yet.
- **First come, first served.** A `child-owner` line says who may create beneath a node. It
  does not say which name each of them may create. Where `spaces` holds it out to `user:*`,
  anybody may take `spaces.sally`. The owner of `spaces` co-owns `spaces.sally` and can remove
  it. Nothing maps a name to a principal (`b737wbpb`).
- **Not above somebody else's place.** `owner` from a `child-owner` line does not apply at a
  node that has another principal's record beneath it. The refusal names the place in the way.
  Ownership by record is unchanged: the owner of `spaces` co-owns every space in it.
- **A task's id names one party's work.** Verse refuses a spawn under an id that stands as
  somebody else's. This includes a rule's spawn.
- **Deleting** a node forgets its record and every record beneath it.

## What is checked on a write

Every write that verse makes for anybody goes through one view of the store, and that view acts
as a principal. So verse checks compare-and-swap writes and its own projections, and the call
site does not need to know. A refusal is a 403 with the check's words, and a count under
`perms.refused`.

- **The node written is the whole of what the request spells.** The api's `sub` beneath an
  address is part of the address where it spells names. `{at: team, sub: c.secret}` is a write
  at `team.c.secret`.
- **Every node the value reaches is checked**, wherever a record or a grant means the answer
  could differ from the node above. `{c: {secret: x}}` written at `team` is also a write at
  `team.c`. A `!delete` inside a value replaces its node whole, and so does a value that is not
  an object. Either needs `owner` wherever something beneath could be somebody else's.
- **What a write asks about, its writer may read.** Verse refuses a `when:` precondition on a
  node that the writer may not read, as it would refuse a read of the node. Verse follows a
  [pattern](../glossary.md#pattern) field by field. So verse never refuses a pattern for what it
  does not mention.
- **What a request removes is asked first.** Removing a rule, a level, a package, an io or a
  runner needs `owner` on it. Verse checks it before it stops or deletes anything. Replacing a
  rule or a level is a write over what stands. The thing's record and grants stand through the
  edit. A co-owner who edits a rule does not become its owner.
- **A charter file is applied as one step.** A refusal anywhere puts back what stood, content
  and record.
- **Two checks come before their write**, because what follows cannot be undone. Verse checks
  an answer before it delivers the answer to the work. It checks a control call before it relays
  the call.

## What is checked on a read

- Verse refuses a `get` of what may not be read, as it refuses a write.
- A listing, a replay, a watch and a snapshot **leave out** what may not be read. The same is
  true inside a payload: a node that stands under a record the reader may not read is left out of
  what holds it. Verse counts what it withholds under `perms.hidden`.
- Verse refuses whole a listing or a watch *at* an address that the reader may not read into.
- **A task is read as its record is.** The task's prompt, its transcript, its result and the
  answers queued for its worker each need `read` on the task's record,
  `verse.task.instance.<id>`. The routes, and the one read that is not checked, are in
  [perms.md](../platform/perms.md#reads-by-a-worker-and-by-a-tasks-routes), in the repository.
- One window is real, and it is one commit wide. The record follows the write that it records.
  So the commit that creates a node reaches a live watch before the record that would hide it.
- **A rule reads as its owner.** Verse narrows three things to what the rule's owner may read:
  the snapshot that a condition is asked of, the snapshot that a reference resolves against, and
  the commit that wakes the rule. Verse refuses at install a rule that reads the records
  themselves, unless the rule is the system's.

## Installed things

A rule, an io, a resolver and a schedule act as `via:<address>`. That principal resolves to the
owner on the record **at that address**. Where the address has no record, it resolves to nobody.
It does not fall back to the owner above, because above is verse's own half, which is the
system's.

- **An install leaves its installer's record, or verse refuses it** with 409.
- **Whose a rule is, is said on its record and nowhere else.** A rule's own entity carries no
  installer.
- **The deployment's charter** is installed as the system, and fires as the system.
- **No rule but the system's writes under `verse`.** Verse refuses a `patch` of a subject there,
  or a `derive` into it, with 403. It refuses at install where the rule names the address, and at
  the write. A rule may select a subject there and spawn about it.
- **A charter package takes the address it is installed at** for its installer. An io takes
  the nodes that its claims name in the same way.

How each installed thing gets its record, and what the system makes under `verse`, is in
[perms.md](../platform/perms.md#installed-things-and-their-records), in the repository.

## Questions

A gate's [question](../glossary.md#question) lives at `verse.gate.question.<id>`, with the
docket's record. Its `under` is what asked it.

- **Who may answer.** Whoever co-owns the question through `under`: the task's owner, or the
  charter's owner. Also whoever the question's own `read-write` line names. Verse takes that
  line from the first of these that applies:
  1. the gate's `answerers`;
  2. for a gate that a worker raises, the spawn's gate of that name, or else whom the worker
     named (`task do gate -to`);
  3. the `child-grants` on `verse.gate.question` (`read-write: [user:*, agent:*]`).

  A runner's grant is on its task, and reaches no question.
- **Held out to a resolver**, the line is whoever answers as the resolver: `via:` its address
  for a service, and otherwise the person or the group behind it. A change of hands replaces the
  line. When the question is handed up (escalated, overdue, or its resolver removed), the line
  goes back to the gate's `answerers`. It goes to that `child-grants` only where the gate named
  nobody.
- **Asked again**, a question keeps its id, and who may answer belongs to the new asking.
- **Answerable once posed.** Posing is two writes: the record, and then the question. An answer
  that arrives before both waits a moment, and then gets `503 … still being posed`.
- **By whoever made the request.** A ballot is keyed by the principal that the check admitted,
  and a record names that principal. This holds for an answer, an escalation, a `say`, a cancel,
  a change of vector, and the `principal` a spawn is answerable to. `need` counts one ballot for
  each.
- **What a worker reports is not evidence of who answered.** For a task that runs on a broker
  or a remote runner, who is working the task is who claimed it. How verse reads what such a
  worker sends is in [perms.md](../platform/perms.md#a-relayed-tasks-frames), in the repository.

## What each act writes

| act | writes |
|---|---|
| a raw put | what it names, as the requester |
| a spawn | the task, as the requester. For a firing, the task is written `via:` the rule, which is the charter's owner. |
| a worker's claim | `read-write` and `control` for the claimant on the task and the run |
| posing, settling, escalating, handing on a question | as `system:docket`, in the request's scope, whoever is working the task |
| holding a question out to a resolver | its `read-write` line, to whoever answers as the resolver |
| an install | the thing, as its installer, with their record |
| a suspension | as the system, naming who holds it under `held` |

## The root, and the config

The root's record stands at `verse.perms.root`. Its `child-grants` is what a node created with
no record above it holds out. It is `{read: ["*"]}` unless the config says otherwise.

`verse` has the system's record with no grants. That record keeps the root's grants out of
verse's own half.

Who owns the root, how the config sets it, and the rows that verse writes at start (the seed)
are in [perms.md](../platform/perms.md#the-root-and-the-config), in the repository.

## `verse perms`

```
verse perms                      the records of every node you may read
verse perms --root               the root's record
verse perms <path>               that node's record, to anyone who may read the node
verse perms <path> -grant '{read: [ana@example.com]}' [-below]    add a line
verse perms <path> -revoke '{read: [ana@example.com]}'            take one back
verse perms <path> -grants '[…]'                                  replace what the node holds out
verse perms <path> -child-grants '{…}'                            what is created beneath starts out holding out
```

Setting a record on a node that has none takes the place first
([above](#creating-and-taking-a-place)). A row carries no copy of the `child-grants` it was
created under. The row is its owner's through the place, so closing the place closes what is
already in it.

`verse status` counts these under `perms`, since the process started:

| Field | What it counts |
|---|---|
| `refused` | refusals, by level |
| `unreadable` | checks that the mirror could not answer |
| `hidden` | what listings withheld |
| `unparsed` | grantee names on a record that the grammar does not read. Each admits nobody, and versed logs each once per record. On a verse whose records were all written under the grammar, `unparsed` is zero. |

<a id="suspending"></a>
## Suspending

`verse flow suspend -charter … -subject …` is a control write ([control](../control.md)). Verse
asks twice who may make one.

**When it is written**, the writer must hold `control` over the charter it names, or at the
subject it names. Either one is enough. The charter is a package's address, or a rule's own
record. Verse asks about a selector with `*` in it at its literal head. Verse refuses a
suspension that names neither.

**When it applies**: every time work would make progress, verse asks the scope's records at
that moment. A holder's suspension covers the work while the holder holds `control` over the
work's charter or at its subject. So a wildcard's reach does not grow as owners appear beneath
it. A check that cannot be answered covers the work.

A rule that the system installed is covered only by a system holder. A holder whose authority
lapses stops covering new work at once. What the suspension already held stays held until
somebody resumes it.

<a id="scopes"></a>
## Scopes

Baseline's grants govern a [scope](../glossary.md#scope). Verse reads a scope's records through
the scope's own view of the store, which is the overlay on baseline. So every grant that stands
in baseline stands in the scope. A record that moves in baseline is seen in every open scope at
once.

A grant made in a scope is copy-on-write, like any write to the world. `verse perms -scope me`,
and the record that every scoped creation leaves, land in the overlay. They govern the scope's
writes on top of baseline's, and baseline never sees them. Two scopes may hold different owners
for one address. Dropping a scope drops its grants.

A task or a gate cannot be acted on from a scope that did not create it. What a scope owns is in [connect](connect.md#what-a-scope-owns).

The reason is that a grant has no live side. A task is a handle and a gate is an awaiter, and
each runs in one engine. A grant is a record that a cache reads, and a cache layers as records
do.

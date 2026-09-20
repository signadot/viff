# Permissions reference

The model is [perms.md](../perms.md), and [perms-guide.md](../perms-guide.md) walks it by
example. This page is the detail: the grammar, a record's fields, what is checked and when, what
each verb writes, the seed. The reasons a rule is the way it is are in the source, beside the
code that enforces it (`entity/attribution.go`, `versed/perms.go`, `versed/place.go`).

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

Every surface that takes a name reads this and refuses what it does not: `-as`, the config's
`user:` and `agent:`, a grant line, a `child-grants`, a group's members, a gate's `answerers`, a
binding's `who` and `group`.

- A bare email is read as `user:<email>` and stored with its kind. Any other bare word names
  nothing, and neither does a bare `verse.…` address.
- **A request presents `user:`, `agent:` or `runner:`.** `via:` and `system:` are names verse
  makes, and a request carrying one is refused 400, as is one carrying a name the grammar does
  not read.
- **A request that is not a read presents one.** Every method but GET and HEAD is refused 400
  without `Verse-Principal`. Three routes are registered otherwise, each acting for a party the
  route itself names: a source's `hint`, and a resolver instance's `verdicts` and `position`.
- A principal is listed the first time it presents itself, under
  `verse.principal.<kind>.<name>`.
- `system:<part>` names the part of verse that wrote — `docket` (questions), `mirror` (task
  projection and a worker's grant), `perms` (records), `seed`, `charter` (the deployment's
  charter), `default` (everything else). The parts are attribution, not authority: they are one
  owner, any of them owns the root, and nothing grants to one rather than another.

**How a grantee matches.**

| grantee | admits |
|---|---|
| `<kind>:<id>` | the principal named exactly that, and anything installed that acts as them |
| `<kind>:*` | any principal of the kind, and anything installed that acts as one |
| `via:<address>` | the installed thing at that address and nothing else — not its owner. A trailing `*` covers what acts beneath (`via:verse.sources.*`). Only a rule, a source or a binding is named this way; a runner acts as `runner:<identity>` |
| `group:<name>` | the group's members, each matched as above. A member may be another group, which is followed; a group already met on the walk is not entered again, so groups that name each other end the walk instead of looping, and admit exactly the members found along it |
| `*` | everyone |

A stored name the grammar does not read admits nobody. Nothing rewrites it; `verse status`
counts each check that meets one under `perms.unparsed`, and versed logs it once per record.

## Records

Records live at `verse.perms.node.<address>`, and the root's at `verse.perms.root`. They are
written by verse alone and readable by the system alone; `verse perms` is how anybody else
reads and sets them.

| field | |
|---|---|
| `owner` | the principal who owns the node. Absent on a record that only holds grants out (a seed row) |
| `grants` | a list of lines. `[]` holds out nothing |
| `child-grants` | levels → grantees: what a record made beneath this node starts out holding out, copied when that record is written. It is no fallback: changing it touches nothing that exists. The nearest `child-grants` up the tree applies; the root's is the last |
| `under` | an address. Whoever co-owns that node co-owns this one. Verse writes it on a question (what asked it) and on a resolver's instance (its binding) |

A grant line:

| field | |
|---|---|
| `read`, `read-write`, `control` | grantees of that level |
| `child-owner` | grantees who may create beneath, and own what they create. Always about what is beneath, so it takes no `below`. There is no grant of `owner` at a node |
| `below: true` | the line covers what is beneath the node and not the node |
| `at: [segs…]` | the line is about another path, with at most a trailing `*`. Absent, it is about the node the record is on, which nearly every line is |

**A grant does not reach through an owner record.** Walking up from a node, the grants on each
record apply until a record naming an owner has been passed; the grants on that record apply,
and the ones above it do not.

**A group** is `verse.principal.group.<name>`: `members`, a list of grantees, owned by whoever
made it. Membership is read when it is checked.

## Levels

| level | admits |
|---|---|
| `read` | reading the node and what is beneath it |
| `read-write` | writing into the node, fields removed as well as set |
| `owner` | everything: deleting the node (which takes what is beneath it), replacing it whole, setting its record. Held by the owner on record and the owners above; conferred by `child-owner` only on a creation |
| `control` | steering work: run, cancel, pause, resume, say, a change of vector, suspend. Held by an owner by record and by whoever it is granted to |

A refusal that says "needs owner" about a creation means no `child-owner` line reaches there.

## Creating, and taking a place

- **Who owns a created node.** A writer who holds `owner` where they write — by owning above, or
  by a `child-owner` line — owns what they create, and a record is written if that differs from
  the owner above. A writer who holds `read-write` creates the node too, and it belongs to the
  owner of the place: no record is written.
- **Where the record goes.** At the topmost node the write brought into being. The walk up
  stops at anything already there, as a node or as a place somebody took.
- **`child-owner` applies to a creation only.** A write admitted by that line alone is committed
  on the condition that nothing stands where it lands, at the node and at each node its value
  reaches. Of two people making one node at once, one owns it and the other is refused. Where
  something does stand, the write is admitted on whatever else the writer holds there, or
  refused naming the owner.
- **Taking a place** (`verse perms <path>` where no record stands) needs `owner` there. By a
  `child-owner` line that means nothing may stand at the node yet. Under `verse` a place is
  taken only where something stands, and then only by its owner, so a name the system has not
  used yet cannot be reserved.
- **First come, first served.** A `child-owner` line says who may create beneath a node, not
  which name each of them may create: where `spaces` holds it out to `user:*`, anybody may take
  `spaces.sally`. The owner of `spaces` co-owns it and can remove it; nothing maps a name to a
  principal (`b737wbpb`).
- **Not above somebody else's place.** `owner` from a `child-owner` line does not apply at a
  node that has another principal's record beneath it. The refusal names the place in the way.
  Ownership by record is untouched: the owner of `spaces` co-owns every space in it.
- **A task's id names one party's work.** A spawn under an id that stands as somebody else's is
  refused, a rule's spawn included.
- **Deleting** a node forgets its record and every record beneath it.

## What is checked on a write

Every write verse makes on anybody's behalf goes through one view of the store acting as a
principal, so compare-and-swap writes and verse's own projections are checked without the call
site knowing. A refusal is a 403 with the check's words, and a count under `perms.refused`.

- **The node written is the whole of what the request spells.** The api's `sub` beneath an
  address is part of the address where it spells names: `{at: team, sub: c.secret}` is a write
  at `team.c.secret`.
- **Every node the value reaches is checked**, wherever a record or a grant means the answer
  could differ from the node above: `{c: {secret: x}}` written at `team` is also a write at
  `team.c`. A `!delete` inside a value, or a value that is not an object, replaces its node
  whole, and needs `owner` wherever something beneath could be somebody else's.
- **What a write asks about, its writer may read.** A `when:` precondition on a node the writer
  may not read is refused as a read of it would be. A pattern is followed field by field, so it
  is never refused for what it does not mention.
- **What a request removes is asked first.** Removing a rule, a level, a package, a source or a
  runner needs `owner` on it and is checked before anything is stopped or deleted. Replacing a
  rule or a level is a write over what stands: its record and grants stand through the edit,
  and a co-owner who edits a rule has not become its owner.
- **A charter file is applied as one step.** A refusal anywhere puts back what stood, content
  and record.
- **Two checks come ahead of their write**, because what follows cannot be undone: an answer is
  checked before it is delivered to the work, and a control call before it is relayed.

## What is checked on a read

- A `get` of what may not be read is refused, as a write is.
- A listing, a replay, a watch and a snapshot **leave out** what may not be read, and the same
  inside a payload: a node standing under a record the reader may not read is left out of what
  holds it. What is withheld is counted under `perms.hidden`.
- A listing or a watch *at* a path the reader may not read into is refused whole.
- One window is real and one commit wide: the record follows the write it records, so the commit
  that creates a node reaches a live watch before the record that would hide it.
- **A rule reads as its owner.** The snapshot a condition is asked of, the one a reference
  resolves against and the commit that wakes it are narrowed to what the rule's owner may read.
  A rule that reads the records themselves is refused at install unless it is the system's.

## Installed things

A rule, a source, a binding and a schedule act as `via:<address>`, which resolves to the owner
on the record **at that address** and to nobody where it has none. It does not fall back to the
owner above: above is verse's half, which is the system's.

- **An install leaves its installer's record, or is refused.** Each of those four installs ends
  by asking who the place acts as; where that is nobody, or somebody whose record stood at the
  name first, the install is taken back out and answered 409.
- **A store can still hold one with no record** — written by hand, or by an older build. The
  start names each in its log. Remove it and install it again.
- **Whose a rule is, is said on its record and nowhere else.** A rule's own entity carries no
  installer.
- **The deployment's charter** (`systemCharter`, and the base charter in the binary) is installed
  as `system:charter` with the system's record on each rule, and fires as the system.
- **What the system makes under `verse` carries the system's record**: a task a system rule
  spawns, the clock and calendar the base charter derives. The record keeps the row's
  `child-owner` line from reaching inside the node.
- **No rule but the system's writes under `verse`.** A `patch` of a subject there, or a `derive`
  into it, is refused at install (403) where the rule names the address, and at the write. A
  rule may select a subject there and spawn about it.
- **A source takes the nodes its claims name** — each claim's levels up to its first glob — for
  its installer, at install. What it reflects beneath is owned through them. A source over a
  node somebody else owns is refused, so two sources sharing a node share an installer.
- **A charter package takes the address it is installed at** for its installer, the same way.

## Questions

A gate's question lives at `verse.gate.question.<id>` with the docket's record, `under` what
asked it.

- **Who may answer.** Whoever co-owns the question through `under` — the task's owner, or the
  charter's — and whoever the question's own `read-write` line names. That line is, in order: the
  gate's `answerers`; for a gate a worker raises, the spawn's gate of that name, else whom the
  worker named (`task do gate -to`); else the `child-grants` on `verse.gate.question`
  (`read-write: [user:*, agent:*]`). A runner's grant is on its task and reaches no question.
- **Bound to a resolver**, the line is whoever answers as the binding: `via:` its address for a
  service, the person or the group behind it otherwise. A change of hands replaces the line;
  handed up — escalated, overdue, its binding removed — it goes back to the gate's `answerers`,
  and to that `child-grants` only where the gate named nobody.
- **Asked again**, a question keeps its id, and who may answer belongs to the new asking.
- **Answerable once posed.** Posing is two writes, the record and then the question. An answer
  that arrives before both waits a moment and is then told `503 … still being posed`.
- **By whoever made the request.** A ballot is keyed by, and a record names, the principal the
  check admitted: for an answer, an escalation, a `say`, a cancel, a change of vector, and the
  `principal` a spawn is answerable to. `need` is one ballot each.
- **A relayed task's frames are not evidence of who did what.** A *frame* is an event a running
  task streams back: a status, a pending gate, a decision, a `say`, a vector change. For a task
  verse hosts, frames are made in verse. For one it relays to — a broker's worker, a remote
  runner — they are whatever the worker sent. So there: who is working the task is who claimed
  it; an answer the route accepted is already on the question, and the worker's decision frame
  only acknowledges that it arrived; a gate nobody was asked about is settled with the frame's
  verdict, by the worker of record (or the runtime's `auto`, `deadline`, `cancelled`), casting
  no ballot; only the question the task is waiting on is its to settle; and a vector change is
  by whoever the control route relayed that mode for, else the worker.

## What each act writes

| act | writes |
|---|---|
| a raw put | what it names, as the requester |
| a spawn | the task, as the requester — or `via:` the rule for a firing, which is the charter's owner |
| a worker's claim | `read-write` and `control` for the claimant on the task's record and the run's, as `system:mirror`; every report from then on is written as the claimant. A runner reached by url gets the same at the first reflection, as `runner:` its `identity` |
| posing, settling, escalating, handing on a question | as `system:docket`, in the request's scope, whoever is working the task |
| binding a question | its `read-write` line, to whoever answers as the binding |
| an install | the thing, as its installer, with their record |
| the deployment's charter | as `system:charter`, at start, after the restore |
| a suspension | as the system, naming who holds it under `held` |

## The root, and the config

The root's record stands at `verse.perms.root`. Its `child-grants` is what a node created with
none above it holds out, `{read: ["*"]}` unless the config says otherwise. Its owner is
`user:admin@verse`, an identity of the deployment's rather than a person's, which co-owns
everything and is what puts the top back. `below` holds out what is beneath the root and not the
root itself, which is nobody's to own and everybody's to read.

`perms: {admins: […]}` seeds the named grantees into the `admin` group and makes the root's
line `child-owner: [group:admin]`. What an admin creates is recorded as the person who acted.
The list only SEEDS the group: after that, membership is changed live by `user:admin@verse` with
`verse group`, so editing the list on a running verse changes nothing, and versed says at start
when the root's record is not what the config would write. `--reseed-perms` is the one start
that rewrites the top — the root, `verse` and the admin group — from the config, replacing them
whole. `perms: {child-grants: {…}}` sets the root's `child-grants` the same way; `{}` is a setting, and
says what anybody creates is theirs alone. ([up.md](up.md#configuration).)

`verse` has the system's record with no grants, which is what keeps the root's grants out of
verse's half.

## The seed

Written at start by `system:seed`, each row once: a row that stands is left as it is, an
operator's edit included.

| at | level | to |
|---|---|---|
| `verse.task.instance.*`, `verse.task.run.*`, `verse.task.claim.*` | child-owner, read | everyone |
| `verse.trigger.rule.*`, `verse.trigger.strata.*`, `verse.trigger.package.*` | child-owner, read | everyone |
| `verse.sources.*`, `verse.runners.*`, `verse.resolvers.*`, `verse.schedule.*` | child-owner, read | everyone |
| `verse.principal.group.*` | child-owner, read | everyone |
| `verse.principal.*`, `verse.watch.*`, `verse.trigger.suspension.*` | read | everyone |
| `verse.gate.question.*` | read | everyone |
| `verse.gate.question.*` | read-write | `user:*`, `agent:*` |

Two `child-grants` stand beside them: on `verse.gate.question`, `{read: ["*"], read-write: [user:*,
agent:*]}`, copied onto each question at pose, which a private verse narrows to close the docket;
and on `verse.schedule`, `{read: ["*"]}` whatever the root's says, so schedules, their ticks and
the derived calendar are readable in a verse that holds out nothing else.

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
([above](#creating-and-taking-a-place)). A row carries no copy of the `child-grants` it was created
under: it is its owner's through the place, so closing the place closes what is already in it.
`verse status` counts `perms.refused` by level, `perms.hidden`, `perms.unparsed`, and checks the
mirror could not answer.

<a id="suspending"></a>
## Suspending

`verse flow suspend -charter … -subject …` is a control write ([control](../control.md)), and
who may make one is asked twice.

**When it is written**, the writer must hold `control` over the charter it names — a package's
address, or a rule's own record — or at the subject it names, either. A selector with `*` in it
is asked at its literal head. A suspension naming neither is refused.

**When it applies**: every time work would make progress, a holder's suspension covers that work
while the holder holds `control` over the work's charter or at its subject, asked of the scope's
records then. So a wildcard's reach does not grow as owners appear beneath it, and a check that
cannot be answered covers. A rule the system installed is covered only by a system holder. A
holder whose authority lapses stops covering new work at once; what was already held stays held
until somebody resumes.

<a id="scopes"></a>
## Scopes

A scope is governed by baseline's grants: its records are read through the scope's own view of
the store, which is the overlay on baseline, so every grant standing in baseline stands in the
scope. A record that moves in baseline is seen in every open scope at once.

A grant made in a scope is copy-on-write, like any world write: `verse perms -scope me`, and the
record every scoped creation leaves, land in the overlay, govern the scope's writes on top of
baseline's, and are never seen by baseline. Two scopes may hold different owners for one address,
and dropping a scope drops its grants.

A task or a gate cannot be acted on from a scope that did not create it, and a grant can be made
in one, because a grant has no live side: a task is a handle and a gate an awaiter, each running
in one engine, while a grant is a record that a cache reads, and a cache layers as records do.
What a scope owns is in [connect](connect.md#what-a-scope-owns).

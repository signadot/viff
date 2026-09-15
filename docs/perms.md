# Permissions: who may write where, and who may read

Every node in the verse has one owner, fixed when the node is created; the owners of its
ancestors co-own it; and anyone else acts on it by a grant. That is the whole model, and this
is how it runs: where the owner is recorded, what a grant is, what the check consults, what a
new node holds out by default, and the one rule that makes a fresh verse usable by everybody
until somebody takes a place of their own.

The surface is small. A request names who makes it (`user:` in the CLI's config, or `-as`), in
the [grammar](#principals-and-grantees) every name in this model is written in; a
refused write or read is a 403 that says who wanted what and which record decided; `verse
status` counts the refusals; `verse perms <path>` is how an owner reads and sets their own
record; and the records live at `verse.perms.node.<address>`, written by versed alone. [reference/entities.md](reference/entities.md#two-halves-one-tree) has the
spellings; [reference/up.md](reference/up.md#configuration) the config. [perms-guide.md](perms-guide.md)
walks the same model in the order a person meets it, from an entity of their own to a gate a
resolver answers.

## Principals and grantees

A principal is a name with its kind before the colon, and a grantee is a principal or a set of
them. One grammar says both, and every surface that takes a name — `-as`, the config's `user:`,
a grant line, a default, a group's members, a gate's `answerers`, a binding's `who` and `group` —
reads it and refuses what it does not:

```
principal ::= user:<email>          # a person
            | agent:<name>          # an operator agent
            | runner:<identity>     # a runner
            | system:<part>         # versed itself
            | via:<address>         # a rule, a source, a binding: whoever acts through it
grantee   ::= principal
            | *                     # everyone, the unattributed request included
            | <kind>:*              # every principal of the kind
            | group:<name>          # the group's members, each as themselves
            | <email>               # shorthand for user:<email>, at the keyboard only
```

A bare email is read as `user:<email>` and stored with its kind, so one form comes back from
every read. Any other bare word names nothing — `user`, `coderunner` and a group's address
among them — and neither does a bare `verse.…` address, which is never a person.

- **a person**, `user:walter@example.com`. The CLI sends it on every request
  (`Verse-Principal`) from its config's `user:`, and the server lists it under
  `verse.principal.user.<email>` the first time it presents itself.
- **a runner**, `runner:coderunner`. A worker claims brokered work as `-as runner:<identity>`,
  and a runner reached by url acts as `runner:` the `identity` on its entity, or its name. The
  claim lists it under `verse.principal.runner.<identity>`. A runner acts as itself, not as
  whoever installed it; and since the kind is in the name, `runner:coderunner` and
  `user:coderunner` are two principals that nothing has to tell apart.
- **an operator agent**, `agent:<name>` — a harness driving verse from outside through the api,
  with the whole verse as its tool surface. It is a principal of its own, not the person whose
  terminal it runs in: `agent:` in the CLI's config makes every request the agent's, listed
  under `verse.principal.agent.<name>`. Today it is granted what a person is; the name is what
  its grants narrow against later.
- **the system**, versed itself, as the part of it that wrote: `system:docket` poses, settles,
  escalates and hands on a question and releases a binding; `system:mirror` projects a task and
  grants the worker that claimed it; `system:perms` writes the mirror's records and who has been
  met; `system:seed` writes the seed; `system:charter` installs the charter the deployment
  stands behind; `system:default` is the restore at start and whatever else names no part. The
  parts are attribution, not authority. Any of them owns the root, co-owns everything beneath
  and passes every check; they are one owner, so a part creating beneath another writes no
  record; and nothing grants to one part rather than another.

A charter, a source and a binding have no identity of their own. Each is a node with an owner,
and each acts **via the address of its record**: a firing writes as
`via:verse.trigger.rule.<pkg:name>`, a reflector as `via:verse.sources.<system>.<name>`, a
binding's verdict as `via:verse.resolvers.<kind>.<name>`, and the check resolves the address to
the record's owner. The write says who acted; the owner is who is answerable. A rule whose
record has no owner yet — installed before its charter was re-applied — acts as the nearest
owner above it, the system, and never as an address: an address is not a party. A `via:` naming
somewhere nothing acts through is refused where it is written: a runner has an address, and acts
as `runner:<identity>` instead.

**A request presents `user:`, `agent:` or `runner:`.** `via:` and `system:` are names versed
makes — when a rule fires, a source reflects, a binding answers, versed acts — and a request
carrying one is refused 400 at the door, as is one carrying a name the grammar does not read.
The charter the system stands behind is installed from the file the deployment names, not over
the api.

**A grantee matches by what it is.** `<kind>:<id>` admits the principal named exactly that, or
whose owner resolves to it, so a grant to `user:ana@example.com` reaches what ana owns acting as
itself. `<kind>:*` admits any principal whose name, or whose owner's, carries the kind.
`via:<address>` admits the place acting as that address and nothing else — not its owner, since
the address is the identity — and a trailing `*` covers what acts beneath it
(`via:verse.sources.*`). `group:<name>` admits the group's members. `*` admits everyone. A name
already stored that the grammar does not read admits nobody; nothing rewrites it, `verse status`
counts every check that meets one under `perms.unparsed`, and versed logs it once per record.

Self-asserted, all of it. There is no login, and the server trusts a principal no harder than
it trusts `-as`. What the model buys is that every write names a party the check can hold
against what its place permits, on the write itself rather than in a field only some verbs
carry — so that when a principal is verified, nothing else has to change.

## Ownership

**The owner of a node is the nearest record at or above it in the mirror**, and the root is
the system's, whichever part of it writes. The mirror records exactly the changes of ownership
down the tree: a task under `verse.task.instance` gets one small record naming the person who
ran it, and a charter package's address gets one naming its installer, taken when the package
is installed — so the rows its rules derive beneath it are owned through the address and need
none of their own.
Installing at an address somebody else owns is refused, with the check's words.

**A source takes the nodes its claims name, the same way.** A claim in a source's `owns` names
a node: its levels up to the first glob. The shipped sources claim a repository's place, so the
node is the place itself — `git.verse`, `git-issue.verse`, `github.signadot.verse` — and a
glob such as `linear.issue.ENG-*` names `linear.issue`. Installing the source writes an owner
record at each node for the installer, and what the source reflects beneath is owned through it
with no record of its own. Ownership here is not single-writership: two sources whose claims are
proven disjoint may still share a node, and the node decides who answers for what is reflected
beneath it — so a source over a node somebody else owns is refused, and two sources sharing one
share an installer.

```tony
# verse.perms.node."verse.task.instance.run-1"
owner: user:walter@example.com
grants:
- read-write: [runner:coderunner] # about the node the record is on
  control: [runner:coderunner]
```

**Ownership changes only by create and delete.** There is no transfer; to hand something over,
place it under a node the other party owns. A whole-entity delete is a co-owner's act —
eviction — and forgets the record, and every record beneath it, so whoever creates there next
owns what they create and no ghost of the old owner stands in their way. A grant withdrawn
stops future writes and touches nothing that exists.

**The owner at creation follows the level held.** When a write creates a node, the principal
holding `owner` there — by co-owning the node above, or by a grant — becomes the owner, and a
record is written if that differs from the owner above. A principal holding `read-write` there
creates the node too, and it is the node's owner's: a file a grantee writes into a shared
place belongs to whoever shares it. Holding neither, the write is refused.

**Questions stand at their own address and are co-owned through what posed them.** A question
lives at `verse.gate.question.<id>` and its record — `system:docket`'s, an owner record of its own —
carries `under`, the address of the task or the rule that asked; the check follows that link
for ownership and for nothing else. So the task's owner, or the charter's, answers what their
own work asked, exactly as if the question stood beneath them, and a runner's grant on its task
— which does not reach through the task's record — reaches no question the task raised. The
executor may not resolve its own gate, and nothing special-cases it.

**Who else may answer is the question's own `read-write` line.** It is written when the
question is posed: the gate's `answerers` when it named them; for a gate a worker raises, the
spawn's gate of that name when it names answerers, else whom the worker named (`task do gate
-to`), a worker asking somebody; else the default on `verse.gate.question` — the docket's
openness, `read-write: [user:*, agent:*]` in the seed. Binding
the question to a resolver replaces that line with whoever answers as the binding (`via:` its
address for a service, the person or the group behind it otherwise), unbinding gives it back to the
default, and `read` stays as the default gave it. `need` over the answerers is one-or-many:
one ballot each. Whoever may answer may hand the question on, `verse gate escalate`, to the
next binding covering its subject by name, and to the docket when there is none.

## Grants and levels

A grant is a line on a record: who holds which level, and where.

```tony
# verse.perms.node."spaces.walter@example.com"
grants:
- read: [user:ana@example.com]               # this node, and everything beneath it
- below: true                                # the children and beneath, not the node
  read-write: [user:*]
- at: [spaces, walter@example.com, shared, "*"]  # another path, with at most a trailing *
  read-write: [user:ana@example.com, group:payments]
```

A line says no `at` when it is about the node the record is on, which nearly every line is —
the default copied at creation, a runner's grant on its task, a question's answerers — so a
record does not repeat its own address per line; `below: true` on such a line covers the
children and beneath, which is what a grant about *creating* needs, and not the node. `at`
names any other path, with at most a trailing `*` for the same meaning: the claim grammar,
refused elsewhere for the reason a claim refuses it. Each grantee is a name in the
[grammar](#principals-and-grantees), and a line naming one it does not read is refused where it
is written.

**A group** is a named set with an owner: `verse group add payments oscar@example.com
ana@example.com` writes `verse.principal.group.payments`, its maker's to change, and
`group:payments` is the grantee that admits its members. A member is any grantee — a principal,
`<kind>:*`, `*`, a place acting, or another `group:` — and the members are walked exactly: a
group already met on the walk is skipped, so a group of groups nested any depth admits its
members and a cycle admits nobody because the walk ends. Membership is read when it is checked,
since a group is for changing who is in it without touching every place it is granted — the one
grantee whose meaning moves after the fact, on purpose.

The levels nest: `read` < `read-write` < `owner`. `control` stands beside them — run, cancel,
pause, resume, say, suspend — and is held by an owner (by record or by an `owner` grant) and by
whoever it is granted to. A flow verb asks it of every task it selects, and one task refused
refuses the whole steer.

**A grant does not reach through an owner record.** Walking up from a node, the grants on each
record reach it until a record carrying an owner has been passed; the grants *on* that record
reach it, and the ones above do not. Two things fall out. The seed's `owner: ["*"]` beneath
`verse.task.instance` is about creating — once a task has its record, nobody but its owner and
its runner writes it. And a question, standing elsewhere, is reached by no grant on the task.

## The check

Every write verse makes on anybody's behalf goes through one view of the store, acting as a
principal: a request's, a firing's (via the rule's address), a source's, the mirror's. The view
resolves the principal, walks the node's prefixes reading records, and refuses or admits **in
the store**, so the compare-and-swap writes and the mirror's projections are checked without
any call site knowing. A refusal is `entity.ErrForbidden`, a 403 at the api with the check's own
words — *user:bob@example.com holds read at demo.pr.1 and writing it needs read-write; the
nearest owner on record is user:alice@example.com at demo.pr.1* — and a count on `verse status`
under `perms`.

Reads go through the same view. A `Get` of what may not be read is refused as a write is; a
listing, a replay, a live watch and a snapshot **leave out** what may not be seen, so one
principal sees one set whichever way they ask, and what a listing withholds is counted apart
(`hidden`) since the listing was answered. **What an answer carries is read the same way, at
every node in it:** a node inside a readable payload that stands under a record the reader may
not read is left out of the payload — by a `Get` of what holds it, a listing, a replay, a watch
and a snapshot alike — so a record governs its node whichever ancestor the read was asked at.
A listing or a watch *at* a path is refused whole
when the reader may not read into it — the node, or what a grant at it holds out beneath. One window is real and one commit wide: the owner
record follows the write it records, so the commit that creates a node reaches a live watch
before the record that would hide it.

**A charter reads as its owner.** The engine evaluates each rule through the store acting as
the rule: the snapshot a condition is asked of, the one a reference resolves against, and the
commit that wakes it are narrowed to what the rule's owner may read. A rule is neither woken
by nor latches on a change it may not see. The perms mirror is the system's with nothing held
out on it, so a system charter watching `verse.perms` and `verse.trigger.rule` sees policy
change, and anybody else's rule reading the mirror is refused at install and told why.

Two things are asked of the check ahead of a write, because the record they would write comes
after something that cannot be undone: an **answer** is checked before it is delivered to the
work, and a **control** call before it is relayed.

## Defaults: what a new node holds out

A record is written where ownership changes, and that is the moment it has to be decided what
the new node holds out to everyone else. The answer is a **default**: a record may carry
`default:`, grantees by level, and a node created beneath it — the nearest such record up the
tree — gets that copied onto its own record as a grant at the node, the moment its record is
written. A directory's default ACL rather than a process's umask, since a place is what verse
addresses; copied at creation, since ownership is fixed at creation and a policy changed
later touches nothing that exists.

```tony
# verse.perms.node."spaces.walter@example.com"
owner: user:walter@example.com
grants: []                 # the place itself holds out nothing
default: {}                # and nothing created beneath it does either
```

Two things follow. A node created under a record with no default inherits from above, up to
the root; and a record holding an empty `grants: []` holds out nothing, where one with no
grants field at all is from before defaults existed and reads as holding out the root's.

**The root's default is the one that is not data**, since the root has no record: `read:
["*"]`, so whatever anybody creates is readable by everyone until a default beneath says
otherwise. It is set in the server's config (`perms: {default: {…}}`, [reference/up.md](reference/up.md#configuration)),
and `{}` there is a verse where what anybody creates is theirs and their grantees' alone.

**Outside `verse`, the tree is open to write until a record says otherwise.** Everyone holds
`owner` at a node outside `verse` with no owner record at or above it, so a raw put creates what it names as
its requester's, and a source's reflections are its installer's. This is the other rule in
code rather than in a record, and the two are different questions: what a place with no record
permits, and what a new record holds out.

## `verse perms`: a place of one's own

The one verb by which a person writes to the mirror. Bare, it lists the records of every node
the requester may read — who owns what you can see, and what each place holds out — since the
mirror itself is the system's and `verse data verse perms` answers nobody else. With a path it
reads that node's record to anyone who may read the node, and its owner sets it: a node with no record of its own gets one — the place is taken by whoever already
holds `owner` there, holding out what the default above says — and then `-default` sets what
is created beneath, `-grants` replaces what the node itself holds out, and `-grant` adds a
line.

```sh
verse data spaces scott notes -put '{todo: [buy milk]}'    # scott's, readable by everyone
verse perms spaces scott -default '{}' -grants '[]'         # scott's place: nothing held out, beneath or on it
verse data spaces scott private -put '{…}'                  # scott's alone
```

Another user is refused `verse data spaces scott private` — a Get of what they may not read —
and refused `verse data spaces scott`, since a listing at a place they may not read into is
refused whole rather than answered empty; `verse data spaces scott notes` still answers them,
by name, since that record holds out the read it was created with. `verse perms spaces scott
-grant '{read: [ana@example.com]}'` opens the place to one person, and `-revoke '{read:
[ana@example.com]}'` takes that back and nothing else; `-grants '[]'` takes everything back.

## The seed

What the system holds out, written at start by `system:seed` under an absence precondition, so a restart writes
nothing and an operator's edit stands:

| at | level | to |
|---|---|---|
| `verse.task.instance.*`, `verse.task.run.*`, `verse.task.claim.*` | owner | everyone |
| `verse.trigger.rule.*`, `verse.trigger.strata.*`, `verse.trigger.package.*` | owner | everyone |
| `verse.sources.*`, `verse.runners.*`, `verse.resolvers.*` | owner | everyone |
| `verse.gate.question.*` | read-write | `user:*`, `agent:*` |
| `verse.gate.question.*` | read | everyone |
| `verse.principal.*` | read | everyone |
| `verse.principal.group.*` | owner | everyone |
| `verse.trigger.suspension.*` | read | everyone |

And one **default** beside them, on `verse.gate.question`, copied onto every question's own
record at pose: `{read: ["*"], read-write: [user:*, agent:*]}`. A private verse narrows that one
default to close the docket.

A question is the one row that is not everyone's: it is posed by versed and answered by
whoever may, and "whoever" is a person — the runner working the task holds its grant on the
task and nothing here. A private verse deletes the row, and then only a co-owner or the
binding a question is held out to answers it.

## What the acts write

- **A spawn** writes the task as its requester — the person, or via the rule's address for a
  firing, which resolves to the charter's owner — so the task is theirs.
- **A claim** is the assignment the model calls the owner's act: when a worker claims a
  brokered task, `system:mirror` writes `read-write` and `control` for the claimant on the task's record
  and on this run's, and the mirror writes every report from then on **as the claimant**. A
  runner reached by url is known at spawn, so the same happens at the first reflection, for
  `runner:` the `identity` on its entity (its name by default). A question the task raises is
  posed and settled as `system:docket` whoever is working the task.
- **A binding** is a grant: when a question is bound to a resolver, the question's record holds
  out `read-write` to whoever answers as the binding — `via:` its address for a service, the
  person or the group behind it otherwise — replaced when the question changes hands and given back to
  the default when it is unbound. A verdict through an instance, an escalation and a position
  are made via the binding; a person answers and escalates from the docket.
- **A person answering** holds it by co-owning the question — their task, their charter — or
  by the seed's row. An `exclusive` binding still decides whose answer *counts*; it is not a
  security boundary and never was.
- **Versed's own acts** on a question — the clock settling an overdue one, the layer rule
  handing one on when a reader arrives or a binding is removed — are written as `system:docket`
  in the request's scope, not as whoever read the docket.
- **The system charter**, from `systemCharter:` in the deployment's config, is applied at start
  as `system:charter` after the restore. Its rules are nobody's but the system's.
- **A rule's record** says who installed it (`by`), written by the install and kept through a
  restore and through a charter file refused part way, which puts a replaced rule back as its
  installer.
- **A suspension** is written by the route as the system, and names who holds it under `held`.

<a id="suspending"></a>
## Suspending

`verse flow suspend -charter … -subject …` is a control write ([control](control.md)), and who
may make one is asked twice.

**When it is written**, the writer must hold `control` over the charter it names — a package's
address, or a rule's own record — or at the subject it names, either; a selector with `*` in it is
asked at its literal head, the segments before the first open one. A suspension naming neither is
refused.

**When it applies**, which is what decides what it covers: every time work would make progress, a
holder's suspension covers that work while the holder holds `control` over the work's charter or at
its subject, asked of the scope's mirror then. So a wildcard's reach does not grow as owners appear
beneath it — `-subject 'gitlab.*'` by somebody who owns nothing there stops covering
`gitlab.alice` the moment alice owns it — and a check the mirror cannot answer covers, since work
that failed open would run what somebody stopped. A rule the system installed (`by: system:charter`)
is covered only by a system holder, from either side: a place with no owner record holds `control`
out to everyone, so perms alone cannot tell the system's rules apart, and the install can.

A holder whose authority lapses stops covering new work at once; what was already held stays held
until somebody resumes.

<a id="scopes"></a>
## Scopes

A scope is governed by baseline's grants. The check reads a mirror of `verse.perms`, and a
scope's mirror is loaded from and watched on the scope's own view of the store, which logd
answers as the overlay on baseline — so every grant standing in baseline stands in the scope,
and opening a scope is not a way around one.

A grant made in a scope is copy-on-write, like any world write: `verse perms -scope me` and the
owner record every scoped creation leaves land in the overlay, govern the scope's writes on top
of baseline's, and are never seen by baseline's mirror. Two devs' scopes therefore hold
different owners for the same address without either touching baseline's record, and dropping
the scope drops its grants with everything else it wrote.

This is the world's rule rather than the program's — a task or a gate cannot be acted on from a
scope that did not create it, a grant can be made in one — because a grant has no live side. A
task is a handle and a gate is an awaiter, each running in one engine, and an act on it from a
scope would land on baseline's live thing while its record landed in the overlay. A grant is a
record that a cache reads, and a cache layers as records do. What a scope owns is in
[connect](reference/connect.md#what-a-scope-owns).

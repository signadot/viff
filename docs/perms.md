# Permissions: who may write where, and who may read

Every node in the verse has one owner, fixed when the node is created; the owners of its
ancestors co-own it; and anyone else acts on it by a grant. That is the whole model, and this
is how it runs: where the owner is recorded, what a grant is, what the check consults, and
what the two open defaults are that make a fresh verse usable by everybody until somebody
narrows them.

The surface is small. A request names who makes it (`user:` in the CLI's config, or `--as`); a
refused write or read is a 403 that says who wanted what and which record decided; `verse
status` counts the refusals; and the records live at `verse.perms.node.<address>`, written by
versed alone. [reference/entities.md](reference/entities.md#two-halves-one-tree) has the
spellings; [reference/up.md](reference/up.md#config) the config.

## Principals

A principal is a string, and there are four kinds of party behind one:

- **a person**, by email — `walter@example.com`. The CLI sends it on every request
  (`Verse-Principal`) from its config's `user:`, and the server lists it under
  `verse.principal.user.<email>` the first time it presents itself.
- **a runner**, under its own name — `coderunner-bot`. A worker names itself when it claims
  brokered work, and the claim lists it under `verse.principal.runner.<name>`. A runner acts
  as itself, not as whoever installed it; and a name that has claimed work is a runner's even
  when it turns up on the api, so it is never listed as a user too.
- **an operator agent** — a harness driving verse from outside through the api, with the
  whole verse as its tool surface. It is a principal of its own, not the person whose terminal
  it runs in: `agent:` in the CLI's config makes every request the agent's, under its own kind
  (`Verse-Principal-Kind: agent`, listed under `verse.principal.agent.<name>`). Today it is
  granted what a person is; the name is what its grants narrow against later.
- **the system**, versed itself: `system`. It owns the root of the tree and every kind-rooted
  node under it, co-owns everything beneath, and is what a mirror's projection, a posed
  question and the seed are written as. A request cannot be made as it — a header saying
  `system` is refused at the door — and the charter the system stands behind is installed from
  the file the deployment names, not over the api.

A charter and a source have no identity of their own. Each is a node with an owner, and each
acts **as the address of its record**: a firing writes as `verse.trigger.rule.<pkg:name>`, a
reflector as `verse.sources.<system>.<name>`, and the check resolves the address to the
record's owner. The write says who acted; the owner is who is answerable. A rule whose record
has no owner yet — installed before its charter was re-applied — acts as the nearest owner
above it, the system, and never as an address: an address is not a party.

Self-asserted, all of it. There is no login, and the server trusts a principal no harder than
it trusts `--as`. What the model buys is that every write names a party the check can hold
against what its place permits, on the write itself rather than in a field only some verbs
carry — so that when a principal is verified, nothing else has to change.

## Ownership

**The owner of a node is the nearest record at or above it in the mirror**, and the root is
the system's. The mirror records exactly the changes of ownership down the tree: a task under
`verse.task.instance` gets one small record naming the person who ran it, and the ten thousand
refs a source reflects under a slice get none, since each is owned by whoever installed the
source and the source's record already says so.

```tony
# verse.perms.node."verse.task.instance.run-1"
owner: walter@example.com
grants:
- at: [verse, task, instance, run-1]
  read-write: [coderunner-bot]
  control: [coderunner-bot]
```

**Ownership changes only by create and delete.** There is no transfer; to hand something over,
place it under a node the other party owns. A whole-entity delete is a co-owner's act —
eviction — and forgets the record, so whoever creates the node next owns it. A grant withdrawn
stops future writes and touches nothing that exists.

**The owner at creation follows the level held.** When a write creates a node, the principal
holding `owner` there — by co-owning the node above, or by a grant — becomes the owner, and a
record is written if that differs from the owner above. A principal holding `read-write` there
creates the node too, and it is the node's owner's: a file a grantee writes into a shared
slice belongs to whoever shares it. Holding neither, the write is refused.

**Questions stand at their own address and are co-owned through what posed them.** A question
lives at `verse.gate.question.<id>` and its record carries `under`, the address of the task or
the rule that asked; the check follows that link for ownership and for nothing else. So the
task's owner, or the charter's, answers what their own work asked, exactly as if the question
stood beneath them, and a runner's grant on its task — which does not reach through the
task's record — reaches no question the task raised. The executor may not resolve its own gate,
and nothing special-cases it.

## Grants and levels

A grant is a line on a record: a path it covers, and who holds which level there.

```tony
grants:
- at: [verse, task, instance, "*"]       # beneath this path
  owner: ["*"]
- at: [spaces, walter@example.com, shared, "*"]
  read-write: [ana@example.com, user]
- at: verse.gate.question.run-1~d1       # the node itself, and beneath it
  read-write: [verse.resolvers.security.payments-review]
```

`at` is a path with at most a trailing `*` — the claim grammar, refused elsewhere for the
reason a claim refuses it. Without the star the grant covers the node and everything beneath;
with it, the children and beneath, which is what a grant about *creating* needs. A grantee is a
principal by the name it acts under or the owner it resolves to, `*` for everyone (the
unattributed request included), or a kind — `user`, `runner`, `agent` — meaning whoever is
listed under it.

The levels nest: `read` < `read-write` < `owner`. `control` stands beside them — run, cancel,
pause, resume, say — and is held by an owner (by record or by an `owner` grant) and by
whoever it is granted to.

**A grant does not reach through an owner record.** Walking up from a node, the grants on each
record reach it until a record carrying an owner has been passed; the grants *on* that record
reach it, and the ones above do not. Two things fall out. The seed's `owner: ["*"]` beneath
`verse.task.instance` is about creating — once a task has its record, nobody but its owner and
its runner writes it. And a question, standing elsewhere, is reached by no grant on the task.

## The check

Every write verse makes on anybody's behalf goes through one view of the store, acting as a
principal: a request's, a firing's (the rule's address), a source's, the mirror's. The view
resolves the principal, walks the node's prefixes reading records, and refuses or admits **in
the store**, so the compare-and-swap writes and the mirror's projections are checked without
any call site knowing. A refusal is `entity.ErrForbidden`, a 403 at the api with the check's own
words — *bob holds read at demo.pr.1 and writing it needs read-write; the nearest owner on
record is alice at demo.pr.1* — and a count on `verse status` under `perms`.

Reads go through the same view. A `Get` of what may not be read is refused as a write is; a
listing, a replay, a live watch and a snapshot **leave out** what may not be seen, so one
principal sees one set whichever way they ask, and what a listing withholds is counted apart
(`hidden`) since the listing was answered. One window is real and one commit wide: the owner
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

## Two open defaults

A fresh verse is usable by everyone, and the two rules that make it so are in code rather
than in a record, because the root has no record to hang them on. They are the first thing a
private verse changes.

- **The world's half is open to write until a record says otherwise.** Everyone holds `owner`
  at a world node with no owner record at or above it, so a raw put creates what it names as
  its requester's, and a source's reflections are its installer's. A node with an owner is
  that owner's, and their co-owners', and their grantees'.
- **Everyone reads everything but the mirror.** Whatever the records say, a read is admitted
  except at `verse.perms`, which only the system reads. With this rule off
  (`entity.Attribution.OpenReads`) a read is admitted by co-ownership or a `read` grant and by
  nothing else, and a charter matches only what its owner can read.

## The seed

What the system holds out, written at start under an absence precondition, so a restart writes
nothing and an operator's edit stands:

| at | level | to |
|---|---|---|
| `verse.task.instance.*`, `verse.task.run.*`, `verse.task.claim.*` | owner | everyone |
| `verse.trigger.rule.*`, `verse.trigger.strata.*`, `verse.trigger.package.*` | owner | everyone |
| `verse.sources.*`, `verse.runners.*`, `verse.resolvers.*` | owner | everyone |
| `verse.gate.question.*` | read-write | `user`, `agent` |

A question is the one row that is not everyone's: it is posed by versed and answered by
whoever may, and "whoever" is a person — the runner working the task holds its grant on the
task and nothing here. A private verse deletes the row, and then only a co-owner or the
binding a question is held out to answers it.

## What the acts write

- **A spawn** writes the task as its requester — the person, or the rule's address for a
  firing, which resolves to the charter's owner — so the task is theirs.
- **A claim** is the assignment the model calls the owner's act: when a worker claims a
  brokered task, versed writes `read-write` and `control` for the claimant on the task's record
  and on this run's, and the mirror writes every report from then on **as the claimant**. A
  question the task raises is posed and settled as the system whoever is working the task.
- **A binding** is a grant: when a question is bound to a resolver, the question's record holds
  out `read-write` to the binding's address, replaced when the question changes hands and
  removed when it is unbound. A verdict through an instance, an escalation and a position are
  made as the binding.
- **A person answering** holds it by co-owning the question — their task, their charter — or
  by the seed's row. An `exclusive` binding still decides whose answer *counts*; it is not a
  security boundary and never was.
- **Versed's own acts** on a question — the clock settling an overdue one, the layer rule
  handing one on when a reader arrives or a binding is removed — are written as the system in
  the request's scope, not as whoever read the docket.
- **The system charter**, from `systemCharter:` in the deployment's config, is applied at start
  as the system after the restore. Its rules are nobody's but the system's.

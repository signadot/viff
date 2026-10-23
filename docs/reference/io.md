# Iods

This page is the surface of an iod. For what an iod *is* (one program, two halves, and which side
dials each half), see [../io.md](../io.md). To build one, copy
[examples/io](examples/io/main.go).

An [iod](../glossary.md#iod) is an entity at `verse.iods.<name>`:

| | |
|---|---|
| `name` | its name, and the whole of its address under `verse.io`. Required |
| `root` | the root of the tree this iod writes, and is the single writer of. It is the first segment of every address it writes (`git`, `github`, …). Required |
| `url` | verse dials this base for the reflection contract |
| `dir` | a local git repository, reflected in task, with no network |
| `dial-in` | the iod dials verse. It is designed, and refused at install until it is built |
| `owns` | the addresses this iod claims to be the single writer of (see below) |
| `out` | its out half: where it reads what is asked of it, whom it acts as, and who may ask (see below) |
| `scope` | the copy-on-write scope it reflects into. Empty is baseline |
| `lastReflect` | when verse last *wrote* from this iod. Verse writes it |
| `lastError` | the last failure of the reflection. The next success clears it. Verse writes it |

The first four rows and `lastReflect` belong to the in half, the
[source](../glossary.md#source): the reflection that verse dials. `out` belongs to the other
half, the [sink](../glossary.md#sink), which dials verse.

**A registration has exactly one of `url`/`dir`/`dial-in`.** The three differ only in where the
connection comes from. `Reflect` is the same call on all three. For a runner, absence means
something. Here it does not: the field that is present says which way it is.

The address of an iod is its name and nothing else: `verse.iods.<name>`. It is under the reserved
root, like everything verse keeps about its own running. So an operator tells two iods of one
connector apart by the names the operator gave them.

`root` answers the question that verse asks of a registration: who writes `git.*`? The root is
one segment of an address. So it may not contain `:`, `/` or `.`. A name may not contain `/`,
because a name is a segment of the route that serves the iod.

**The convention is to name an iod after its connector**, such as `git`, `git-issue` or `slack`.
Most verses run one iod of each kind. A name that says no more than `root` says would be a second
word for one fact. The name is still free. A second iod on one connector is where a name of its
own is useful (`git` beside `git-buildhost`).

An iod reflects the world outside verse. It never reflects what verse keeps under `verse`:

- Verse refuses a reflection that names an address under `verse.`.
- Verse refuses, at install, a claim that names such an address.
- No claim reaches there, however wide it is written. `*` claims every address but `verse`.

## Ownership

`owns` is a list of [claims](../glossary.md#claim-of-an-iod). The claims do three things:

- An install proves them disjoint from the claims of other iods.
- They bound what a `-forget` removal may tombstone.
- They name the nodes the iod owns.

The node of a claim is its levels up to the first glob. Installing takes each such node for the
installer. So what the iod reflects beneath the node belongs to the installer, with no owner
record per entity. Verse refuses an install over a node that somebody else owns
([perms.md](../perms.md#ownership)).

An ownership claim is an **address**, not a payload predicate. An install compares declarations,
and it has no entities in hand.

```tony
{at: "git.verse.head"}         one entity
{at: "git.verse"}              a whole subtree: everything under it, at every depth
{at: "linear.ENG.issue.*"}     a level may be `*`, and may END in one — a literal prefix
{at: "verse-dev.drift"}        …which is how an iod that keeps two levels of its own says so
{at: "linear.project.*.owners.ENG"}   one place inside a node several iods share
```

A claim is **one pattern per level, at any depth**. This is the same shape as an address. A
claim owns the node it names and everything under it. So `git` owns every repository, and
`verse-dev.drift` owns the tree that a package keeps there.

**Sharing is a deeper address.** Two iods that share a thing do not share a node. They own two
nodes beneath it, such as `linear.project.p1.owners.ENG` and `…owners.OPS`. These are two
addresses, and verse proves them apart with the same comparison, level by level, as any other
pair. Sharing is not an exception to single-writership. It is single-writership one level down.
The extra level costs nothing, because an address has no fixed depth.

Two claims are **disjoint** when any level is disjoint. Two levels are disjoint when they are:

- two literal names that differ, or
- two prefixes, where neither is a prefix of the other.

If no level separates the claims, the shorter claim contains the longer. For example, `git`
overlaps `git.verse.ref.x`, and it is not disjoint from it. The path in a payload follows the
same rule. Verse asks it once of the address and once inside the payload.

- A `*` level is disjoint from nothing. So verse can prove `git.*.*` apart from nothing under
  the same root.
- A `*` anywhere but at the end of a level is a parse error. It is not an approximation.
- Levels and paths both compare **segment by segment**. So `owners.ENG` and `owners.ENGINE` are
  disjoint. `owners` contains `owners.ENG`, and an absent path contains every path.

Git claims **one address per repository**, because a repository is a level of every address that
git writes:

```tony
owns:
- at: "git.verse"
```

Prefer a claim of this kind, with no glob. It is disjoint from `git.verse-extras` by a literal
comparison, where a glob such as `git.verse*` would claim too much. It is also a node where the
install can write an owner record. A claim that globs at its second level names only its first
segment as a node.

**A claim is a document, not a string.** The ids of tasks make this necessary. A terse
`git.*."verse/*@owners.ENG"` would need `@` as a separator. But `@` already appears inside ids
(the id of a task is `<chain>@<subject>`), so no parser could read such a claim back. A grammar
with two forms would have to be read two ways everywhere, so a claim has one form.

An install with no `owns` takes what the probe reported. Verse can prove a registration with no
claims apart from nothing. So verse refuses it beside any other registration in its scope.

## Commands

```
verse iod                                what is installed, with lastReflect/lastError
        [-probe]                             …and what each one says right now (not recorded)
verse iod add <name> <url> -root <root>       install one, probing the url first
       add -dir <d> <name>                  …or a local git repo, reflected in task
       add [-file <f>] | [file] | stdin      …or as a document (.tony/.json/.yaml)
           [-no-probe]                       install without asking it what it is
           [-allow-overlap]                  install though another iod may write the same place
       show <ref>                            the entity
       check <ref>                           ask it what it is and what it claims, right now
       remove <ref>                          take it away; refused if it reflected anything
              [-forget]                      …the reflection is over: tombstone what it wrote
              [-keep]                        …the truth is moving: leave the last known state
```

A `<ref>` is `verse.iods.laptop` or `laptop`: the whole address or the bare name. The middle form
(`io.laptop`) is not accepted.

`verse status` names the installed iods under `iod:`. It shows the refs only. `verse iod` shows
their health.

The list is in `verse status` because it is the one line that says whether anything is
*arriving*. Every other counter moves on what verse already has. So a verse whose last iod was
removed looks the same as a healthy, quiet verse, until that list is empty.

Every command takes the global `-scope`. A scoped iod is installed, listed and removed in the
scope it reflects into.

## Routes

| | |
|---|---|
| `GET /iods` | list; `?probe=true` adds a live `reach` per iod |
| `POST /iods` | install; `?probe=false` skips the check, `?allow-overlap=true` the disjointness proof. `201` and the io's row as the listing shows it, with `reach`, the `owns` it settled on (taken from the probe when you named none), and `warning` when it is unreachable or not running (`io.Row`) |
| `GET /iods/{name}` | the entity |
| `GET /iods/{name}/check` | `{at, reach}`, plus `url` when the iod has one and `why` when the probe gave a reason, plus `says: {root, owns}` when it answered |
| `DELETE /iods/{name}` | remove. `?forget=true` or `?keep=true`; `409` with both named when it reflected something and neither is given |
| `POST /iods/{name}/hint` | `?at=<address>` (repeatable): reconcile those addresses. None means a full resync. `204` |
| `GET /outboxes` | `?to=<out place>`: the outboxes packages declare sending there, as `[{from, to}]`. Only the iod whose out half reads the place (its `as`) may ask; `403` for anybody else, `404` when no iod reads it |

A route that names one iod has two answers when it does not find the iod:

| | |
|---|---|
| `404` | `in your view, no iod "<name>" is installed`. The iod is not installed, or it is not yours to read. The answer is the same for both. |
| `503` | `iod "<name>" could not be read: …`. The store did not answer. Nothing is known to be missing, so ask again. |

The listing leaves out the iods you may not read. It answers `503` when verse could not read the
store.

`reach` is one of `a source`, `not a source` and `unreachable`. Verse writes nothing about it to
the store.

A `500` from an iod is *unreachable*, not *not a source*. Something speaks HTTP and fails. That is
at least as often an iod with a temporary fault as a wrong url. Refusing the install would refuse
a fault that fixes itself.

The listing reports `lastPass` from the live reflector: when a pass last *completed*, whatever
it did. It reports `running: false` for a registration with no reflector. Verse stores neither
in the document.

**A [hint](../glossary.md#hint) carries no data.** It names which address went stale, and
nothing else. So the route can stay unauthenticated in a wide-open dogfood verse. A delivery
that is duplicated, reordered or forged costs at most a reconcile of an address whose contents
its sender does not choose.

A malformed ref is `400`, not a silent full resync. A hint to a registration that is installed
but not running is `503`, not a quiet `204`. A hint that verse drops must not look like one that
worked.

## The contract an iod serves

The contract has two routes, no state and no way to write:

| | |
|---|---|
| `GET /iods` | `{root, owns}` — what this program is. Has no effect, which is what makes it the probe |
| `GET /reflect` | `?at=<address>` (absent = everything it owns) → a `Reflection` |

```tony
covers:
- path: "git.verse.ref.*"
- path: "git.verse.head"
entities:
- at: git.verse.ref.main
  payload: {repo: verse, name: main, sha: "82bd107…", type: commit}
- at: git.verse.head
  payload: {repo: verse, ref: main, sha: "82bd107…", dirty: false}
```

`covers` says what the [reflection](../glossary.md#reflection) is **complete for**:

- An entity under a covered pattern and absent from `entities` is tombstoned.
- An entity under no cover is written, and never tombstoned.

This is how git keeps history. Git covers `ref` and `head`, and it does not cover `commit`.

The `path` of a [cover](../glossary.md#cover-of-a-reflection) is a pattern. It has one segment
per level, and `*` matches any one segment. The path is also what converge **reads**: the refs
under `git.verse.ref` and the node `git.verse.head`, not all of `git`.

The first segment of a cover is a literal, and it is the `root` of this iod. A cover that begins
with `*` would claim every address it is compared against, so verse refuses it.

A cover may add `value`, a payload predicate. Use it where an address alone cannot say what the
answer is complete for. GitHub covers the open issues of a repository and its recent closed
issues as two halves of `github.<org>.<name>.issue.*`.

**A cover goes as deep as the answer does.** An iod that owns one address inside a shared thing
covers that address, such as `linear.project.*.owners.ENG`. Converge writes, compares and
tombstones there, and nowhere else. Two iods can be the single writers of two nodes under one
project. Each is proved apart by its `owns`, and neither can reach the other.

Removing the last owner empties `owners`, and that empties the project. Each step up is a
conditional delete. It is the only contended write in the design, and it stops as soon as
something else still stands.

A cover is an `entity.Pattern`. That is the same selector that the `when` of a charter becomes.
So the payload predicate is spelled `value`, as it is in a charter.

On the wire, each cover also carries an empty `op: ""`. It means nothing here, and it comes from
upstream: the code generator honours `omitzero` on every field of that struct except the field
whose type implements `MarshalText`. Ignore it. A cover has no op, and verse refuses a cover
whose op is not empty.

**A failed reflect must fail loudly**: `500`, never `200` with an empty body. An empty answer
that carries covers is an instruction to tombstone everything under them, and verse will believe
it.

## The reflector

Verse runs one [reflector](../glossary.md#reflector) per installed registration.

| When | What happens |
|---|---|
| install | verse starts it |
| removal | verse stops it |
| restart | verse restores it, after the charter |

A reflector restores after the charter because a reflection is a write that rules react to. A
level-triggered pass does not repeat itself. A rule restored late would miss what it missed, for
good.

A restart may fail to list the registrations: the store is busy, or the read passes its
deadline. Then verse restores them when the listing does happen. After the charter is back,
verse asks for the listing again until it answers. Until then, `verse status` lists
`restored sources` under `restoring`, each registration shows `running: false`, and verse
refuses a hint to it. None of this needs any action.

Each reflector runs three kinds of pass:

- an initial full pass;
- a periodic full pass every `versed.ResyncEvery` (one minute);
- a targeted pass for each hint.

Verse does **not** restore scoped registrations on restart. `entity.Store` has no way to list
scopes, and a scoped store reads baseline layered underneath, so nothing can find them. Scoped
charters have the same gap. Whatever owns a scope creates it on demand, so the thing that made
the scope can install into it again.


## The out half

An iod that acts back says so on its registration. The install writes the grants that the
registration describes:

```tony
out:
  places:     [slack.Signadot.out]     # where it reads what is asked of it
  as:         agent:slack-iod           # who it acts as, and owns every status it writes
  requesters: [group:verse-dev]        # who may ask it for something
```

| | |
|---|---|
| `places` | where it reads what is asked of it, as kinded paths. Each must lie inside what this iod claims, and outside what its reflection covers. It is a list, because one iod may serve several. A Linear iod reads one address per team, since a claim per team is what keeps two installs apart |
| `as` | the principal it acts as. This principal owns every status the iod writes, so nobody else can forge one |
| `requesters` | who may ask, as grantees. Empty asks for nobody. That is how an operator says that the grants are managed by hand |
| `lastError` | the failure that stopped a pass, and when it began. Verse writes it from the iod's report (see below). What the last pass did is in the report too, kept in scratch rather than on the record |

```sh
verse iod add slack http://slack-iod:9801 --root slack \
  --out slack.Signadot.out --as agent:slack-iod --requesters group:verse-dev
```

**The install writes four grants**, from the record, once:

```
read-write   <requesters>       below the place    they may ask, and may not write the answers
read         <as>               below the place    the iod reads what is asked
child-owner  <as>                                  and OWNS each status it creates
child-grants {read: ["*"]}                         which everyone may read
```

The grant to the requesters is *below*, and not a child grant, on purpose. A child grant is
copied onto every record created beneath, statuses included. So it would let the askers write
the answers. Nobody sees that difference until somebody edits a status. For this reason verse
writes the grants from the record, and does not leave them to a runbook.

The install checks these things, while the operator is present:

- `places` and `as` are required.
- `as` parses as a principal.
- Every requester parses as a grantee.
- Each place lies inside what this iod claims. An iod asked for things outside its own slice would
  seed grants in the tree of somebody else.

The install does not check whether a place is outside the COVERS of the reflection. The connector
must arrange that, because covers come with an answer and not with a claim.

`--forget` takes the grants back, with everything else the iod left. `--keep` keeps them, because
a re-install lands back on a place whose grants stand.

### Outboxes: what a package sends

A package asks an iod for something through an outbox it declares
([`sends:`](charter.md#a-charter-can-be-installed-at-an-address)): a place inside the package,
named with the iod's out place.

```tony
at: verse-dev.announce
sends: [{from: out.slack, to: slack.Signadot.out}]
```

Each pass, the out half reads its own place and then every outbox sending to it, which it asks
verse for (`GET /outboxes`). An input in an outbox is handed to the connector as one asked at the
iod's own place, so a connector serving several places treats it the same. Its status is written
beside it, inside the package, where the package's rules can read it.

The package's install writes the grants, at the outbox:

```
read         <as>               below the outbox   the iod reads what is asked
child-owner  <as>                                  and OWNS each status it creates
```

The package's own rules ask as its installer, who owns the outbox already. So there is no
requesters' line, and no line for anybody else.

The iod does nothing to opt in. Any registered iod's out place may be named in a `to`.

The grant follows both sides. Removing the iod takes back the grant on every outbox sending to
it, whether the removal said `--forget` or `--keep`: an outbox is a package's arrangement with an
iod that is gone. Installing the iod again grants the outboxes still declared for its places, so
the packages need not be installed again.

### What is asked, and what became of it

| | where | written by | holds |
|---|---|---|---|
| input | anywhere under `<place>` | whoever asks | `to` — whom or what it is about — and whatever the connector reads (`text`, Slack's `asks`) |
| status | the sibling `<name>~status` | the iod alone | whatever the connector records, and `error`/`failedAt` while a failure stands, with `failed: permanent` and `failedOn` for a refusal |

**An input is a node that carries `to`, at any depth.** Whatever writes an input decides where it
stands. A person types one under the place. The fan-out of a charter writes one per person under
(rule, subject, key). For one message per answerer of a question, that is seven levels down.

A contract that read the target from a fixed level could be written by hand, and by nothing
else. So the address says where a request came from, and the payload says whom it is for.

The sink never reads a name that ends in `~status` as an input. Otherwise a status would ask for
the thing it answers.

A pass is **level-triggered**. Every pass offers every input to the connector, with what the last
pass recorded beside it. The connector answers what to record now, or nothing when the external
system already matches.

A failure is written once, with the instant it began. The success that ends it clears it.

**A refusal is permanent.** The far side may refuse what was asked: a 4xx, or Slack's `ok: false`
with an error that is not a rate limit or an outage. Or the connector may refuse the input itself:
no text, a target that is no target, a target changed after delivery. Asked the same way, the
answer would be the same. So the status records `failed: permanent`, and `failedOn`, a fingerprint
of the input as it was refused. The input is not offered to the connector again while it stands
unchanged.

- **To ask again, edit the input.** An edit is a new request: it is offered once, and is
  permanent again only if it is refused again.
- **This includes a credential the far side refuses.** Once the credential is fixed, a refused
  input goes out when it is edited.
- **A failure that is not a refusal is retried on every pass.** That covers the far side down, a
  rate limit, and the network.

A request sent again on every pass after it was refused is what tripped GitHub's secondary rate
limit for the account an iod acted as.

A connector that keeps something current on every pass is still told of an input it does not
offer. Slack's keeps the thread a message's replies are matched in.

### Health: verse is told

Verse dials a reflection, so it knows how the reflection is doing. Verse never dials an out half,
so the out half reports:

```
POST /iods/<name>/pass   {delivered, failed, error?}
```

When the pass finished and what it did (`lastPass`, `delivered`, `failed`) go to the store's
*scratch*: a document beside the log with no history and no watches, for facts that are only
about now, so a pass costs no commit. The failure that stopped a pass (`lastError`, with when it
began) goes on the registration, written when it begins and removed by the pass that ends it;
a pass that repeats either writes nothing. `verse iod` reads both and shows them beside the
columns of the reflection: the report under `report`, the failure under `out.lastError`. So an
iod with a healthy reflection and an out half refused for an hour does not look healthy. A
scoped iod's report is its scope's, beside baseline's and not on it, and goes with the scope.

The counts are those of the last pass, not totals. Somebody can act on "the last pass delivered
nothing and failed twice".

The report is unattributed, like the hint beside it, and for the same reason. It is a fact about
the iod's own half of the world. A forged report costs a misleading timestamp.

## Control: what people do over there

A reply or a mention is not a reflection and not a delivery. It is a control request,
and an intervention is never a store write ([control.md](../control.md)). So a connector that
hears one asks verse for it through the ordinary route. It does not write the record.

`io.Controller` is that seam. It gets `func(principal string) *client.Client`: a factory, not a
client. So the connector makes each request as whoever the external system says did the thing.

Three invariants hold for every connector:

- **Which** request it is, is decided without reading prose. The Slack iod knows which question a
  reply answers, because it posted the message that the reply is under.
- **What** it may ask for is bounded by what the record already offers, such as the options on a
  question. It never invents one.
- **Who** it is made as is the person, never the iod's own principal. An answer recorded as
  `agent:slack-iod` is an answer that nobody gave.

The connector asserts that person to verse in the way the CLI asserts its own user. Verse trusts
the assertion as it trusts the CLI's. The fix is a principal that verse verifies for itself, and
that fix does not belong to this seam.

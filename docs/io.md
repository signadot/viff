# Ios

An **[io](glossary.md#io)** is one program that connects verse to one external system, such as
a git repository, GitHub, Linear or Slack. An io has two halves.

| Half | What it does |
|---|---|
| the **[source](glossary.md#source)** (the in half) | It reads the external system. Verse writes what it read into the [store](glossary.md#store), under the io's [root](glossary.md#root-of-an-io) (`git`, `github`, …). The io is the single writer of everything under that root. [Rules](glossary.md#rule) and agents read what it wrote, and react. |
| the **[sink](glossary.md#sink)** (the out half) | Somebody writes what they want said or done, at an address the io reads. The io makes the external system match, and writes back what happened. |

The two halves differ in one thing: **which side dials**.

- Verse dials the source and asks it what is true.
- The sink dials verse. It reads what is asked of it, and it writes its own statuses.

The rest follows from that difference. [seams.md](seams.md) makes the general point: the
direction of a connection decides what is possible on it.

One program holds both halves. So an io has one credential, one rate budget, one registration
and one deployment.

Not every io has both halves. Git has a source, and it acts out through a **task**. A push
cannot be taken back, and verse gates each step of an irreversible effect and does it once.

The rule is reversibility:

| Use | For | Examples |
|---|---|---|
| a sink | what is kept in step with a record, and corrected by editing the record | a message, a comment, a notice |
| a task | what cannot be taken back | a push |

For the surface of an io (the entity, the verbs, the routes, the contracts it serves), see
[reference/io.md](reference/io.md). Verse ships five ios: git, git-issue, Slack, Linear and
GitHub. For them, see [reference/iod.md](reference/iod.md). To build your own io, copy
[examples/io](reference/examples/io/main.go). It is a real program, and it compiles with the
tree.

```sh
verse io add git   http://git-io:9801    -root git
verse io add slack http://slack-io:9801  -root slack
verse io
```

**Name an io after its connector, unless two of them run.** The name is the whole of the
address (`verse.io.<name>`), and you choose it freely. Where one io of a kind runs, the name
`git` says what `--root git` already says. The reader has nothing new to learn.

Where two ios of one kind run, the name is the only thing that tells them apart. For example,
one reflects the repositories on the cluster, and one reflects the repositories on a build
host. Give the second a name of its own:

```sh
verse io add git-buildhost http://build-host:9801 -root git
```

## The core law: a hint is a hint, reconcile is the truth

An external system offers two ways to integrate:

| Way | What it is |
|---|---|
| a webhook | a hint with low latency, such as "PR 42 changed". It may be missed or duplicated, and it may arrive out of order. |
| a poll-reconcile | a pass that reads the external truth and makes the store match it. It is authoritative and level-triggered. |

Verse makes the same split inside itself:

> webhook : poll-reconcile :: Observe : Since

So the law is:

> **A hint never writes entities.** It schedules a reconcile of the affected ref(s); the
> reconcile reads the source of truth and writes to converge.

A [hint](glossary.md#hint) names the addresses that went stale. A reconcile of those addresses
reads the external system and [converges](glossary.md#converge) the store with it. A periodic
full reconcile catches every hint that went missing.

**Verse does not parse deliveries at all.** The source owns its own deliveries. It installed
the git hook, and it holds the webhook secret. It tells verse which addresses went stale.

A delivery carries no data. So a delivery costs at most a reconcile of an address whose
contents its sender does not choose. For this reason the hint route can stay unauthenticated
in a wide-open dogfood verse.

The reasons are in [Why it is this way](#why-it-is-this-way).

## Ownership = single writer = no CAS

A source that owns `github` is the only writer under `github`. So the store has no lost-update
problem there. An address that a source reflects needs no compare-and-swap, because it has one
writer.

Compare-and-swap still matters where several rules write a shared *subject*. A reflected entity
is not a shared subject.

Inside one verse process, one `Git` value keeps the single writer. Across deployments, only the
install check below keeps it.

**Converge writes with no precondition** (`entity.ReplaceAt`). It uses the snapshot it has
already read to spell the removals, so the write is a replace and not a merge.

A compare-and-swap here would protect an address with a single writer against a second writer
that cannot exist. The gain is modest. Verse keeps it for the rule, not for the measurement.

## The out half: verse asks, the io does it

The sections above and below this one are about the source. This section is about the sink.
The sink runs the same reconciliation in the other direction.

1. Somebody writes what they want, at an address the io reads. It may be a person with
   `verse data`, or a charter's `derive`. What they write is an **input**: a node with a `to`
   field, which says whom or what it is about.
2. The io writes back what happened, at the sibling address `<name>~status`.

The writer of an input chooses its depth. A person types one input under the address. A
fan-out writes one input per person, wherever its keys put it.

Every few seconds a pass offers every input to the connector, with what the last pass
recorded beside it. The connector says what to record now, or nothing when the external system
already matches.

There is no queue, and nothing is delivered exactly once. A pass reads the truth and does the
difference. So a crash in the middle of a pass costs a repeat of one comparison. It does not
lose a message.

The sink differs from the source only in direction, and in what follows from direction:

| | in | out |
|---|---|---|
| who dials | verse | the io |
| what it carries | entities and covers | inputs, and a status per input |
| the law | the single writer of what it covers; covered-and-omitted is tombstoned | what is asked is compared with what was done, and the difference is done |
| failure | recorded on the registration, retried by the next pass | recorded on the status, retried by the next pass; a refusal is permanent until the input changes |
| who verse trusts | what the reflection answers | what the io says it did (it cannot see for itself) |

The addresses a sink reads must lie **inside the io's claim and outside its covers**:

- Inside the [claim](glossary.md#claim-of-an-io), so that no second writer can be asked for the
  same things.
- Outside the [covers](glossary.md#cover-of-a-reflection), so that a reflection never
  tombstones a request.

A sink also reads the [outboxes](glossary.md#outbox) that packages declare for it: places
inside a package, outside the io's claim, that the package says this io reads. It reads them as
it reads its own place, and writes each status inside the package
([reference/io.md](reference/io.md#outboxes-what-a-package-sends)).

The registration says three things: where the sink reads, whom the io acts as, and who may
ask. The install writes the grants that the registration describes. So you set up a sink in one
act, not with four `verse perms` lines and a flag
([reference/io.md](reference/io.md#the-out-half)).

Not every io has an out half. The outbound act of git is a push, which cannot be taken back.
A push belongs to a task, which verse gates per step and does once. **The rule is
reversibility**: a sink is for what is kept in step with a record and corrected by editing, and
a task is for what cannot be taken back. This rule keeps the out half from becoming an RPC
framework.

## The seam: a source answers, verse converges

```go
// A Source is NOT handed a store. It reads external truth and answers with it; converging
// the store with that answer is the Reflector's job.
type Source interface {
    Root() string
    Reflect(ctx context.Context, ref entity.Ref) (Reflection, error)
}

type Reflection struct {
    Covers   []entity.Pattern // what this answer is COMPLETE for: the tombstone authority
    Entities []entity.Entity
}
```

A source answers with a [reflection](glossary.md#reflection). Verse
[converges](glossary.md#converge) the store with it. Converging does three things:

- It **skips what is unchanged**.
- It **[tombstones](glossary.md#tombstone) what is gone**, within what the pass covered. So the
  reconcile of one repository never deletes the refs of another.
- It **never tombstones what the answer did not claim**.

None of that is knowledge about git. All of it is knowledge about the store. So verse does it
once, and a source does not do it.

`Covers` names what the answer is complete for:

- An entity under a covered pattern and absent from `Entities` is tombstoned.
- An entity in `Entities` under no covered pattern is written, and never tombstoned.

A cover is a path. Its first segment is the root of the source. Git covers `git.<repo>.ref.*`
and `git.<repo>.head`, and it does not cover `git.<repo>.commit.*`. So a commit that falls out
of the bounded window of tips is not mentioned, and it stays. The history that git keeps is a
declaration in the answer. It is not a branch in the code of verse.

A write **replaces** the entity. It does not merge into it. The answer is what the entity *is*,
so a field that the source stops reporting is gone.

The reasons are in [Why it is this way](#why-it-is-this-way).

## An io you deploy

`io/iod` serves the contract as two routes:

| Route | Answer |
|---|---|
| `GET /io` | a descriptor |
| `GET /reflect` | a `Reflection` |

`io.HTTP` is the client that verse drives an io with. `taskd` uses the same pattern, for the
same reason. `io.Git` behind `iod` *is* the sample external io. The same value behind the
reflector of verse is the built-in io. So a deployed io has no privileged local path to differ
from.

**Three ways to reach an io, and one contract.** A registration carries exactly one of these:

| Field | Who connects |
|---|---|
| `url` | verse dials |
| `dir` | the git io, in the verse process |
| `dial-in` | the io dials. It is designed and not built. |

The three differ only in where the `net.Conn` comes from. `Reflect` is the same call on all
three. The field that is present says which way it is.

A runner differs here: a runner with no url is refused. For a runner, absence already meant
`local`, so an entity with no url was ambiguous.

**Verse dials, and the hint goes the other way.** A source that is pulled is a pure function of
the external truth. It has no ticker, no backoff, no retry policy, no diff, no cursor and no
CAS. So the sample program is short enough to copy.

**A source may keep state whose loss costs work. It may never keep state whose loss costs
correctness.** Two sources keep state:

- The GitHub source keeps a high-water mark, so it can ask for what changed.
- The GitHub source keeps an ETag per endpoint, so a listing that has not changed costs
  nothing.

If either is lost, the source falls back to the full read. The full read gives the correct
answer, more slowly. A cursor over *events* is different in kind: its loss is a window of
missed events. Apply this test to anything else that a connector wants to remember.

The transport must keep one rule: **a failed reflect is an error, never an empty
Reflection.** An empty answer that carries covers is a correct instruction to tombstone
everything under them. Suppose a status that nobody read, or a body that nobody could decode,
came back as the zero value. Then a broken source would look like an external system that is
empty, and converge would believe it.

## Installing one, and the two checks

Adding a source **probes it**. A probe has three outcomes, as for a runner, and for the same
reasons:

| Outcome | Meaning | What the install does |
|---|---|---|
| *a source* | it answered, and the answer decoded | installs it |
| *not a source* | it answered, and it is not a source | refuses it |
| *unreachable* | it did not answer | installs it, with a warning |

A manifest often lands before its pod. That is the ordinary state of a source that is
deployed beside the entity that names it. So an unreachable source is installed.

Two checks have no analogue for a runner. An in-process source passes both by construction. An
external source may not.

- **The root in the descriptor must be the root that the operator named.** Suppose a `github`
  registration points at a git source. The source would write every reflection into the wrong
  address, and it would think it is right. Nothing can recover from that later.
- **Every claim must be provably disjoint from every registration that stands in the same
  scope.** Otherwise verse refuses the install, and the message shows both claims. Verse could
  not prove them apart, so the operator must decide.
  - Two sources that write different addresses in the same entities each claim a deeper
    address. Verse then proves the claims apart like any others.
  - `-allow-overlap` is for two sources that do write the same address. The cost is stated:
    two writers on one address, each tombstoning what the other reflected.

**The operator chooses the root.** A root separates ios more coarsely than a claim does, and
for a different reason. The operator draws the partition between deployed sources: by
credential, by budget, or by which addresses are busy.

- Two deployments that share `github.*` need only disjoint claims. Each source computes its
  claims from its own configuration. Nobody writes them by hand.
- A deployment that wants no coordination at all takes `-root`, and owes nobody a proof. Two
  paths whose first segments differ are disjoint.
- A second *instance* of a product gets its own root, so its addresses are not confused with
  those of the first.

The operator who chooses a separate root pays for it. A rule's path begins with a literal, so
every rule about the separate root names it. For this reason the root is a flag and not the
shape.

**An ownership claim is an address, not a payload predicate.** `owns` is an address, such as
`{at: "git.verse"}` (the address of one repository).

- A level may be `*`.
- A glob in a level is a literal prefix and a trailing `*`, and nothing else.
- Two claims are disjoint when any level is disjoint.
- A `*` level is disjoint from nothing. So `git.*.*` claims all of `git`, and verse can prove
  it apart from nothing else under that root.

A claim may name an address at any **depth**. This lets two sources share a thing, and neither
is a second writer of anything. Each source is the single writer of its own node beneath the
thing. Verse proves the two apart with the same comparison, one level further down.

The Linear source declares such a claim. A project belongs to several teams at once. So each
team claims `linear.project.*.owners.<KEY>`, and nothing at the project itself (see below).

## Removing one, and the fork

By default, removing a source tombstones what it wrote. An address that nobody reflects is an
address that nothing can correct. Without the tombstones, a charter would go on believing in a
branch of a repository that verse no longer watches.

One common case needs the other answer. **Moving a repository to another host is a remove and
an add of the same physical truth.** Tombstoning would fire every branch-deleted rule, and then
every branch-created rule. A change of bookkeeping would destroy live facts and create them
again. This is the same problem as renaming a runner.

So verse **refuses to remove a source that still has something reflected**, until you say which
case it is:

| Flag | Meaning |
|---|---|
| `-forget` | The reflection is over. Tombstone what the source wrote. |
| `-keep` | The same truth is moving. Leave the last known state. It is frozen, and it grows old. |

Removing a source that has nothing reflected needs no flag.

`owns` alone bounds what `-forget` may delete. It can tombstone only what the install proved
this source to be the single writer of.

## What verse records, and what it refuses to

Verse writes `lastReflect` and `lastError` on the registration.

| Field | When verse writes it |
|---|---|
| `lastError` | on a **transition** only. A source that fails every minute writes once. The write that clears it says that the source recovered. |
| `lastReflect` | only when a pass **wrote** something. It is the poll cursor. |

Suppose verse stamped `lastReflect` on every pass. It would write to `verse.io.*` once per
resync interval, forever. A rule that watches those addresses would fire on a verse where
nothing happens.

`lastReflect` cannot show a source that is *wedged*: alive and answering, and reflecting
nothing new. On a quiet repository a healthy source looks the same. So **verse reports when a
pass last completed, and never stores it.** `verse io` reads it live from the reflector.

A runner follows the same rule about wellness. A fact about reachability that is written down
can be stale, and stale in the direction that costs.

A clean shutdown is not a failure, and verse does not record it as one. Cancelling a reflector
fails the pass in flight. If verse wrote that failure down, every source would come back from a
clean stop carrying it. A listing after a restart would then show every source as broken, when
all are fine.

## Scoped ios

An io registers into a [scope](glossary.md#scope). The io writes its reflection into that
scope. `verse connect` is an example: it reflects a working tree into a scope for one session.
It is a scoped io, built as a special case.

The general case is the sandbox. **The branches of my checkout shadow the view that baseline has
of the same repository, for my rules, and do not touch anyone else's.**

The ownership check is per scope:

- Two sources in one scope must be provably disjoint.
- A scoped source may claim what a baseline source also claims. That is the overlay working
  as designed.

**The registration lives in the scope it reflects into.** So `DeleteScope` drops the overlay,
and takes the registration, everything reflected and the reflector with it. A scoped source has
no removal step to forget. It cannot outlive the sandbox it was for.

**A scope can hide what it does not own.** A scoped converge tombstones what the external truth
no longer has. Inside a scope, most of what it finds is inherited from baseline. The tombstone
hides the inherited entity from the reads and watches of the scope. Baseline still holds the
entity. Verse drops the tombstone with the overlay.

**Hiding works at every depth.** A scope hides an inherited node beneath an entity in the same
way. So a sandbox can say "that team does not own this project in here". Neither direction
leaks:

- The scoped write does not reach the value in baseline.
- The scoped hide does not remove the key in baseline.

Baseline stays the default. Most deployments want one shared reflection that everything reads,
and an empty `scope` gives that.

## Five of them, and what the ones after git cost

Verse ships `iod`, a second binary with one subcommand per system it connects
([reference/iod.md](reference/iod.md)): git, git-issue, Slack, Linear and GitHub. `iod` is in
the same image as `verse`, and deliberately not in the same process.

The five are not privileged. They are ordinary external sources that are maintained in this
repository. To write a sixth, copy the sample program. **The third and the fourth io add
nothing to `io/`, `versed/` or the CLI.** This tests the claim that the seam is general.

**The credential is the one difference between them.**

| Io | Credential |
|---|---|
| git | none |
| git-issue | none. It reads the same directory as git. |
| Slack, Linear, GitHub | a token |

Because git needs no credential, git can also run in the verse process (`-dir`), and its
"webhook" is a shell script of four lines. The three with a token stay on the far side of the
seam. This is what "nothing new about credentials" means in practice (see below).

Four facts hold for the three ios that need a credential.

**A delivery endpoint is the same as the git hook.** The Slack Events API endpoint, the Linear
webhook and the GitHub webhook live in the *source*. Each verifies its own signature. Each posts
what the shell one-liner posts: which address went stale, as a query parameter, and nothing
else.

So the law holds across four senders that share no code. A forged delivery still does no harm.
The signature check is there because the contracts of those systems ask for it. Correctness
does not depend on it.

**In GitHub, a hint names the thing and not its container.** A targeted pass has a size:

| Io | A targeted pass reads | Why |
|---|---|---|
| git | the whole repository, with `for-each-ref` | git is content-addressed, and a smaller target costs the same |
| Linear | a whole team | one query reads a team |
| GitHub | one issue | GitHub answers a single issue in one request |

So a hint about `#42` converges that issue alone. The cover of the answer must say so:
`github.<org>.<name>.issue.<n>`, not the live set. Otherwise one delivery would tombstone every
other open issue in the repository. The size of the hint and the size of the cover are the
same question.

**A source knows its API budget.** The resync interval of verse is a constant: one minute. The
complexity budget of Linear is per hour. The budget of Slack is per method per minute. So verse
offers `io.Throttle`. Within its window, a source answers with its last *successful* reflection,
and does not ask again.

A throttle costs the latency of a hint during a burst. A source that is rate-limited reflects
nothing at all until the limit lifts, so this is the better direction. Verse never caches a
failure. A cached failure would be `every` minutes of a source claiming that an external system
is empty.

**Where a system offers one, a conditional request is better than a clock.** GitHub offers
one: an ETag per endpoint and `If-None-Match`. The source no longer guesses that nothing changed.
GitHub tells it. GitHub does not charge for a 304. So a quiet repository costs nothing, where
the throttle could only make it cost less.

This is the content-addressing of git by another route: the external system does the
comparison. The throttle stays underneath, as the backstop for a burst.

**A cover is how a source says what it could not read.** The people in Slack need a scope that
its channels do not need. So when `users.list` fails, the source drops the cover of users *and*
the user entities. The answer is then complete for the channels, and complete for nothing else.
Verse leaves standing what the source could not read, and does not delete it.

The channels are treated differently. A pass that lost the channels has nothing true left to
say, and it fails. This asymmetry is what `Covers` is for. Without it, the job would need a
flag.

**A Linear project belongs to several teams at once**, so it has no owner by position. An
ownership that is an address at the project cannot say this. Two sources over two teams would
each tombstone what the other wrote.

The answer is a **deeper address**. A team is not a second writer of `linear.project.<id>`. It
is the single writer of `linear.project.<id>.owners.<KEY>`, a node like any other.

- The install proves two such nodes apart with the same comparison, one level further down.
- Converge writes, compares and tombstones at that node, and nowhere else.
- The last owner to leave empties `owners`, and a conditional delete removes the project.

**Sharing a thing is not an exception to single-writership; it is single-writership at a deeper
address.**

The cost is a project with no top. Every field that Linear has about a project is global. A
global field has no owner among the teams. So the node of each owner carries its own copy, and
a charter reads a project through an owner. That is the true shape of a thing that nobody owns
all of.

**GitHub has the same shape, and shares no code with Linear.** A GitHub project belongs to an
organisation or a user, and it spans repositories. So no source for one repository can claim
it as an address. This makes it a limit of the address grammar, and not a quirk of one data
model.

A GitHub *user* is a third case, and a different one. A user has exactly one owner, and that
owner is the system. So the answer is not a node under a shared thing. It is a source of its
own, at an address of its own. Under `github`, the next level is an organisation. A source of
people that claimed a level there would claim a name that GitHub could give an organisation.
Verse reflects neither kind.

### The window, and why Slack is not git

A git source carries commits under **no** cover, so history stays. The Slack source covers its
messages. So the last `-depth` messages per channel are a window that slides, and verse
tombstones what falls out.

The difference is not a preference:

- The history of a repository is finite and cannot change. A task that wants more follows a ref
  into the real repository.
- A channel is an unbounded event stream. A store that kept all of it would be a warehouse, and
  a source is not a warehouse.

Covering the messages also makes the source *more* truthful. A message deleted inside the
window goes. A message under no cover would stand forever.

**GitHub takes a third position, and it is not a compromise between those two.** A set of
issues is unbounded, like a channel. But the part of it that anybody reacts to, the *open*
part, is bounded and known. So the source covers the same kind **twice**:

```tony
covers:
- {path: "github.signadot.verse.issue.*", value: {state: open}}
- {path: "github.signadot.verse.issue.*", value: {state: closed}}
```

| Cover | What the answer holds | What goes |
|---|---|---|
| `{state: open}` | *every* open issue | an open issue that is absent |
| `state: closed` | the closed issues it read: the most recent `-settled` | a closed issue that ages out, as a Slack message does |

An issue that closes appears under the second cover with its new state. It ages, and then it
goes.

**Both states must be covered, or neither works.**

- If the source covered only the repository, and answered with the live set plus a tail, the
  first pass would tombstone every older issue.
- If the source covered only `state: open`, one husk would remain for every issue ever closed.
  That is the warehouse again, arriving slowly.

A cover cannot say "everything updated since T". The reason is in
[Why it is this way](#why-it-is-this-way).

## Git: the exemplar

A git repository fits the pattern better than the SaaS systems. It needs no credentials and no
network, and it is native to the SDLC.

- The "webhook" is a git hook, `reference-transaction`. It posts to the hint route of the
  registration with `at` set to the address of the repository. It has no signing and no
  network. The delivery says only which address went stale (`Git.InstallHook`).
- Reflection is **cursorless**, because git is content-addressed. `git for-each-ref` gives a
  `ref→sha` map. Comparing the shas with the stored entities *is* the level-triggered diff. There
  is no since-token, and no window of missed events.
- It is **multi-repo native**. A repository registers under an id, and the id is a level of
  every address (`git.<repo>.ref.<name>`, `git.<repo>.head`). A pass is per repository. So the
  reconcile of one repository never tombstones the refs of another.
- It reflects state that knows about git: branch, HEAD, dirty, ahead and behind. SDLC flows
  react to that ("commit landed on main", "branch deleted", "tag created"), and not to "a byte
  changed". It is an index, not a warehouse. A task that needs a diff follows the ref into the
  real repository.

## Control: what people do over there

A reply in a Slack thread, or a mention, is not a reflection and not a delivery. It
is a control request, and [an intervention is never a store write](control.md). So a connector
that hears one asks verse through the ordinary route, as the person who did it. It writes
nothing.

`io.Controller` gets a way to act as somebody, not a way to write. It is a client factory, with
one principal per request. Three invariants hold for every connector:

- **Which** request it is, is decided without reading prose.
- **What** it may ask for is bounded by what the record already offers.
- **Who** it is made as is the person.

An answer recorded as the io's own principal would be an answer that nobody gave.

Where the verse verifies people, the connector presents its own token and names the account the
person is, and verse admits the request as the person through that account's binding, from a
connector the deployment trusts for the provider
([a person relayed by a trusted io](reference/perms.md#a-person-relayed-by-a-trusted-io)). The
invariants hold the same way: the request is made as the person, and the record says which
connector it came through.

## A Source is not a controller, and cannot be

A `Source` looks like a `libctl.Handler` run under `RunController`, mounted at its own address.
The shape fits, until you ask what fires.

**A mounted subtree fires no rules.** Mounts render under `entity.MountRoot`, a top-level
segment disjoint from the entity root. A mount is driven by a task, so it has no history, and a
watch with history must never span one. `LogdStore.WatchSubtree` renders only under the entity
root.

So a subtree that a controller serves is readable by its ordinary path. No watch sees it: no
change events, no `Since` replay, nothing a charter can react to. A git `Source` mounted this
way would reflect a repository perfectly and fire nothing. Firing is the whole point of a
source.

Mounting under the entity root instead does not help. It re-creates the composed root watch
that silently drops `FromCommit`, and `MountRoot` exists to prevent that. See
[writing a controller](controllers.md) for both halves of that rule.

The line is clean, and `connect` sits on both sides of it: **bytes somebody asks for may be a
mount; facts something reacts to must be ordinary writes.** A `Source` is entirely the second
kind. It stays an interface of verse, with nothing underneath it.

## What a Source does not do

**No write-back through the reflection.** A reflection reads. It never writes the external
system, and it has no way to.

Acting back is the out half. It goes in a different direction and follows a different law: a
level-triggered delivery of what somebody asked for, with a status beside each input that says
what happened ([the out half](reference/io.md#the-out-half)). The addresses of the out half lie
under the io's claim and outside the covers of the reflection. So the reflection stays the single
writer of everything it reflects, and it never tombstones what the out half and its askers
write.

**No paging.** One reflect answer is one payload in memory. A source of ten thousand issues
narrows its `Covers`, and reflects one address at a time. That is the same level-triggered pass
at a finer grain. If that stops being enough, the answer is a paged reflect, not a stream.

**No authority.** Verse dials a url that a writer of the entity chose, and it accepts hints from
anyone. The install probe of a runner opens the same edge.

**Nothing new about credentials.** The events endpoint of the Slack source takes requests from
the internet. It needs an ingress and a signature check. All of that lives in the Slack source,
where the signing secret already is. Verse grows no public face for it. Verse knows nothing
about Slack, and every source uses the same hint route.

## Why it is this way

### Why a hint never writes

The law gives three properties: idempotency, self-healing, and independence from order and from
duplicates. Verse converges against the real state of the system, not against a delivery that
may fail. Translating the payload of a webhook straight into entity writes is the same mistake as
trusting `Observe` without `Since`.

Verse does not map a delivery to addresses itself. A second implementation of that mapping, one
per source, is how the law comes to be broken in one of the two places. Across a network, a
difference between the two is the hardest to recover from.

### Why converge is in verse and not in the source

The store cannot cross a process boundary, but that is not the reason. A client of the store is
easy to ship, and `POST /entity` already exists, so an external program could write `git.*`. The
reason is what converge does. It must not be written again for each source.

- Without **skip unchanged**, every periodic resync would fire every rule over the addresses of
  the source again.
- Without scoped **tombstones**, the reconcile of one repository would delete the refs of
  another.

A connector author who held this knowledge would get it wrong in a way that nothing notices,
until a charter fires two hundred times.

`Covers` states the scope of a reconcile in the answer, and not in a parameter. So the history
that git keeps is a declaration, and not a branch in the code of verse.

A write replaces and does not merge, for this reason. A merge would leave a stale field
standing. The check for unchanged compares against what the source said. So the entity would
differ on every pass, forever, and fire its rules each time.

### Why verse dials

A pushed reflection would move the reconcile loop into every source. That is the same
duplication, one level up and across a network. A pulled source has no loop of its own.

### Why a failed reflect is an error

An empty answer with covers tells verse to tombstone everything under them. Verse cannot tell
that answer from a source that failed. So the source must fail loudly.

### Why a claim is an address

The reason is where the question is asked. Converge evaluates `Covers` against stored entities,
and it can read their payloads. An install compares two *declarations*, and it has no entities in
hand. Addresses compare statically. Predicates do not.

The limit on a glob in a level makes disjointness a comparison of strings. General disjointness
of globs would need an intersection of automata.

### Why a cover cannot say "updated since T"

The obvious alternative for GitHub is to cover what the pass *read*: "everything updated since
T". It cannot be said. A cover is an `entity.Pattern`, and its predicate is a tony match.
**There is no ordering operator.** `{state: open}` can be said, and `{updatedAt: after T}`
cannot. So the split must be on a state that the payload carries. A source can take this shape
only when the external system has such a state.

### Why a scope has its own reflections

One could argue that a reflection is a fact about the outside world, so every scope should read
the same one. That argument is wrong, and `verse connect` shows why. A verse whose reflections
are all shared cannot say "in here, the repository looks like this". A sandbox exists to answer
that question. Shadowing is the feature, and for this reason the ownership check is per scope.

If a scoped delete could remove only entities local to the scope, a scoped source could not say
"that branch does not exist in this sandbox". Every scope would then have the set of baseline as
a floor.

### Why iod is a separate process

Folding a connector into the engine would put the wire format of every API, and the failure
mode of every credential, into the binary that runs the rules of every user.

### Why a stream is not the answer to paging

A stream would put ordering back into a contract whose whole point is that it has none.

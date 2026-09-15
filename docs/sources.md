# Sources

A Source reflects an external system — a git repo, GitHub, Linear — into the slice of the
entity-verse it owns (`git.*`, `github.*`, …), and is the single writer of that slice. Triggers
and agents *read* the reflected entities and react. A source only reflects: acting back on the
external system is the outbound path, not this one.

For the surface — the entity, the verbs, the routes, the contract a source serves — see
[reference/sources.md](reference/sources.md). For the five verse ships — git, git-issue, Slack,
Linear, GitHub — see [reference/sourced.md](reference/sourced.md). To build one of your own, copy
[examples/source](reference/examples/source/main.go), which is a real program that compiles
with the tree.

```sh
verse source add git.laptop http://build-host:9801
verse source add slack.workspace http://slack-source:9801
verse source
```

## The core law: a hint is a hint, reconcile is the truth

External systems offer two integration modes, and they are the same split verse already runs
internally:

> webhook : poll-reconcile :: Observe : Since

A webhook is a low-latency hint ("PR 42 changed") that may be missed, duplicated, or arrive out
of order. A poll-reconcile is the authoritative, level-triggered pass that reads external truth
and converges the owned slice. So:

> **A hint never writes entities.** It schedules a reconcile of the affected ref(s); the
> reconcile reads the source of truth and writes to converge.

That one rule buys idempotency, self-healing, and independence from order and duplicates, because
you converge against the system's real state rather than a flaky delivery. Translating a webhook
payload straight into entity writes is the same mistake as trusting `Observe` without `Since`. A
periodic full reconcile is the net that catches every missed hint.

**Verse does not parse deliveries at all.** The source owns its own — it deployed the git hook, it
holds the webhook secret — and tells verse which refs went stale. A second implementation of "map a
delivery to refs" per source is how the law comes to be broken in one of two places, and across a
network a divergence is least recoverable. It pays twice: a delivery carrying no data can cost at
most a reconcile of a slice its sender does not choose the contents of, so the hint route can stay
unauthenticated in a wide-open dogfood verse.

## Ownership = single writer = no CAS

A Source owns `github.*` and is its only writer, so within that slice there is no lost-update
problem: reflected slices need no compare-and-swap because they have one writer. CAS still matters
where several triggers write a *shared subject*, which a reflected slice is not.

In task, one `Git` value holds that. Across deployments nothing holds it but the install check
below.

**Converge spends the single-writer property rather than paying for it twice.** It writes with no
precondition (`entity.ReplaceFrom`), using the snapshot it already read to spell the removals that
keep the write a replace rather than a merge. A compare-and-swap here would defend a single-writer
slice against a concurrent writer that cannot exist.

What that is worth is modest — a pass costs about a quarter less — and the reason to keep it is the
rule rather than the measurement.

## The seam: a source answers, verse converges

```go
// A Source is NOT handed a store. It reads external truth and answers with it; converging
// the store with that answer is the Reflector's job.
type Source interface {
    System() string
    Reflect(ctx context.Context, ref entity.Ref) (Reflection, error)
}

type Reflection struct {
    Covers   []entity.Pattern // what this answer is COMPLETE for: the tombstone authority
    Entities []entity.Entity
}
```

The store cannot cross a process boundary, but that is not the reason it is on this side of the
seam. Shipping a store client is trivial and `POST /entity` already exists, so an external program
could write `git.*`. The reason is what converge *does*, which must not be re-implemented once per
source:

- **skip unchanged**, or every periodic resync re-fires every trigger over the slice;
- **tombstone what is gone**, scoped to what the pass covered, so one repo's reconcile never
  deletes another's refs;
- **never tombstone what the answer did not claim**.

None of that is knowledge about git. All of it is knowledge about the entity-verse, and a connector
author holding it will get it wrong in a way nothing notices until a charter fires two hundred
times.

`Covers` is the load-bearing field: a reconcile's scoping, said out loud rather than held in a
parameter. An entity under a covered pattern and absent from `Entities` is tombstoned; an entity in
`Entities` under no covered pattern is written and never tombstoned. Git's "keep history" is then a
declaration in the answer rather than a branch in verse's code. It covers `git.<repo>.ref.*` and
`git.<repo>.head` and does not cover `git.<repo>.commit.*`, so a commit that has fallen out of the
bounded tip window is not mentioned, and stays.

A write **replaces** rather than merges, because the answer is what the entity *is*: a field the
source stopped reporting is gone. Merging would leave the stale field standing, and since the
unchanged-check compares against what the source said, the entity would then differ on every pass
forever, firing its triggers each time.

## A source you deploy

`source/sourced` serves the contract as two routes — `GET /source` for a descriptor, `GET /reflect`
for a `Reflection` — and `source.HTTP` is the client verse drives it with. That is `taskd`'s trick,
for `taskd`'s reason: `source.Git` behind `sourced` *is* the sample external source, and the same
value behind verse's own reflector is the built-in, so there is no privileged local path for a
deployed one to differ from.

**Three ways to reach one, one contract.** A registration carries exactly one of `url` (verse
dials), `dir` (the in-process git source), or `dial-in` (the source dials — designed, not
built). They differ only in where the
`net.Conn` comes from; `Reflect` is the same call down all three. This is deliberately not the
runner's "a runner with no url is refused": there, absence already meant `local`, so a url-less
entity was ambiguous. Here the field present says which it is.

**Verse dials; the hint reverses.** A pushed reflection would move the reconcile loop into every
source, which is the duplication above, one level up and across a network. Pulled, a source is a
pure function of external truth: no ticker, no backoff, no retry policy, no diff, no cursor, no CAS.
That is what keeps the sample program short enough to copy.

**Where a source may remember something anyway**, since two of them do: **a source may keep
state whose loss costs work, never state whose loss costs correctness.** The GitHub source keeps
a high-water mark so it can ask for what changed, and an ETag per endpoint so an unchanged
listing is free — and losing either degrades to the exhaustive read, which is the correct
answer, slowly. That is categorically different from a cursor over *events*, whose loss is a
window of missed ones, and it is the test to apply to anything else a connector wants to
remember.

One rule the transport must not break: **a failed reflect is an error, never an empty
Reflection.** An empty answer carrying covers is a legitimate instruction to tombstone
everything under them, so a status nobody read or a body nobody could decode, returned as the
zero value, would spell a broken source as an emptied external system — and converge would
believe it.

## Installing one, and the two checks

Adding a source **probes it**, with the runner's three outcomes and for the runner's reasons: *a
source*, meaning it answered and decoded; *not a source*, meaning it answered and is not that,
which is refused; and *unreachable*, which is installed with a warning, because a manifest landing
before its pod is the ordinary state of a source deployed alongside the entity that names it.

Two checks have no runner analogue, and both are what an external source loses that an in-process
one has by construction:

- **The descriptor's system must be the one the operator named.** A `github` registration
  pointed at a git source is unrecoverable later: every reflection would be written into the
  wrong slice by a source that thinks it is right.
- **Ownership must be provably disjoint from every standing registration in the same scope**, or
  the install is refused, with both claims in the message: verse could not prove them apart, so the
  operator is the one who can say. Two sources that write different *places* in the same entities
  say so with a path on the claim and are then proved apart like anything else. `-allow-overlap`
  is what is left for two that genuinely write the same place, with the cost stated rather than
  discovered: two writers on one slice, each tombstoning what the other reflected.

**And the system itself is the operator's**, which is a coarser separation than a claim and exists
for a different reason. A source is deployed, and more than one will be. The partition between them
is drawn by whoever runs them — by credential, by budget, by which slices are busy — and that has
no reason to line up with how the external system is organised. Two deployments sharing `github.*`
need only disjoint claims, computed from each source's own configuration rather than written by
hand. One that wants no coordination at all takes `-system` and owes nobody a proof, since two
systems are disjoint on the first segment. It is also how a second *instance* of a product gets a
slice that is not confused with the first.

What it costs is paid by whoever chose it: a charter cannot glob a system, so every rule about a
separated slice names it. That is why it is a flag rather than the shape.

**An ownership claim is an address, not a payload predicate**, and the reason is where the
question is asked. Converge evaluates `Covers` against stored entities whose payloads it can
read; an install compares two *declarations* with nothing in hand. Addresses compare
statically; predicates do not. So `owns` is an address — `{at: "git.verse"}`, a repository's
place — with `*` allowed at a level and a level's glob restricted to a literal prefix and a
trailing `*`, which makes disjointness a string comparison where general glob disjointness would
be automata intersection. Two claims are
disjoint when any segment is; a `*` segment is disjoint from nothing, so `git.*.*` claims the
lot and can be proved apart from nothing in its own system.

A claim may also name a place **inside** an entity (`at: [owners, ENG]`), which is the one way
two sources may write one entity: each is the single writer of its own slot, proved apart by the
same comparison a segment further down. The Linear source declares one — a project belongs to
several teams at once, so each team claims `owners.<KEY>` and nothing at the top (below).

## Removing one, and the fork

Tombstoning what a source wrote is right about the common case: a slice nobody reflects any more is
a slice nothing can correct, and a charter would go on believing in a branch of a repo verse has
stopped watching. It is wrong about one that will happen. **Moving a repo to another host is a
remove and an add of the same physical truth**, and tombstoning it fires every branch-deleted
trigger in the verse and then every branch-created one behind it: a bookkeeping change destroying
and re-creating live facts, which is the runner's rename case in a different costume.

So removal with a non-empty reflected slice is **refused until told which**: `-forget`
(the reflection is over) or `-keep` (the same truth is moving; leave the last known state, now
frozen and quietly ageing). A removal with nothing reflected needs no flag, there being nothing
to be wrong about. What `-forget` may delete is bounded by `owns` and nothing else: it can only
tombstone what the install proved this source was the single writer of.

## What verse records, and what it refuses to

`lastReflect` and `lastError` go on the registration. `lastError` is written on **transition**,
so a source failing every minute writes once and the write that clears it is the one that says
it recovered. `lastReflect` is written only when a pass actually **wrote** something — it is
the poll cursor, and stamping it every pass would put a write on `verse.sources.*` once per
resync interval forever, so anything watching that slice would fire on a verse where nothing is
happening.

That cursor cannot catch a source that has *wedged* — alive, answering, reflecting nothing new —
because on a quiet repo a healthy source looks identical. So **when a pass last completed is
reported and never stored**, read live from the reflector on `verse source`. That is the runner's
rule about opinions of wellness: a reachability fact that is written down can be stale in the
direction that costs.

A clean shutdown is not a failure and is not recorded as one. Cancelling a reflector fails the
pass in flight; written down, every source would come back from a clean stop carrying it, and a
listing after a restart would show a verse full of broken sources that are all fine.

## Scoped sources

A source registers into a scope. The obvious-sounding alternative — a reflection is a fact about
the outside world, so every scope should read the same one — is wrong, and `verse connect` refutes
it: it reflects a working tree into a per-session scope, which is a scoped source built as a
special case. The general form is the sandbox: **my checkout's branches shadow baseline's view of
the same repo, for my triggers, without touching anyone else's.** A verse whose reflections are all
shared has no way to say "in here, the repo looks like this", which is the question a sandbox
exists to answer.

Shadowing is the feature, which is why the ownership check is scoped: two sources in one scope
must still be provably disjoint, while a scoped source claiming what a baseline source also
claims is the overlay doing its job. **The registration lives in the scope it reflects into**,
so `DeleteScope` drops the overlay and takes the registration, everything reflected, and the
reflector with it — a scoped source has no deregistration path to forget and cannot outlive the
sandbox it was for.

**A scope can hide what it does not own.** A scoped converge tombstones what external truth no
longer has, and inside a scope most of what it finds standing is inherited from baseline. If a
scoped delete could only remove scope-local entities, a scoped source could not say "that branch
does not exist in this sandbox", and every scope would be stuck with baseline's set as a floor. It
can: the tombstone hides the inherited entity from the scope's reads and its watches, leaves
baseline holding it, and is dropped with the overlay.

**And it holds one level down.** A scope hides an inherited slot the same way, so a sandbox can say
"that team does not own this project in here", with neither direction leaking: the scoped write
does not reach baseline's value, and the scoped hide does not remove baseline's key.

Baseline stays the default: one shared reflection everything reads is what most of a deployment
wants, and an empty `scope` is what it gets.

## Five of them, and what the ones after git cost

Verse ships `sourced`, a second binary with a subcommand per system it connects
([reference/sourced.md](reference/sourced.md)): git, git-issue, Slack, Linear, GitHub. It rides in
the same image as `verse` and is deliberately not in the same process, because folding a connector
into the engine would put every API's wire format and every credential's failure mode in the binary
that runs everybody's triggers. There is nothing privileged about the five. They are ordinary
external sources that happen to be maintained here, and the sample program is the thing to copy for
a sixth. **The third and the fourth needed nothing new in `source/`, `versed/` or the CLI**, which
is the seam's claim, tested twice.

**Only one thing differs between them, and it is the credential.** git needs none, nor does
git-issue, which reads the same directory, so git can also run in task (`-dir`) and its "webhook"
is a four-line shell script. The three that reach a service hold a token, which is what puts them
on the far side of the seam for good, and what "nothing new about credentials" below means in
practice.

Four things came out of building the three that need one.

**A delivery endpoint is the git hook in another costume.** The Slack Events API endpoint, the
Linear webhook and the GitHub one live in the *source*, verify their own signature, and post the
same thing the shell one-liner posts: which ref went stale, as a query parameter, and nothing
else. That is the law holding across four senders that share no code — and the reason a forged
delivery is uninteresting is unchanged, so the signature check is there because their contracts
ask for it, not because correctness rests on it.

GitHub is where **a hint names the thing rather than its container**, which the first three had
no reason to. git's targeted pass is a whole-repo `for-each-ref` because git is
content-addressed and a finer target costs the same; Linear's is a whole team because that is
what one query reads. GitHub answers a single issue in one request, so a hint about `#42`
converges that issue alone — and the cover it answers with has to say so, `{repo, number}`
rather than the live set, or one delivery would tombstone every other open issue in the
repository. The arity of the hint and the arity of the cover are the same question.

**A source is where an API budget is known.** verse's resync interval is a constant (one
minute); Linear's complexity budget is hourly and Slack's is per-method-per-minute. Making the
interval a registration field would move that knowledge to the side that does not have it, so
the answer is `source.Throttle`: a window inside which a source answers with its last
*successful* reflection rather than asking again. It costs a hint's latency in a burst, which is
the right direction — a source that has been rate-limited reflects nothing at all until the
limit lifts. A failure is never cached, for the reason everything here turns on: a cached
failure is `every` minutes of a source claiming an external system is empty.

GitHub adds the better answer where a system offers one: **a conditional request beats a clock**.
An ETag per endpoint and `If-None-Match` turns "guess that nothing changed" into "be told", and
GitHub does not charge for a 304 — so a quiet repository costs nothing where the throttle could
only ever have costed less. It is git's content-addressing arriving by another route, with the
external system doing the comparison, and the throttle stays underneath it as the backstop for
the burst.

**A cover is how a source says what it could not read.** Slack's people need a scope its
channels do not, so a `users.list` that fails drops the user cover *and* the user entities: the
answer is then complete for the channels and complete for nothing else, and what could not be
read is left standing rather than deleted. The channels get no such treatment — a pass that lost
them has nothing true left to say, and fails. That asymmetry is the whole of what `Covers` is
for, doing a job that would otherwise have to be a flag.

And one thing that did not survive contact, and then did: **a Linear project belongs to several
teams at once**, so it has no positional owner. Ownership-as-an-address could not spell it, and
two sources over two teams would each tombstone what the other wrote — so for a while the source
reflected no project at all, and that absence was the first real evidence against the whole
grammar.

The way out was not a different vocabulary but a **deeper address**. A claim may name a path
inside a payload, so a team is not a co-writer of `linear.project.<id>` — it is the single writer
of that entity's `owners.<KEY>`. Two of those are proved apart at install by the comparison one
segment further down, converge writes, compares and tombstones there and nowhere else, and the
last owner out takes the entity with a conditional delete. **Sharing an entity is not an
exception to single-writership; it is single-writership at a deeper address.**

What it costs is a project with no top: every field Linear has about a project is global, and a
global field has no owner among the teams, so each slot carries its own copy and a charter reads
a project through an owner. That is the honest shape of an entity nobody owns the whole of.

**And the shape arrived again, in a system sharing no code with Linear.** A GitHub project is
org- or user-owned and spans repositories, so no per-repo source can claim it as an address
either — which is what makes this a limit of the address grammar rather than a quirk of one data
model. A GitHub *user* is a third variant and not the same one: it has exactly one owner and that
owner is the system, so the way out is not a slot but a source of its own, in a system of its
own: under `github.` the level after the system is an organisation, and a people source claiming
a level there would be claiming a name GitHub could give an organisation. Neither kind is
reflected today.

### The window, and why Slack is not git

A git source carries commits under **no** cover, so history stays. The Slack source covers its
messages, so the last `-depth` per channel is a window that slides and what falls out is
tombstoned. The difference is not a preference: a repository's history is finite and immutable,
and a task that wants more follows a ref into the real repo — while a channel is an unbounded
event stream, and an entity-verse that keeps all of it is exactly the warehouse a source is not
supposed to be. Covering it also makes the source *more* truthful, not less: a message deleted
inside the window goes, where an uncovered one would stand forever.

**GitHub is a third position, and it is not a compromise between those two.** An issue set is
unbounded like a channel, but the part of it anybody reacts to — the *open* part — is bounded and
knowable. So the same kind is covered **twice**:

```tony
covers:
- {path: [github, signadot, verse, issue, "*"], value: {state: open}}
- {path: [github, signadot, verse, issue, "*"], value: {state: closed}}
```

The first says the answer holds *every* open issue, so one that has gone absent is gone. The
second says it holds the closed ones it read — the most recent `-settled` — so an aged-out one is
tombstoned as a Slack message is. An issue that closes appears in the settled half carrying its
new state, ages, and leaves. **Both halves must be covered or neither works**: covering only the
repository and answering with the live set plus a tail would tombstone the entire back catalogue
on the first pass, while covering only `state: open` leaves a husk per issue ever closed, which is
the warehouse arriving slowly rather than at once.

The obvious alternative — cover what was *read*, which is "everything updated since T" — is not
expressible, and it is worth knowing why before reaching for it. A cover is an `entity.Pattern`,
whose predicate is a tony match, and **there is no ordering operator**: `{state: open}` is
sayable and `{updatedAt: after T}` is not. So the split has to be on a state the payload carries,
which is what makes this a shape a source can only take when the external system has one.

## Git: the exemplar

A git repo fits the pattern more cleanly than the SaaS ones, needs no credentials or network,
and is SDLC-native.

- The "webhook" is a git hook — `reference-transaction` locally, `post-receive` on a bare repo
  — curling the registration's hint route with the ref that names the repo. No signing, no
  network, and nothing in the delivery but which slice went stale (`Git.InstallHook`).
- Reflection is **cursorless**, because git is content-addressed: `git for-each-ref` yields a
  `ref→sha` map, and comparing shas against the stored entities *is* the level-triggered diff —
  no since-token, no missed-event window.
- It is **multi-repo native**: a repo is registered under an id, the id is a level of every
  address (`git.<repo>.ref.<name>`, `git.<repo>.head`), and a pass is per-repo, so one repo's
  reconcile never tombstones another's refs.
- It reflects git-*aware* state — branch, HEAD, dirty, ahead/behind — which is what SDLC flows
  react to ("commit landed on main", "branch deleted", "tag created"), not "a byte changed". An
  index, not a warehouse: a task needing a diff follows the ref into the real repo.

## A Source is not a controller, and cannot be

A `Source` looks like a `libctl.Handler` run under `RunController`, mounted at its system's
subtree, and the shape really does fit — right up until you ask what fires.

**A mounted slice fires no triggers.** Mounts render under `entity.MountRoot`, a top-level
segment disjoint from the entity root, because a mount is task-driven and so has no history,
and a history-bearing watch must never span one. `LogdStore.WatchSubtree` renders only under the
entity root. So a controller-served slice is readable by its ordinary ref and invisible to every
watch there is: no change events, no `Since` replay, nothing a charter can react to. A git
Source mounted this way would reflect a repo perfectly and fire nothing, which is the entire
point of it. Mounting under the entity root instead is not a trade — it re-creates the composed
root watch that silently drops `FromCommit`, which is what `MountRoot` exists to prevent. See
[writing a controller](controllers.md) for both halves of that rule.

The line it draws is clean, and `connect` sits on both sides of it: **bytes somebody asks for
may be a mount; facts something reacts to must be ordinary writes.** A Source is entirely the
second kind, and stays a verse-level interface with nothing underneath it.

## What a Source does not do

**No write-back.** A source reflects; acting on the external system stays the outbound executor
path.

**No paging.** One reflect answer is one payload in memory. A source of ten thousand issues
narrows its `Covers` and reflects a slice at a time, which is the same level-triggered pass at a
finer grain. If that stops being enough the answer is a paged reflect, not a stream — a stream
would put ordering back into a contract whose whole point is that it has none.

**No authority.** verse dials a url an entity-writer chose and accepts hints from anyone, which
is the same edge the runner install probe already opens.

**Nothing new about credentials.** The Slack source's events endpoint arrives from the internet
and needs an ingress and a signature check, and all of that lives in the Slack source, where the
signing secret already has to be, rather than in a public face verse has to grow. Verse knows
nothing about Slack, and the hint route is the same one every source uses.

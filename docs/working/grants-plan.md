# Grants and their enforcement: implementation plan

Status: plan, nothing built. The design is the ownership model in git-issue
`yezdk01bh12kshx9m9n0` ("verse users", §ownership: the model), whose scope items 1–3 landed on
`main` at `dad479ba`: a principal on every write, the owner record at creation in the perms
mirror, and a wide-open default in code. This is how its follow-up lands — writes first, then
reads, in that order because a read check filters every watch and a write check refuses one
call. Read the issue first; this file does not restate its reasoning, only the order of work,
what each step decides, and what it must prove.

The shape being built, in one line: a `grants` field on the perms record that already exists,
one check in the store view every write already goes through, and the same check asked of
reads — per request at the api, and per rule in the engine.

## Where the check stands

Everything a request or a firing writes goes through `entity.As` (`entity/attribution.go`): a
request's store is `scopedStore(r)`, a firing's is the engine's causal store stamping the rule's
address on top of the server's system view, and a source's is `As(store, ref.String())`. The
view already resolves an address-shaped principal to the owner it confers, walks a node's
prefixes for the nearest owner record, and caches both. So the check is one more thing that
view does in `Write`, ahead of the commit, and no call site learns of it. That is the property
the whole plan protects: **a write path that bypasses the view is a write nothing checks**, and
today there is exactly one — `noteUser` writes through `s.Store` as the system, which is fine
because the system co-owns everything, and stays fine only as long as nothing else joins it.

The check itself, for a write at `ref` by principal `p`, resolved to the owner it confers `o`:

1. walk up from `ref` reading owner records; if `o` is the owner on any record at or above
   `ref`, or the root's (the system), `o` is a co-owner: allowed, at level `owner`.
2. otherwise the grants that can apply are those on the **nearest** record at or above `ref`
   and no other — that is what "a grant does not cross an owner record" means operationally,
   and it is what keeps the check one record-chain walk with no join. A grant there whose `at`
   covers `ref` and whose level admits the write, held by `o` or by `*`, allows it.
3. otherwise refused.

Creation follows from the level found: `owner` records `o` where it differs from the owner
above (the rule the mirror already keeps); `read-write` records nothing, so the node reads as
its parent's. The "wide-open default" that `record` currently spells in code ("the requester
holds `owner` everywhere") becomes step 4's seed data, and the comment goes with it.

## Order of work

Eleven steps in three phases. Each leaves the tree green under
`go test $(go list ./... | grep -v /scratchpad)` and `./smoke.sh`. Steps 1–6 build the write
check and run it in shadow; 7 turns it on; 8 is what enforcement makes necessary; 9–11 are
reads. The dependency order is real: 5 needs 2's refusal to prove its negative, 6 needs 5's
runner principal, and 10 needs 9's reader.

### Phase A — writes

#### 1. Grants as data, and the question `Holds`

A `grants` field on `verse.perms.node.<address>`, beside `owner`, spelled as the issue does:

```tony
grants:
- at: [verse, task, instance, *]
  owner: ["*"]
- at: [spaces, walter@example.com, shared, *]
  read-write: ["*"]
```

Decisions this step makes, each of which the tests pin:

- **`at` is a path with at most a trailing `*`**, the claim grammar (`source/own.go`), and a `*`
  anywhere else is refused for the reason a claim refuses it. Without the star the grant covers
  the node itself and everything beneath, which is what a runner's grant on its task needs;
  with it, the children and beneath, which is what a creation grant needs. Lift the trailing-star
  rule out of `ParseClaim` rather than copying it; a claim additionally refuses verse's half,
  which a grant must not.
- **A grantee is a principal string, `*`, or a principal kind** — `user`, `runner`, `agent` —
  meaning every principal listed under `verse.principal.<kind>.*`. The kind is needed once, in
  step 6, and it is the whole difference between "anybody may answer an unbound question" and
  "any *person* may": `*` includes the runner, and a runner answering its own question is the
  one thing the gate model forbids.
- **Levels are `read`, `read-write`, `owner`, `control`**, and they nest in that order for the
  first three: `owner` admits what `read-write` does, `read-write` admits `read`. `control` is
  beside them, not above: it is run, cancel, pause, resume, and step 8 is where it is read.
- **The root has no record.** The system owns it and holds out no grants of its own there; the
  seed (step 4) hangs its grants on the kind-rooted nodes, which do have records. This avoids
  inventing an address for a node that has none.

`entity.Holds(store, principal, level, ref) (bool, error)` is the function, on the attributed
view so it shares the caches. The error is a store that did not answer, which is not a `false`:
a check that fails closed on an unreadable mirror refuses every write while the store is down,
and one that fails open admits everything; neither is an answer, and step 2 decides what the
write path does with it.

*Proves:* the issue's own examples, as a table. A user creating under `verse.task.instance`
holds `owner` there by the seed grant and nowhere else; a read-write grantee's creation under
`shared` holds `read-write` and not `owner`; a grant on a task's record does **not** reach a
question that carries its own record (the "does not cross" case); a grant held by a kind
matches a listed principal of that kind and not an unlisted string; an address-shaped principal
is checked as the owner it resolves to, so a firing of scott's charter holds what scott holds.
And the negative that matters most: with no grant and no co-ownership, `Holds` is false, so the
default answer is refuse.

#### 2. The check in `Write`, in shadow

`attributed.Write` asks `Holds` before committing. A refusal is a new error, `entity.ErrForbidden`,
distinct from a precondition that did not hold — the world saying no is a normal answer and this
is not the world, it is policy — and the api maps it to 403 with the level it wanted and the
nearest record it found, so a refused `verse d -put` says what would have admitted it.

**Shadow first.** Until step 7 a refusal is counted and logged and the write proceeds. The
counter is on `/status` beside the fired total, keyed by the level refused, and the log line
names principal, path and the record consulted. Staging is the oracle for "nothing existing is
refused" and there is no other: the test suite exercises the flows somebody wrote a test for,
and the drive on staging exercises the ones somebody uses. The counter is the measurement the
flip in step 7 reads, and it is removed there; it is a rollout instrument, not a mode.

An unreadable mirror (`Holds` errored) is logged as its own count and the write proceeds in
shadow. What it does once enforced is decided in step 7 against what shadow saw.

**The cache and the mirror.** The attributed view caches owner records and never learns of a
record written elsewhere; today that costs one refused owner write per node, which the design
accepted. A grant is different: a revoked grant cached for the life of the process is a check
that admits what policy withdrew. So the view watches `verse.perms` (a subtree watch, the same
call the engine's mirror reaper uses) and invalidates the record it names on every commit.
One versed runs against one store today, so the watch mostly sees the process's own writes; it
is there for the replica that does not exist yet and for the `verse d -put` on the mirror
nobody should make and somebody will.

*Proves:* every refusal the table in step 1 expects surfaces as a count and a log line while
the suite is byte-for-byte unchanged in what it writes. The error is distinguishable from
`committed == false` at every call site that inspects one. A grant revoked by a write on the
mirror is not honoured on the next check.

#### 3. Owner at creation, by level

`record` currently makes the requester the owner unconditionally and comments that this is the
wide-open default. It becomes: the owner where the write held `owner`, nothing where it held
`read-write`, and the write does not reach here where it held neither. The three-way rule from
the issue, in the one place creation is decided, reading the level `Holds` found rather than
asking twice.

*Proves:* `entity/attribution_test.go` rewritten against the seed rather than the default — the
same assertions about who owns a task, a rule and a reflected ref, now because a grant said so.
A read-write grantee's creation under `shared` records nothing and reads as scott's.

#### 4. The seed: what the system grants

The grants that make today's flows hold, written by versed at start under a `Created()`
precondition so a restart writes nothing and an operator's edit stands. In code, as one
function with one table, because it is the deployment's policy and there is one deployment;
a file is the shape it takes when there are two.

| at | level | to | why |
|---|---|---|---|
| `verse.task.instance.*`, `verse.task.run.*`, `verse.task.claim.*` | owner | `*` | a run is its runner's request's, and a claim its sequence's |
| `verse.trigger.rule.*`, `verse.trigger.strata.*`, `verse.trigger.package.*` | owner | `*` | anyone installs a charter, until policy edits are gated (deferred, below) |
| `verse.sources.*`, `verse.runners.*`, `verse.resolvers.*` | owner | `*` | installs |
| every world-half system | owner | `*` | a source installed by scott creates `gh.pr.42` as scott; a raw put creates `demo.pr.1` as its requester |
| `verse.gate.question.*` | read-write | `user`, `agent` | the unbound default: a person answers what nobody was bound to (step 6 narrows this) |

The world-half row is the awkward one: the world half's systems are not enumerable up front
(a source may reflect a new one), so the grant wants to hang on the root, and the root has no
record; a node record is keyed by a node's address, and the root has none to key by. So that
one grant is implicit: **the world half is `owner` for `*` until a record says otherwise**,
stated in `Holds` as the base case beside "the root is the system's", and documented as the one
grant that is not data. It is exactly the open world the issue describes and the one thing a
private verse would change first; naming it in code keeps it one line to find.

*Proves:* the full suite and smoke green with shadow reporting **zero** refusals — this is the
step where the counter first means something, and a non-zero count here is a flow the table
missed, not a bug in the flow.

#### 5. The runner grant, and the mirror writing as the runner

Two halves of one fact, done together because the second is what makes the first checkable.

A spawn onto a runner writes `read-write` and `control` for the runner on the task's own record,
`at` the task's address without a star, as the owner's act: written through the spawn's store,
which is the owner's view (`spawnerFor` and `spawnTask` both already act as the party the task
is owned by). Nothing else changes in the spawn.

The mirror then writes a brokered task's reports as the claimant. The broker knows who claimed
(`claimBy`, from the claim body's `identity`) and tells the mirror nothing; the `EvStatus{running}`
frame a first claim emits gains `By: identity`, and `Reflect` derives its store `As(store, by)`
from that frame onward. The claim path notes the identity under `verse.principal.runner.<name>`
the way `noteUser` notes a person, which is what lets the `runner` kind in a grant mean
something. A local task's mirror stays the system's, there being no runner behind it.

*Proves:* a brokered task's reports land under enforcement (the grant admits them); a report
by a different identity on the same task is refused; a runner's raw put on the question its
task raised is refused — the grant is on the task's record, the question carries its own, and
the walk does not cross. And the negative that the issue calls load-bearing: with the mirror
still writing as the system, the last of those passes for the wrong reason, so the test asserts
the principal on the write, not only the outcome.

#### 6. Questions: co-owned through their origin, answered by grant

The issue decides that a question is system-owned, lives under its origin so its co-owners fall
out of the tree, and is otherwise answered by grant, a resolver binding being that grant. Two
of those three are built here as written; the middle one is built differently, and this is the
one place the plan departs from the body, so the reasons are laid out.

**"Under the origin" as an address is the expensive spelling.** Records in verse's half stand
four deep, so a question under `verse.task.instance.<id>` stands at five or six and is a field
of the task entity to every depth-four reader: `verse ps`, `task get`, a charter matching
`task.instance.*` would all carry the questions inside the payload. The docket and the
readiness probe read one subtree (`pendingQuestionsErr`, at `verse.gate.question` at one depth)
and would become a cross-depth query, which `..` is refused for and nothing else spells. A
charter gate's origin is a rule record, and the same nesting puts a question inside the rule's
definition. That is the residual's open question (`gate-unify-residual.md` §2) arriving with a
cost attached.

**So the edge is computed, in one place.** The record keeps its address and already names its
origin (`task`, or `at`+`subject` for a firing's gate). `Holds`, asked about a question, walks
the origin's address as well as the question's own: the task's owner and the rule's owner are
co-owners, exactly as the tree would have said, and no reader of the address moves. The perms
mirror records nothing new; the origin is on the question. If the address ever moves, the edge
is deleted and the walk is the plain one — the computed edge is the cheaper spelling of the
same fact, not a different fact.

Then the grant: **`BindResolver` writes `read-write` for the binding's resolver principal on the
question's record**, at the question's address. A binding is a node with an owner like a rule,
so the grantee is the binding's address and resolves to whoever installed it; an exclusive
binding changes nothing here — `exclusive` decides whose answer *counts*, and its own comment
says it is not a security boundary. A worker-raised gate on a brokered task binds nobody and is
answered by the seed's `user` grant, which is the unbound default the docket already describes.
The seed row for questions narrows to that: `read-write` to `user` and `agent` at
`verse.gate.question.*`, and a private verse deletes the row.

*Proves:* the task's owner answers a question its task raised; the charter's owner answers a
question its rule posed; the bound resolver answers; a stranger is refused; the runner is
refused (step 5's case, now positive on the other side). Ballots on a threshold question land
for each grantee. `escalate` and `reown` — which rewrite `resolver` on the record — leave the
grant coherent: the new binding is granted, the old one's grant removed, in the same write.

#### 7. The flip

Enforced: a refusal refuses. The shadow counter is removed rather than kept as a mode — a
verse that can be configured open is a verse whose refusals mean nothing, and the seed is how
an open verse is spelled. An unreadable mirror **refuses**, with the store's error: the write
path already fails closed on an unreadable precondition, and this is one.

Staging first, by the drive: shadow reporting zero refusals over the flows the signadot charters
run, then this ships. `contract` tier: `Breaks: writes — a request that names nobody, or names
a principal holding nothing at the path, is refused with 403; set user: in config`. The notice
is the follow-up issue this plan is filed under.

*Proves:* the shadow table's refusals are now 403s with the message from step 2; the suite is
green because steps 3–6 made it so, not because a test was widened.

#### 8. What refusal makes necessary: the system, and the operator agent

Two things the issue lists that are harmless until step 7 and holes after it.

**The system principal is not self-assertable.** `Verse-Principal: system` over the api is
refused at the middleware, since a header saying "system" is precisely the assertion that was
fine while nothing was refused. What installs a system-owned charter is versed itself, from a
package path in its own config (the deployment's, beside `--data`), applied at start as the
system the way the baseline charter is restored. A person who needs a system charter changed
changes the file the deployment ships.

**The operator agent is its own principal**, and today it is granted what a user is. `agent:`
in the CLI's config beside `user:`; when set, the CLI sends it as the principal and the server
notes it under `verse.principal.agent.<name>`. The seed's `user` rows carry `agent` beside
them, so nothing an agent does today is refused, and there is a name on the record where its
grants narrow later. The `control` level is read here too: `task cancel|pause|resume|say`
check `control` on the task, held by the owner and by the spawn's runner grant, which is what
`by` on those calls was standing in for.

*Proves:* a request as `system` is refused; the system charter installs at start and its rules
fire as the system; an agent's task is owned by the agent and listed under the agent kind; a
control call by a stranger is refused, and by the owner or the runner admitted.

### Phase B — reads

#### 9. The read check at the api

`Holds(read)`: a co-owner, or a grant at `read` or above. `Get` refused is a 403, not a 404 —
the store's own doc is emphatic that not-found must not lie, and "you may not" is a different
fact from "there is nothing". `List`, `ListAt`, `State` and `Watch` **filter**: what a principal
cannot read is not in the answer, and a watch on a slice they cannot read at all is refused the
way a watch on an absent path is, so the person who typed it is told. The catch-up half of a
watch (`Since`) filters the same way as the live half, or a replay shows what a stream then
hides.

The seed grants `read` for `*` at the same rows as step 4, and the perms mirror is deliberately
not among them: it is owned by the system with no grant, so under the plain rule only the system
reads it, which is what the issue wants and nothing special-cases. `verse d verse perms` by a
person is therefore refused after this step; an operator who needs to read policy reads it as
the system charter does, or the seed grows a `read` row for `user` and the issue's argument for
gating policy edits gets a first reader to argue with.

*Proves:* the filters agree — the same principal sees the same set through `List`, `Watch` live,
`Watch` catch-up and `State`; a `Get` on an unreadable node is 403; the mirror is unreadable by
a user and readable by the system.

#### 10. A charter reads as its owner

The engine evaluates every rule through its own store: `e.store.Get` in `select.go`, `deref.go`,
`seq.go`, `flowtree.go`, and the dispatch delivers every commit to every trigger. Two changes:

- the reads a rule's evaluation makes go through a per-trigger reader, `As(e.store, e.Principal(t.Name))`
  with the read check on — one constructor, cached per trigger, invalidated when the rule is
  reinstalled since its owner may have changed;
- `dispatch` filters a commit's changed refs per trigger before `evaluate`, so a rule is not
  woken by, and cannot latch on, a change it may not read. The per-commit skip (`touches`) is
  asked after the filter, or a rule whose only readable change was filtered out still evaluates.

The cost is an owner walk per changed ref per trigger, against the cache; step 2's watch keeps
the cache honest. Measure it on the trigger benchmarks before and after, and if it shows, the
answer is a per-commit memo of ref→owner shared across triggers, not a weaker check.

The perms mirror falls out: a system charter watching `verse.perms` and `verse.trigger.rule`
sees policy change and can pose the ask the issue describes; `perms` joins `trigger:*` and
`task:*` in the list of what a subject must name to see (`trigger-model.md`), and a charter
owned by anyone else naming it matches nothing, which is correct and should be said in the
install-time check rather than discovered as silence.

*Proves:* two users, two charters, one private entity: the owner's charter fires and the other's
does not, and the other's latch is not written. The system charter sees a grant widen. A rule
naming `perms` in its subject and owned by a user is refused at install with a message saying
why, since a rule that can never match is the failure the install check exists to name.

#### 11. Land it

Delete this plan; write what runs. The issue body's model section becomes a doc — `docs/perms.md`,
the subject with an argument — and `reference/entities.md`'s two paragraphs on principals and
the mirror grow the grants field and the levels. `trigger-model.md` gains `perms` in the opt-in
list, `runners.md` says a spawn grants its runner, `reference/tasks.md` says who may answer.
The residual goes to `docs/working/grants-residual.md` and carries at least the deferred list
below; `gate-unify-residual.md` §2 is rewritten to say the edge is computed and why.

## What this plan deliberately does not do

- **Policy edits as gated changes.** `verse c -a` writes a rule directly, and a grant is written
  by whoever holds `owner` at the node. The issue's shape — the edit lands as a proposal under
  the installer's own node and a system charter promotes it — is a phase of its own, claimed
  after step 10 exists to watch the mirror. Until then, the system charter *sees* a widening and
  poses a question about it after the fact, which is an audit and not a gate.
- **Evidence from outside the agent, checked at install.** Decidable once owners are on every
  node and the runner's grant is confined, which steps 4–5 make true; the check itself is a
  charter-install rule and belongs with the one above.
- **Scopes.** A `verse connect` scope overlays the baseline store; a grant written in a scope is
  visible in the scope and nowhere else, which is the right default and is not designed here.
  `DeleteScope` drops its records with the rest.
- **Authentication.** The principal stays self-asserted; step 8 refuses one string, not forgery.
  The task's `Grant`/`Vector` axis (`roadmap/authority.md`) is what a task may *do* and is
  untouched; this is who may write where.
- **The durable record.** The principal reaches the log as the commit's author (go-tony
  v0.0.213), which is what the check reads; the mirror is verse's and stays so.

## Risks worth watching

**The walk on hot paths.** `reconcileTasks` and `ps` already pay for reads at the root; a read
check per entity on a listing multiplies that by an owner walk. The cache answers most of it,
but step 9 should be measured against `verse ps` on staging's ~2,200-record mirror before it
ships, and the read filter on a listing should walk the mirror once per distinct prefix, not
once per entity.

**Staging's transition records.** ~90 entities created by pre-ship rules are recorded as owned
by the rule's *address*, and entities that predate the ship are owned by whoever first touched
them, mostly the system. Under enforcement the first set is owned by a string that is no party:
nobody but the system co-owns them, and a user's charter cannot write them. Re-applying the
charters does not fix records already written. Decide before step 7 whether staging is wiped
(git-issue `hd63vzmfh12krqkhm1n0` already says a pre-rebuild store must be) or the ~90 records
are rewritten by hand as the system; do not build a migration for one deployment.

**A grant that admits by `*` and a kind that lists nobody.** `runner` matches principals under
`verse.principal.runner.*`, which is populated by claims. A test that never claims has an empty
kind and a grant to `runner` that admits nothing, which is correct and will read as a bug once.

**Shadow that lies.** A refusal counted in shadow proceeds, so a creation that would have been
refused still records an owner, and the record then admits the next write for real. The count
is right and the mirror is slightly wrong for the life of shadow; acceptable on staging, and one
more reason shadow ends at step 7 rather than staying available.

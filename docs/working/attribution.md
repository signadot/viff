# Attribution: a parameter at install, an argument at launch

Status: proposal, largely overtaken. What it argued for is built another way and described in
[perms.md](../perms.md): every entity write carries a principal, on the write and on the commit;
a runner carries an `identity` on its entity and acts as it, a brokered worker as the identity
on its claim; a charter and a source act as their record's address, resolved to their owner.
What is still open here is the shape of the argument — an identity as a *parameter* at install
verified against what the runner reports — since nothing verifies one yet.

What runs: a spawn carries `Provenance{Principal, Subject, Chain}` to the runner over the
task-control contract, `verse task run --as` is the run's principal rather than only the identity
for a gate answered while attached, and the [coding
runner](../reference/examples/README.md#running-the-coderunner) writes all of it into the commit it
pushes — which is the case this note names below as *where effects leave verse*, now with a
referent. `source/git.go` parses trailers back off a commit, so the loop closes.

What does not: everything below about an identity being a **parameter at install** and an
**argument at launch**. The runner is told who a launch is for and is trusted to say so; nothing
verifies it, and no installed thing carries an identity of its own.

It came out of designing [a coding runner](coding-runner-residual.md) and [a runner
descriptor](runner-descriptor.md) — the point of that exercise being to find out how attribution
threads through a system once there are real doers in it, since that is the layer
[authority](../roadmap/authority.md) has to stand on.

It concerns *who is responsible*, not *what may be done*. The second is `Grant` and it is
authority's subject; this note must not be read as producing one. See [the guards](#two-guards).

## What is on record today

Verse records **provenance** thoroughly and **identity** almost not at all, and the two are easy
to mistake for each other.

| | what it carries | what it is worth |
|---|---|---|
| `launch.Spec.Subject` / `.Chain` / `.Doer` | the flow, the kpath of the action inside its trigger, which provider won | complete structural provenance; **no identity** |
| `task:instance` | the runner name *and* url stamped at spawn | survives the runner entity being removed; **no identity** |
| control calls — `resolve`, `say`, `cancel`, `pause`, `vector` | `by` | **self-asserted, independently, at every call site** |
| `claim` | `by`, `identity`, `at` | the one place the wire already distinguishes an identity from a `by` |
| **entity writes** | `Write{At, Value, When}` | **no author field at all** |

The last row is the one that matters most and is the least visible. Every install — a charter, a
runner, a resolver binding, an environment — is an entity write, and an entity write records what
changed and where and nothing about who. The only identity reaching the substrate is a
process-level `ClientID: "versed"` on every write the server makes, which is true of all of them
and therefore distinguishes none of them.

[questions.md](questions.md) already states both halves: *"`--as` is just self-reported, and entity
writes don't record an author at all."* [attached-resolvers-residual.md](attached-resolvers-residual.md)
records the consequence at the sharp end, and declines to test it: *"Verifying that a caller is who
it says is authority's whole subject, and anything local would be a lock with the key taped to it
… there is no test for it in the tree, deliberately: a test would assert the hole stays open."*

## The criterion

The runner design carries several names verse deliberately does not interpret — a driver, a
runner url, an isolation profile, an environment. That looks like an attribution problem and
mostly is not, because of this:

> **An opaque name is safe for attribution exactly when a binding act is on record.**

An operator reads what `hardened` means on a runner and anchors an environment to it. Verse never
learns what the word means, but if it later means less than they thought, responsibility traces
to the anchoring act and to the descriptor as published. Nobody is unaccountable and nothing had
to be interpreted.

Which locates the actual defect. It is not that the vocabulary is opaque — every opaque name in
the system has an act behind it. It is that **the acts are entity writes, and entity writes have
no author.** The criterion is currently unsatisfiable, everywhere, for the same one reason.

| opaque name | the act that binds it | is that act attributable |
|---|---|---|
| runner url | `verse runner add` | no |
| driver name | a deployment hosts that driver | outside verse |
| isolation profile / class / rung | an environment anchors policy to it | no |
| environment | the runner operator defines it | outside verse |
| `by` on a control call | — | **there is no act; it is the assertion itself** |

## The mechanism: a parameter at install, an argument at launch

**An installed declaration takes an identity as a parameter; a launch fills it with an argument.**

Formal and actual, on the two occasions that already exist. The parameter is part of the
installed record — fixed when the charter, runner or environment was written. The argument is
part of the launch — a firing, or `verse task run`.

### It separates two identities that one field cannot hold

`by` conflates them, which is why it answers neither well:

- **The parameter — who is responsible.** Whoever put the rule in place. Fixed at install,
  unchanged by any number of firings.
- **The argument — why this, now.** The precipitating cause of *this* launch.

[authority.md](../roadmap/authority.md) quotes the identity model as asking three questions of an
agent — *"who deployed it, what is it authorized to do, and on whose behalf is it currently
acting"* — and this gives each its own mechanism, sharing no field: the parameter, the `Grant`,
the argument.

The split earns its keep on the case a single field cannot represent. **A trigger that fires
because an entity changed has no launcher.** With one field you must choose between the person
who wrote the charter and the person whose write precipitated the firing, and they are different
facts about different parties. With two, the parameter roots at the install and the argument
carries the cause, and an autonomous firing is fully described for the first time.

### Most of the chain is derived, so there is one hole rather than many

Today `by` is asserted *independently* at every control call — approve, deny, say, cancel, slide
the vector — so there are as many holes as call sites and each is forgeable on its own.

A chain rooted at an install act asserts **once** (which human), and every link after it is
derived from records verse already holds: the charter exists as an entity, the firing is a record,
`Chain` already carries the kpath inside the trigger, `Doer` carries the selection, `task:instance`
carries the runner. So a chain is **corroborable against the store** with no cryptography at all —
verse can check that the charter named exists and that its install record says what the chain
claims.

That is the argument for doing this before login exists rather than after. It does not close the
root hole; it narrows the surface from N independent assertions to one, and puts the remaining one
where [a fix already has an owner](#what-this-does-not-do).

### Where the argument lives

On the **claim** — the per-request record, not the shared definition. Three independent lines
arrive at that:

- this note, because the argument is what varies per launch;
- [expose.md](../roadmap/expose.md), borrowing definition / claim / instance from Agent Sandbox:
  *"an environment record has to stay free of request-specific data … or environments stop being
  shareable and pre-warmable"*;
- [authority.md](../roadmap/authority.md), on the derived grant: *"on the claim, not the
  environment. An environment definition is shared and pre-warmable; a grant is derived from one
  request and belongs to that spawn alone."*

Three routes to the same record is usually a sign the shape is right.

## Two guards

**Attribution, never authorization.** The moment a policy reads a chain and concludes "alice
installed it, so it runs with alice's permissions", this has become on-behalf-of — which
`authority.md` rejects by name and asks to be stated explicitly wherever the design appears:

> The position of this note is neither: the envelope derives from what the user asked for, not
> from what the user is *able* to do … Say this explicitly wherever the design is presented,
> because a reader will otherwise pattern-match it to on-behalf-of and conclude we are
> re-deriving something ordinary.

The chain says who is answerable. The grant says what may be done. Wiring them together undoes
that position silently.

**The parameter is the authoring party, fixed at install, never expanded from a payload.** A
trigger's fields are templates whose `$vars` expand from the changed entity — so a chain expanded
after binding is a chain anyone who can write an entity can write themselves into. That is the
same risk `authority.md` names for derived envelopes, and the precedent is already in the tree:
`launch.Spec.Tools` is the one string field `Bind` deliberately does **not** expand.

A third, which belongs to authority rather than here: **what constrains who may supply an
argument.** A parameter any launcher may fill with any name is `--as` again. Recording the
argument is this note's job; deciding who may present it is authority's, and keeping them apart
is what makes this forward-compatible with an RBAC layer instead of a small competing one.

## What this does not do

**No verification.** The root of a chain is still an assertion. This makes it one assertion
instead of many and makes the rest checkable, which is a different claim from making it true.

**No per-slice write authority**, which is what the root actually depends on —
`attached-resolvers-residual.md` names it as *"the adjacent half of the same gap: who may install
a binding. A standing binding is exactly as strong as the ability to add and remove it."* A
parameter recorded on an entity anyone may overwrite is worth what the overwrite costs.

**No login, no subjects, no roles.** `questions.md` puts this the right way round — *"login and
RBAC can come later, but policies, permissions & subjects … might be things to place into the
model early"* — and a parameter and an argument are that placement: two holes of the right shape,
in the right records, before there is anything to put in them.

**No interpretation of what it records.** A chain is carried, corroborated against the store, and
displayed. Verse does not decide what an identity means any more than it decides what `hardened`
means.

## Where effects leave verse

The coding runner is the first place verse-caused effects leave through **a credential verse does
not hold and cannot bind to the originator.** A source's outbound write is caused directly by a
charter; a coding runner's push is executed by the runner under its own identity, on behalf of a
task whose internal steps verse only ever saw as self-report. The external record reads
"coderunner-bot pushed `verse/auto/xyz`" and the entire link back to a person is inside verse.

There is a cheap externalization channel and it is worth designing in rather than discovering:
**the commit trailer and the branch name.** Trailers naming the charter, the firing and the task
id are checkable by anyone who can read both the repository and the verse — which is the only
independently corroborable link available when the credential is shared, and it is the mechanism
[contributing.md](contributing.md) already relies on to make the log queryable.

This is also why the `identity` isolation class is a class and not a reported fact. Whether work
acts under a principal distinct from the runner's decides whether attribution survives leaving
verse — and as a rung on an ordered chain it is *refusable*, so a deployment that cannot provide
it is refused by policy and running there anyway is somebody's recorded choice. As a bit verse
merely reads, it would be a fact with no act behind it: nobody responsible, and it happens anyway.

## Open questions

- **What a chain is, concretely.** A list of refs? A ref plus a free string at the root, since the
  root is the one link that is not an entity? Whether a delegation chain (`call_agent` children)
  extends it or is reconstructed from the task tree, which already records parentage.
- **Whether the parameter can constrain its argument** — "must be the installer", "must be
  absent", "free" — or whether every constraint is authority's and the parameter is only a slot.
  The first is tempting and is how attribution starts quietly becoming policy.
- **What an absent parameter means.** Refusing would break every charter installed to date;
  defaulting to "roots at the install record" is the safe absence and matches how `control:` and
  `defaultTo:` handle theirs elsewhere.
- **Whether entity writes get an author field, or a wrapper does.** `entity.Write` is deliberately
  the *only* write the store has — "creating, updating, deleting, patching a field, and claiming
  an address are all this operation with different arguments" — so a field on it touches
  everything, and a wrapper that only some paths use is an author nobody can rely on.
- **Chain depth and display.** A chain grows with delegation; `verse ps` and `task get` need a
  summary form, and a summary that drops the middle is a summary that can mislead.
- ~~Whether the argument should reach the runner.~~ **Decided: it does, and first.** A runner
  cannot keep its own work auditable without knowing who called, and for the coding runner it is
  what puts a checkable link into a commit trailer. So the argument threads through `launch.Spec`
  → `task.Launch` → the spawn wire → `taskd` → the Builder ahead of any of the install-side work,
  and until the parameter exists a chain simply roots at whatever the launch supplied. See
  [runner-descriptor.md](runner-descriptor.md#the-near-term-piece-the-calling-principal-on-the-launch).

## What it would touch

An author on the install path (`entity.Write` or a wrapper, and every `verse * add`), a parameter
on the installed records (`trigger`, `runner`, and whatever holds an environment), an argument on
the launch path (`launch.Spec`, `task.Launch`, the spawn wire, `taskd`), and the display
(`task get`, `verse ps`, `trigger show`). Documented, on landing, wherever `by` is currently
described — [task-control.md](../task-control.md) and
[attached-resolvers.md](../attached-resolvers.md) — and it retires half of
[attached-resolvers-residual.md](attached-resolvers-residual.md) §3, which should be edited rather
than appended to.

This is `contract`: it adds a field to the wire and, if the parameter is ever required, changes
what an install accepts. The tier is not the hard part — the hard part is that a chain recorded
and not verified reads, to anyone who does not know better, as a chain that was verified.

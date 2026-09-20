# A runner descriptor — sketch

Status: **sketch** on verse's side, and none of that is argued to the end. Treat every section as a
placeholder with a decision in it, not as a design.

Landed: the half that lives on a runner — `GET /runner`, the leaf vocabulary, the per-leaf ceiling —
is built and exercised in the [coding runner](../residuals.md) (`km5mzagq`).

What is still sketch is everything on **verse's** side. Nothing consumes the descriptor: `verse
runner add` still probes with `GET /tasks`, verse holds no isolation vocabulary of its own, and an
environment naming a `profile` is refused whatever it names because no runner publishes a
catalogue.

One thing the landing settled and this note had not: a **per-leaf ceiling is not a catalogue of
buildable points**. bwrap's network is one switch, so it builds `(full, full)`, `(none, none)` and
— with an egress proxy — `(none, restricted)`, while a per-leaf reading of the same numbers admits
`(none, full)`, which cannot be built. `provides:` below should be read as `perLeaf:`, which is what
the runner actually calls it, and the refusal stays the authority.

Wire compatibility is **not** a constraint here: breaking it is fine at this stage, which
collapses most of what an earlier draft spent its length on.

## Why a descriptor at all

Two questions want the same call, and one of them is already recorded as owed.

**How control is applied.** [the runners residual](../residuals.md) (`f6bmet8r`) has it: an answer accepted
for a brokered task is *queued*, an answer accepted over a url is assumed *applied*, and verse
learns which by a Go type assertion (`broker.handle` implements `RelaysControl()`,
`runner.HTTP`'s handle does not) — so the distinction exists in the process and not on the wire.
The residual's own answer: *"a descriptor on the contract — `GET /runner`, saying what this is and
how control is applied — which would answer both this and the install probe in one call."*

**What containment it provides.** A runner that runs coding work has to say whether it can give a
task a private filesystem, a distinct principal, or no network — facts about a box and its kernel
that the charter naming the runner cannot possibly know.

## The face

`GET /runner`, required. A runner that does not serve it is not installable.

```tony
runner:  coderunner
control: synchronous              # or: relayed
drivers: [llm, claude]
isolation:
  provides:                       # the strongest rung this runner can reach, per leaf
    network:    {ingress: none, egress: restricted}
    filesystem: {scope: private, readOnly: yes}
    principal:  distinct
  defaults:                       # where an unstated coordinate lands
    network:    none              # sugar: expands to every leaf under it
    filesystem: {scope: private, readOnly: no}
    principal:  distinct
  local:                          # runner-defined, comparable only here, never shadowing
    gpu: {chain: [shared, exclusive], defaultTo: shared}
  profiles:                       # the catalogue: points this runner can actually build
    - name: dev
      at:
        network:    {ingress: none, egress: full}
        filesystem: {scope: shared, readOnly: no}
        principal:  distinct
    - name: hardened
      at:
        network:    {ingress: none, egress: restricted}
        filesystem: {scope: private, readOnly: no}
        principal:  distinct
        local:      {gpu: exclusive}
```

**Activation** is the install: `verse runner add` calls `GET /runner`, and what comes back decides
whether the runner installs at all. Sketch of the outcomes — it answers and decodes (installed,
with what it said recorded); it answers and is not a runner (refused); nothing answers
(installed with a warning, as today, since a pod that is pending is the ordinary case). The
descriptor is read **at install and by `--probe`, never before a spawn**: the existing rule that
probes are diagnosis and never dispatch, and *"the spawn is the better probe"*.

**The entity still carries a url and nothing else.** The descriptor is served *by* the runner, not
written *about* it by an operator — which is what keeps it from becoming the second place a
ceiling is expressed.

## Isolation: verse defines the classes, runners provide them

The decision, and it reverses an earlier draft of this note.

verse holds a small vocabulary of **properties**, not mechanisms, and it is a **tree**:

| leaf | chain, weakest first | |
|---|---|---|
| `network.ingress` | `full` → `restricted` → `none` | what can reach the task |
| `network.egress` | `full` → `restricted` → `none` | what the task can reach |
| `filesystem.scope` | `host` → `shared` → `private` | what it can see |
| `filesystem.readOnly` | `no` → `yes` | whether it may write what it sees |
| `principal` | `runner` → `distinct` | whether it acts as the runner |

`restricted` means "some, not all" and says nothing about whether that is a proxy, a filter or a
mesh; `private` means "only what it was given". Mechanisms — namespaces, seccomp, Landlock,
gVisor, uid pools — stay entirely the runner's business, and a runner maps its own onto these.

**The tree is how the factoring gets pushed far enough down.** A flat class is still a chain, and
a chain still flattens whatever incomparability survives inside it: one `network` axis forces
"can be reached" and "can reach out" onto one line, and one `filesystem` axis cannot tell
"read-only rootfs plus a writable worktree" from "full rootfs, only `/tmp` writable". Splitting
until every **leaf** is genuinely a chain is what removes that residue rather than relocating it.

Three rules, and they are what keep a tree from being two orderings on one thing:

- **Only leaves carry chains.** Interior nodes are grouping and have no order of their own.
- **Comparison recurses.** A node's point is the tuple of its leaves, so ≥ is defined at every
  level and is componentwise all the way down. `network` alone is a meaningful comparison; it just
  means both its leaves.
- **Defaults are per leaf**, with a node-level default as sugar that expands to every leaf under
  it — so `network: none` in a descriptor is shorthand and not a fourth kind of value.

It also gives runner-local classes a home with no collision risk: they hang under `local.`, so
`local.gpu` can never shadow a class verse names later.

Why verse holds them, in three lines:

- **The precedent is in the tree.** `task.Effect{Verb, Resource}` is verse-defined vocabulary and
  the deriver is the seam where *"a foreign agent's tools become the platform's typed
  capabilities"*. This is the same construction one layer down.
- **`task.Launch` already prefers it.** *"Sending the predicates makes the meaning travel with the
  request and keeps the vocabulary in exactly one place."* Runner-published words put it in as
  many places as there are runners.
- **`authority.md` already speaks this way.** *"A floor is infrastructural and therefore
  non-semantic — it can withhold a network, never a particular command"* presumes a shared notion
  of a network being withheld.

What it buys that runner-published words did not: policy over the floor **generalizes across
runners** (no per-runner anchoring, which was the sharpest cost of opacity), delegation can be
compared across a runner boundary, and versioning the chains is verse's problem solved once
rather than each runner's.

**Runner-local classes** sit alongside for what verse has not named — `gpu` above. They compare
within one runner only, and policy that wants portability stays off them.

### The rules, briefly

- **A profile is a point**: one coordinate per leaf. Comparison is componentwise — `hardened ≥
  dev` iff ≥ at every leaf — which is a partial order and correctly reports two points as
  incomparable rather than flattening them.
- **Delegation is non-increasing at every leaf.** A `call_agent` child may not name a point below
  its parent's resolved point, anywhere in the tree. That answers the floor-inheritance question
  [expose.md](../../roadmap/expose.md) carries as open, with the shape `Grant.Subset` already uses,
  and it runs inside the runner at the boundary where `Grant.Subset` already runs.
- **An unstated coordinate reads as the leaf's default**, for comparison *and* execution — one
  reading, because a coordinate is a specification the runner builds from rather than a
  description of something built. The two-readings version (weakest to compare, strongest to run)
  is the natural first answer and is a bug: the profile then compares one way and runs the other.
- **Defaults are declared, not positional.** A runner names them, so appending a rung never
  silently re-defaults an existing profile, and a leaf whose strongest rung is expensive or only
  sometimes constructible can still be offered without being defaulted to.
- **The default point must be constructible.** Every leaf's default, taken together, is where an
  unprofiled spawn lands — so it must run. That is the conformance test: not "what do the words
  mean" but "is the default reachable, and is every published profile buildable".

### What it deliberately does not do

**No selection.** [runners.md](../../runners.md): *"No selection beyond a name. No `match`, no
`capability`."* This guard is now **harder to hold**, and that is a real cost of verse-defined
classes: portable coordinates are matchable across runners in a way a runner-local `sealed` never
was, so "find me a runner with `network: none`" becomes a coherent thing to want. The spawn names
one runner; that runner provides the point or refuses.

**No verification — for now.** verse cannot check that a runner claiming `network: none` withheld
one. With shared classes that becomes a *current state* rather than a principle: a canary task
that tries to reach the network is a test verse could eventually run, which was impossible while
the words were per-runner.

## The near-term piece: the calling principal on the launch

Landed: the launch carries the calling principal to the runner, which is what puts a checkable
link back into a branch name and a commit trailer.

## Open

- **Where the tree stops.** Five leaves cover what the coding runner needs. Process visibility
  between tasks is the obvious next one and could hang under `principal` or stand on its own;
  `filesystem.readOnly` might want to be a three-rung `write` axis (all of scope / only what it
  was given / none) rather than a boolean. The rule that decides these is that a leaf splits when
  something real cannot be said without splitting it — not before.
- **How coarse the rungs stay.** `restricted` is deliberately one word for several mechanisms.
  The first deployment that needs to distinguish two kinds of restricted is the one that decides
  whether the chain grows or the runner describes the difference in prose.
- **Standard driver names.** Same move as the classes: a third-party runner declaring `drivers:`
  against a known set is what lets an operator know a spawn will be accepted. Undecided whether
  the set is closed.
- **Where the profile name is written** — inside the environment record (so the charter names one
  thing) or beside it on the spawn (so one workspace can run at two containments). The monotonic
  reading, that a spawn may only tighten what its environment declared, is the obvious composition
  rule if both exist.
- **Whether the descriptor lists environments.** Useful for `verse runner show`, and the same
  selection bait as everything else here.

## What it would touch

`GET /runner` on the runner face (`task/taskd`), the client and probe (`runner/`), the class
vocabulary and the componentwise check (`task/`), the profile and principal fields on the spawn
wire (`task.Launch`, `runner.HTTP.Spawn`, `taskd`'s decode, `versed.launchOf`), and what `verse
runner`, `--probe` and `verse runner show` print. On landing it closes the first half of
[the runners residual](../residuals.md) (`f6bmet8r`).

`contract`: the descriptor is required, so existing runners stop installing.

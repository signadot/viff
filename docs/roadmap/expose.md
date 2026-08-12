# `verse expose`: a named, durable directory surface (reframing `connect`)

Status: design note, not implemented. This reframes [`connect`](../working/connect.md) — it keeps the
federation mechanics (docd content controller, `dir-status`/`dir-content`) and changes what is
*named*, what *persists*, and what is *automatic*.

## The reframing

`verse connect` fuses three separable things into one ephemeral session:

1. federating a directory's files into the verse (the surface),
2. a nonce scope (an isolation lifetime that dies with the session), and
3. auto-spawning a validator per file (one specific reactive program).

`verse expose` keeps (1), gives it a name, and — because a working tree's files must never
land in the shared baseline (see "Exposures are scopes" below) — couples it to a named, durable
scope that holds the federated files. What it drops is connect's *automatic* reactivity (3) and
its *task-tied nonce*: the scope becomes a named handle that outlives any single `expose`
invocation, and the "validate my tree" loop becomes just *expose + a validator charter*, not a
built-in.

## What expose is

```sh
verse expose <path> --name <name>     # register a durable, uniquely-named directory surface
```

- Named, unique, and meaningful. The exposure has its own name, enforced unique, chosen (by
  convention) to capture *whose* tree it is, *where*, and its *role* in the work — because that name
  appears in the federated refs and triggers filter on it (below). It is not the scope's identity: a
  scope may be an opaque nonce, but an exposure name must be legible. A baseline registry entry
  records it (root, the scope it uses, owner/role, live/stale) so you can list exposures.
- Files live in an isolated scope, addressed by the exposure name. The federated entities land in
  a scope (never the shared baseline), addressed as `local:dir-status-<name>:*` /
  `local:dir-content-<name>:*` and carrying descriptive `{owner, root, role}` fields — so the refs
  are meaningful and filterable, not an opaque nonce.
- No validators. Exposing federates the files into the scope; it installs no triggers and
  spawns nothing.

## Programming an exposure with triggers (decoupled from its session)

Because the exposure is a durable, meaningfully-named surface (not tied to a task, as connect's
nonce is), reactivity is attached to it explicitly and separately — a trigger matching
`local:dir-status-<name>:*`, globbing the name (`dir-status-alice-*`), or matching the `{owner, role}`
fields, running in the exposure's scope. That legible name is the point: a trigger can target "Alice's
review trees" without knowing an opaque nonce. Its lifetime is the exposure's, not the `expose`
task's — you expose a directory once and program it over time, the triggers survive Ctrl-C and
re-attach, and different agents can share one exposure. Contrast connect, whose trigger dies with the
session.

Connect's validator-per-file behavior becomes one charter you opt into against `<name>`, nothing
more.

## Mapping the agent's file tools to an exposure

The agent tools are `read_file` / `list_dir` / `write_file` / `run_bash` (`task/harness`).
Today `read_file`/`list_dir` federate through a `task.ContentReader` when one is bound;
`write_file` still hits the server's local disk. For an exposure:

- `read_file`, `list_dir` — served from the exposure's mount. The content controller already
  serves `dir-content-<name>` for reads; `list_dir` federates against the exposure's file set
  (which `dir-status-<name>:*` already enumerates), instead of reading the server's local FS.
- `write_file` — federated write-back *into the named exposure*, the next section.
- `run_bash` — does not federate the same way (it runs commands, not file ops); it still needs
  a task running where the files physically are (the local-proc topology in
  [deployment and steering](../deployment-and-steering.md)). Out of scope here.

## The write half

A named exposure is what federated writing was missing: a definite target and an authority
model. But a write does not fall out of the read by symmetry, and the reason is worth stating
because it decides the design.

`read_file` federates cleanly because a read is a pure follow-the-reference — idempotent,
side-effect-free, and honest with *index-not-warehouse*, since the content is served on demand
from the one system that owns it and never stored in the verse. A write mutates an external
system. There is no "serve on demand" for a mutation; something must actually apply it to the
tree. So a write is an *outbound effect*, the same category as commenting on a PR or pushing a
tag — the outbound-executor path [sources](../sources.md) reserves.

Three constraints shape it. *Read-your-writes*: after `write_file(p, c)`, the next
`read_file(p)` goes through the content controller and must return `c`. *Index, not
warehouse*: file contents must not end up in logd; the exposed tree stays the single source of
truth for bytes. *A write is gated and attributable*: the harness already derives it as a
`Write` effect on a `File` resource, and a write into someone's tree makes gating matter more,
not less.

The answer those three force is write-through: the content controller grows a `Patch` path
for its slice, confines the path under the exposure's root as reads already do, and writes the
file. Read-your-writes then works immediately, because content is served live from the tree —
the controller simply re-reads it. Bytes never enter the verse. The harness's `write_file`,
when bound to an exposure, routes through the verse to that slice instead of `os.WriteFile` on
the server root, mirroring how `read_file` already routes reads: the same seam, extended from
a reader to a writer.

Two alternatives are worth recording as closed. A *transparent staging overlay* — pending
writes invisible in the real tree but merged into reads — is a dead end, because it means
warehousing the pending bytes somewhere and overlaying them on the controller's reads, which is
exactly the warehousing the model has been careful to avoid. And *writes as proposed diffs*,
recorded as entities for the human to apply, is a good review surface but a weak working
substrate: it breaks read-your-writes unless the controller overlays pending diffs on reads,
which is the blocked overlay again. Layer it on top later if a review flow wants it.

"Don't touch my live tree" needs no new machinery either: point the task at a *different*
exposure — a scratch worktree exposed under its own name — and the same write-through path
lands somewhere else.

Open: conflict policy when a human and an agent write the same file (gated by default,
last-write-wins when auto, is the likely answer); atomicity across the wire (write temp, then
rename in the controller); and `list_dir`, which is the trivial sibling — a `Match` of the
mount subtree *is* the listing, with none of write's hazards.

## Letting an agent "cd" into an exposure

A task's working context is its `Root` + bound `ContentReader` (`harness`), fixed at spawn
today. To let an agent change directories to an exposure:

- Bind a task to an exposure by name at spawn — its file tools resolve against
  `local:*-<name>:*`. Binding must not mean *only*, though: see "A task sees more than one
  surface" below.
- A working-surface control, analogous to re-vectoring: an operator (or the engine) swaps a
  running task's bound exposure — "attach to exposure `<name>`" — the file-context equivalent of
  `SetVector` sliding the gate vector mid-run.
- An agent `cd <name>` tool, restricted to exposure names the task is permitted to see, so
  an agent can move between exposed directories on its own.

Open: whether `cd` is primarily an operator control, an agent tool, or both (operator sets the
initial surface; the agent moves within a permitted set).

### A task sees more than one surface

The harness today is all-or-nothing: `fsExec` federates `read_file` when a `ContentReader` is
bound and reads the local filesystem under `Root` when it is not (`task/harness/exec.go`) —
there is no third case. So binding a task to an exposure would *take away* its own tree: it
could not read the charter or config it was spawned beside, write scratch output next to itself,
or look at a second exposure it is meant to compare against. Real work needs all three at once —
the exposure it is operating on, its own `Root`, and sometimes another exposure.

A task's working context should therefore be a table of named surfaces with one current
surface, not a single bound reader:

- The table maps a surface name to a resolver — an exposure (served as `local:*-<name>:*`
  through the content controller) or the task's own `Root` (the local filesystem, named
  `local` by convention). Its membership is fixed at spawn and adjusted by the same
  working-surface control that swaps a binding above; it *is* the permitted set `cd` moves
  within.
- The current surface is what a bare path resolves against — so unqualified
  `read_file`/`list_dir`/`write_file` keep working exactly as they do now, and `cd <name>` only
  moves the cursor.
- Qualified paths — `<name>:<path>` reaches another surface in the table without moving.
  That is what a compare-across-exposures or copy-out task needs, and it keeps cross-surface
  access legible in the execution ledger instead of implicit in whatever the cursor happened to
  be.

Deliberately not a search path: a bare path must never fall back from the current surface to
the local filesystem. Silent fallback is how a task reads the wrong `config.yaml`, and it
would dissolve exactly the isolation the exposure's scope exists to provide — a file missing on
the current surface is an error, not a reason to go looking elsewhere.

Authority stays per-surface. A write is gated at the surface it lands on (the task's vector
checked at the write boundary, per "Write authority" below), so the table can hold an exposure a
task may read and another it may also write, and the difference is enforced where the write
crosses rather than by which surface happens to be current.

`run_bash` is unaffected: it runs where the task runs, so a current surface that is a remote
exposure gives it nothing — same local-proc constraint as above.

## Exposure and scope: coupled by convention, not unified

Isolation is still non-negotiable — a working tree's files are ephemeral, private working state
that must not be mirrored into the shared public baseline, and a scope is exactly the isolation
primitive. So an exposure's files live in a scope. But don't collapse the exposure and its scope
into one identity.

The reason is trigger ergonomics. Triggers condition on `system:kind:id` refs, and people will want
to filter those by *whose* directory it is, *where* it lives, and *what role* it plays in their work.
That filtering only works if the identifier in the ref is meaningful and human-chosen — an
opaque scope nonce is un-filterable. If exposure and scope were one thing, the exposure would inherit
the scope's identity, and either the scope stops being a disposable nonce or the refs stop being
legible.

So keep them as two coupled records:

- A scope is the isolation primitive — a copy-on-write overlay, possibly an opaque nonce
  ([scope lifecycle](scope-lifecycle.md)).
- An exposure is a first-class, uniquely-named record whose legible name (owner / where / role,
  by convention) drives the federated refs and fields — coupled to a scope by convention (it
  records which scope holds its files) rather than *being* the scope.

`verse expose <path> --name alice-webapp` registers the exposure `alice-webapp`, gives it a scope for
isolation, federates the tree into that scope under `local:dir-status-alice-webapp:*`, and records the
binding. `verse expose rm alice-webapp` drops the exposure and its scope together. The two lifecycles
travel together but the two *identities* stay separate — [scope lifecycle](scope-lifecycle.md)
governs the scope; the exposure registry references it.

This still reconciles the session tension — triggers are decoupled from the *expose task* because
the exposure (and its scope) is named and durable, not because they escape it — while keeping the
refs legible enough to filter by owner, place, and role.

## Exposure, or environment? — the `launch.Spec` axis with no referent

`launch.Spec` already declares the axis this note is circling. Its fields are all *references to
something named elsewhere* rather than inlined things:

	Vector      → the gate vector (the autonomy dial) — soft, slidable
	Grant       → hard authority — what it may do at all
	Environment → the sandbox/workspace floor — unbypassable

Two of those words sit next to each other from here on, so keep them apart. "Scope" is taken:
everywhere in verse it means the entity-store isolation overlay — `entity.Store.Scope`,
`config.Scope`, `--scope`, `VERSE_SCOPE`, connect's nonce, and this document up to this point.
The authority axis is the *grant* — the word the type (`task.Grant`), the delegation check
(`Grant.Subset`), and the launch field (`launch.Spec.Grant`, wire field `grant`) all use. This
note says *grant* for authority and reserves *scope* for isolation.

`Environment` **now has a referent, and not the one this note assumes.** It used to have none —
`Bind` expanded its `$vars` and nothing resolved the name — and the axis has since been plumbed
to the runner, where [the coding runner](../working/coding-runner-residual.md) resolves it against its own
config: a workspace of repositories, a toolchain, a containment.

That is worth reading as a fork rather than as this note being answered. The runner-side referent
carries the name **unresolved**, the way `driver:` travels, so the vocabulary lives with the
party that can build one and a runner handed a name it does not have refuses the launch. What
this note proposes is a *verse-side* referent — a registry of named, durable environments the
engine itself knows, which is what makes an exposure filterable by a trigger and shareable
across tasks. Both can hold: one is where an environment is *built*, the other is where it is
*named and programmed*. What is not yet decided is whether a verse-side registry resolves down
into the runner-side one, or whether they stay two vocabularies that happen to share a field.

`Vector` is in the same state, and it is the more visible of the two: the spawned task's
vector is whatever its driver hardcodes (`harness.Vector()` for llm, an empty `Default: auto` for
scripted), so a trigger cannot say "spawn this one auto — I already gated the firing." That is
why a trigger-spawned agent asks twice: once at the firing, through the trigger's own `gates`,
and again per action, through a vector the trigger had no way to name. Both axes want the same
thing — a named record the runtime resolves at spawn — which is the shape `expose` is arriving at
from the file-surface side. Where that spawn-time authority should come from is
[authority](authority.md).

Which suggests the record being designed here is not really "an exposed directory". It is a
sandbox definition, of which the directory surface is one member:

	environment <name>
	  surfaces:      the table above — exposures plus the task's own root, one of them current
	  env:           the variables the task, and its run_bash, actually see
	  capabilities:  the floor — which tools exist at all, which surfaces are writable, network or not

with `verse expose` as the verb that puts a directory *into* one, and `spawn { environment:
<name> }` as what binds a task to it.

An environment is not a scope, and adding it does not re-merge what the previous section
separated: a scope isolates *entities* (a COW overlay in the store), an environment configures
*execution* (surfaces, variables, capability floor). The exposure is what ties one to the other.

### It makes the surface table declarative

The multi-surface table above is otherwise an ad-hoc bundle of spawn arguments, assembled per
task and re-assembled by every trigger that launches into the same tree. An environment record
is its natural home: declare the surfaces once under a name, and every task launched into it
gets the same set, `cd` moves within a set that was *declared* rather than accumulated, and a
trigger action says `environment: alice-webapp` instead of enumerating mounts. The `Environment`
axis then carries the working context the way `Vector` carries the autonomy dial.

### Env today is inherited, which is a leak

`run_bash` builds `exec.CommandContext(ctx, "bash", "-lc", command)` and never sets `c.Env`
(`task/harness/exec.go`), so the agent's shell inherits the *server's* entire environment —
including the `PORTKEY_API_KEY` / `PORTKEY_VIRTUAL_KEY` the harness itself runs on. An
environment record fixes that by construction rather than by policy: the environment *defines*
the variables, so the default becomes "what this sandbox declares" and inheritance becomes an
explicit passthrough list. A floor that has to be enforced by a gate is not a floor.

### Capabilities: floor, grant, dial

Three layers, narrowing, and the note's "base capabilities" is the outermost:

- Environment — what is *possible*. Which tools exist at all, which surfaces are writable,
  whether there is a network. Enforced by *construction* — the tool is absent from the driver's
  tool set, the surface is absent from the table, the variable is unset — which is what earns
  the word "unbypassable" in `launch.go`. A vector slides mid-run via `SetVector`; a floor does
  not slide.
- Grant — what this task may *exercise* of what is possible, non-increasing down a
  delegation chain (`Grant.Subset` checked at each `call_agent` boundary).
- Vector — which of those must *ask*. Soft, slidable, the autonomy dial.

Read as possible ⊇ granted ⊇ auto. The difference between the first layer and the other two is
observable and worth preserving: a `Deny` gate means the tool exists, the agent reaches for it,
and is refused — a refusal it can see, log, and route around. Outside the floor there was
nothing to reach for. Denials belong in the ledger; absences belong in the definition.

## Prior art: two systems that already define a sandbox

Two existing designs cover parts of this ground, and both are worth borrowing from — one for
the environment record, one for the surface table.

### Agent Sandbox (Kubernetes) — the record split is one finer than ours

Agent Sandbox is a Kubernetes project (`kubernetes-sigs/agent-sandbox`, generally available as of
2026) for running agent workloads in isolation. It defines four resource types, and the first
three are the shape this note has been converging on:

- `SandboxTemplate` — a reusable, named definition: a pod template (container image,
  environment variables, mounted volumes) plus a network policy. This is the *sandbox
  definition* above, near enough exactly.
- `SandboxClaim` — a request for an environment made *from* a template
  (`sandboxTemplateRef`), which the platform satisfies with an instance.
- `Sandbox` — the running instance: one stateful pod with a stable hostname, persistent
  storage that survives restarts, and pause / resume / scheduled-deletion lifecycle.
- `SandboxWarmPool` — pre-provisioned replicas of a template, so a claim is satisfied
  immediately rather than by cold-starting a pod.

The claim is the record this note is missing. We have a definition (the environment) and an
instance (the exposure with its scope), and no word for *a spawn asking for one*. Adding it makes
`launch.Spec.Environment` resolvable in the obvious way — a spawn does not merely *reference* an
environment, it claims an instance of one — and it dissolves the open question below about
whether `expose` creates environments or joins them: `expose` registers a definition, a spawn
claims an instance, and the two readings stop competing for the same record.

A consequence worth taking seriously: a warm pool means the definition must be instantiable
before anyone claims it. So an environment record has to stay free of request-specific data.
That cuts against the tempting move of folding "what this run may do" into the environment — a
derived grant ([authority](authority.md)) belongs to the *claim*, not the definition, or
environments stop being shareable and pre-warmable.

Their floor is real but non-semantic, which is why ours is not redundant. Agent Sandbox
enforces isolation with gVisor (a user-space kernel interposed between the container and the host
kernel), optionally Kata Containers (lightweight virtual machines), default-deny network policy,
and Kubernetes' own role-based access control and quotas. That is a floor enforced by
construction, exactly as argued above — but it can only say "no network"; it can never say "may
exec `go test` but not `rm -rf /`". A `Grant` of predicates over `Effect` (verb + typed resource)
*can* say that. The two compose: an infrastructural floor underneath, a semantic grant above.
This is the answer to "why not just use a container."

### Model Context Protocol roots — the surface table, already specified

The Model Context Protocol has roots: a list of URIs the client hands the server to
declare where it may operate, with a `roots/list_changed` notification when the list changes
without restarting anything. That is the named-surface table of "A task sees more than one
surface", plus the working-surface control, already written down by someone else.

Two differences, both in our favor and worth preserving:

- Roots are advisory; surfaces are load-bearing. A root is a boundary a server is trusted to
  honor. A surface here is the *resolver* — it sources the bytes. A task cannot read outside
  its table by ignoring a convention, because there is nowhere else for a path to resolve.
- Direction of control. Roots are handed *from the client to the server*, never requested by
  the server. Same instinct as the no-silent-fallback rule: what a task may see is declared by
  whoever launched it, not negotiated by the task.

### What has no counterpart

Both systems are built for isolation, density, and latency — running untrusted,
model-generated code cheaply and safely. Neither cares whether a sandbox is *legible*. Agent
Substrate (Google's newer, early-stage layer above Agent Sandbox) addresses actors by stable
identity so it can multiplex a large population of mostly-idle agents onto a small pool of pods,
but nobody filters reactions on "Alice's review trees, by role" — because their agents are not
programmed by a shared reactive control plane.

That is the difference in kind. The floor is a solved problem worth borrowing outright. The
named, filterable, programmable surface — the thing that makes a trigger able to target an
exposure it was not told about by nonce — is what this design is actually adding, and it is why
the exposure name has to be meaningful rather than opaque.

## What this buys

- `connect` becomes a thin preset — `expose <path> --name <n>` plus a validator charter over
  `<n>`, on the unchanged content-controller mechanics.
- Reactivity is decoupled from the session, so a directory can be exposed once and its program
  evolved, shared, or reasoned about independently of any live connector.

## Open questions

- Naming convention — how an exposure name (and its `{owner, role}` fields) should encode
  whose/where/role so triggers filter cleanly, without turning the name into a brittle path.
- Uniqueness domain — exposure names are unique over the exposure registry; a scope keeps its own
  (possibly nonce) identity. Whether names are baseline-global or namespaced by owner is open.
- Lifecycle / un-expose — `verse expose rm <name>` drops the exposure and its coupled scope;
  who reclaims a stale one is the same concern as [scope lifecycle](scope-lifecycle.md) (the
  exposure record references a scope record; both are reclaimed together).
- Write authority — who may `write_file` into an exposed tree, gated by the task's vector at
  the write boundary (the existing gate model).
- `cd` semantics — control vs tool vs both; permission to move between exposures.
- Expose vs. environment — whether `verse expose <path> --name n` *is* a single-surface
  environment (sugar) or registers into a separate exposure registry that environments then
  reference. The second keeps a directory shareable across environments; the first is one record
  fewer. The definition / claim / instance split borrowed from Agent Sandbox above probably
  settles this — `expose` registers a definition, a spawn claims an instance — but the claim
  record has to be designed before that is more than a suggestion.
- Floor inheritance across delegation — a `call_agent` child names an environment; must it be
  the parent's, or a sub-environment of it? `Grant.Subset` gives the grant its monotonicity at the
  boundary; the floor needs an equivalent rule or delegation becomes the escalation hatch (spawn
  a child into a roomier sandbox).
- Variables vs. secrets — an environment that declares creds is a secret-bearing record.
  Whether values live in it, or it references a secret store and resolves at spawn.
- Surface qualification — the concrete syntax for a qualified path (`<name>:<path>` collides
  with nothing in a filesystem path, but neither does a `/<name>/` mount prefix), and whether the
  local `Root` is always in the table or must be granted like any other surface.

## References

- [Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) (`kubernetes-sigs/agent-sandbox`) and its
  [documentation](https://agent-sandbox.sigs.k8s.io/docs/) — the `Sandbox` / `SandboxTemplate` /
  `SandboxClaim` / `SandboxWarmPool` resource types; field-level detail in the
  [custom resource definition reference](https://docs.cloud.google.com/kubernetes-engine/docs/reference/crds/agentsandbox).
- [Bringing you Agent Sandbox on GKE and Agent Substrate](https://cloud.google.com/blog/products/containers-kubernetes/bringing-you-agent-sandbox-on-gke-and-agent-substrate)
  — warm pools, pod snapshots, and the isolation defaults.
- [Agent Substrate](https://github.com/agent-substrate/substrate) — the actor/worker multiplexing
  layer above it (early-stage; its API is expected to change). An accessible walkthrough:
  [How Google Agent Substrate Works](https://www.solo.io/topics/ai-infrastructure/how-google-agent-substrate-works).
- [Roots](https://modelcontextprotocol.io/specification/2025-06-18/client/roots) — the Model
  Context Protocol's client-declared filesystem boundaries and its change notification.

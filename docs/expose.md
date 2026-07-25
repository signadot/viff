# `verse expose`: a named, durable directory surface (reframing `connect`)

**Status: design note, not implemented.** This reframes [`connect`](connect.md) — it keeps the
federation mechanics (docd content controller, `dir-status`/`dir-content`) and changes what is
*named*, what *persists*, and what is *automatic*.

## The reframing

`verse connect` fuses three separable things into one ephemeral session:

1. **federating a directory's files into the verse** (the surface),
2. a **nonce scope** (an isolation lifetime that dies with the session), and
3. **auto-spawning a validator per file** (one specific reactive program).

`verse expose` keeps (1), gives it a **name**, and — because a working tree's files must never
land in the shared baseline (see "Exposures are scopes" below) — couples it to a **named, durable
scope** that holds the federated files. What it drops is connect's *automatic* reactivity (3) and
its *process-tied nonce*: the scope becomes a named handle that outlives any single `expose`
invocation, and the "validate my tree" loop becomes just *expose + a validator charter*, not a
built-in.

## What expose is

```sh
verse expose <path> --name <name>     # register a durable, uniquely-named directory surface
```

- **Named, unique, and meaningful.** The exposure has its own name, **enforced unique**, chosen (by
  convention) to capture *whose* tree it is, *where*, and its *role* in the work — because that name
  appears in the federated refs and triggers filter on it (below). It is not the scope's identity: a
  scope may be an opaque nonce, but an exposure name must be legible. A baseline registry entry
  records it (root, the scope it uses, owner/role, live/stale) so you can **list** exposures.
- **Files live in an isolated scope, addressed by the exposure name.** The federated entities land in
  a scope (never the shared baseline), addressed as `local:dir-status-<name>:*` /
  `local:dir-content-<name>:*` and carrying descriptive `{owner, root, role}` fields — so the refs
  are meaningful and filterable, not an opaque nonce.
- **No validators.** Exposing federates the files into the scope; it installs no triggers and
  spawns nothing.

## Programming an exposure with triggers (decoupled from its session)

Because the exposure is a durable, meaningfully-named surface (not tied to a process, as connect's
nonce is), reactivity is attached to it **explicitly and separately** — a trigger matching
`local:dir-status-<name>:*`, globbing the name (`dir-status-alice-*`), or matching the `{owner, role}`
fields, running in the exposure's scope. That legible name is the point: a trigger can target "Alice's
review trees" without knowing an opaque nonce. Its lifetime is the exposure's, **not** the `expose`
process's — you expose a directory once and program it over time, the triggers survive Ctrl-C and
re-attach, and different agents can share one exposure. Contrast connect, whose trigger dies with the
session.

The old validator-per-file behavior becomes one charter you opt into against `<name>`, nothing
more.

## Mapping the agent's file tools to an exposure

The agent tools are `read_file` / `list_dir` / `write_file` / `run_bash` (`process/harness`).
Today `read_file`/`list_dir` federate through a `process.ContentReader` when one is bound;
`write_file` still hits the server's local disk (the write-back gap — [write-back.md](write-back.md)).
For an exposure:

- **`read_file`, `list_dir`** — served from the exposure's mount. The content controller already
  serves `dir-content-<name>` for reads; `list_dir` federates against the exposure's file set
  (which `dir-status-<name>:*` already enumerates), instead of reading the server's local FS.
- **`write_file`** — federated write-back *into the named exposure* (closing write-back.md). A
  named exposure is what write-back was missing: a definite target and an authority model — the
  write crosses the process's gate vector at the boundary before it lands in the exposed tree.
- **`run_bash`** — does not federate the same way (it runs commands, not file ops); it still needs
  a process running where the files physically are (the local-proc topology in
  [deployment-and-steering.md](deployment-and-steering.md)). Out of scope here.

## Letting an agent "cd" into an exposure

A process's working context is its `Root` + bound `ContentReader` (`harness`), fixed at spawn
today. To let an agent change directories to an exposure:

- **Bind a process to an exposure by name** at spawn — its file tools resolve against
  `local:*-<name>:*`.
- **A working-surface control**, analogous to re-vectoring: an operator (or the engine) swaps a
  running process's bound exposure — "attach to exposure `<name>`" — the file-context equivalent of
  `SetVector` sliding the gate vector mid-run.
- **An agent `cd <name>` tool**, restricted to exposure names the process is permitted to see, so
  an agent can move between exposed directories on its own.

Open: whether `cd` is primarily an operator control, an agent tool, or both (operator sets the
initial surface; the agent moves within a permitted set).

## Exposure and scope: coupled by convention, not unified

Isolation is still non-negotiable — a working tree's files are ephemeral, private working state
that must **not** be mirrored into the shared public baseline, and a scope is exactly the isolation
primitive. So an exposure's files live in a scope. But **don't collapse the exposure and its scope
into one identity.**

The reason is trigger ergonomics. Triggers condition on `system:kind:id` refs, and people will want
to filter those by *whose* directory it is, *where* it lives, and *what role* it plays in their work.
That filtering only works if the identifier in the ref is **meaningful and human-chosen** — an
opaque scope nonce is un-filterable. If exposure and scope were one thing, the exposure would inherit
the scope's identity, and either the scope stops being a disposable nonce or the refs stop being
legible.

So keep them as **two coupled records**:

- A **scope** is the isolation primitive — a copy-on-write overlay, possibly an opaque nonce
  ([scope-lifecycle.md](scope-lifecycle.md)).
- An **exposure** is a first-class, uniquely-named record whose legible name (owner / where / role,
  by convention) drives the federated refs and fields — **coupled to a scope by convention** (it
  records which scope holds its files) rather than *being* the scope.

`verse expose <path> --name alice-webapp` registers the exposure `alice-webapp`, gives it a scope for
isolation, federates the tree into that scope under `local:dir-status-alice-webapp:*`, and records the
binding. `verse expose rm alice-webapp` drops the exposure and its scope together. The two lifecycles
travel together but the two *identities* stay separate — [scope-lifecycle.md](scope-lifecycle.md)
governs the scope; the exposure registry references it.

This still reconciles the session tension — triggers are decoupled from the *expose process* because
the exposure (and its scope) is named and durable, not because they escape it — while keeping the
refs legible enough to filter by owner, place, and role.

## What this buys

- **`connect` becomes a thin preset** — `expose <path> --name <n>` plus a validator charter over
  `<n>`, on the unchanged content-controller mechanics.
- **Reactivity is decoupled from the session**, so a directory can be exposed once and its program
  evolved, shared, or reasoned about independently of any live connector.

## Open questions

- **Naming convention** — how an exposure name (and its `{owner, role}` fields) should encode
  whose/where/role so triggers filter cleanly, without turning the name into a brittle path.
- **Uniqueness domain** — exposure names are unique over the exposure registry; a scope keeps its own
  (possibly nonce) identity. Whether names are baseline-global or namespaced by owner is open.
- **Lifecycle / un-expose** — `verse expose rm <name>` drops the exposure and its coupled scope;
  who reclaims a stale one is the same concern as [scope-lifecycle.md](scope-lifecycle.md) (the
  exposure record references a scope record; both are reclaimed together).
- **Write authority** — who may `write_file` into an exposed tree, gated by the process's vector at
  the write boundary (the existing gate model).
- **`cd` semantics** — control vs tool vs both; permission to move between exposures.

# Scope lifecycle: make a scope first-class, not a `connect` side-effect

**Status: design note, not implemented.** This records a gap and a direction; nothing here is
built yet. The one existing instance to generalize is `connect`'s scope teardown.

## The gap

A copy-on-write **scope** is a general store capability. The global `--scope <name>` flag folds
`?scope=<name>` into *every* client request (`urlFor`), and `versed.scopedStore` resolves it to
`Store.Scope(name)` — writes land in the overlay, reads see baseline layered with the scope
(`entity.Store.Scope`; real COW on docd, passthrough in memory; see
[deployment-and-steering.md](deployment-and-steering.md)). So **any** write can create a scope:

```sh
verse --scope myexperiment entity put demo:pr:1 '{"stage":"open"}'   # overlay "myexperiment" now exists
```

But **teardown is owned by exactly one command.** Only `verse connect` deletes a scope — and only
*its own* nonce scope, on Ctrl-C, via an internal `POST /scope/delete` (`connect`'s `dropScope`). A
user-named scope, or the ad-hoc scoped write above, leaves an overlay with:

- **no record it exists** — nothing enumerates scopes; you cannot ask the verse "what scopes are
  there?"; and
- **no way to reclaim it** — there is no `verse scope delete`; the `/scope/delete` route +
  `entity.ScopeDeleter.DeleteScope` exist, but the only caller is `connect`.

So scoped overlays (and their lazily-created per-scope engines, `versed.scopeEngs`) **accumulate
and leak** until the store's data dir is wiped.

## Why this is a bug, not a missing feature

The asymmetry is the tell: **creation is universal and implicit; teardown is specific and manual.**
A capability every command can trigger should not have its lifecycle privately owned by one command.
`connect` didn't add scopes — it just happens to be the only thing that cleans up after itself.

## The direction: record a scope as an entity on first scoped *write*

**Any write carrying `--scope` records the scope in the baseline verse, if not already present** —
materializing a scope registry out of the same substrate as everything else: entities.

- A `scope:<name>` entity (reserve a `scope` system so it can't collide with a user system),
  carrying metadata: created rev, creating client/identity, a label/purpose, kind
  (**nonce** ephemeral vs **named** durable), maybe last-write and a lease/heartbeat.
- Recorded **in the baseline**, so it is discoverable from anywhere regardless of which scope a
  client is in.
- Recorded **on write, not read**: a read *through* a scope materializes no overlay; only a write
  does. Recording on first write mirrors the store's own materialization point — the registry entry
  is born exactly when the overlay is.
- Because it is just an entity, scope lifecycle becomes **reactive** like the rest of the verse: a
  reaper trigger can drop nonce scopes whose lease has lapsed; a goal can own a scope for the length
  of a task. (Mind the ignition guard — a `scope:*` registry should not accidentally self-trigger.)

## An API + CLI on top

Generalize `connect`'s private teardown into a first-class surface, over the same
`ScopeDeleter`/engine-cancel machinery `deleteScope` already uses:

```
verse scope list                 # enumerate scopes (registry entities) + metadata
verse scope get <name>           # one scope's record (+ entity counts, engine state)
verse scope delete <name>        # stop the scope engine, DeleteScope the overlay, drop the record
verse scope create <name>        # (optional) pre-register a scope with no writes yet
```

Wire: promote the ad-hoc `POST /scope/delete?scope=` into a proper resource —
`GET /scopes`, `GET /scopes/{name}`, `DELETE /scopes/{name}` (and maybe `POST /scopes`).

`connect` then becomes **a client of this**, not its owner: its nonce scope is one whose record is
auto-created on the first metadata write and auto-deleted on disconnect — the same
`verse scope delete` path, no bespoke code.

## Open questions

- **Where the recording happens.** Cleanest at the store seam — `Store.Scope(name)`'s first
  materializing write records the baseline registry entry — so it is uniform across *all* writers
  (CLI, `connect`, triggers). But a scoped store writing into the *baseline* registry is a layering
  wrinkle: the scoped view needs a baseline handle. The alternative is to record in the write plane
  (`versed`'s scoped mutators), which misses direct-embedding store clients like `connect`.
- **Idempotency + cost.** Recording must be once-and-cheap: check-and-set (write the record only if
  absent), with a per-client "already recorded this scope" cache so it is not a store round-trip per
  write.
- **Ownership + GC.** Nonce (ephemeral, lease-reaped) vs named (durable, explicit-delete) must be a
  field on the record, so a reaper knows what it may reclaim. A lease/heartbeat from the creating
  client turns "the connector went away" into an observable, collectable fact.
- **Delete semantics.** `verse scope delete` must be the union of what `deleteScope` does today
  (cancel the per-scope engine, `DeleteScope` the overlay) *plus* removing the registry entry — and
  be safe to call on a scope with a live client (fence it, like process cancel).

## What this preserves

The store seam (`Store.Scope` / `ScopeDeleter`) is unchanged; this is a **registry + API layer above
it**. `connect`'s effective behavior (nonce scope, auto-drop) is unchanged — it simply stops being
the sole owner of teardown. See also [connect.md](connect.md) and
[deployment-and-steering.md](deployment-and-steering.md).

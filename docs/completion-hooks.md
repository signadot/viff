# Completion hooks: chaining a process, visibly

An action's `spawn` starts a process, and `onDone` says what to do when it finishes. This
note is about why the hook is a field on the action rather than a second trigger, and what
that costs. The grammar itself is in [the trigger model](trigger-model.md#completion-hooks-one-trigger-chaining-visible);
[tutorial §2](tutorial.md#2-install-a-charter-and-drive-it) installs a charter that uses one.

## What the hook replaces

Without hooks, what happens when a spawned process finishes is invisible in the charter. The
completion step is a second, decoupled trigger that matches `process:instance status=done`
and pattern-matches the subject through the process's `flow`:

```tony
- name: review
  condition:
    when: { system: demo, fields: { stage: open } }
  action:
    patch: { stage: reviewing }
    spawn: { driver: scripted, task: "review $subject" }
- name: reviewed
  condition:
    when:    { system: process, kind: instance, fields: { status: done } }
    subject: demo:pr:*
  action:
    patch: { stage: reviewed }
```

That is a real pattern and it stays supported. But for a plain linear chain — review, then
reviewed, then merge — it scatters one intent across separate rules and makes the author
reason about the reflected `process:instance` namespace. A hook collapses it:

```tony
- name: review
  condition:
    when: { system: demo, fields: { stage: open } }
  action:
    patch:  { stage: reviewing }
    spawn:  { driver: scripted, task: "review $subject" }
    onDone: { patch: { stage: reviewed } }
```

Reach for the decoupled form when a completion should fan into several reactions, advance a
different subject, or be authored by someone else. Reach for the hook when the chain is
linear and owned by one trigger.

## One field per terminal status

There are four hooks — `onDone`, `onFailed`, `onCancelled`, `onOrphaned` — matching the four
terminal statuses in `process/status.go`. Each is itself a full `Action`, carrying `patch`,
`spawn`, `seq`, and its own hooks, so chains nest.

They are named fields rather than an `on map[status]*Action`, and the reason is mostly tony.
A `map[process.Status]*Action` will not compile, because tony-codegen hardcodes
`map[string]…` keys with no key conversion; `map[string]*Action` compiles but is opaque in
the generated schema and loses the status typing. As four fields, each is structurally
identical to the existing `Spawn *launch.Spec`, so the struct stays fully generated — no
custom codec, no hand-written `UnmarshalJSON`. Validation shrinks to nothing: an author
literally cannot write a non-terminal hook, because there is no such field.

The trade is that a new terminal status would need a new field and a regeneration, which a
stable four-member enum does not, and that a misspelled `onDoen` is silently dropped like any
other unknown field.

## How a hook finds its process

Per-spawn scoping is free, because the engine already tracks every spawned process in an
in-memory registry keyed by process id and carrying the subject. Storing the hooks alongside
that entry means completion is matched to the exact process this trigger spawned — no marker
on `process:instance`, no subject-pattern re-derivation.

The completion signal itself has to be race-free, which is subtler than it looks. `versed`
always runs `Engine.ObserveScoped`, never `Engine.Start`, so the store-driven dispatch loop
is dormant in production and runs only in unit tests. The one completion signal that mode
would otherwise have is a bus subscription, which is live-from-now with no replay — racy for
a process that finishes fast. A leaked registry entry would be cosmetic, but a missed hook
would stall the chain. So hooks ride a dedicated `process:instance` subtree watch opened
before any spawn: `WatchSubtree` replays current entities and then delivers edges, and old
terminal entities on replay are harmless because the registry gate rejects untracked ids.
Whichever path wins an atomic delete from the registry is the one that applies the hook, so a
duplicate or replayed terminal applies the effect once.

A store that is not a `SubtreeWatcher` degrades to the racy backstop. Both `MemStore` and
`LogdStore` implement it.

## Addressing: the chain is a kpath

A process's id is `<chain>@<subject>`, where the chain is the
[kpath](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath) of the action
inside its trigger. A hook descends by field name:

| level | chain | id for `demo:pr:1` |
|---|---|---|
| top `review` | `review` | `review@demo:pr:1` |
| `onDone` → merge | `review.onDone` | `review.onDone@demo:pr:1` |
| nested `onDone` → approve | `review.onDone.onDone` | `review.onDone.onDone@demo:pr:1` |
| `onFailed` sibling | `review.onFailed` | `review.onFailed@demo:pr:1` |

The chain is the address of the action inside the trigger rather than a private scheme, which
is what makes it deterministic — and therefore idempotent, since `Spawn` dedups by id. It is
also published on the process entity as `chain`, with its root as `trigger`, so "every
process of this trigger" is a payload predicate rather than a glob over ids. The `@` join
replaced an earlier `-`, which could not be decoded back because trigger names spend `-` too.

Resume rehydrates hooks from that chain: `AdoptChain` navigates the installed charter to find
the action again. The hooks were never the thing to persist, only the address.

## Validation

`onX` requires a `spawn` or a `seq` — a completion hook needs something that can complete —
checked at install, recursively, through `AddTrigger`. Goal-owned triggers install through the
same path, so they are covered.

## Limitations

`onOrphaned` is inert, and rehydration did not change that. Orphaning and adopting are the two
alternatives in reconcile: a process that can be re-attached is adopted and never marked
orphaned, and one that cannot is orphaned and never tracked. So no tracked process is ever
marked `orphaned`, whatever its hooks. Making it fire would mean tracking a process whose
runtime is gone — one with no handle — which is a different change. `onDone`, `onFailed` and
`onCancelled` all work within a run.

If nothing is tracked, no hook fires. When the spawner returns nil, or a provider selection
does not resolve, there is no process, so there is correctly nothing to complete.

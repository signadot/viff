# LLM memory: what a doer remembers between spawns

Moved from `docs/working/work/llm-memory.md` (last at `0cba64af`); the note follows whole, its relative links as they were written there.


Status: proposal, nothing here runs.

Every LLM-driven task starts blank. `llmDriver.messages` lives only for the run
(`task/harness/llm.go`); when the task ends, whatever the model figured out about the
task, the codebase, or its own past mistakes ends with it. The next spawn — even the next spawn
of the *same doer* — starts from the same frame and learns the same lessons again. This note
proposes closing that gap by giving a doer a small, durable memory backed by the entity store,
keyed to the doer's own identity rather than to which LLM vendor happened to answer.

## The gap, precisely

Nothing today persists a conversation. `mirror.go` reflects a task's status (`doer`,
`grant`, `tools`, lifecycle) into `verse.task.instance.<id>` — durable, but index-only and
one-way, built for "observe another task," not "recall what a task learned"
([change events](../../change-events.md) is why an event is a level, not a transcript). There is
no embeddings/vector-search path anywhere in verse to build on: the one `Vector` type in the
codebase is the gate/autonomy dial (`task/vector.go`), unrelated. So this note is scoped
deliberately small — a doer keeping a handful of notes about itself, not similarity search over
history. If the real want turns out to be "surface roughly related past incidents," that is a
different (and much bigger) feature than the one proposed here.

## Keyed by whom, not by which model answered

"Provider" already means something in verse, and it isn't "Anthropic vs OpenAI." The LLM loop
itself has no provider abstraction at all — it speaks one OpenAI-shaped Chat Completions dialect
through the model gateway, and swapping vendors is an environment variable
(`<GATEWAY>_BASE_URL`/`OPENAI_MODEL`), not a code path. "Provider" in verse means the who-axis:
a governed selection among `provider.<capability>.<id>` entities
([provider selection](../../provider-selection.md)). That is the identity this note keys memory
to, because it is already the identity that answers "which doer is this, across runs" — the
LLM-vendor sense answers a different and less useful question (every doer sharing a vendor
would share a memory).

The hook already exists, one layer short of the loop. `launch.Fragment.ID` is documented as
"the doer's own identity... an entity ref (`provider.treat.fixer`) for a registry doer, the
bare name for an inline `eligible` list." It survives `Spec.Overlay` into `Spec.Doer`
(`launch/launch.go`), resolved in `trigger.go` before a task is ever built. Today its only
consumer is `mirror.go`, which stamps it onto the task entity as a status field — pure
provenance, read by nobody. `task.Launch` (the runner-facing subset of a spec,
`task/runner.go`) does not carry `Doer` at all, and `launchOf` (`versed/server.go`), the one
place that builds a `Launch` from a `Spec`, does not copy it across — not a deliberate exclusion
(unlike `vector`/`grant`/`environment`, which `launchOf`'s own comment explains), just a field
nobody has needed yet. `harness.Config` (`task/harness/build.go`) has no `Doer` field either.
Closing that chain — `Spec.Doer` → `task.Launch.Doer` → `harness.Config.Doer` — is the one
piece of wiring this proposal actually requires; everything else is new, additive code.

**Corner:** an inline `eligible: [fixer, memo]` doer has no entity behind it — `Doer` falls back
to the bare name. Two unrelated charters that happen to both write `eligible: [fixer]` would
share a memory entity though nothing else connects them. That's a naming-discipline problem,
not one the substrate prevents; worth a convention (e.g. memory only for registry doers, or a
namespacing rule) before this ships, not after.

## What the store actually lets you do

First pass at this design assumed `entity.Write` was a whole-payload replace, which is wrong and
worth correcting precisely because it changes the shape on offer.

A write addresses a `Path{Ref, Field}` and **merges** at it — a field written on its own leaves
its siblings untouched, at any depth (`entity/write.go`, `TestWriteDeepField`). That's not a
bespoke merge verse invented: `entity.Write` is a real tony patch, executed through
`go-tony/mergeop` — a value whose tag names an operation is *interpreted*, not stored, which is
the whole reason `Raw()`/`AsData()` exist (`entity/raw.go`). The documented vocabulary
([matching and patching](https://signadot.github.io/tony-format/matchpatch/)) includes `all`
(apply to every array/object member), `key` (treat an associative list as an object, keyed by a
field — real element-wise list merge), `dive`, and `raw`. Nothing in verse uses `key` today; it
is a real, documented capability with zero mileage on it, not a foundation to build on for a
first version.

What that leaves, practically:

| operation | mechanism | contention |
|---|---|---|
| write one named field, independent of others | plain `Write{At: ref.At(field), Value: ...}` | none — different fields never collide |
| overwrite one field two writers might race on | plain `Write` + a `Require` precondition | last-writer-wins unless guarded |
| append to a growing list/log | `entity.Modify` (read-modify-write, CAS retry) | handled — "two writers to one ref cannot lose an update" |
| append without a read-modify-write round trip | tag the list `!key` for associative merge | undocumented in practice — nobody has tried it |

This proposal takes the CAS row as the *only* write path, uniformly — see below for why that's
affordable once memory is leveled per key rather than shared across one entity.

## Shape

Leveled, not flat: a doer's memory is a namespace of top-level keys — `pitfalls`,
`style_notes`, whatever the doer decides is worth naming — each its **own entity**, not a field
inside one shared payload.

```tony
memory.note.provider.review.claude.pitfalls
  "flagging `err != nil` after a stub return has cost two false positives; check callers"

memory.note.provider.review.claude.style_notes
  "this repo's reviews read terse; skip restating what the diff already says"
```

Two things this buys over one entity carrying many fields, and both are about concurrency, not
aesthetics:

- **`Store.List(pred)` is the directory listing.** Enumerating a doer's keys (`ID` has the
  prefix `<doer>:`) is one query, and the frame injection below is built from it directly.
- **A CAS retry on one key never contends with a write to a different key.** `entity.Modify`'s
  precondition is "this entity's payload is unchanged since I read it" (`write.go`), scoped to
  whatever single `Ref` it's called on. Fold every slot into one shared entity and a `Modify` on
  `pitfalls` starts retrying against a concurrent write to `style_notes` too, for no reason — two
  sub-agents under one doer noting different things about themselves would serialize behind each
  other. Split into one entity per key and that stops being true: contention exists only where
  it's real, between two writers of the *same* key.

A "log" isn't a separate mechanism in this shape. It's just a key whose value happens to be a
structure the doer maintains itself (a JSON array it appends to across its own `remember`
calls), written through the same per-key path as a one-line scalar note — there is no
snapshot-vs-log fork to design around.

## The tools: `recall` and `remember`

`Spec.Doer` is resolved before the task is ever built — `trigger.go` overlays the winning
fragment before calling into the spawner, well before `harness.Build` runs. That's what makes a
cheap push possible: at spawn, `harness.Build` lists `memory:note:<doer>:*` — key names only,
via `List` — and folds that listing into the frame at the same seam that already appends the
answer-contract clause (`contractClause`, right after `frame` is resolved): *"you have memory
under: pitfalls, style_notes."* Cheap, and unlike a tool call, it can't be skipped.

Content itself is pull, not push, and that's a scaling argument as much as a design one — once
memory is leveled into an open-ended set of keys, injecting every key's content into every frame
stops being cheap the moment a doer accumulates more than a handful. So:

- **`recall(key)`** — a new tool (`toolDefs()` in `task/harness/llm.go`, dispatched in
  `task/harness/exec.go`) that does one `Get` on `memory:note:<doer>:<key>` and returns its
  payload. Uncontended, no CAS — a read is a read.
- **`remember(key, value)`** — the write side, through `entity.Modify` against the same ref:
  read, apply the new value, write under a precondition that nothing changed since the read,
  retry on conflict. Always CAS, even for keys that will almost never see a concurrent writer —
  at the write frequency memory implies, the redundant check costs nothing, and it's the
  difference between a guarantee and something that happens to hold until it doesn't.

This does mean `harness.Config` (and `exec.go`'s executor) need an `entity.Store` handle they
don't have today — the harness package currently has no dependency on `entity` at all. That's
the real new edge this proposal adds to the dependency graph, and worth naming rather than
discovering later.

## Authority: a memory tool must derive effects, or it does not work

Not a corner — a prerequisite, and the reason is structural. `Grant.Permits` is fail-closed by
design: *"an action with no derived effects matches no verb predicate and is refused under any
non-wildcard grant"* (`task/authority.go`). Tools become typed capabilities only by way of
`Derive` (`task/harness/exec.go`, `task/deriver.go`), so a `remember` that no deriver
classifies produces no effects, matches nothing, and is **refused for every doer carrying a
named grant** — working only under `grant: all`. The feature would silently be absent for
exactly the governed doers it is most useful to.

So `recall`/`remember` need a resource kind of their own — `KindMemory` alongside `KindFile`,
`KindTask`, `KindAgent` (`task/effect.go`) — and a `Derive` case emitting
`Effect{Verb: Read, Resource: Memory(key)}` and `Effect{Verb: Write, Resource: Memory(key)}`.

What makes this cheap is that the named grants match on **verb alone**, with no resource
predicate (`namedGrants` in `task/authority.go`). Nothing in the vocabulary needs editing;
every existing name already answers sensibly:

| grant | `recall` | `remember` |
|---|---|---|
| `all` | ✓ | ✓ |
| `no-delegate` | ✓ | ✓ |
| `read-only` | ✓ | **refused** |
| `none` | refused | refused |

`read-only` permitting recall and refusing remember is worth *adopting* rather than inheriting
by accident: a doer told read-only may consult what it has learned and may not persist anything
new, which is the right reading — memory is durable state that shapes this doer's own future
spawns, so it belongs on the write side of that line. The consequence to accept with it is that
memory accumulates only for doers with write authority.

**What this does not buy.** Under `no-delegate`, `remember` and `write_file` are
indistinguishable — both are `Write` — so a charter cannot say "edit files but do not persist
memory". Gate vectors *can* express exactly that (`ResourcePred{Kind, Pattern}`) as soon as the
kind exists; the named grants simply don't use resource predicates today. Whether the grant
vocabulary should grow a name that separates them is a real question and out of scope here — it
runs into the same structured-grant frontier
[provider selection improvements](../residuals.md) (`5ny7rn0r`) parks at.

## Corners

- **Scope — settled: use whatever store the task was handed.** A task running under a
  dev/sandbox `Store.Scope` ([scope lifecycle](../../roadmap/scope-lifecycle.md)) reads and writes
  memory through that scope, and this needs no special-casing because copy-on-write already has
  the wanted semantics: reads see the baseline layered with the scope, writes land in the scope
  (`entity/store.go`). A sandboxed run therefore consults production memory and cannot
  contaminate it — which is the behaviour you would design if `Scope` didn't already do it.
  Passing the scoped store is the whole implementation.
- **Inline `eligible` doers get no memory in the first slice.** Per the corner above, an inline
  name has no entity behind it and two unrelated charters writing `eligible: [fixer]` would
  collide in one memory namespace. Registry doers (`provider.<cap>.<id>`) have unambiguous refs;
  inline names don't, and an inline name "contributes nothing else" anyway
  ([provider selection](../../provider-selection.md)). Withholding memory from them costs nothing
  now and avoids inventing a namespacing rule nobody needs yet.
- **Growth and curation is a narrower worry than it first looks.** "An index, not a warehouse"
  (`entity/entity.go`) is a statement about entities that index an *external* system — a PR, a
  Linear issue — where the real content lives elsewhere and the entity is a pointer to it.
  Memory has no external referent: the entity **is** the record, so that constraint doesn't bind
  it, and the store is free to hold whatever internal-native shape a doer's memory needs. The
  real cost is a different, more mechanical one: events are levels, not deltas (`entity.go`'s
  `Event.To` carries the whole payload as of each commit, see [change events](../../change-events.md)),
  so a key whose value keeps growing makes every touch of it replay the whole growing blob to
  anything watching. That's a reason to keep any one key's value bounded — cap it, or have the
  doer rewrite rather than endlessly append — but it's an observability cost to manage, not a
  violation of the store's own design stance.
- **Livelock under CAS.** `remember` always writes through `entity.Modify`'s retry loop, and
  leveling per key keeps contention rare by construction — but a bare retry loop with no backoff
  is a real livelock shape the day it isn't rare (several sub-agents under one doer hammering the
  same key). The retry needs jittered backoff and a retry cap that surfaces as a tool error past
  some ceiling, the same way a gate denial already comes back as one (`resultContent` in
  `llm.go`) — a real mechanical detail this note flags but doesn't design.
- **Not recall-by-similarity.** Repeating the scope note up top because it bears repeating: even
  leveled into named keys, this is browsable structure, not embeddings. A directory listing
  tells a model what topics exist; it does not tell it which past note is *relevant* to the task
  in front of it. If the goal drifts toward "what did we do the last time something like this
  happened," this design does not get you there.

## Staging

Smallest slice first: thread `Doer` through (`Spec` → `task.Launch` → `harness.Config`),
inject the key listing into the frame at spawn, add `recall`/`remember` against per-key entities
with CAS writes from the start. The leveled shape and the CAS write aren't separable extras to
add later — leveling is *why* uniform CAS is affordable (see Shape), so shipping one without the
other would either serialize unrelated writes behind one shared entity or write without a
guard. The one thing genuinely worth deferring is the `!key` associative-merge trick: it earns
its keep only for a key whose value is itself a collection a doer wants merged element-wise
rather than replaced, and nothing here needs that yet.
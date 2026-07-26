# Plan: kpath as the shape of a process flow

## Context

A trigger that spawns, and whose spawn carries completion hooks, makes a *tree* of processes
about one subject. Today that tree is encoded in a hand-rolled string:

```go
// trigger/engine.go:43-45
chain string // "the trigger name at the top, then name:done, name:done:done, …"
// trigger/trigger_api.go:90
func spawnID(name string, ref entity.Ref) string { return name + "-" + safeSeg(ref.String()) }
```

giving `review-demo:pr:1`, `review:done-demo:pr:1`, `review:done:done-demo:pr:1`. It is
deterministic and collision-free (an action has one `spawn`, a process reaches one terminal
status, so live chains form a *path* — uniqueness is structural, not enforced). But it is a
private scheme with three defects:

1. **The `-` join is ambiguous.** Trigger names carry `-` routinely (`ship-reviewed`, tutorial
   §4), so `ship-reviewed-demo:pr:1` cannot be split back into (chain, ref): first-`-` is wrong,
   last-`-` breaks on `hotfix-1`. The id is deterministic but not *decodable*.
2. **`:` does two jobs at two depths** — chain descent and ref segments — so the boundary is
   invisible without already knowing the trigger name.
3. **Nothing can query it structurally.** `!glob` (landed for `id`) is `filepath.Match`, whose
   structural character is `/`, and `safeSeg` rewrites `/` → `_` because a process id must be one
   URL path segment (`trigger_api.go:101-107`). So a spawn id is one flat glob segment.

Meanwhile the chain is not really a naming scheme at all. **It is the path of the spawning
action inside the trigger's own `Action` tree** — which is exactly what a
[kpath](https://signadot.github.io/tony-format/) addresses, and which verse already speaks
(`entity.MountKPath`, `entity/logdstore.go:204`).

| charter position | kpath |
|---|---|
| the trigger's own spawn | `review` |
| `action.onDone` | `review.onDone` |
| nested `onDone.onFailed` | `review.onDone.onFailed` |
| `action.seq[2]` (proposed `seq`) | `review.seq[2]` |
| that step's `onDone` | `review.seq[2].onDone` |

What adopting it buys, none of which is new machinery:

- **Typed nodes come from the format.** kpath "encodes both navigation and structure type in the
  syntax" (`ir/kpath/doc.go`): `.field` for a field, `[n]` for a dense array element, `(name)` for
  a keyed one. A `seq: []Action` *is* a dense array.
- **The tree assembles itself.** `Parent()`, `IsPrefix()` and `Compare()` give nesting and sibling
  order, so the flow-tree view is a fold over sorted kpaths, not a parser.
- **A chain is a lookup key into the charter.** Given `review.seq[2].onFailed`, you can fetch that
  exact `Action` from the installed trigger. Nothing today can do this.
- **`par` needs no new syntax.** `review.seq[0]` vs `review.par[0]` — the *field* types the node,
  the bracket only positions it.
- **One wildcard vocabulary** for queries (`review.seq[*]`, `review.*`) instead of inventing one —
  and as of go-tony **v0.0.92** it is matchable: `Matches` / `MatchesPrefix` are one-directional
  (pattern → target) and kind-strict, and `HasWild` answers the whole-path question. So
  `review.seq[*]` matches `review.seq[2]` and prefix-matches `review.seq[2].onDone`, with no
  hand-rolled matcher here. **Prerequisite: bump `go.mod` from v0.0.90 to v0.0.92.**

## The design issue this turned up: kpath bracket syntax is shell-hostile

Process ids are pasted into commands constantly (`verse process show <id>`, `verse gate resolve`,
`verse process events`). Under zsh — the shell in use — two of kpath's three bracket forms fail
*before verse runs*:

| id | zsh |
|---|---|
| `review.seq[2]@demo:pr:1` | `zsh:1: no matches found` ✗ |
| `review.seq(2)@demo:pr:1` | `zsh:1: no matches found` ✗ |
| `review.par{0}@demo:pr:1` | passes ✓ (but sparse-array is the wrong kind semantically) |
| `review.seq.2.onDone@demo:pr:1` | passes ✓ |
| `review.onDone@demo:pr:1` | passes ✓ |

This is not a new principle; it is one the codebase already states. `shortID` is hex
specifically so a generated id "stays free of anything the surfaces it crosses read as
punctuation — a URL's `#` or `?`, **a shell's globs**, and `process.GateSep`"
(`cmd/verse/process.go:174-177`), and `client.go:41-51` records the `#`-truncation bug that
motivated it. Requiring quotes on the everyday path contradicts it.

(URL routing is *not* the constraint — verified: `[2]`, `{0}`, `(lint)`, `[*]` and `.*` all
round-trip raw and percent-escaped through `ServeMux` `/processes/{id}/claim`. Only the shell
objects.)

### Resolution: one chain, two renderings

- **Canonical form — a kpath**, `review.seq[2].onDone`, published on the process's mirror entity
  as `chain`. This is what the tree, the charter lookup, and any structural query use. It never
  crosses a shell.
- **Id form — dotted, shell-safe**, `review.seq.2.onDone@demo:pr:1`. Brackets flatten to `.`.

The flattening is lossless *because the schema types the segments*: `Action`'s field set is
closed (`set`, `spawn`, `seq`, `par`, `onDone`, `onFailed`, `onCancelled`, `onOrphaned`), so a
segment following `seq` is an element and a segment matching a hook name is a field. Retyping
`review.seq.2.onDone` → `review.seq[2].onDone` is a walk of the `Action` schema, not a guess.

Rejected alternatives:

- **Put the kpath in the id and require quoting.** Contradicts the stated id principle on the
  everyday path, and the failure mode (`no matches found`) does not point at the cause.
- **Keyed form `(name)` everywhere.** Same zsh failure.
- **Sparse `{n}`.** Survives zsh, but claims a sparse array where there is a dense one — buying
  shell-safety with a lie about the node's kind.
- **Percent-encode brackets in the id.** `review.seq%5B2%5D@…` is neither legible nor paste-safe.

Wildcard *queries* (`'review.seq[*]@demo:pr:*'`) do need quotes — but wildcards on a command line
always do, and the tutorial already quotes them (`verse flow pause 'demo:pr:1/**'`). It is
concrete ids, the ones copied out of output, that must be clean.

## Decisions

1. **Id** = `<dotted-chain>@<safeSeg(ref)>`. `@` bounds chain from subject; split on the **first**
   `@`, which is unambiguous because chain segments cannot contain one.
2. **Trigger names are validated at install** as bare kpath identifiers — no `.`, `@`, `[`, `]`,
   `(`, `)`, `{`, `}`, quote, or whitespace. This keeps the id quote-free by construction and
   sidesteps `token.KPathQuoteField`, whose `token.Quote` output (`"`) is not legal unescaped in a
   URL path. It also closes the existing latent collision where a trigger named `review:done`
   produces the same id as `review`'s `onDone` hook.
3. **The canonical chain is a kpath string** carried on `flowProc` and published on the mirror
   entity.
4. **The mirror publishes `trigger` and `chain`** (and, once `seq` lands, `step`/`steps`), so
   chain queries are plain `fields` equality rather than id globbing.
5. **The flow tree is an API response, never mirrored entities** — materializing it would rebuild
   the `flows[subject]` index that migration step 4 deliberately replaced with a query.
6. **The lossy-rewrite hash gets its own marker**: `…@demo:pr:a_b+1f2c3d4e`, so `-` stops carrying
   structural meaning anywhere in the id.

## Implementation

### 1. Id construction — `trigger/trigger_api.go`

- `spawnID(chain string, ref entity.Ref) string` — `chain + "@" + safeSeg(ref)`, hash suffix
  separated by `+` when `safeSeg` rewrote something. The parameter is a *chain*, not a trigger
  name; at the top level they coincide.
- `chainID(kp string) string` — kpath → dotted id form (strip brackets to `.`).
- `chainKPath(id string, a *Action) (string, error)` — the inverse, walking the `Action` schema.
  Needed by the tree builder and by resume; a test pins the round-trip.
- `validTriggerName(string) error`, called from `AddTrigger`.

### 2. Chain descent — `trigger/engine.go`, `trigger/trigger.go`

`applyHook` currently descends by *status*: `fp.chain + ":" + string(status)`
(`engine.go:1041`). The kpath segment is the *field name*, so it descends by hook field
instead — `review.onDone`, not `review.done`. `hookFor` already switches status → field; add the
paired `hookField(process.Status) string`. This is the one behavioural rename inside the chain:
`done` → `onDone` etc.

### 3. Publishing the chain — the ripple worth knowing about

`mirror.Reflect` builds the payload and **keeps the map it Put** (`mirror/mirror.go:50-65`),
re-Putting it on every process event. So the engine cannot add `chain`/`trigger` with a later
`Modify` — Reflect's next Put clobbers it. The fields must reach Reflect, and Reflect is called
inside the `Spawner` (`versed/server.go:212`), which receives only `(id string, spec launch.Spec)`.

Two ways through, and the first is cheaper:

- **Carry them on `launch.Spec`** as engine-owned fields, exactly as `Subject` already is
  (`trigger.go:263` sets `spec.Subject`; the charter is not supposed to write it). Reflect
  publishes them alongside `flow`.
- Change the `Spawner` signature — a public type with an implementation in `versed`, so it
  ripples further for no additional benefit.

### 4. Resume — a limitation that becomes fixable

`Adopt` re-tracks with `hooks:nil, chain:""` (`engine.go:966-971`), which is why a chain stalls
across a restart and why `onOrphaned` is inert (docs/completion-hooks.md). With the chain on the
mirror entity, `reconcileProcesses` (`versed/server.go:217-250`) can read it back, and — because a
chain is a *lookup key into the installed charter* — re-derive the hooks by navigating the trigger's
`Action`. **Out of scope for this change**, but it is the reason to prefer kpath chains over any
opaque scheme, and the plan should not close the door on it.

### 5. The flow tree — `GET /flow/tree` (behind `verse flow show <subject>`)

A join of three sources, no stored index:

- **static shape** — the installed trigger's `Action`, walked to enumerate every reachable chain.
  This is what lets the view render steps that have not run yet as `pending` (and is a real
  argument for a declarative `seq` over nested `onDone`: the arity is known before anything runs).
- **history** — `process:instance` entities with `flow == subject`. Load-bearing: `retire` deletes
  finished processes from the live registry (`engine.go:1003`), so history exists *only* in the
  mirror. This is why `chain` must be published there.
- **live overlay** — the registry, for `pending`/`pendingGate`/`lastAction`.

Nodes are typed in the *response* (`root | seq | par | hook`), not by parsing the id — the id's
job is uniqueness and paste-safety; typing the node is the API's.

## Critical files

- `trigger/trigger_api.go` — `spawnID`, `chainID`/`chainKPath`, trigger-name validation.
- `trigger/engine.go` — `flowProc.chain` (now a kpath), `applyHook` descent, `spawnHooked`,
  `spawnLabel` (`:428` — must keep producing the id the spawn will really use; a test already pins
  the two together).
- `trigger/trigger.go` — `hookField`; the `apply` → `spawnHooked` call (`:271`).
- `launch/launch.go` — engine-owned `Chain`/`Trigger` on `Spec`.
- `mirror/mirror.go` — publish `trigger`/`chain`.
- `versed/server.go` — the tree endpoint; later, chain rehydration in `reconcileProcesses`.

## Tests

1. Id round-trip: kpath → dotted id → kpath, over hook chains, `seq` indices and keyed steps.
2. **Paste-safety**: assert a generated id contains no shell-glob metacharacter and no
   `process.GateSep` — this pins the principle that motivated the whole form. (`spawn_test.go:150`
   already does the GateSep half.)
3. Determinism and distinctness: `review`, `review.onDone`, `review.onDone.onFailed` differ; a
   re-fire about the same subject reuses the id (`spawnHooked`'s live dedupe).
4. `spawnLabel` still equals the id `apply` uses (existing test, updated form).
5. Ambiguity regression: a trigger named `ship-reviewed` decodes correctly; `review:done` is
   rejected at install.
6. Mirror carries `trigger`/`chain`; a completion hook's spawn carries the descended chain.
7. Tree assembly: static-shape ∪ history ∪ live, with an unrun step rendered `pending`.

## Verification

`go build ./...`; `go test -race ./...`; `make smoke`. End-to-end on an isolated server
(`--store memory`, spare port): install a nested-`onDone` charter, drive it, and confirm the ids,
the mirror `chain`, and the tree — pasting each id back into `verse process show` **unquoted**.

## Open questions

- **Keyed steps — now leaning against.** If `seq` elements get names, `review.seq(lint)` is the
  kpath and `review.seq.lint` the id. Two costs, one of them new: a step named `2` is ambiguous
  with an index, and — confirmed against v0.0.92 — matching is **kind-strict with no key
  wildcard**, so `review.seq[*]` does not match `review.seq(lint)` and `(*)` parses as a *literal*
  key named `*` that silently matches nothing. A keyed step would therefore be unreachable by any
  wildcard query. Recommendation: keep `[n]` canonical and carry a step's name as a display label
  on the node, revisiting only if upstream adds a key wildcard.
- **Wildcard chain queries** — resolved upstream in **61766xadh12kstqqcnn0** (v0.0.92): use
  `MatchesPrefix` for "everything under this chain" and `Matches` for an exact wildcard shape.
  `fields` equality on the published `chain` remains the cheap path for the common case.
- **Sequencing order.** This change is independent of `seq` and can land first; `seq` then adds
  `[n]` segments to an already-kpath chain. The alternative (land `seq` first) means changing the
  id form twice.
- **Compatibility.** Ids are the dedupe key and the live-registry key, so processes in flight
  across an upgrade will be respawned rather than deduped. Given resume already drops hooks, this
  is rename-scale. Docs to update: tutorial §3 (`spawns: review-demo:pr:1`, line 155),
  `docs/trigger-model.md` §"Completion hooks", `docs/completion-hooks.md` §4's id table.

# Addresses without the document: a plan for `--paths` and `--kpaths`

Status: **not started**. Branch `paths-cli`. Needs a go-tony release that does not exist
yet — see [What this waits on](#what-this-waits-on).

`verse data --paths` prints one address per line. To do it, the CLI asks for the whole
subtree and walks it: `GET /state?at=…` answers "the node the path reaches, with
everything beneath it" (`versed/server.go:1798-1807`), and `printPaths`
(`cmd/verse/client.go:1370`) decodes that body and calls `entity.Doc.Paths`
(`entity/doc.go:364`) to recover the names. The rationale is written in the code, and it
was true when it was written: *"Client-side, because the addresses are already IN the
answer"* (`client.go:1366`).

They are in the answer because verse paid for the answer. A listing of a thousand
records ships a thousand payloads to print a thousand names, and `--depth 2` ships
exactly as much as no bound at all.

go-tony's session protocol can now answer the names alone. A match at a wildcard path
names a set and answers it one node at a time, and `return` says what each answer
carries — `path`, `id`, `iterType`, `body`. **With no `body` in the spec the store reads
no node**: the walk that finds the members already knows their names, and `iterType`
reads only the first event of each member. That is this plan: move the walk to the
store, and leave the CLI printing what it is handed.

## What must not change

The listing's behaviour is pinned by tests, and they are the specification. A
server-side walk has to answer exactly what `Doc.Paths` answers:

- **Everything beneath what was reached**, into payloads, not to some level —
  `TestPathsWalksEverythingUnderTheAddress` (`cmd/verse/pathswalk_test.go:17`) asserts
  the walk goes past the record into its fields. Nothing in a document says where an
  entity ends, which is why the walk does not guess.
- **`--depth` is a ceiling on the number of segments in an address**, `0` meaning no
  bound (`entity/doc.go:372`), not a level to stop at —
  `TestDepthBoundsTheWalk` (`pathswalk_test.go:52`).
- **Addresses reached through arrays get none — and that changes here.** `Doc.Paths`
  recurses into `ObjectType` only (`doc.go:381`), so a listing shows nothing inside an
  array. See [Arrays are listed too](#arrays-are-listed-too): the walk gains them, which
  is the one place it deliberately answers more than it did.
- **A path naming one node does not print that node**, but a wildcard or descent path
  prints each node it reached (`doc.go:376`, `entity/doc_test.go:209`).
- **Dedup** by `RefAt(segs...).String()` (`doc.go:375`).
- **Rendering is the caller's**, and the single choice point is `renderAddr`
  (`client.go:1423`): steps by default (`entity.JoinSteps`), one kinded path per line
  under `--kpaths` (`entity.JoinSegs`). Every `--kpaths` line must read back through
  `--at` as one shell argument — `TestKPathsIsTheListingAsOneToken`
  (`cmd/verse/kpaths_test.go:16`).
- **`--if`/`--if-at` are asked of each node the path REACHED, and never prune inside a
  node they keep** (`versed/server.go:1798-1807`, `cmd/verse/payloadfilter_test.go:160`).
- **Error parity with the document form**: 404 `nothing stands at …`, 503 on an
  unreadable store, and never an empty listing where the truth is a failure —
  `TestPathsRefusesWhatTheDocumentFormRefuses` (`cmd/verse/unreadablepaths_test.go:71`).
  A node holding nothing lists nothing and exits 0 (`:95`).
- **One snapshot.** Today's listing walks one `Match` answer. The paged listing must not
  be a walk over several states; the cursor pins the commit, so this is kept by using it
  rather than re-asking.

## The shape

### The store call

`MatchEachReturning(ctx, path, pattern, "id,iterType", fn)`
(`go-tony/system/libctl/logd_set.go:73`) over `<prefix>.*`, one level at a time:

- **`id`** is the next segment — the name a node lives under, as a value (`a1`, `r1`),
  which is exactly what a `[]string` address is built from. No kpath parsing, no
  re-quoting: verse holds the segment vector and appends.
- **`iterType`** says whether there is anything under the member and which wildcard
  reaches it — `Object` → `.*`, and `api.IterWildcard` spells it. Today's rule ("only
  objects are recursed") becomes "recurse where `iterType` is `Object`", which is the
  same rule said by the store instead of guessed from a materialized node.
- Paging lives inside `MatchEachReturning`; a page's cursor names the commit the set is
  read at, so every level of one listing reads one state.

The walk is therefore: ask a level, take the ids, recurse into the members whose
`iterType` is `Object`, stop at `--depth` segments.

### Where the walk runs: the server

Not the CLI, for three reasons, in order of weight:

1. **Permissions.** A listing is subject to the same visibility as a read. Today the
   filter is a prune of the materialized node (`entity/attribution.go:908`); a listing
   has no node to prune and needs the per-address decision instead —
   `attributed.keep(ref)` (`:494`) against the `placeTrie` from `places()` (`:867`),
   which marks the addresses where visibility changes. `noteHidden` (`:543`) keeps the
   count the server already reports.
2. **Scope.** Reads are COW-overlaid per sandbox; the session that answers must be the
   scoped child (`entity/logdstore.go:1646`, reached through
   `Server.scopedStore`, `versed/server.go:1487`). A CLI-side walk would have to
   re-derive that.
3. **Round trips.** The level-at-a-time walk is many small requests. Over a session they
   pipeline; over HTTP from the CLI they would not.

### The pieces

| where | what |
|---|---|
| `entity/logdstore.go` | `PathsAt(at []string, depth int) ([][]string, error)` — the level walk on `s.sess`, beside `ReadAt` (`:1251`) |
| `entity/store.go` | the capability on the Reader interface (all-required, per the note at `:20-26`) |
| `entity/attribution.go` | `(*attributed).PathsAt` — per-address `keep`, `noteHidden`, `CheckInto` at the prefix as `ReadAt` has it |
| `versed/server.go` | `GET /state` answers a listing when asked for one: the selector, the perms gate (`:1815`), the scope and the error plumbing are already there |
| `cmd/verse/client.go` | `printPaths` prints what the server sent; `renderAddr` unchanged |

**The wire carries segments**, `[[seg, seg], …]`, as `Doc.Paths` answers them
(`doc.go:356`): rendering stays a client choice, and `--kpaths` is not a second request.
`--depth` and the listing flag ride the query, which they do not today
(`client.go:1206` sends neither).

**`Doc.Paths` stays.** It is the oracle the new walk is tested against, and it still
answers for a document already in hand.

### Arrays are listed too

The walk emits addresses inside arrays, which today's does not. `entity.step` already
reads them — `[0]`, `{7}`, and `votes(alice)` where the array carries `!key(voter)`
(`entity/doc.go:212-235`) — so the model addresses them and only the listing pretends
they are not there. A walk that shows less than what a read reaches is the silent
wrongness `--paths` exists to prevent: *"the walk is what makes it impossible to be
silently wrong"* (`cmd/verse/pathswalk_test.go:17`).

An earlier draft withheld positions on the grounds that they rot under an insert. That
does not separate them from anything else: delete an entity and its address is gone too,
and a listing is a snapshot either way. The residual difference is real but narrow — a
dead field address names the same place and reads as absent, while a shifted position
names a *different* element and reads as a success — and it is an argument about acting
on a stale address later, not about what a listing may show.

**Positions cost nothing:** `id` is the index, and `iterType` is `Array` or
`SparseArray`, so they fall out of the same level walk.

**Keyed elements cost a read, for a mechanical reason worth knowing.** `iterType` reports
the keying the SCHEMA gives an array (`!logd-key`), and verse uses none — it marks a
keyed array with `!key(voter)` inside the document, written as data so the merge operator
is not executed away (`entity/doc.go:225-232`). So over the wire verse's keyed arrays
arrive as plain `Array`, and the names of their elements live in each element's key
field: the listing cannot know them without the bodies. Either the walk asks for
`body` at an array (paying there and nowhere else), or verse's arrays adopt logd's keying
and `iterType` answers `KeyedArray` for free. That second one is a data-model change with
its own consequences and is not decided here.

Until then the honest behaviour is: list array members by position, and name a keyed
element by its key only where the body was read anyway.

### `--if` and `--if-at`

The pattern needs the node, so a filtered listing asks `return: "id,iterType"` **with**
the pattern: logd reads each member to test it and still sends no body. The filter
applies where it applies today — to the nodes the path reached, not to every descendant
— so the pattern rides the first level of the walk and the levels beneath are unfiltered.

### Mounts

Controller-mounted slices live under `versemnt`, disjoint from the entity root
(`entity/logdstore.go:121`, and the reasoning above it). So no mount sits under a
`verse.…` walk, and docd passes these reads to logd with the retspec intact — past a
mount it would refuse a spec asking for more than `body`. The rule to encode: a walk
whose prefix is `IsMountOwned` (`logdstore.go:142`) falls back to materialize-and-walk,
which is what every read of a mounted slice does anyway.

## What this waits on

go-tony `v0.0.222` is pinned (`go.mod:10`) and has none of this:
`MatchRequest` there is `{Commit, PathData}` and `MatchResult` is `{Commit, Body}`.
`v0.0.223` has the set match but **not** `iterType`, which landed after it. So this
needs **the next go-tony release**, for:

- a match at a wildcard path answering a set, with `limit`/`cursor` paging;
- `return: path|id|iterType|body`;
- `libctl.MatchEachReturning`, `SetMember.IterType`, `api.IterWildcard`;
- a set read at a past commit listing arrays by that commit's schema.

Bumping is a one-line `go get` — no `replace`, no vendoring. The one coupling: verse
spawns logd and docd with an `o` binary pinned to its own go-tony version
(`cmd/verse/docd.go:22-25`), and a local `replace` disables that auto-install, so
testing against an unreleased go-tony means `verse up --o-bin <path>`.

## Steps

1. **Bump go-tony** to the release carrying `iterType`. No behaviour change; the suite
   must be green before anything else moves.
2. **`LogdStore.PathsAt`** — the level walk, no permissions, no server. Test it against
   `Doc.Paths` over the fixtures in `entity/doc_test.go:182-209`: same order, same dedup,
   same `--depth` behaviour, and the same addresses **plus** the ones inside arrays. The
   oracle holds everywhere the old walk had an opinion; where it had none, the new
   addresses are the deliberate difference and are asserted on their own.
3. **Attribution** — `(*attributed).PathsAt` with per-address `keep` and `noteHidden`.
   Test that a principal who cannot read a place does not see its address, and that the
   hidden count matches what a document read reports.
4. **The route** — `GET /state` answers a listing when asked. Same 404/503/403, same
   scope, same `--if`.
5. **The CLI** — `printPaths` becomes a printer; `--depth` ships in the query. The
   existing tests must pass **unchanged wherever no array is involved**: that is the
   parity proof. A fixture holding an array now lists more, and those expectations change
   on purpose — `kpaths_test.go:16` counts lines, so check what its fixtures hold before
   assuming the count still stands.
6. **The cost proof** — a test that a listing of N records does not transfer N payloads.
   The honest measure is bytes off the wire or reads at the store; a counting session
   wrapper around `sess` is the cheapest place to see it.

## Verification

- `go test ./cmd/verse/ ./entity/ ./versed/` — the pinned suite, unchanged:
  `pathswalk_test.go`, `kpaths_test.go`, `unreadablepaths_test.go`, `descend_test.go`,
  `payloadfilter_test.go`, `flags_test.go`, `entity/doc_test.go`.
- By hand, against a store with a wide container: `verse data demo pr --paths` and
  `--kpaths --depth 3`, and every line read back through `--at`.
- The listing of a subtree larger than one page (>1000 members) comes back whole, in
  order, with no address twice.

## Open questions

- **An unbounded listing is unbounded work.** `--depth 0` walks to the leaves, a level of
  requests at a time. Does the server cap it and say so, or is a listing as expensive as
  the reader asked for? Today's version pays one big read instead, so this is a change in
  where the cost lands, not a new cost.
- **`verse charter --paths`** walks a server-built charter tree
  (`cmd/verse/charter.go:238`), not the store. It is untouched here; if the two listings
  should share a renderer, that is a separate cleanup.
- **Should verse's arrays carry logd's keying?** Decided for this plan: no, not here. The
  walk lists array members by position and names a keyed element only where a body was
  read anyway (see [Arrays are listed too](#arrays-are-listed-too)). Teaching verse's
  arrays `!logd-key` would make `iterType` answer `KeyedArray` and the names free, and it
  is a data-model change with its own consequences — worth asking on its own, not as a
  side effect of a listing.

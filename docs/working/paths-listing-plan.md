# Addresses without the document, and addresses inside arrays

Status: **steps 1-7 done** — go-tony is in, the listing spells an element, the store answers a
listing (`LogdStore.PathsAt`), permission filters it per address, the route answers it and the
CLI prints it. What remains is dropping the LOCAL ONLY `replace` when the go-tony fixes it needs
are released — see that commit — and tony-format `3kgxprskh12krjrmndn0`, which is what still
holds a listing to the read budget. Branch `paths-cli`.

Two changes, and they are independent:

1. **The listing comes from the store.** `verse data --paths` downloads the whole subtree
   and walks it client-side; the substrate can now answer the addresses alone.
2. **The listing can spell an element.** `--paths` stops at an array, so it shows less
   than a read reaches.

The second is why this plan was rewritten. Its conclusion is not what the first draft
assumed: **the address model does not change.** What changes is what a *listing* carries.

## The address model already spells an element

`entity.Path` is `Ref` + `Access` (`entity/write.go:20-32`): the names, and below them a
kpath fragment reaching into an array — `votes[0]`, `votes(scott).choice`. `TargetOf`
(`write.go:208-247`) cuts a step vector at the first access, names above it, fragment
below. This is not aspirational: a read takes it (`cmd/verse/arrayaccess_test.go:27` runs
`data demo pr 1 votes '[0]'` against a real server) and **so does a write** — `writePath`
(`entity/logdstore.go:826`) appends the access with no dot, and `arrayaccess_test.go:49`
pins that the element changes and the array stays an array.

The design note on `Access` says why it rides beside the Ref rather than inside it
(`write.go:26-30`): a path is what ownership claims and the reflector are written in, and
*"is `votes[0]` disjoint from `votes[1]`"* is a question those would then have to answer.
That reasoning holds, and this plan keeps it. Its last clause — *"Nothing but a command
line addresses an element"* — is what changes: a listing will too.

**What cannot spell one is the listing**, in exactly three places:

- `Doc.Reached` (`entity/doc.go:310`) — its walk has cases for `Descend`, `Field`,
  `FieldAll` and nothing else: *"A node reached through an array has no address of names,
  and is not among them."*
- `Doc.Paths` (`doc.go:364`) — recurses only where `n.Type == ir.ObjectType` (`:380`), and
  answers `[][]string`: a vector of **names**.
- `renderAddr` (`cmd/verse/client.go:1423`) — `JoinSegs` or `JoinSteps`, both of which
  treat every element as a name. `quoteStep` (`write.go:703`) goes further and
  deliberately quotes an access *into* a name, so that a listing line can never be misread
  as one: `{"demo","pr","1","[0]"}` prints `demo pr 1 '"[0]"'`, pinned by
  `entity/segs_test.go:167`.

## What changes

**The listing carries `entity.Path`, not `[]string`.** `Doc.Paths` answers `[]Path`; the
store-side walk answers the same; the dedup key becomes `Path.KPath()` rather than
`RefAt(segs...).String()`.

**`--kpaths` is free.** `Path.KPath()` (`write.go:65-74`) already spells
`demo.pr."1".votes[0].choice` — names joined, access appended with no dot.

**`--paths` needs one new renderer, and `inSteps` is not it.** `inSteps`
(`client.go:1304`) glues the access to the preceding word — `demo pr 1 votes[0]` — and
that line does **not** read back: `KPathOf` (`write.go:168-194`) tests the first character
of each step, so `votes[0]` is not an access, falls to the default arm, and is quoted into
a *field* named `votes[0]`. For the `removed …` line it prints, that is cosmetic. For a
listing it would be a lie, because every printed line must read back through `--at`
(`cmd/verse/kpaths_test.go:16`) and through a shell (`entity/segs_test.go:204`).

The spelling that reads back is the one the read already takes: **the access as its own
step word**, which is how `arrayaccess_test.go` types it.

    demo pr 1 votes [0]              JoinStepsAt(Path{Ref: …votes, Access: "[0]"})
    demo pr 1 votes [0] choice       Access "[0].choice", split into its own steps
    demo pr 1 '"[0]"'                unchanged: a FIELD named [0] (quoteStep)

So: add `JoinStepsAt(Path) string` beside `JoinSteps`, which renders the names with
`quoteStep` and then each segment of the access fragment as its own step word. `JoinSteps`
and `quoteStep` are untouched, and `segs_test.go:167` stays green — the listing never puts
an access into a name vector, which is the whole point of keeping the two apart.

**The walk descends arrays, by position.** Where today's recursion stops at a non-object, it
continues: each element contributes an address whose Access is `[i]`, and anything under an
element extends that fragment. Identity is out — see [One keying](#one-keying).

## What does not change, deliberately

**`Ref` stays names-only**, and the access never enters it. This is not tidiness; the Ref
is load-bearing as a *name* vector in places that would each need their own answer:

- **Perms are keyed on the rendering.** `PermsRef` (`entity/attribution.go:35`) stores a
  record at `verse.perms.node.<Ref.String()>` — the address as one segment — and readers
  recover it with `ParseAddr`, which refuses a non-name. A key that failed to parse would
  be *silently dropped* from the place trie (`places()` `:874`, `recordsBeneath` `:635`),
  and `forget` (`:1381`) scans those keys by textual prefix.
- **The place trie is a name trie** (`:830-903`) and `prune` (`:908`) descends objects
  only. Element-level permissions are not merely unimplemented — the narrowing walk cannot
  reach one.
- **Identity tokens flatten segments.** `SegToken`/`SafeSeg` (`entity/token.go:44-90`) has
  no rewrite for `[`, and task ids are built from segments (`spawnID`,
  `trigger/trigger_api.go:410`). `trigger/chain.go:17-27` already had this exact problem
  and solved it by flattening `review.seq[2]` to `review.seq.2`.
- **Watch subtrees, ownership claims and suspensions** all parse addresses through the
  name-only readers (`PathSegs`, `ExprSegs`, `StepNames`), each with its own refusal text.

An element's **visibility is its holding node's**, and that needs no new machinery: the
perms check on a write already ignores the access (`attribution.go:251-252` takes
`w.At.Ref`), so a listing filtered per address is filtered at the node that holds the
array — which is what `keep(ref)` already answers.

## The asymmetry this creates

`KPathAdmits` (`entity/doc.go:443-453`) answers false for an index or a keyed element,
*"since no address is spelled that way"*. It backs `Reaches`, `Commit.Touches`,
`Subtree.covers` and `versed.selector.matchRef` (`versed/server.go:1567`). So **a watch at
an element delivers nothing, while `/state` at the same path answers** — and
`walkChange` (`entity/change.go:49-51`) stops at the node holding the array on purpose: *"a
change inside one is a change to the node it is inside, and an address cannot name
further."*

That matters here because the two faces are pinned to agree:
`TestStateAndWatchNameTheSameSet` (`versed/payloadfilter_test.go:496`), and the comment at
`versed/server.go:1546` says they must agree *"in what they answer, not in what they
pick"*.

After this change a listing prints addresses that can be **read** but not **watched**.
Three ways to stand:

1. **List them, document it, file the watch question.** A listing that hides what a read
   reaches is the worse inconsistency — that is what `--paths` exists to prevent
   (`pathswalk_test.go:17`).
2. **Teach change-detection to name elements**, so a watch at `votes[0]` means "when the
   array changes". That changes what a change *is*, which is a bigger question than a
   listing and touches `KPathAdmits`, `Reaches`, `Touches` and every `Subtree`.
3. **Do not list them**, and keep today's silence.

**Decided: 1**, and the watch's silence is filed as `6a04ga4nh12krh87ndn0`. The listing's
job is to say what is there; pretending the elements do not exist does not fix a watch that
accepts a path and ignores it.

## One keying

**`!key(f)` is refused on a write** (`entity.checkNotKeyed`), and an element is addressed by
position everywhere: in a listing, in a read, in a write. That is the UX this plan is for — a
reader writes an address the same way whatever it names.

The tag was a second keying on top of the store's. logd keys an array in its SCHEMA
(`!logd-key`), which is what makes a name mean an element, refuses a position on a keyed array,
and renames the elements when the keying changes; verse declares no schema, so its `!key` only
survived as data and left the store holding a positional array while the value claimed names for
its elements — readable by verse alone. Nothing used it: the tag appears in test fixtures, in
Kubernetes manifests under `deploy/`, and in charter files that are executed rather than stored.

It also takes work out of the listing: with no tag to discover, an array is a level like an
object, so the store's walk asks for its members and **reads no node on any path** — where before
it had to read every array to learn what its elements were called.

**The verse surface for the store's keying is TBD**: which arrays are keyed, who declares it, and
what a keying change means for a source writing into one. This plan does not decide it, and paths
work under either answer — which is the point of settling the spelling first.

**The motivating array is in the world half.** Arrays in stored payloads are small —
`parents` (0-2), `labels`, `assignees` — with one exception: `discussion`
(`source/gitissue.go:311-320`), one element per comment, unbounded. That is the world half
(pure data, mirrored in), which is where growth was expected. The verse half's arrays are
author-sized. So today this change buys correctness, not speed.

## What must not change

The listing's behaviour is pinned by tests, and they are the specification:

- **Everything beneath what was reached**, into payloads, not to some level
  (`cmd/verse/pathswalk_test.go:17`) — and now into arrays as well.
- **`--depth` is a ceiling on the number of segments in an address**, `0` meaning no bound
  (`entity/doc.go:372`), not a level to stop at (`pathswalk_test.go:52`). An access counts
  as the step it is.
- **A path naming one node does not print that node**; a wildcard or descent path prints
  each node it reached (`doc.go:376`).
- **Rendering is chosen in one place** (`renderAddr`), and `verse charter --paths` prints
  through the same function (`cmd/verse/charter.go:264,296,305`).
- **`--if`/`--if-at` are asked of each node the path REACHED, and never prune inside a node
  they keep** (`versed/server.go:1798-1807`).
- **Error parity**: 404 `nothing stands at …`, 503 unreadable, never an empty listing where
  the truth is a failure (`cmd/verse/unreadablepaths_test.go:71`); a node holding nothing
  lists nothing, exit 0 (`:95`).
- **One snapshot.** A paged listing reads one commit, which the cursor pins.

## The store call

`MatchEachReturning(ctx, path, pattern, "id,iterType", fn)`
(`go-tony/system/libctl/logd_set.go:73`) over `<prefix>.*`, a level at a time:

- **`id`** is the next segment — the name a node lives under, as a value, which is what an
  address is built from. For an array member it is the index, which becomes an Access
  rather than a name.
- **`iterType`** says what is under a member and which wildcard reaches it: `Object` →
  `.*`, `Array` → `[*]`, `SparseArray` → `{*}`, `KeyedArray` → `(*)`, scalars → nothing.
  `api.IterWildcard` spells it.
- Paging lives inside the call; a page's cursor names the commit, so every level of one
  listing reads one state.

**With no `body` in the spec the store reads no member** — the walk that finds the members
already knows their names, so a listing ships names rather than what they stand for.

It does **not** yet lift the read budget's ceiling, and the plan said it would. logd enumerates
a level by building the node above it (`setChildren` reads the container), so a listing of a
container past the budget is refused exactly as a read of it is. Filed as tony-format
`3kgxprskh12krjrmndn0`, with the two ways out: enumerate from the index, or take the names off
the store's event stream as `encodedMatch` already does. Pinned here by
`TestAListingCostsNamesAndSharesTheReadBudget`, so the day it changes, a test says so.

## Where the walk runs: the server

Not the CLI, for three reasons in order of weight: **permissions** (a listing has no
materialized node to prune, so it needs `keep(ref)` per address, plus `noteHidden`),
**scope** (the COW child session must be the one that answers —
`entity/logdstore.go:1646`, `versed/server.go:1487`), and **round trips** (the levels
pipeline over a session and would not over HTTP).

**Mounts stay out of the way**: controller mounts live under `versemnt`, disjoint from the
entity root (`logdstore.go:121`), so no mount sits under a `verse.…` walk and docd passes
the retspec through — past a mount it would refuse a spec asking for more than `body`. A
walk whose prefix is `IsMountOwned` falls back to materialize-and-walk.

## Steps

1. ~~Bump go-tony~~ — done, `v0.0.224`, suite green, nothing calling it.
2. ~~The listing spells an element, client-side only.~~ Done. `Doc.Paths` answers `[]Path`;
   `JoinStepsAt` renders one; `renderAddr` takes a `Path`; the walk descends arrays. No
   `Doc.Paths` answers `[]Path`, `JoinStepsAt` renders one, and the walk descends arrays.
3. ~~`LogdStore.PathsAt`~~ — done: the level walk on the session, tested against `Doc.Paths`
   as the oracle over a real store. With `!key` refused it reads no node on any path.
4. ~~Attribution~~ — done: `(*attributed).PathsAt` filters per address with `keep` and
   `noteHidden`, and a withheld subtree is skipped rather than asked about again. It is on
   the `Store` interface, which is all-required — a test pins that the attributed store
   passes every method through, and it is what caught this one.
5. **The route** — `GET /state` answers a listing when asked, with the same 404/503/403,
   the same scope, the same `--if`.
6. **The CLI** — `printPaths` becomes a printer of what the server sent; `--depth` ships in
   the query.
7. ~~The cost proof~~ — done, and it found the ceiling is still there: versed's
   `TestStateAnswersALitingAsAddresses` shows the wire carries the names and not the payloads,
   and `TestAListingCostsNamesAndSharesTheReadBudget` pins that a container past the read
   budget is still refused, because the enumeration builds it (`3kgxprskh12krjrmndn0`).

## Verification

- `entity/segs_test.go:167` unchanged — a field named `[0]` still prints quoted.
- `entity/segs_test.go:204` extended: the `sh`/`xargs` round trip over addresses that carry
  an access, so a printed element line reads back as the element.
- `cmd/verse/kpaths_test.go:16` — every `--kpaths` line reads back through `--at`,
  including element lines. Its fixtures hold no array today, so its count stands; check
  before assuming.
- `cmd/verse/arrayaccess_test.go` unchanged — reading and writing an element are untouched.
- `cmd/verse/pathswalk_test.go`, `unreadablepaths_test.go`, `descend_test.go`,
  `payloadfilter_test.go`, `flags_test.go`, `entity/doc_test.go` — the listing suite, which
  changes only where a fixture holds an array.
- By hand: `verse data demo pr --paths` and `--kpaths --depth 3`, every line read back; a
  container larger than one page comes back whole, in order, no address twice.

## Open questions

- ~~The watch asymmetry~~ — **filed as `6a04ga4nh12krh87ndn0`**: a watch reaching into an
  array is accepted and then silent, where the read answers. Decided for this plan: list
  the addresses and let that issue settle whether a watch refuses such a path, honours it
  at the holding node, or makes an element a change unit.
- **`inSteps` prints a line that cannot be typed back** (`demo pr 1 votes[0]`). Cosmetic in
  a `removed …` confirmation, wrong as a spelling. Fix it to use `JoinStepsAt` once that
  exists, or leave it as the one place where a printed address is not an input.

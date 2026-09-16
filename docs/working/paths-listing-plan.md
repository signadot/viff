# Addresses without the document, and addresses inside arrays

Status: **step 1 done** — go-tony `v0.0.224` is in (`go.mod:10`) and nothing calls the new
surface yet. Branch `paths-cli`.

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

**The walk descends arrays.** Where today's recursion stops at a non-object, it continues:
each element contributes an address whose Access is `[i]` (or `{i}`), and anything under an
element extends that fragment. Keyed elements are discussed under
[What we do not get yet](#what-we-do-not-get-yet).

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

**Recommendation: 1.** The listing's job is to say what is there; the watch's silence is a
separate defect, and pretending the elements do not exist does not fix it.

## What we do not get yet

**Keyed elements stay theoretical.** `(scott)` reach is implemented and tested, but
**no verse writer stores a `!key(...)` array** — the tag survives only a write made as data
(`entity/doc.go:225-232`), and a grep finds it in test fixtures, in Kubernetes manifests
under `deploy/`, and in charter files that are executed rather than stored. So the walk
lists positions, and a keyed name appears only where a body was read anyway. Nothing to
decide until verse stores one.

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

**With no `body` in the spec the store reads no node at all** — the walk that finds the
members already knows their names. It also lifts a ceiling: a read builds the node it
answers under the session's read budget (`DefaultReadBudget`, 64 MiB), so a listing today
is refused on exactly the containers a listing is most wanted for.

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

1. **Bump go-tony** — done, `v0.0.224`, suite green, nothing calling it.
2. **The listing spells an element, client-side only.** `Doc.Paths` answers `[]Path`;
   `JoinStepsAt` renders one; `renderAddr` takes a `Path`; the walk descends arrays. No
   store change, no server change — `--paths` gains array addresses over the existing
   transport, where it is cheap to test. This is the address-grammar work, and it lands
   on its own.
3. **`LogdStore.PathsAt`** — the level walk on the session, answering `[]Path`. Tested
   against `Doc.Paths` as the oracle: same order, same dedup, same `--depth`, same
   addresses.
4. **Attribution** — `(*attributed).PathsAt` with per-address `keep` and `noteHidden`, and
   `CheckInto` at the prefix as `ReadAt` has it. Paging interacts: filtering after a page
   means a page can come back short, so the cursor loop keeps pulling.
5. **The route** — `GET /state` answers a listing when asked, with the same 404/503/403,
   the same scope, the same `--if`.
6. **The CLI** — `printPaths` becomes a printer of what the server sent; `--depth` ships in
   the query.
7. **The cost proof** — a listing of N records does not transfer N payloads. A counting
   wrapper around `sess` is the cheapest place to see it.

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

- **The watch asymmetry** above: list-and-document (recommended), teach change-detection to
  name elements, or keep the silence.
- **`inSteps` prints a line that cannot be typed back** (`demo pr 1 votes[0]`). Cosmetic in
  a `removed …` confirmation, wrong as a spelling. Fix it to use `JoinStepsAt` once that
  exists, or leave it as the one place where a printed address is not an input.

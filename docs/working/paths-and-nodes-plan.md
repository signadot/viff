# Paths and nodes: plan

Status: step 0 done (84a72f95 the ratchet, f311d0a7 the read leak, 2f07ed46 ChangedAt's counts,
e5bc5154 the reap watch, 4eaddde8 7de326e3's claim, ef526f2f the stale sentences; 04f21f51
reverted the wildcard read in favour of step 6). Step 1 done (4128776b). Step 2 done (1f3e31ba the walks, df48b248 operators, d6941656 row 11,
9cde72e9 row 14). Step 3 done (1f468b6c, 0cdc30bf, 70217850; a staging ship needs a wipe
and deploy/repopulate.sh). Steps 4 and 6 done together (f103b7b9: exact listings broke reads
rebuilt from rows; `place`/`coverPlace` stay as ListAt's arguments, and the watch's replay half is
still fixed-arity until step 5). Step 5 done (6c9e2d25: an observer names the patterns its
commits answer at, and the engine asks each commit what it touched; `entity.List`'s own fixed
depth is what is left, and goes with `entity.Depth`). The model is [paths-and-nodes.md](paths-and-nodes.md); read that
first — this file does not restate it, only the order of work and what each step must prove.

The order is set by three things. Live defects go first, each with a test that fails today,
because they cannot wait for a model change and because those tests are then the oracle the model
change is held to. After that the steps go from the bottom of the stack up — one length, one
containment question, one listing, one change, one read, one write — so each consumer rewritten
later is rewritten against an entity package that already says one thing. `..` goes last: the
model admits it, nothing else depends on it, and it is the one step that needs go-tony to answer
something new.

Every step leaves `make check && go test ./... && ./smoke.sh` green (scratchpad excluded).

## 0. Stop the bleeding

Fixes with their tests, none waiting on the model.

- **The ratchet.** `scripts/check-paths.sh` in `make check`: per file, the lines using the
  fixed-position vocabulary — `Ref`'s and `Match`'s `System/Kind/ID` accessors, the `mKey*` and
  `subjPart*` keys, `entity.Depth`/`halfDepth`/`atDepth`/`AtNamesEntity`/`RefOf`, `system:` keys
  and `$subject.system/.kind/.id` in charters and tests, and `system:kind:id` / "three segments"
  prose in docs/ — against a checked-in baseline. Growth fails; shrinkage fails until the baseline
  is lowered with `-update`, which refuses to raise it. *Proves:* nothing in steps 1–9 can be undone
  quietly, and the baseline's line count is the plan's progress.
- **The read leak.** A read of an ancestor leaves out every node under a record the reader may
  not read, as a listing leaves out a row; the watch route likewise. The test is the note's
  reproduction: ana refused at `demo pr 1 secret` and not shown it at `demo pr 1` or `demo pr`.
  *Proves:* a record governs its subtree whatever path the read was asked at.
- **The reap watch.** `trigger/engine.go:347` watches `verse.task.instance`. A test that runs a
  task to completion in scoped mode with `reapOnDone` out of the way, and sees it reaped and its
  hook applied. *Proves:* the store-driven path works on its own.
- **`ChangedAt`'s two counts.** Both branches compare the root-inclusive length. A test with a
  verse-half subject over a commit with no delta. *Proves:* the throttle sees a loop through
  `verse.*`.
- **`7de326e3`'s claim.** Correct its comment and its test, and say in that commit that the
  `Breaks:` did not happen; step 7 makes it true.
- **The stale sentence** at entities.md:308-310. The uncommitted `wildStands` change is discarded;
  its test cases carry to step 6 as its oracle.

## 1. One length

`Ref.Segs()` is the path. `Ref.Len()` goes, and every caller counts with the root included. Mount
routing (`Get`'s `rootFor`, `rootForAt`, `IsMountOwned`) asks of the same segments.

*Proves:* step 0's third defect cannot be written again. A test pins `Get`, `ListAt`, `Standing`
and `WatchSubtree` rendering one store path for a verse-half mounted ref.

## 2. Reach and overlap, each asked one way

Two primitives in entity, with a table test pinning them to what the tests they replace answer
today: **under** (pattern reaches the path or an ancestor-or-equal of it: kpath `MatchesPrefix`,
an operator or claim prefix matched on the name at its position) and **above** (the path is a
strict ancestor of what the pattern reaches: the pattern cut to the path's length, `Matches`).
Then, one commit each, behaviour held by the suite:

- `Pattern.pathCovers` → under. `Subtree.covers`/`within` → under/above, keeping the empty-subject
  guard, which is the shorthand's and goes in step 3. `Grant.Covers`, source's `under` and
  `distinctPlaces` → under.
- `MatchesAt` → under or above. **The above half stays until step 5**: it is what lets replay's
  fixed-arity events reach a deeper watch, and a test pins that so step 5 flips it on purpose.
- `place.holds` is equal depth, not containment: `Matches` over its prefix and wildcards; `place`
  itself goes in step 4.
- `pathContains` → under over patterns. An operator on the outer side is then matched honestly
  rather than flattened to `*`, which tightens containment: its own commit, with a stratum that no
  longer contains what it did not name.
- **Overlap**, one per-segment walk: `pathOverlap` and `Claim.Disjoint` ask it. A crossed pair
  (`[demo, *, x]` and `[*, pr, x]`) is a test.
- **Row 14:** `Claimed` skips a claim longer than the ref, so a scalar above a claim's depth is not
  covered by it.
- **Row 11:** `matchContains`' shorthand branch compares the root, so install-time confinement and
  run-time `mayWrite` agree on a verse-half patch until step 3 deletes the branch.

*Proves:* rows 11 and 14; every "is A under B" in the tree is one of two functions. Row 10 is the
shorthand's `Select` and lands with step 3.

## 3. The shorthand goes

`{system, kind, id}` in a match, `$subject.system/.kind/.id`, `Pattern.Select`'s dimension branch,
`Match.MatchRef`'s dimension form and `System/Kind/ID` are deleted. Count the real uses of the
shorthand first (115 files mention `system:` or `kind:`; sources, runners and prose account for
some), then rewrite charters in deploy/, scripts/, docs/ examples and tests to paths in the same
commit. What `$subject.system` was used for is answered by the subject's path.

`Tier: contract`, `Breaks:` the charter grammar.

With it go the shorthand branches of `matchContains`/`matchOverlap`, `Subtree.match`, the
empty-subject guard in `Subtree.covers`, and `SubtreeAt` dropping empty segments in the middle
(so `{kind: pr}` watched a top-level `pr`).

*Proves:* rows 2, 3 and 10 — one spelling of a selection, a root that is never ignored, and `[verse, task, instance]` is a path of
three that names the kind node only if a rule says so.

## 4. One listing

A listing is the nodes a pattern reaches — objects, arrays, scalars, nulls, empty objects — and
nothing else: kpath's own walk (`ListKPath`) over the subtree at the pattern's literal prefix,
each match keyed by the path it answers (`Node.KPath()`). `walkLeaves` goes from the listing path;
the walk over a delta (where a removal is an operation and a comment operand a statement) is its
own function. `ListAt`, `docSnapshot.ListAt`, rule selection, `reflector.current` and
`reflectedBy` use it; `place`, `coverPlace` and `reflectedBy`'s root `List` go.

*Proves:* a rule over `demo.pr.*` does not fire on a scalar at `demo.pr`; the snapshot and the
live store list the same set; a source's current set is what it wrote.

## 5. One change

A change under a pattern is every path it reaches whose node differs between the document before
a commit and after it. The subtree watch already holds both. `Since` answers the same question
over the log; `Commit.Changes`' fixed arity, `walkEntityLeaves` and `ChangedAt`'s second branch go,
and so does the above half of `MatchesAt` and `within` in `touches`, which exist for them.

*Proves:* row 6 — replay and live at one address — and that removing a derived place reaches the
causal chain. The trigger suite is the oracle that firing is unchanged.

## 6. One read

The break. Steps translate to a kpath at the CLI and nothing downstream reads steps. `/state` and
`/watch` answer by that kpath: the stored subtree narrowed by it; no path is the root; not-found
when it reaches nothing; `-if` filters what was reached and `{}` means it kept nothing.
`Nest`-from-rows, `pathStands`, `atDepth`, `halfDepth`, `Depth` and `Doc.Entities` go, and the
CLI's `-paths`, `-if` and `-trim` walk the answer by the kpath that was asked.

`Tier: contract`, `Breaks:` every command whose short path meant three-deep rows — `data -if`,
`data -trim`, `watch` — with the spelling that says it (`demo pr '*'`).

*Proves:* rows 1, 5, 7 and 8; the wildcard cases from `f5478b3e` and the discarded change,
`solo '*'` over `solo: {hollow: {}}` among them; a null reads as `null` wherever it is asked.

## 7. One write, and the charter tree on kpaths

A write is a stored path, an optional access, a value: `TargetOf`'s split goes, and permission
and ownership key on the path written however it was spelled. `Match.Names` is "the subject is a
stored path", at any length. `!get-path`'s string form reads a kpath. Charter package addresses
are kpaths from the file to the route to the tree; `splitCharterPath`, `trigger.ParseAt`'s
`strings.Split`, `belowAddr` and `atOrBeneath` go.

*Proves:* rows 4, 12 and 13 — `{path: [demo, cfg]}` creates, `demo tags '[0]' --put` writes, the
engine's and the CLI's spelling of one field write need the same grant — and `verse c
verse-dev.drift` is one segment that reaches no two-segment package.

## 8. `..`

go-tony first: `Matches`/`MatchesPrefix` with `..` in the pattern (tony-format
17hj5ygkh12ks7j7n5n0); listing with `..` already works and already answers where. Then verse stops
refusing it — the step clause
(`..` as a whole word, `'".."'` the name), reads, listings, watches and rule subjects — each held
to the same tests as steps 4–6 with a `..` spelling of their patterns added.

*Proves:* `verse data '..' -if X` answers every node X holds of, once each, nested where it stands,
and a rule over `[verse-dev, '..', finding]` selects derivations from subjects of different depths.

## 9. The docs

Rewrite to the note's nouns across docs/ (not working/ or roadmap/). The docs sweep's fixed-depth
list is the checklist: tutorial.md:60-61, 91; entities.md:168, 180-181, 290, 298-300, 354-356,
581-583; overview.md:117, 216-218; connect.md:88-89; store.md:27, 53-54; change-events.md:29-33,
163-166, 227-238; trigger-model.md:3-6, 401, 417, 444-447, 467-469, 553-554, 563-565, 624-637,
965, 1168, 1209, 1220-1222, 1688; strata.md:80, 88-89, 97, 106-107, 143, 190-191, 308-309;
reference/sources.md:32; controllers.md:78, 85-88, 125-127; cycle-throttle.md:64. Line numbers are
as of 2026-09-15 and move with every step, so each step fixes the prose its own change contradicts
and this step is what is left.

*Proves:* a grep across docs/ for `three segments`, `slice`, `leaf`, `system:kind:id` and `half`
comes back empty.

## Landing

This note, this plan and entity-tree.md go when step 9 lands. What is left goes to
`paths-and-nodes-residual.md`.

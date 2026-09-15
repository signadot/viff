# Paths and nodes: one tree, one set of words

Status: landed. The plan is [paths-and-nodes-plan.md](paths-and-nodes-plan.md) and what it did
not finish is [paths-and-nodes-residual.md](paths-and-nodes-residual.md). This note is the model
the code cites; it is written as the proposal it was, and everything in it runs.

verse keeps one document. What it calls an entity, a place, a slice, a leaf, a row or a record
has drifted into at least six operational meanings, and the code decides "is anything there"
four different ways. Every bug fixed on 2026-09-15 (`002b51a5`, `7de326e3`, `f5478b3e`, and
the uncommitted `wildStands`) was a local patch over one of those disagreements, and two of
the patches were wrong in ways the patch's own reasoning could not see. Two of the
disagreements are live defects, one of them a permission leak.

[entity-tree.md](entity-tree.md) made the right decision — "entity-hood is a LENS, not a stored
fact … the QUESTION carries the depth, not the store" — and applied it halfway. A fixed depth
(`entity.Depth`, `halfDepth`) still decides what a read returns, what a watch replays, what a
write names and where a rule's subject begins. This note says what the model is when the
decision is applied everywhere, what that deletes, and what is broken today.

## What is broken today

Reproduced (run in-process) or traced to the line. Each is a consequence of the model below
not holding, and the plan's first step fixes the ones that cannot wait for it.

**A permission record does not protect its node from a read of an ancestor.** Reproduced.
scott writes `demo.pr.1 = {open: yes, secret: {x: 1}}` and takes `demo pr 1 secret` with no
grants. ana's `verse data demo pr 1 secret` is refused; `verse data demo pr 1` and `verse data
demo pr` both answer `secret: {x: 1}`. A listing keeps a row by checking the row's own address
(`attributed.keep`, entity/attribution.go:486), and `accessOf` walks up from there, never down
into what the row's payload carries.

**The engine's store-driven reap and completion-hook watch is on the wrong half.** Traced.
trigger/engine.go:347 opens `WatchSubtree(entity.Depth, "task", "instance")`, which the store
renders as the world's `task.instance`; task records are `VerseRef(task, instance, id)`
(mirror/mirror.go:37), under `verse.task.instance`. The listing at engine.go:1831 passes
`entity.VerseRoot` for the same records. In scoped mode nothing it watches is ever written;
`reapOnDone` is the documented backstop, which is why nobody saw it.

**The loop throttle cannot see a loop through verse's own half.** Traced, and probed by the
sweep. `Commit.ChangedAt` compares `ev.Ref.Len()` — segments below the root — when a commit has
no delta (entity/entity.go:420), and `len(segs)` — root included — when it has one (431).
`changedIn` passes `len(ref.Segs())` (trigger/causal.go:217), which its own comment says is
deliberate. Scoped commits carry no delta, so every `verse.*` subject is looked for one level
too deep, every firing starts a fresh chain, and the throttle has nothing to walk.

**`7de326e3` claims what it did not do.** Traced. Its comment and `Breaks:` say `verse c
verse-dev.drift` now names a system called `verse-dev.drift`. The server's `splitCharterPath`
(versed/charterpath.go) joins segments with `"."` and compares against package addresses kept as
dotted strings, so the one segment `verse-dev.drift` still equals the package. The test used an
address with no package behind it and passed for that reason.

**`002b51a5` left a sentence saying the opposite.** docs/reference/entities.md:308-310 still says
"a `*` typed as a step is a name".

The rest are disagreements a user or a charter can reach. Each names a document and two
operations that answer differently about the same place.

| # | document | disagreement |
|---|---|---|
| 1 | `demo: {hollow: {}, n: null}` | `verse data demo` shows neither; `verse data demo hollow` stands; `GET /entity demo.n` is `null`, `verse data demo n` is `{}` |
| 2 | `demo: {pr: 5}` | subject `{system: demo, kind: pr}` selects `demo.pr` and fires on it; `{path: [demo, pr, "*"]}` selects nothing |
| 3 | any `verse.task.instance.*` | subject `[verse, task, instance]` "names one entity" (`Match.Names`, match.go:314, counts the root against `Depth`) and fires once about the kind node |
| 4 | none | `{path: [demo, cfg]}` with a patch never fires (fewer than three segments do not name); `[demo, cfg, x]` creates |
| 5 | `demo.pr: {1: {stage: open}, 2: closed}` | `verse data demo pr '*' stage` is a 400 (`narrow` steps into a string); `verse watch` on the same path emits `demo.pr.2` |
| 6 | any | `verse watch demo pr 1 stage --since N` replays at `demo.pr.1` with the whole payload (`Since` is fixed-arity) and streams live at `demo.pr.1.stage` |
| 7 | `demo: {n: 5, pr: {1: …}}` | `verse data demo` shows `n: 5`; `verse data demo --trim X` drops it (`Doc.Entities` does not see a scalar above depth) |
| 8 | any | `verse data demo pr '*' stage --paths` prints `demo pr 1` — the entity, not the stage |
| 9 | `git.verse.commit.<sha>` | at depth 3 `git.verse.commit` is one entity whose payload is the whole history; `git.verse.*` fires on it |
| 10 | a verse-half task | `{system: task, kind: instance}` with root `""` selects verse's task records in `Pattern.Select` (the pick ignores root) and not in `Pattern.MatchRef` (which checks it) |
| 11 | a verse-half patch | install-time confinement (`matchContains`, dimension form, ignores root) admits what run-time `mayWrite` refuses |
| 12 | none | the same field write spelled by the engine (Ref + Field) and by the CLI (a four-segment Ref, no Field) needs different permission levels, and `--rm` of the CLI's spelling is a whole-node delete that runs `forget()` |
| 13 | none | a write through an array access at an entity's own depth is refused (`TargetOf` splits at `Depth`); one level deeper it is accepted |
| 14 | a scalar at `slack.foo` | a `--forget` for claim `slack.*.ws/*` tombstones it (`Claim.Disjoint` is symmetric, so a shorter listed ref counts as covered) |

The three sweeps behind this (entity package, its consumers, the published docs) found more; the
docs alone carry 21 statements that cannot all be true, and a vocabulary where *place* has seven
senses, *record* four, *slice* five, and `at:` five. What follows is what they have in common.

## Why it keeps happening

**A read is rebuilt from rows.** `/state` asks the store for the subtree at the path, which the
store returns whole, then `walkLeaves` breaks it into rows at a depth and `Nest` rebuilds a
document from the rows. Whatever the row walk has no rule for is gone from the answer: an empty
object above the depth has no fields to visit, a null above the depth is suppressed on a stale
belief that the store answers absence with null (logdstore.go:1697, contradicted by `Get`'s own
comment at 529), and a scalar above the depth survives only because an exception was added for
it. Every "stands" test in versed — `pathStands`, `reaches`, `wildStands` — exists to recover
from the rebuild what the rebuild threw away.

**A fixed depth survives the decision that abolished it.** `Depth` is 3; `halfDepth` makes it 4
under `verse`; `atDepth = max(halfDepth, len(at))` makes a read at one or two segments list rows
at three. `walkEntityLeaves` gives `Commit.Changes`, `List`, `Observe` and `Since` one fixed arity.
`TargetOf` splits a write into entity and field at `Depth`. `Match.Names`, `splitAddress`,
`Subtree.match`, `Pattern.Select`'s dimension branch and `ListUnder` each count to three with their
own rule for whether the root counts. Nothing about the tree is three deep; the constant is a
memory of when addresses were `system:kind:id`.

**The root is a level in some counts and not in others.** `Ref.Segs()` includes it,
`Ref.Len()` excludes it (entity.go:132, 205). `System()/Kind()/ID()` are positions below it.
Mount routing uses `ref.System()/Kind()` in `Get` and `at[0..1]` — root included — in `rootForAt`.
Three consumers have been fixed for one count and broken for the other.

**Containment is asked seven ways.** `MatchesAt` passes a shorter ref, `pathCovers` refuses one,
`Subtree.covers` uses kpath, `place.holds` wants exact length, `Claim.Disjoint` is symmetric,
`pathContains` flattens operators to `*`, and `Match.MatchRef`'s dimension form ignores the root.
Each was right for its first caller.

**Addresses are still strings in places.** Charter packages are dotted strings compared with
`strings.Join`/`strings.Split` (`splitCharterPath`, `trigger.ParseAt`, `belowAddr`), which is
where `7de326e3`'s claim fell through.

## The model

Five nouns. Everything else is either one of these or a question built from them.

**The tree.** One document. What the store keeps it under (logd's `verse.` prefix, the mount
root) is the store's business and is never visible as a level or called a root.

**A path** is a sequence of segments from the top of the tree. Its length is its length —
`verse` at the front is a segment like any other, and counts. There is no half, no entity depth
and no second length. verse keeping its own records under `verse` is a naming convention, like a
charter keeping its facts under its address.

**A node** is whatever stands at a path: an object, an array, a scalar, a null, an empty object.
All of them count the same. **A path stands** when the store holds a node there; the store says
not-found or path-conflict when it does not, and that is the only definition of absence.

**A pattern** is a kinded path, and kpath is the reference: go-tony's `ir/kpath` grammar is what
a pattern means, not a spelling of something verse defines. Fields, accesses (`[0]`, `{n}`,
`(key)`), the wildcards `*`, `[*]`, `{*}`, `(*)`, and `..` — any depth, the node itself included.
**The empty kpath is the root.** A pattern **reaches** the nodes kpath says it denotes: without
`..`, the nodes at its own depth whose paths match it; with `..`, at every depth the descent
allows. A literal path reaches at most one node. A stored path — anything a write lands at or a
record is kept at — has no wildcard and no `..`, as kpath itself says of a query segment.

**Steps translate to a kpath, and the kpath is canonical.** A command line's steps are a spelling:
each clause becomes one kpath segment (a name, a quoted name, an access, `*`, and `..` as a whole
word), and from there on every route, record, listing, log line and error carries the kpath.
Nothing downstream of the CLI reads steps, and nothing re-derives segments any other way.

Two questions are asked of patterns, and each is asked one way. **Reach** — does this pattern
reach that path, is this write under that confinement — is kpath's (`Matches`, `MatchesPrefix`).
**Overlap** — can two patterns reach a common path: two strata, two sources' claims — is one
walk to the shorter length asking, at each position, whether the two segments can meet; for
segments kpath spells, that is kpath's `Matches` asked of the two segments each way.

A charter's subject path may put a tony operator where kpath has a field (`[demo, !glob "hot-*"]`),
and a source's claim may put a literal prefix there (`ws/*`). Either stays a match on the field
name at that position; the kpath structure around it — its length, its wildcards, its `..` — is
kpath's.

**An entity** is not a kind of node. It is the word for a node a question is about: the subject a
rule selects, one row of a listing, one event of a watch. Every one of those questions carries a
pattern, so every one carries what depth it means. `system`, `kind` and `id` are gone — the
`{system, kind, id}` match shorthand, `$subject.system/.kind/.id`, and every accessor behind them.

From those:

- **A read of a pattern** answers the nodes it reaches, as the stored subtree narrowed to the
  pattern — never a document rebuilt from rows. No path reads the root. It answers not-found when
  the pattern reaches nothing. `-if` filters what was reached; if it keeps nothing the answer is
  `{}`. A node that is `{}` is also `{}`, and the two are told apart by whether a filter was given.
  So `verse data -if X` asks X of the root node, and asking it of every node the verse holds is
  `verse data '..' -if X` — the question saying what it means rather than a default depth saying
  it for it.
- **A listing** (a rule's selection, a source's current set, `-paths`) is every node the pattern
  reaches — empty objects, nulls and scalars included — and nothing it does not. A scalar at two
  segments is not in a three-segment pattern's listing; it is a node, and a read of its path, or
  of a pattern with `..`, answers it.
- **A change** under a pattern is every path the pattern reaches whose node differs between the
  document before a commit and after it: created, replaced, removed, or taken with an ancestor.
  Replay and live delivery answer the same question.
- **A write** is a stored path, an optional access below it, and a value. It has no entity/field
  split. A rule's patch names what it creates when its subject is a stored path, at any length.
- **Permission** on a read is permission on every node the answer carries. A record at a node
  governs that node and what is below it, whichever ancestor the read was asked at; what the
  reader may not see is left out of the answer, as a listing already leaves out a row.

## What it deletes

- `entity.Depth`, `halfDepth`, `atDepth`, `AtNamesEntity`, `RefOf`, `Ref.Len` (the second length),
  `System/Kind/ID`, and the charter shorthand with its match dimensions and `$subject` fields.
- `walkLeaves`'s three exceptions (scalar above depth, null above depth, a removal met on the
  way), `commentOperand` in walks of a materialized document, and `walkEntityLeaves`' fixed arity.
- `Nest`-from-rows on the read path, `Doc.Entities`/`EntitiesAt`/`EntityPaths`, and the client's
  second walk of a document the server already rebuilt.
- `pathStands`, `reaches`, `wildStands` and `Standing` as a separate question on the read route.
- `TargetOf`'s split, `Write.Sub` beside `Path.Field` as two ways to say "below the entity", and
  `ChangedAt`'s two branches.
- Six of the seven containment tests, the `Pattern.Select` dimension branch, and the dotted-string
  charter addresses with their splitters.
- In the docs: *slice*, *leaf*, *half*, *record* for a payload, four things called a root, and every
  sentence that makes three segments a property of the store (listed in the plan's last step).

## What it keeps

- verse's own registries (`verse.task.instance.<id>`, runners, sources, resolvers) name the depth
  of their own schema where they read it. That is a fact about a schema its owner wrote, not a
  default the entity package supplies to everyone.
- Steps as the command line's spelling, with the step clauses as landed plus `..`; kpath as what
  they mean.

entity-tree.md §8 deferred recursive descent until everything else had settled. This is that
settling, and `..` is in the model: a set spanning depths is what a reader of derivations from
rules of different subject depths needs, and kpath already defines it. It lands last — nothing
else in the model depends on it, and until it lands a pattern holding one is refused by name, as
it is today.

## What it breaks

**A filter or a watch at a shorter path stops meaning rows three deep.** `verse data -if '{stage:
open}'`, `verse data demo pr -if …`, `verse data demo pr -trim '{stage}'` and `verse watch demo`
each ask about three-segment rows today because `atDepth` supplies the depth the path did not.
Under the model the path is the question, so those ask about the one node the path names. What
they meant is spelled in the pattern: `verse data demo pr '*' -if …`, `verse watch demo '*' '*'`,
or `'..'` for every depth. The reference already tells this story for `git-issue verse issue`
(entities.md, "It asks at the depth the path asks at"); the model makes it true at every length.

**Every charter written with `system:`/`kind:`/`id:` or `$subject.system` is rewritten** to a path.
115 files in the tree mention `system:` or `kind:`; not all of them are the shorthand, and the plan
counts before it cuts.

## Decided

2026-09-15, in conversation:

- **kpath is the reference.** Steps translate to a kpath and the kpath is canonical; no path is
  the root; `..` is supported, and may land after the rest.
- **The charter shorthand is deleted**, not compiled.
- **A read leaves out a node the reader may not see**, as a listing leaves out a row.

## Open questions

- **A read's answer shape for a pattern.** Today it is the matches nested under their paths from
  the literal prefix, which is what `-paths` and the filters walk. Keeping that shape is assumed;
  what changes is that it is narrowed from the stored subtree rather than rebuilt. With `..` a
  node can be reached both as itself and inside an ancestor that was also reached; the answer
  nests it once, where it stands.
- **What go-tony has to answer for `..`.** `ListKPath` already answers where each match stands
  (`Node.KPath()` on a result), so a listing is kpath's own walk keyed by that. `KPath.Matches` and
  `MatchesPrefix` answer false for any pattern holding `..`, so containment cannot use it yet:
  tony-format 17hj5ygkh12ks7j7n5n0 (plan, step 8).

## This has been decided before

This is at least the fifth session to conclude that `system:kind:id` goes and paths stay, and the
vocabulary has come back each time — through a helper that counts to three, a shorthand kept for
old charters, a sentence in a doc. The plan's first step puts a ratchet in `make check`
(`scripts/check-paths.sh`) that fails when that vocabulary grows anywhere in the tree, and fails
when it shrinks without the baseline being lowered, so the only direction it can move is out.

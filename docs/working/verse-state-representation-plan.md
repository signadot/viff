# `verse state`: one document, one kinded path

Status: **built** (branch `verse-state`), in the three commits below. What the plan got wrong,
and what building it settled, is recorded in "What landed" at the foot of this note — the plan
itself is left as it was written, because the argument is what makes the diff readable and a
plan rewritten to match its outcome teaches nobody anything.

The change in one line: **the CLI stops presenting a table of entities and starts presenting the
document the store already holds, addressed by the same kinded path the store writes into.**

## Why

`verse entity` presents the store as a list — `[{ref, payload}, …]` narrowed by `--system`,
`--kind`, `--if`, `--if-at` — while `get`/`put`/`rm` address one entity by `system:kind:id` and
reach inside it with a separate `--at`. Three vocabularies for one question, and a noun
("entity") that describes a row rather than what the command answers.

The store holds no list. It holds one document, `{system: {kind: {id: payload}}}`, and the tree
already says so where it matters: `docSnapshot` is *"root-rooted: `{<root>: {system: {kind: {id:
payload}}}}`"* and **private** (`entity/snapshot.go:47`); `trigger/path.go:13-18` grounds the
charter grammar on *"a verse is one document … so an entity address is a path prefix and `path:`
simply continues it into the payload"*; `entity/write.go:11-17` says `Path` exists because
*"'which entity' and 'which place in it' are the same question asked at different depths … and
(later) reading or watching one"*. The listing flattens what the store had in hand, and every
consumer rebuilds the map it threw away (`cmd/verse/vote.go:149,185`, `cmd/verse/connect.go:398`,
`scripts/level-ping-pong.sh:112`).

## What kpath is

This plan takes the kpath package documentation as its input rather than inferring the grammar
from what the printer emits — `ir/kpath/doc.go`:

> Kinded paths encode both navigation and structure type in the syntax:
> `.field` object field · `[index]` dense array · `{index}` sparse array/map · `(key)` keyed
> array · `.*` `[*]` `{*}` wildcards

The kind of every element is in the syntax, so nothing is ambiguous and nothing is inferred by
counting segments. Checked against the parser (`ir/kpath/kpath.go:824-880`):

- **`.1` is the field named `1`.** An unquoted field runs until `.[{(`; digits are an index only
  inside `[ ]` or `{ }`. `demo.pr.1` names the entity and `demo.pr[1]` is a different question
  entirely. (`KPath.String()` re-emits `."1"` — the printer being conservative, not a second
  meaning.)
- **An unquoted field carries `:` `@` `+` `/` `-` unescaped.** Only `.`, `[`, `{`, `(` and the
  tony-quoting set force quotes.
- **A `(key)` segment runs to `)`**, so a keyed-array key needs no quoting for any character a
  verse id uses, dots included.
- **`*` is a wildcard only as a whole segment**, so `git.*.verse/*` still means what
  `git:*:verse/*` means today — kpath's `*` for the kind, `source/own.go`'s trailing-prefix glob
  for the id. The ownership grammar survives untouched.

And `!key` (logd indexes it; `ir/kpath.go` has `keyedElems`/`ElemKey`/`KeyField`) makes an array
diff by identity rather than position:

```
$ o d a.tony b.tony        # both: entities: !key(ref) [ … ]
entities: !key(ref)
[ { payload: { stage: !replace { from: open, to: reviewed } }, ref: demo:pr:1 } ]
```

So the array shape is not condemned by its diffs — one annotation fixes that. What survives as
the reason to reshape is that every consumer re-indexes, which `!key` does not touch.

## The delimiter is the whole disagreement, and it dissolves

`demo:pr:1` and `demo.pr.1` look like two grammars for identity. They are one grammar with two
delimiters: a ref is a kinded path whose delimiter is `:`. Once kpath takes a delimiter option —
sed-style, chosen per path — that stops being a fork in the design and becomes an argument.

That settles the thing that made this hard. Verse ids are built from refs (`spawnID` is
`chain + "@" + safeSeg(ref.String())`, `trigger/trigger_api.go:243-259`) and `ChainSep`'s comment
states the requirement: *"the chain is a kpath (dots …) and the ref is an address (colons), so
the boundary needs a character neither grammar spends."* With a fixed `.`, a derived id like
`comment-drift@drift:head:head-moved@git:head:verse` is fine but `…@drift.head.…` would need
quoting — making the most common ids unpasteable without shell escaping, the concern `safeSeg`
and `validTriggerName` already exist for. With a delimiter option, you pick one the segments do
not spend and nothing needs quoting or rewriting:

```
verse state demo:pr:1                       # ':' — the ref spelling, unchanged
verse state ,demo,pr,comment-drift@drift:head:head-moved@git:head:verse,stage
verse state demo.pr.1.labels.urgent         # '.' — the default
```

**So there is no identity clobber in this plan.** `Ref.String()`, stored refs, charter subjects
and the `owns` grammar all stay as they are; they were kinded paths the whole time. What verse
adds is the ability to *continue* past the id, which is what `--at` stands in for today.

**Upstream ask, and the only real dependency:** the kpath delimiter option. Until it lands, the
CLI's default `.` plus quoting covers every real id — of ~40 on staging, exactly one (a task id
whose chain carries a hook segment) needs quotes. Nothing below blocks on it; it removes the last
rough edge.

## The shape of it

**One grammar.** A ref is a kpath's first three segments; `demo.pr.1.labels.urgent` continues
into the payload; `demo.pr` is a kind, `demo` a system, the empty path the whole verse. Selecting
uses the same string: `demo.pr.*`, `demo.*.1`.

**One document.** The answer is that path's subtree, verbatim — what the store holds, not a shape
assembled for the wire.

**One reach.** `[i]`, `{i}` and `(key)` continue into arrays inside a payload, which no verse
address can do today: a proposal's `votes: !key(voter) […]` is `merge.proposal.X.votes(scott)`.

**One noun.** `verse entity` becomes **`verse state`**: the command answers with the verse's state
at a path, not with rows of a table. `entity` stays in the Go API, the wire and the source
protocol, where a single addressed thing genuinely is the unit. (`verse state` sits close to
`verse status`; if that grates, rename `status` — its strata/runners/health summary is the smaller
surface and the worse fit for its name.)

## Commit 0 — an entity may hold any value

`Tier: contract`. A bug, and a prerequisite: the document's leaves are payloads, and one payload
value is currently unspellable. `nodeToPayload`'s own comment settled this a level down —

> It used to drop every untagged null field at any depth … That convention worked and cost the
> data model a legal value … **A store may choose how it spells absence; it may not choose which
> values you are allowed to hold.** Absence is `!delete` now.

— and never reached entities: `walkEntities` (`entity/logdstore.go:1553`) reads a null leaf at
depth 3 as a tombstone and `Get` (`:389-403`) reads a null node as absence, so `payload: null`
cannot be stored. The write side is already unambiguous (a Go-nil `Write.Value` → `deleteOp()` =
`!delete null`, `:478`); only the read conflates, and only because two walks share one function:

- the **delta** walk (`:1379`, over a patch; `WatchSubtree`) genuinely needs null-means-deleted —
  that is how a removal arrives in a patch;
- the **materialized** walks (`List` `:696`, `snapshot.go:67`) do not, since a deleted entity is
  not there to walk.

Split into `walkEntityChanges` (deltas, unchanged) and `walkEntityDoc` (materialized, a null leaf
is the payload `null`); stop `payloadToNode` mapping nil → `{}`.

**Check first**, because it sizes the commit: whether logd's `Match` returns a `!delete`d key as a
null leaf or omits it. `absentPath` (`:568-580`) asserts the former — *"A ref that was DELETED
still has its key"* — and ORs both shapes into every create precondition, while `List` skips
`ch.deleted`. If Match omits the key, both are dead code and this is small. If it keeps it,
deletion needs a spelling that is not a bare null before a null payload can be legal. Either way
`Absent()` (`entity/store.go:129`, `!irtype null`) needs revisiting — once an entity may *be*
null, "nothing is there" and "the value is null" stop being one match.

## Commit 1 — `verse state <path>` answers the document

`Tier: contract`. `GET /state?at=<kpath>` (the route renames with the command) answers the subtree
at that path:

| path | body |
|---|---|
| *(none)* | `{system: {kind: {id: payload}}}` |
| `demo` | `{kind: {id: payload}}` |
| `demo.pr` | `{id: payload}` |
| `demo.pr.*.stage` | the wildcard's matches |

A path naming nothing is `{}` with 200 at collection depth (1–2 segments) and 404 at 3+, where a
thing is named rather than a slice. A slice with no members is absent rather than `{}` — an empty
object would assert that the kind exists. `--system`/`--kind` are gone from the read *and* from
`watch`, replaced by the positional path.

`entity/doc.go` (new) — a pure function over what `List` already returns; **no new `Store`
method**. `LogdStore.List` is already `Match(seg(root))` + `walkEntities`, and the payload
predicate needs payloads, so filtering stays at the handler either way.

```go
//tony:schemagen=verse-state,notag,codec=custom
type Doc struct{ Node *ir.Node }
func Nest(ents []Entity, at kpath.KPath) Doc
func (d Doc) Entities(at kpath.KPath) []Entity
```

Reuse rather than reinvent: `walkEntityDoc` is `Nest`'s inverse at depth 3 (that is the test);
`walkLeaves`/`refAt` (`:1002-1029`) *are* `Doc.Entities` at depths 1–2 — which is what
`entitiesFromDoc` (`:972`) already does for the subtree watcher, so re-express that as a fold of
`Doc.Entities` and delete one of two near-duplicate walkers; build every level with `ir.FromMap`
(it sorts) so the document is deterministic top to bottom. Trap: `ir.FromMap` re-parents its
children, so clone if ever fed a snapshot's own nodes.

`listEntities` (`versed/server.go:1196`) changes only its last line. `queryPattern` (`:1084`) is
untouched, which keeps the read face and the watch face the same selector, with `at` narrowing
both.

**`--paths`** prints one address per line, derived client-side by `Doc.Entities`. Not the array
returning: no payloads, one address per line — the `ls -1` of the read, and what keeps scripts
honest now that ids are keys rather than tokens inside a rendered ref.

**Schema.** `schema/curated_test.go` holds the curated set *equal* to the Go types declaring
`codec=custom`, in both directions, so a hand-written entry with no type behind it fails that
test — do not weaken it. Naming `entity.Doc` satisfies it by construction, and moves one response
off a `map[string]any` literal, which `api/api.go`'s package doc asks for. Then
`schema/root.tony:32` `entities: .[array(verse-entity)]` → `state: .[verse-state]`,
`versed/server.go:829` `list(…)` → `one("verse-state")`, and the three comments quoting the old
line (`api/api.go:290`, `versed/schema_test.go:262`, `cmd/verse/schema_test.go:19`).
`verse-entity` stays — `/entity` and every watch frame carry it.

**Consumers**, one move throughout: stop recovering an address in order to make a second request;
read the answer out of the document already in hand. `cmd/verse/vote.go:149,185` and
`cmd/verse/connect.go:398` decode `entity.Doc` and drop their `TrimPrefix`/`SplitN` ref surgery;
`scripts/level-ping-pong.sh:118,129-135`, `scripts/lineage.sh:130`,
`scripts/attached-resolver.sh:163,208` become a narrowed read or `--paths`;
`smoke.sh:379-414,474-486` assert on keys instead of `ref:` strings, and `:109-116`'s plural-alias
check follows the rename. Docs: [reference/README.md](../reference/README.md),
[reference/entities.md](../reference/entities.md), [reference/store.md](../reference/store.md),
[tutorial.md](../tutorial.md).

**Not** changed: `source.Reflection.Entities` (`source/reflector.go:55`) — the source protocol is
a different wire with out-of-tree implementors, and its array pairs with `covers`. Say so in the
commit body so nobody "finishes the job".

## Commit 2 — one address, all the way down

`Tier: contract`. `verse state get|put|rm` take the whole path, subsuming `--at`:
`verse state get merge.proposal.X.votes(scott)`. `entity.ParsePath` is the one parser
(`kpath.Parse`; the first three segments are the ref, the rest the field path), and
`Path.String()` (`entity/write.go:28-35`) is already its inverse — it renders
`demo:pr:1.labels.urgent` and already calls that *"the form the CLI accepts"*, which `ParseRef`'s
`SplitN(s, ":", 3)` never implemented. `api.PutEntity` keeps `{ref, field[]}` on the wire; the
wire has no reason to re-parse what the CLI already split, and the structured form is what
`writePath` consumes. `!get-path`'s `{system, kind, id, path}` collapses to one string in
charters, and `trigger/path.go:108-112` and `api/api.go:33-50`, which currently justify the split
in prose, get rewritten in the same commit.

Where payloads hold arrays worth addressing (votes, calls, findings), annotate them `!key` — that
is what makes `(key)` reach them and their diffs identity-based.

## What must not move

- **`/watch` stays an event stream**, one frame per entity change. The document is a level; a
  stream of documents is a diff nobody asked for, and it breaks "at most one commit out, never
  several folded together" (`entity/store.go:96-101`).
- **`--if` filters which ids remain** — per entity, at the handler, before `Nest`. It never prunes
  inside a payload: the leaves are whole payloads. `--if-at` says where the predicate is
  evaluated, not what the answer covers; that third thing is `Pattern.Slot`.
- **The read face and `watch` name the same set** (`versed/server.go:1073-1083`). With one a
  document and the other events, that stops being visible by eye, so it gets a test.
- **Refs, `entity.Entity`, `verse-entity`, `queryPattern`, the `Store` interface** — unchanged.

Known asymmetry to state rather than fix: `List` reads `seg(root)` only, so mount-owned slices
(`local:dir-content-*`, under `MountRoot`, `entity/logdstore.go:250-278`) are not in the answer.
Calling it "the document, whole" makes that pre-existing gap into a claim that is not quite true —
say so in [reference/entities.md](../reference/entities.md); unioning a second `Match` is a real
decision, not a detail, and does not belong in this commit.

## Verification

**Commit 0** — `put demo.pr.1 null` then `Get` returns found with a null payload and `List`
includes it; `Delete` then `Get` is not-found and `List` omits it; a `WatchSubtree` delta still
reports a delete. Then the whole suite, since the tombstone reading is load-bearing in
`claimRun`/`releaseRun`.

**Commit 1** — `entity/doc_test.go`: `walkEntityDoc(Nest(…))` == the entities, over a fixture with
a dotted id, a colon-bearing id (`proposal-appeared@git:ref:x+2a6c8212`), a numeric id, a
non-object payload and a null one; the depth table including a wildcard path; `Doc.Entities`
recovers addresses at each depth; the subtree watcher's reader and the read's reader are the same
walk. `versed/payloadfilter_test.go`: rewrite `listRefs` (`:158-190`) to decode `Doc` — **every
existing assertion stays**, they are all about the set — and add `TestStateAndWatchNameTheSameSet`,
which nothing pins today. `schema`/`versed`: `TestCuratedSetEqualsCustomCodecTypes` and
`TestTheRootIsTheServedSurface` catch a half-done schema change.

**`make smoke-all`** (both renderings), with checks beside the existing entity block: the document
carries `stage` and not `ref:`; the id is a key; raw curl on `/state?at=demo.pr` agrees;
`verse state nosuch` is `{}` and exit 0 (`scripts/attached-resolver.sh:394` uses the bare read as
a liveness probe); `--paths` round-trips back into `verse state get`.

**Live:**

```sh
./scripts/seed-entities.sh
verse state | head -40             # the whole document
verse state demo.pr                # {id: payload}
verse state 'demo.pr.*.stage'      # a wildcard, one grammar
verse state demo.pr --if '{stage: open}' -o yaml
verse state get 'merge.proposal.X.votes(scott)'
o d -loop 'verse state' -loopUntil …
./scripts/lineage.sh && ./scripts/lineage.sh --clean
```

## Trailers

`Breaks:` per commit — **0**: an entity may hold a null payload; a null leaf is no longer read as
a tombstone. **1**: `verse entity` is `verse state` and `/entities` is `/state`; the answer is a
document keyed by system/kind/id, not an array of `{ref, payload}`; `--system`/`--kind` become a
positional path; `--refs` is `--paths`. **2**: `get`/`put`/`rm` take a whole path and `--at` is
gone. Each needs a `Notice:` filed before the push, per
[contributing.md](contributing.md).

## What landed

Three commits, as planned. Five things came out differently, and each was a question the plan
asked rather than an answer it got wrong.

**The Check First resolved to "small".** logd EXECUTES a `!delete` by removing the key, so a
deleted ref reads back not_found — verified against a real logd before anything was written.
`absentPath`'s tombstone branch had never fired, and `List`'s `ch.deleted` skip was dead on a
materialized read. Both went.

**Absence had to move off the value channel entirely, which the plan half-anticipated.**
`Absent()` was `!irtype null`, and that is a real tony operator meaning *the value here has type
null* — true of an entity holding one. It is `Require.Absent`, a field, now. That freed
`!irtype null` to mean what it says, which turned out to be needed: `entity.Unchanged(cur)`
spells expect-unchanged, because a bare null pattern is the presence test and comparing a null
payload against itself that way asked nothing at all.

**One read answer does not settle itself, and the plan did not see it.** logd reports a missing
key as not_found only where it can resolve a proper ancestor; under a root that has never been
written it answers a bare null instead — the same answer an entity holding null gives. `Get`
asks the parent for the one key when it sees a null (`keyStands`). Without it a fresh verse
claimed an entity at every address anyone asked after, which took a refused update's
precondition and Modify's create loop with it.

**The delimiter needed no upstream change.** The plan named the kpath delimiter option as its
one real dependency. It is not needed: WHICHEVER DELIMITER COMES FIRST is the one the address is
written in. `demo:pr:a.b` is a ref, so the dots belong to the id; `git.ref.x@git:ref:y` is a
kinded path, so the colons belong to the id. An id with a dot that you want to continue past is
quoted — `demo.pr."a.b".labels` — which is the plan's own "`.` plus quoting covers every real
id", arrived at from the other side.

**`!key` does not survive an ordinary write.** The plan said to annotate payloads that hold
arrays worth addressing. `!key` is a registered merge operator, so a write EXECUTES it: logd
merges the array by key and stores it untagged. `(key)` reach works against a payload written as
DATA (`entity.Raw`), which is what the engine's own writers do, and `verse state put` cannot
leave one. Said in [reference/entities.md](../reference/entities.md) and in the refusal itself.

Two smaller ones. **Flags come before the path** (`verse state --paths demo.pr`): a command with
subcommands stops parsing flags at its first positional, and that rule is what routes
`state put <ref> <payload> --if` to the write's precondition — so it is named in the error rather
than bent. And **`--paths` prints refs**, since a ref is the same address with `:` for a
delimiter and that is what `get`/`put`/`rm` take.

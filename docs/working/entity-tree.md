# An address is as deep as the tree it lives in

Status: done, with one residual named in §8. The charter became a tree (`verse charter`,
packages at an address) and the entity model did not, which was half a change: a charter
installed at `verse-dev.drift` could write only `verse-dev.drift.<id>` — one level of ids and
nothing below — so a drive keeping two kinds of thing (a tip fact and a ledger of findings)
could not live where its charter lives.

This note is what the rest of the change rests on. It records the one decision that shapes
everything after it, so that the next person — or the next session — does not have to re-derive
it from the code.

## The decision: the QUESTION carries the depth, not the store

With three segments the document is self-describing: `{a: {b: {c: {stage: open}}}}` has entities
at depth 3 and payloads below, and every walk can find them without being told. **At any depth
it is not.** The same document could be an entity at `a.b.c` holding `{stage: open}`, or one at
`a.b` holding `{c: {stage: open}}`. Nothing in it says which.

Two ways out, and they are genuinely different systems:

1. **The store records the boundary** — a marker field in every payload, or an index of entity
   addresses beside the data. Reads become unambiguous, and verse gains a second source of truth
   about where things are, which every writer has to maintain and every migration has to move.
2. **The question carries it.** A rule's subject pattern has a path of N segments, so its
   subjects are the nodes at depth N. A watch is on a subtree and emits at the depth it was
   asked for. `verse data --paths` lists at the depth it read at. Nothing in the store says
   where an entity ends, because nothing needs to: entity-hood is a LENS, not a stored fact.

**Taken: 2.** No marker, no index, nothing to migrate — and two rules may legitimately look at
one subtree at different depths, which under (1) is a contradiction to be resolved and here is
just two questions.

What it costs, stated plainly: a walk can no longer answer "what entities are here" on its own.
Every caller that wants entities now says how deep they are, and a caller that does not know is
a caller that has not decided what it is asking.

## Phases

Each lands with the suite green.

1. **`Ref` is a path.** Done. It holds a root and a segment vector (as a string, since a Ref is
   a map key in a hundred places and a slice is neither comparable nor a key), `Segs`/`Len` are
   the readers, `RefAt` no longer pads a short address to three, and `String` renders the colon
   form for the three-segment addresses that exist today and a dotted path for anything deeper —
   because `a:b:c:d` cannot be read back (an id may hold colons) and `a.b.c.d` can.
2. **`trigger.Match` takes a path.** Done. `{path: [verse-dev, drift, head, "*"]}` beside the
   `{system, kind, id}` shorthand — a union rather than a `rest:` fourth dimension, because
   `!verse-path [a b c]` already spells exactly this and a fourth field only means anything
   alongside the other three. A SHORTER PATH IS A PREFIX, which is what `{system, kind}` with no
   id already means.

   **Every element of a path is a MATCH**, in tony's own grammar, asked of that one segment:
   `!glob "hot-*"`, `!or [hot, cold]`, `!and [!glob "h*", !not hold]`, or a bare name for
   equality. Nothing in verse enumerates the operators — `tony.Match` owns them, which is what
   keeps a segment as expressive as a payload predicate and stops a second grammar growing here.
   ONE CONVENTION sits above that: a bare `*` is any segment, because that is what every other
   address spelling in verse means by it. Inside an operator a `*` is an ordinary character, so
   the id that IS an asterisk is named in the grammar rather than by an escape — `!glob "[*]"`,
   `!or ["*"]`. (`!raw` is the STORE's escape, added when a charter is written down and consumed
   when it is read back; a charter never writes one, and a segment does not borrow it.)

   A `*` is a POSITION, which is the difference a path forces: an unset `kind` means "and
   nothing below it", where `[demo, "*", 1]` means "any one segment, then this".

3. **The store answers at a depth.** Done. `ListAt(at, depth, pred)`, with the depth coming from
   the pattern's own path and the read narrowed to its literal head. A watch stops at the first
   segment that is not a literal — an operator constrains a level without naming it, so it
   cannot narrow a subtree, and the pattern does the rest.
4. **Derive targets.** Done. A derive may be a LIST whose entries name their address as a path
   — `- {path: [verse-dev, drift, head], value: {…}}` — with the nested map staying the
   shorthand for the two-level case. Strata compare as paths (`pathContains`/`pathOverlap`), so
   a package can keep two levels under one address, which is the case this whole note is for.

   `path:` in the OBJECT form always means a path, at every length; `!verse-path [a b c]` folds
   three segments into the dimensions for the sake of every charter written before this. The
   difference matters exactly once, and a stratum is where: `[verse-dev, drift, head]` is a
   PLACE that derived entities land under, not an entity whose id is `head`.

   `!get-path`/`!list-paths` name their address as `at:` — `{at: [verse-dev, drift, head, abc],
   path: sha}`. The address is `at:` in a read and `path:` in a condition, and that is not taste:
   in a read `path:` is already the FIELD path inside the payload, and the field path was there
   first. Everything above the entity must be literal, for the reason it always was — the level
   check asks whether a read lands at or below the rule's own stratum, and it can only ask that
   of segments known at install.
5. **Writes.** Done. `api.PutEntity` carries a `path` beside `ref`, because the ref spelling
   stops at three (`a:b:c:d` re-reads as three plus an id holding a colon) — so a write four
   segments down is an ordinary write, and the CLI sends the whole address it was given rather
   than splitting it at three. The answer is what stands at the address written, which keeps a
   merge visible wherever the address IS an entity.

   `verse data --paths` WALKS, through `Doc.Paths`, and `--depth N` bounds it; the depth-picking
   form was `Doc.EntitiesAt`, which unset answered at
   the depth an address has always had, which is what every listing written before this wants.

   ~~**Watches** report at the fixed depth … the depth is a matter of PRECISION here, not
   correctness … teaching `WatchSubtree` a depth is worth doing when the waking becomes
   measurable, not before.~~ **Wrong, and §8 is where it was paid for.** Firing was never the
   question — the engine does re-evaluate against the store, so a deep rule woke and selected
   correctly. What broke was every consumer that compared the REPORTED address to one it already
   had. The causal chain is the one that bit: `changedIn` asked "did this commit change my
   subject", the answer arrived about a three-segment ancestor, and so every firing about a
   derived entity began a fresh chain — the causal report named the level crossing as an origin,
   and the throttle, which walks back parents to find a loop, could not see one that ran through
   a derive.

   `WatchSubtree` takes a depth now, and `Commit.ChangedAt` is the same question asked of the
   ROOT stream, which serves every consumer at once and so must still pick one arity for
   `Changes`. The depth is not knowable up front; it is knowable at every point of use, which is
   the whole of what a tree costs here.
6. **The charter tree, whole.** Done. `at:` is a place of any depth and a charter writes where
   it is: `at: verse-dev.drift` keeps its tip fact at `verse-dev.drift.head.<id>` and its ledger
   at `verse-dev.drift.finding.<id>`, two levels under one address, each with a stratum of its
   own. The confinement compares as a path, so it neither widens nor truncates at depth.

   No `writes:` override, still — an address that can write where it is has not needed one.
7. **The colons.** Last, deliberately: the ref spelling is in every stored record, every task id
   and every charter written so far, so it moves when the rest is standing. What is left of it:
   `api.PutEntity.Ref` beside `path`, `Ref.String()` picking a spelling by depth, `ParseRef`
   itself, and the `system:kind:id` prose across the docs.

   **The round trip came early, because it was load-bearing.** `entity.ParseAddr` reads back what
   `String` writes at any depth, and everything that RECOVERS a ref from text uses it — a task's
   subject, a motion's subject, a flow, a question's origin. It had to: `applyHook` recovered its
   subject with `ParseRef`, which reads exactly three segments and takes everything past the
   second colon as the id, so a subject four segments deep whose id is itself an address came back
   as a different ref with no error, and the package's confinement refused the patch. Staging
   showed it as a comment review that did its work and could not record that it had.

   The discriminator is where the colons are: `String` only ever puts one in the last segment, so
   a dotted string whose FIRST segment holds a colon is `system:kind:<id with dots>` and nothing
   else. `ParseRef` is deliberately unchanged — a charter and a command line write the colon form,
   and a dotted string there is an address followed by a payload path (`demo.pr.1.stage`), which
   reading as a four-segment entity would swallow.

   Also left, and smaller: `Doc.Entities` (the fixed-depth walk every listing still uses). ~~and
   `WatchSubtree`'s reporting depth~~ — done in §8, and not smaller than it looked.

8. **The colons, whole; and the shapes that came with them.** Done. `Ref.String` renders the
   kinded path at every depth, `ParseAddr` is the one reader, and `ParseRef`/`DottedAddr` are
   gone with the discriminator they existed for. Five separate address splitters became one
   (`trigger.ParseExpr`, `trigger.factSelFromRef`, `source.ParseClaim`, and the CLI's three
   `<kind>:<name>` parsers), an id token is built from SEGMENTS rather than from a rendering
   (`entity.SegToken`), an ownership claim is a pattern per level at whatever depth, and a
   derived entity is ADDRESSED by its provenance — `<target>.<rule>.<subject…>` — rather than
   named after a flattening of it.

   What that last one cost, and it is the residual worth knowing: **a rule reading a derivation
   says how deep one is**, and how deep follows from the depth of the subjects the PRODUCING
   rule selects — `{path: [signadot, feature, "*", "*", "*", "*"]}`, and the same in a
   `!list-paths` `at:`. A path element meaning "at or below" would retire it, and is not in the
   grammar.

# An address is as deep as the tree it lives in

Status: in progress. The charter became a tree (`verse charter`, packages at an address) and the
entity model did not, which is half a change: a charter installed at `verse-dev.drift` may write
only `verse-dev:drift:<id>` — one level of ids and nothing below — so a drive that keeps two
kinds of thing (a tip fact and a ledger of findings) cannot live where its charter lives.

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
   asked for. `verse state --paths` lists at the depth it read at. Nothing in the store says
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
2. **`trigger.Match` takes a path.** `{path: [verse-dev, drift, head, "*"]}` beside the
   `{system, kind, id}` shorthand — a union rather than a `rest:` fourth dimension, because
   `!verse-path [a b c]` already spells exactly this and a fourth field only means anything
   alongside the other three. A SHORTER PATH IS A PREFIX, which is what `{system, kind}` with no
   id already means. `Matches.contains`/`overlaps` become prefix tests, and strata, write
   confinement and the import graph follow from that without changing.
3. **Derive targets and path reads.** `derive: {a: {b: {c: …}}}` is already nested, so a target
   is a path; `!get-path`/`!list-paths` take one.
4. **Writes and watches.** `ParsePath` stops splitting an address into three-plus-a-field: a
   write is a value at a path, which is what the store does anyway. A watch is told its depth by
   whoever opens it (`WatchSubtree(prefix, depth)`), which is where §2's patterns arrive.
5. **The charter tree, whole.** `at:` at any depth, writing where it lives. No `writes:`
   override until that is true — an address that cannot write where it is has not earned a way
   to write elsewhere.
6. **The colons.** Last, deliberately: the ref spelling is in every stored record, every task id
   and every charter written so far, so it moves when the rest is standing.

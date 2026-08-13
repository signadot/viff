# Absence is `!delete`, not a bare null: implementation plan

Status: plan, nothing built. The argument is
[verse-dev-comment-drift.md](verse-dev-comment-drift.md) finding 10, which is where this was
found and why; this file is the order of work and what each step must prove.

The change in one line: **verse stops spelling absence with a bare null.**

## What is wrong

The store spells "this field is gone" by WRITING a bare null, and hides every bare null it reads
so no caller sees the marker. Two halves of one convention, in `entity/logdstore.go`:

| | |
|---|---|
| `addRemovedNulls`, `withRemovals`, `Write{Value: nil}` | append a bare null for what a replace dropped |
| `nodeToPayload` | drop every untagged null at any depth, so the marker never surfaces |

It works, and it costs the data model a legal value. **A bare null cannot be stored as data
anywhere in the verse** — at any depth, including inside a charter's own `!raw` spec, where by
read time the escape is consumed and what is left is an ordinary object for the stripper to walk
into.

That is not an abstract cost. `trigger-model.md` teaches the absence latch —
`value: !not {reviewed: null}` with a hook that writes `reviewed` — as THE shape for a rule
whose written value it cannot predict, and every spawning rule needs a latch. Stored, that rule
reads back as `!not {}`, which is a perfectly good pattern meaning something else entirely: not
"this field is absent" but "this payload is not a record". Every entity in the slice is a
record, so the rule matches none of them, silently, from the first restart onward.

**A store may choose how it spells absence. It may not choose which values you are allowed to
hold.**

The substrate does not have this problem and never did: logd stores `b: null` as an ordinary
value through restart, log switch and compaction, and `!delete` is what removes a key. The
convention is verse's alone, and it is old enough to predate anyone asking whether it had to be.

## Order of work

Four steps. Each leaves the tree green. 1 is the whole change; 2 proves it where it was found;
3 and 4 are what a `contract` tier owes.

### 1. `!delete` on the way in, nothing on the way out

The three write sites emit `!delete null` — a TAGGED null, which the store executes as a removal
— rather than a bare one. `nodeToPayload` stops stripping, and collapses to roughly its own
first two lines: a node is already a payload.

`source/reflector.go`'s `asStored` goes with it. It exists only to mimic the stripper so a
source's answer compares equal to what a read will return; with nothing stripped, the mimicry
has nothing to mimic. (Its comment was one of the two the drive's own coding agent corrected —
which is the neatest available demonstration that this convention is hard to describe: two
comments about it were wrong, in the same file, for months.)

**Proves:** a payload holding `{a: 1, b: null}` reads back holding both, at the top level and
nested, and inside `!raw`. A `Put` still REPLACES — the field it dropped is gone, not null. A
`Write{Value: nil}` still removes the entity. A field removed by `--at` is absent rather than
null, and absent stays distinguishable from present-and-null, which is the property the whole
change is for.

The risk to look at first: `!delete` on a key that is not there must be inert. It is — a
charter's `result: !delete null` fires against entities that have never had a `result` — but it
is worth an explicit test rather than an inference from a charter that happened to work.

### 2. The absence latch survives a restart

The charter-level proof, and the reason this is being done at all: install a rule whose subject
is `value: !not {reviewed: null}`, read the charter back the way `LoadCharter` does, and assert
the subject is what was written — then that it still selects an entity with no `reviewed` and
skips one that has it.

That test would have failed on every build verse has ever had.

**Proves:** the idiom `trigger-model.md` documents is one a charter can actually be written in.

### 3. What an existing `--data` directory holds

The contract half, and the one that cannot be answered by a test.

Entities written under the old convention carry bare nulls at every place a field was ever
removed. After this change nothing hides them, so they surface as fields whose value is null —
present, where they used to read as absent. A charter matching `!not {x: null}` on such an
entity would now find `x` present and holding null.

Three ways to answer it, and the plan should pick one before any code lands:

- **Say so and move on.** `Breaks:` names it, the advice is "start fresh", and this joins
  contributing.md's open question about whether a `--data` directory survives an upgrade at all
  (it is untried, so this may be a cost nobody actually pays).
- **Sweep on open.** A one-shot pass that deletes null-valued fields, run once per store. Honest
  and slow, and it needs somewhere to record that it ran.
- **Read-compat for one release.** Keep stripping on read for entities written before a marker
  revision. The most work, and the only one that costs nothing to a running deployment.

I would take the first: the population is two staging stores and a laptop, the drive has already
found that a `--data` directory is untested across upgrades, and paying for compatibility nobody
has asked for is how a young system gets old.

**Proves:** nothing to run. It is the decision that makes the tier honest.

### 4. The notice, the tier, and the docs

`contract`: the `!` marker, all four trailers, and a `git issue` filed BEFORE the push, per
contributing.md. What breaks is what a `--data` directory holds, which is on that note's list by
name.

The docs that describe the convention are `entity/logdstore.go`'s own comments (which become
much shorter) and `docs/reference/store.md` if it says anything about tombstones. The absence
latch in `trigger-model.md` needs no change — it becomes true.

**Proves:** somebody upgrading learns about it from the notice rather than from a rule that
stopped firing.

## What this does not do

- **It does not change how a MATCH spells things.** `Present()` is a bare null because a null in
  a match position is a wildcard, and `Absent()` is `!irtype null`. Both are patterns rather
  than payloads and neither moves.
- **It does not touch arrays.** A null in a list is a position, not an absence, and the stripper
  already left those alone.
- **It does not make a bare null mean anything new.** It stops meaning "removed" and goes back
  to meaning null, which is what every layer under verse already thought it meant.

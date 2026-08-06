# Phase 1: the path claim, and converge — implementation plan

Status: plan, nothing built. The design is [shared-ownership.md](../shared-ownership.md); the
phase order is [shared-ownership-plan.md](../shared-ownership-plan.md), and this is its phase 1
dived. Read the design first — this file carries only the order of work and what each step must
prove.

The shape: an ownership claim that names a path inside a payload, a cover that says which part
of an entity an answer is complete for, and a converge that writes, compares and tombstones at
that depth. **Verse-side only.** A connector answers exactly as it does today; the sample
program, `source/sourced` and the two routes do not move. That property is what step 6 tests
rather than assumes.

## Two things the design got wrong, found while planning this

**`@` does not survive contact.** The note spells a claim `linear:project:*@owners.ENG`, and
`@` is already `trigger.ChainSep`, appearing *inside* ids today. The worked example is running
in the tree: [`scripts/lineage.sh`](../../../scripts/lineage.sh) writes
`lineage:trace:lin-pr-lineage@lingh:pr:42`, an id carrying both a `@` and two colons. No split
rule recovers a path from that, from either end. `resolver.go` already refuses a `@` inside a
resolver *name* for the same reason (`resolver/resolver.go:177`), which is the tell that the
character is spoken for.

So **a claim is a document**, and there is no separator to be ambiguous about:

```tony
owns:
- {ref: "linear:issue:ENG-*"}
- {ref: "linear:project:*", at: [owners, ENG]}        # the slot claim
```

The terse string survives as **input sugar at the boundary where a human types one** — the CLI,
and a `--file` install — normalized to the above the moment it is parsed. That is what
`entity.Pattern` already does with `fields` as a spelling of `value`: "naming the carrier after
the sugar is how the general case stays invisible."

**It is deliberately not accepted as a second stored or wire shape**, and the first draft of this
plan had it that way for a bad reason — that every registration standing today would go on
parsing. Compatibility is not owed here: [contributing.md](../contributing.md#open-questions)'s
own open question says a `--data` directory surviving a binary upgrade is untried and "start
fresh" is the honest advice, `sourced` ships in the same image as `verse`, and "a consumer
outside the team" is the stated trigger for versioning and has not fired. What the shim would
have cost is more than what it saved — see step 2.

**A cover cannot spell its path as `at`.** `entity.Pattern` already has an `at` on the wire
(`tony:"field=at,omitzero"`), and it means *where to root the predicate*. A cover needs *what
this answer is complete for*, which is a different question about the same object, so reusing
the name would give one wire field two meanings depending on who read it. It needs its own name;
this plan uses `slot`.

## Order of work

Six steps. 1–2 are invisible; 3–5 change behaviour only for a source that asks; 6 is the first
one anybody can see. Each leaves the tree green.

### 1. `Claim` grows a path

`source/own.go`, and nothing else. `Claim` gains `Field []string`; `Disjoint` gains the fourth
segment, compared **segment-wise** — `owners.ENG` and `owners.ENGINE` are disjoint, `owners` and
`owners.ENG` are not, and a claim with no path is a prefix of every path under it.

`ParseClaim` stays as the terse spelling's parser — it is the sugar, and step 2 is where it stops
being the storage. `Claim.String()` renders a claim back for a refusal message to quote.

**Proves:** the four-row table in the design note, plus the two refusals (a root claim beside a
slot claim; `owners` beside `owners.ENG`). And the negative that matters most — every `owns`
string in the tree today parses to a claim with an empty `Field` and compares exactly as it does
now, which is a table test over the existing fixtures rather than an assertion about one.

### 2. `owns` becomes claims, and stops being text

`source.Entity.Owns` goes from `[]string` to `[]Claim` — parsed **once**, at install, where the
operator is standing. `versed/sources.go`'s disjointness call, the CLI's display, and the
descriptor a source serves all follow it.

**This is a break, and taking it is the point.** A claim is stored as unparsed text today and
re-parsed at four call sites (`source/registration.go`, `source/own.go` ×2,
`versed/sources.go`), two of which carry a defensive *skip it if it does not parse* branch —
`Claims`, which decides what a `--forget` may delete, and `reflectedBy`, which finds it.
Those branches exist only because the stored form is text that might not parse, though
`Validate` already proved at install that it does. They are the belt to a suspenders that was
never loose, and they are load-bearing in the worst place: the one that deletes. A typed field
deletes both, and there is no version of the compatible path that does.

What breaks, all of it in one commit: the `owns` field's type on the registration entity, the
`{system, owns}` descriptor at `GET /source`, and any `--data` directory holding a source. The
sample program and `sourced`'s three connectors are in-tree and move with it. `contract` tier —
`Breaks:` and a notice ([contributing.md](../contributing.md#coordinating-a-break)).

Then **`--forget` has to answer in paths**. `source.Claims(owns, ref) bool` and
`reflectedBy` (`versed/sources.go`) decide what a removal may tombstone, bounded by what the
install proved; with slot claims the answer is no longer a set of refs but a set of *places* —
removing a shared entity outright would delete another owner's slot, which is the exact thing
this phase exists to prevent.

**Proves:** a claim round-trips through the store and the wire as one shape; the CLI's terse
spelling normalizes to it and renders back; `--forget` on a slot-claiming source removes its
slot and leaves the entity; and `--forget` on a ref-claiming source still tombstones exactly
what it did before — which is now an assertion about behaviour rather than about a wire format,
since the format moved.

### 3. A cover gains a slot, and the answer keeps its shape

`source.Reflection.Covers` becomes a `Cover` — an `entity.Pattern` plus `Slot []string` — rather
than a bare pattern. On the wire it is one added optional field, so a source that does not send
it converges exactly as it does now.

**The entities in an answer do not change shape**, and this is the decision worth stating
because the obvious alternative is worse. A slot owner still answers with a whole-entity-shaped
payload (`{owners: {ENG: …}}`); the *cover* is what says which part of it this source is
complete for. The alternative — a path on each `entity.Entity` — would put the same fact in two
places, once per entity, and let an answer disagree with itself.

`Reflection.Check` gains the arm that makes that safe: an entity carrying a field **outside**
every slot its covers name is refused, before anything is written. Silently ignoring it would
let a source believe it had written something it had not.

**Proves:** a cover with no slot behaves as today (the whole existing source test suite is the
assertion); a cover with a slot tombstones the value at that slot and leaves the entity
standing; an answer that reaches outside its slot is refused with the field named.

### 4. Converge writes, compares and tombstones at a slot

`source/reflector.go`'s `converge`, plus one addition to `entity/write.go`.

- **`ReplaceFrom` gains a path-scoped sibling.** It writes at `ref.At()` with `withRemovals(next, cur)`;
  the slot version writes at `ref.At(slot...)` and computes removals within the slot, so a field
  another owner holds is never spelled as a null. **This is the one that must not be got wrong**,
  and the reason is [below](#why-the-replace-is-the-dangerous-half).
- **Skip-unchanged compares the slot**, not the payload — a smaller matter than it looks, and
  worth being exact about. With a correct slot write, a whole-payload comparison only makes each
  pass re-write a value that is already there, which produces no commit and so wakes nothing. It
  costs a round trip per shared entity per pass, and one real thing: `converge` counts `Wrote`
  per write *issued* (`ReplaceFrom` discards `Write`'s committed bool), and `recordPass` stamps
  `lastReflect` whenever `Wrote > 0` — so `sources:*` takes a write every resync interval
  forever, which is the churn that field was designed to avoid.
- **`covered()` answers with places, not refs**, and the tombstone becomes `Write{At: ref.At(slot...)}`
  — which, with an empty slot, is today's entity delete unchanged.
- **The last owner out deletes the entity.** After removing its own slot, a slot-scoped pass
  attempts the conditional delete: the container is the slot's path less its last segment, and
  "mine" is that last segment.

```go
mine := `!all.or [!field "ENG", !irtype null]`   // the null arm is NOT optional — see the design note
entity.Write{At: ref.At(), When: []entity.Require{{At: ref.At("owners"), Pattern: mine}}}
```

**Proves:** two reflectors over one entity, running against each other, with a **commit** count
that stops growing — not a write count, and not an equality, for the reason below. Then: the
entity goes when the last slot goes, and does not go while any other stands. And `current()`'s
cost does not change, since it already Lists the system slice whole.

#### Why the replace is the dangerous half

A whole-payload replace spells its removals against `cur`, and ENG's answer has never heard of
OPS — so it writes `owners.OPS: null`, OPS's next pass restores it and drops ENG's, and every
pass after that commits. Four things about that, in the order they hurt:

- **The install check is the only protection there is, and that is by design.** Two writers on
  one slice has no runtime defence: `lastError` stays clear because both passes succeed, no-op
  suppression cannot engage because every write is a real change, and the cycle throttle is not
  in the picture at all. It relates a commit to the *firing* that caused it, and a source write
  has no firing — `recordCause`'s own comment lists "a git source reflecting" among the ways a
  chain **begins**. Two sources fighting is not a deep chain that a depth bound would trip; it
  is an unbounded series of depth-1 chains, which is not a shape that mechanism has a
  vocabulary for. Nothing is broken there; there is simply nothing watching.
- **The runtime gave up its last defence deliberately, and on the strength of this proof.**
  Converge writes with no precondition *because* the install proved there is one writer — "the
  compare-and-swap a Modify carries defends against a concurrent writer that does not exist".
  So a second writer that gets past the install does not merely race: it clobbers, with the
  mechanism that would have made it a lost update rather than a silent overwrite already spent.
- **Which makes this step load-bearing in a way nothing downstream can compensate for.** Two
  writers each tombstoning the other is exactly what `--allow-overlap` warns about, and today it
  is refused loudly at install. Slot claims install **clean**, because they are provably
  disjoint — so a converge that still replaced whole payloads would turn that loud refusal into
  a silent misbehaviour, which is worse than not having slots at all.
- **And it would look like a healthy busy verse.** No error, `lastPass` and `lastReflect` both
  advancing, both sources reporting success, every charter over the entity firing at the resync
  rate — a rule with an `ask` gate posing one a minute into the docket. The only symptom is a
  commit rate, which cannot be alarmed on without knowing what normal is.

### 5. The scoped and overlapping cases

Three existing behaviours have to be re-asked in the presence of slots, and each is cheap to
check and expensive to discover later:

- A **scoped** source claiming a slot that a baseline source also claims — shadowing is the
  feature, so this must still install.
- `--allow-overlap` now warns about a hazard that has a safe form beside it, so its message is
  wrong as written.
- A source installed with **`--no-probe`** claims nothing, and therefore claims no slot either.

**Proves:** the scoped overlay still shadows; the flag says something true; and no existing
install path changed its answer.

### 6. Linear projects, end to end

The case that produced the finding, and the only step with observable behaviour.

`source/linear/api.go` reads a project's *name* onto an issue today (`project { name }`) and
nothing more; it gains a query for the team's projects and their teams. `Linear.Owns` gains
`{ref: "linear:project:*", at: [owners, <KEY>]}`, and `reflectTeam` a project cover slotted by
team key. The comment in `Owns` that explains why projects are absent becomes the comment that
explains how they are present.

**Proves:** two Linear sources over ENG and OPS, against a fake workspace with one project
shared by both teams — both converge, neither tombstones the other, the project carries both
slots, and it leaves when the second source is removed with `--forget`. That is the whole phase
in one test, and it is the one to write first and watch fail.

**And phase 2's premise turned out to need restating, which the fixture answered for free.**
The root plan asks for wakes-per-commit over a shared entity, on the design note's claim that N
owners means N times the evaluations. In steady state it is **zero**: skip-unchanged at the slot
means a quiet owner writes nothing, so a rule over a shared entity wakes only when an owner's
slot really changes. The amplification is bounded by N but proportional to the rate of real
change, not to the number of owners — which is a much milder thing than the note says, and it is
`TestTwoTeamsShareAProjectWithoutTombstoningEachOther`'s four alternating no-op passes that show
it. Whether the remaining N-per-real-change matters still wants a real deployment, which no
fixture supplies.

## What this phase does not do

- **No `!by`.** The payload-predicate claim, for an owner that is a value rather than a place,
  stays unbuilt — and step 6 is what would show whether anything wants it.
- **No engine or charter change.** A rule over a shared entity is evaluated on every owner's
  write and cannot say which owner it cares about. That is phases 3–4, gated on phase 2.
- **No GitHub source.** `github:project:*` and `github:user:*` are the same mechanism and belong
  to [gh-source.md](../gh-source.md); Linear is here because it is the case that already exists.

## What would stop this

- ~~**Step 3's wire change turns out not to be additive.**~~ No longer a risk: step 2 already
  breaks `GET /source`, so step 3's `slot` on the reflect payload rides the same notice and the
  same `Breaks:` line. Both halves of a source's contract move in one commit, which is also the
  kinder shape for anyone holding one — a single upgrade rather than two.
- **Step 4's comparison is subtler than it looks.** `asStored` exists because a read strips null
  fields, and a slot comparison has to survive the same thing one level down. If that turns out
  to need the whole payload after all, the skip-unchanged rule is in question and so is the
  phase.
- **Nobody wants a shared Linear project enough to test it.** Then step 6 is a fixture rather
  than a drive, and the honest thing is to say so and stop — the design note's argument stands
  either way, and it is cheaper to leave it standing than to carry code nothing uses.

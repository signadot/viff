# Strata: what to build, in what order

Status: **done**, all four phases bar the last, which was optional and is not worth doing yet
(see below). What runs is documented in [strata](../strata.md); this stays as the record of what
was decided and why, including the three things the plan was wrong about.

**Where it differed from the plan, in the building:**

- *The file form needs no fan-out change at all.* The CLI branches on `ArrayType`, and a tagged
  list is still an array. What phase 3 actually needed was classifying a mixed charter file by
  field — a rule has a `condition`, a stratum has a `level` and a `match` — so one file can hold
  both and the CLI sends the strata first.
- *`Matches` cannot be generated.* tony-codegen resolves a named slice type as an int, so
  `Stratum` has a hand-written codec and a curated schema signature, like every other
  `codec=custom` type in the package. The check that holds those two sets equal
  (`schema/curated_test.go`) caught the omission.
- *The level check goes BEFORE the latch check, not after.* Latch is documented as deliberately
  last so a charter wrong in some other way is told about that; a rule acting outside its
  declared level is another way, and a more specific one. Holding a rule happens after both, so
  what holding defers is the ordering question and nothing else.

It said where the code goes, what each step could be tested against, and which decisions had to
be made before the first line rather than during it.

The staging principle throughout: **the existing suite is the oracle.** Every step until the
last is additive, and a charter with no strata declared must behave exactly as it does today —
one implicit stratum, intra-stratum unchecked, the cycle throttle unchanged. If a step makes an
existing test fail, the step is wrong, not the test.

## Two decisions the wire format cannot dodge

Both are open in the note, both are cheap to decide and expensive to change once a charter has
been written against them.

**What a derive's first key names — a stratum's `match` or its `name`.** This is not a
preference; it decides whether check #2 is a match test or a lookup. Against the match, a derive
addresses what it writes and the check is `MatchRef` over the target's ref. Against the name, a
derive addresses a stratum and the check is a map lookup, which also lets a stratum's match
change without rewriting every derive that targets it — but then the derive no longer says where
its write lands, and a reader has to hold the stratum table in their head. Take it as the match,
which is what the note takes, unless the second consideration turns out to matter more in
practice than it reads.

**A rule whose `stratum:` names no installed stratum.** Refused, or installed dormant. The note
argues dormant, because refusing makes the order of two independent installs matter, which is the
property one-entity-per-rule exists to avoid. Dormant costs an engine field (rules held back) and
a re-scan when a stratum is installed. Decide before phase 1: the check is in `addTrigger` either
way, and the difference is whether it returns an error or files the rule somewhere.

Settled, and recorded here because the plan was written the other way first: **the name addresses
and the level orders.** A stratum is `trigger:strata:sdlc` carrying `{level, match}`, and a rule
says `stratum: sdlc`. The two constraints that force it — a file keyed `!key(name)`, and an
install that refuses two strata sharing a level — cohere only this way round: were the level the
id, a duplicate level could not be written in the first place, so there would be nothing for the
handler to check.

## A constraint the checks need, which the note does not state

**A stratum's `match` may constrain system and kind, and nothing below.** No `id`, no `value`
predicate.

This is not tidiness. Check #2 asks whether a derive's target lands in a stratum, and at install
time the target's ref is `{system, kind, ID: <engine-generated>}` — the id is a function of
(rule, subject) and neither is known until fire time. A stratum match carrying an id glob makes
that check undecidable at install, which is the one property the whole proposal is bought for.
Check #3 has the same shape: pairwise disjointness over system and kind is a glob intersection
over two segments and decidable; over arbitrary payload predicates it is not.

It costs nothing. `Match.Selects()` (`trigger/match.go:306`) is already the system-and-kind
predicate, `Match.HasPredicate()` already answers the payload half, and every example in the note
is system-only. Rule subjects are unaffected — they keep ids and predicates, because check #1
runs a *subject* against a stratum pattern that constrains only the two segments above it.

## Phase 1 — strata exist; nothing derives yet

The ordering, installed and checked, with no new action face. Check #2 is vacuous because there
is nothing to check, which is what makes this phase safe: it cannot change any firing.

- **`trigger/stratum.go`** (new). `StrataSystem`/`StrataKind` = `trigger`/`strata` beside the
  existing `CharterSystem`/`CharterKind` (`trigger/charter.go:29`), `StrataRef(name string)`, and
  the `Stratum` type — `{Name string, Level int, Match Match}` — with the persist/load pair
  modelled directly on `persist`/`LoadCharter`/`ruleOf`. Same `specField`, same `entity.Raw`
  escape, and for the same reason: a stratum's match is full of operator tags (`!or`, `!glob`)
  and an unescaped write has the store execute them. `ruleOf`'s name-disagrees-with-address
  refusal (`trigger/charter.go:146`) applies verbatim, for its own reason: the name is the address.
- **`Trigger.Stratum string`** in `trigger/trigger.go`, `tony:"field=stratum,omitzero"`. Empty
  means the implicit stratum, which is what every rule installed before this has.
- **`AddStratum`**, carrying the two set-properties, because neither is a property of the stratum
  on its own:
  - **no two strata share a level.** The number is the whole ordering, and a charter that gives it
    twice has not said which comes first. Refused with the shape of `addTrigger`'s duplicate-name
    refusal (`trigger/trigger_api.go:69`) — including the same "remove it first", since replacing
    a stratum silently would re-order rules already installed under it.
  - **check #3, pairwise disjointness**, against the strata already there.

  Both must refuse on the API path and *report-and-skip* on the load path, which is the split
  `LoadCharter` already makes (`trigger/charter.go:110`): a verse whose store somehow holds two
  strata at one level should come up complaining, not refuse to come up.
- **Check #1 in `addTrigger`** (`trigger/trigger_api.go:27`), with the other install validation and
  *before* the latch check, which is documented as deliberately last. It is
  `stratum.Match.MatchRef` over the rule's subject when the subject names, and a pattern-contains
  test when it selects.
- **`LoadStrata`** called before `LoadCharter` at startup, for the obvious reason.
- **`RemoveStratum`**, which is the question phase 1 is most likely to get wrong by not asking it.
  Removing a stratum that rules still name would silently unstratify them — every check those
  rules passed was against a pattern that no longer exists, and nothing re-runs. Refuse while any
  installed rule names it, with the count and a name or two in the message. That is not the
  dangling-rule question in reverse: a rule installed before its stratum was never checked against
  anything, whereas these were checked and would be quietly un-checked. Different directions, and
  only this one destroys a guarantee already given.
- **Wire and CLI.** `POST /strata` in `versed/server.go` beside `addTrigger`
  (`versed/server.go:1420`), including the `?scope=` handling, which is not optional — a scoped
  engine with no strata of its own would silently accept rules the baseline would refuse.
  `verse strata add|show|remove` in `cmd/verse/`, following `trigger.go`'s shape. `verse entity get
  trigger:strata:sdlc` falls out, as it does for a rule.

Oracle: the whole suite, unchanged. Plus new tests that a rule whose subject falls outside its
stratum is refused, that a second stratum at an installed level is refused, that overlapping
strata are refused, and that a stratum match carrying an id or a value predicate is refused with
the reason above.

Watch for: `trigger_gen.go` and `schema_gen.tony` are committed and `make check-generate` fails
if they are stale (`Makefile:90`). A new field and a new schemagen'd type both need `make
generate` in the same commit.

## Phase 2 — `derive`

Only now, and only on top of phase 1, because a derive that installs before the ordering does is
precisely the unchecked cross-entity write the proposal exists to remove. With check #2 live from
the first commit, a derive is unusable until a higher stratum is declared — which is correct, and
means phase 2's tests declare two strata.

- **`Action.Derive`** in `trigger/trigger.go`, beside `Patch`. Shape is `<system>.<kind>` then
  payload, so the Go form is a map from system to a map from kind to `entity.Payload`.
- **`Action.validate`** rejects a derive carrying any `$reference`. This is the load-bearing
  check of the whole design — literal content is what makes the level check static and what makes
  upward reads inert — so it gets an error message that says *why*, in the register of the
  existing refusals (the `provider:`-without-`grant:` message at `trigger/trigger.go:316` is the
  model: it explains the ceiling, not just the rule).
- **Check #2** in `addTrigger`: for each derive, its `{system, kind}` must be matched by some
  stratum whose level is strictly greater than the rule's. Matching nothing, or matching only at
  or below, is refused.
- **`deriveID(rule, subject)`** in `trigger/trigger_api.go` beside `spawnID`
  (`trigger/trigger_api.go:178`), reusing `safeSeg` and the fnv fallback. Injective in subject by
  construction, which is what single-writer rests on.
- **Firing, in `Action.applyOne`** (`trigger/trigger.go:408`), after the patch and before the
  spawn — a derive is state, and a process started about a state the trigger failed to establish
  is the failure that path already refuses to create. The write goes through `patchEntity` with
  `ev.Rev` as the parent, exactly as the subject patch does (`trigger/trigger.go:430`), so the
  higher stratum's rule inherits the causal link across the crossing.

  What that buys is the **report**, not the bound. Losing the link would reset the depth at every
  crossing, and a reset is sound: a chain never returns to a stratum it has left, so an unbounded
  chain has an unbounded tail inside one stratum, and a per-segment counter still trips there.
  What a broken link costs is the throttle's actual deliverable — walking back parents to name the
  rules that looped (`trigger/causal.go:31`) — which would truncate at the crossing and report a
  loop as though it began there. Depth and parent are separate fields on `link`
  (`trigger/causal.go:99`), which is what keeps phase 4 a policy change at the cap rather than a
  re-plumbing: reset the depth, keep the parent.
- **Subject linkage** written by the engine, on the spawn precedent (`trigger/trigger.go:443`):
  the derived entity records which subject it is about, and the consumer at the higher stratum
  reads it exactly as a consumer of a process reads `flow`.

Oracle: a two-stratum charter where the lower derives and the upper acts, asserting the derived
entity's id is stable across a re-fire (the idempotence `spawnID` has), that two subjects give two
entities, and that a cascade's causal depth is continuous across the crossing rather than
restarting. `trigger/causal_test.go` and `trigger/spawn_test.go` have the shapes to copy.

## Phase 3 — the file form, keyed

The transport is already right: the API is per-entity and the CLI fans a charter file out into
one POST per rule (`cmd/verse/trigger.go:129`). What is left is the document shape, and it stays
**flat** — today's list of triggers, with a `stratum:` on each and the list tagged:

```
!key(name)
- name: urgent-label
  stratum: observe
  condition: ...
  action: ...
```

Strata are their own keyed list, `!key(name)` over `{name, level, match}` — a second document in
the same file (`---` already separates them, `cmd/verse/trigger.go:130`) or a file of its own.

Nesting rules inside their stratum was the other candidate and is worse in the way that matters:
it makes a rule's position in the file its declaration of level, so moving a rule between strata
is a re-indent, a diff no reviewer reads as a semantic change, and a merge conflict resolves by
picking a side. Flat, `stratum: observe` is a field like any other, and the fan-out does not have
to flatten anything before it POSTs. The level-keyed document the note sketches is out for a
plainer reason: no sparse arrays.

**The rule to hold to: a collection's `!key` is the entity's id.** Both lists key on `name`,
because both entities are addressed by name — `trigger:rule:<name>` and `trigger:strata:<name>`.
That is what makes the file form and the durable form one thing seen twice rather than two
formats to keep in sync, and it is why re-sending an edited file merges into the entities it
already wrote instead of orphaning them.

Two consequences worth taking while here:

- **Emit strata before rules.** A file then never produces a rule whose stratum names nothing, so
  the dangling case narrows to ad-hoc single-rule installs — which is the only place it was ever
  a real question.
- **`!key` invites `apply`, and this is not it.** Associative merge is what makes "re-send the
  edited charter" meaningful, and the natural next ask is that a rule dropped from the file be
  removed from the verse. `trigger add` adds; a reconciling `verse trigger apply` is a separate
  piece of work with its own question (what owns a rule installed by hand). Name it, do not build
  it here.

**The spelling is verified against go-tony v0.0.113**, the pinned version, which matters because
`!key` has no mileage
in verse — it appears in `deploy/build.tony` and is recorded as unused otherwise
(`docs/working/llm-memory-plan.md:127`), and kpath's `!key` *segment* has no defined upstream
semantics (`docs/trigger-model.md:986`). What holds:

- `codec.ParseNodes` keeps the tag. The root comes back `ArrayType` with `Tag == "!key(name)"`,
  and the elements are ordinary untagged objects.
- **The existing fan-out needs no change at all.** It branches on `doc.Type == ir.ArrayType`
  (`cmd/verse/trigger.go:135`) and the tagged list is still an array, so the tag rides along
  ignored and each element decodes into a `Trigger` exactly as an untagged list's does. Phase 3
  is therefore smaller than it reads: the fan-out is already correct, and the work is the strata
  document and the emit order.
- `---` separation keeps each document's own tag, so strata-then-rules in one file works.

One thing the probe turned up that is worth acting on: **unknown fields decode silently.** A
trigger carrying `stratum: observe` parses clean today, ignoring it — gomap has no strict-unmap
option (`gomap/options.go:62`), so this cannot be fixed upstream. Version skew is not the concern
here; a typo is. `startum: observe` installs, reports success, and puts the rule in the implicit
stratum, where every check is vacuous — so the one field that says which guarantees apply is
exactly the field a misspelling silently discards. The guard is a field-set check on the node in
the API handler, before decode. It is a general improvement rather than strictly this work, but
this is the work that makes a dropped field mean something.

A file that is a bare list of triggers keeps working and means the implicit stratum.

## Phase 4 — the throttle tightening, if it is worth it

Optional, and honestly marginal: it improves how fast a runaway inside one stratum is caught, not
the worst case, which is unchanged. `chainCap` (`trigger/causal.go:136`) reads the installed
trigger count late, at the check; the tightened form reads the count of the *firing rule's*
stratum, and `stamp` resets the depth when a commit crosses a level. Do it only if a real charter
is slow to trip, and treat the reset as the risky half — it is the same shape as the fan-out bug
`causeFor` was written to fix, where a naive count walked itself up and tripped on width.

## Not in scope, deliberately

- **Fencing a spawn's writes to its stratum and above.** The note's authority counterpart. It is
  a fire-time check against a declared ceiling, which is a different mechanism from three
  syntactic install checks, and it belongs with [authority](../roadmap/authority.md).
- **Rollups.** Forbidden by the identity function, and that is what buys single-writer. If an
  aggregate is wanted it needs its own construct.
- **How a derived entity leaves.** Nothing reaps derived entities unless a charter says to.

## What fails quietly

Most of this fails loudly: a check refuses an install, a codec fails to decode, a test goes red.
Two things do not.

**The literal-content refusal on a derive.** Let a `$reference` through and nothing breaks
visibly — check #2 still decides, because the *address* is literal either way. What goes is the
read-side argument: a derive that can write different bytes on each firing makes an upward read a
live cycle instead of an inert re-fire, and the only thing left catching it is the throttle at
runtime. The guarantee degrades from static to hoped-for with no signal.

**The file's `!key` fields.** A wrong key orphans on re-apply rather than erroring, and the
symptom is a store growing duplicate strata while the file looks right.

**A misspelled `stratum:`.** Decoded, dropped, install reported as success, rule in the implicit
stratum where nothing is checked. See phase 3.

Phase 2's causal stamping was on this list and does not belong: losing it costs report quality —
a loop named from the crossing rather than from its origin — not the bound.

## The implicit stratum, and what it does not license

There is no migration and none is wanted: a charter with no `stratum:` anywhere behaves exactly as
today, which is what makes the existing suite a usable oracle. An unstratified rule sits in the
implicit stratum, where check #1 is vacuous — no pattern to match a subject against — and that is
correct, because it is precisely today's semantics.

**A rule in the implicit stratum may not derive.** Check #2 asks for a *strictly higher* stratum,
and the implicit one has no level, so the question has no answer rather than a permissive one.
Refuse it at install: `derive` is the whole reason the ordering exists, and letting it out from
under an unordered rule gives back the unchecked cross-entity write in the first release that has
the feature. Declaring a stratum is what buys a derive, which is the trade the design is.

## How the open questions closed

- **What a derive's first key names** — the MATCH, as the note took it. A derive says where its
  write lands, and a reader does not have to hold the stratum table in their head to see it.
- **A rule whose `stratum:` names nothing installed** — HELD, as the argument leaned. It has
  passed every other check first, so what is deferred is the ordering question alone.
- **The field-set check** — built, and wider than planned: an unknown field is named at the top
  level of a rule, in its condition, and anywhere in its action tree. It stops where the shape
  stops being the charter's (a spawn is a launch spec; a patch payload is the author's).
- **Phase 4, the throttle tightening** — NOT built, and the reason stands: it buys detection
  speed, not a smaller worst case. Nothing has been slow to trip. The causal link across a
  crossing IS carried, so it stays a policy change at the cap rather than a re-plumbing.

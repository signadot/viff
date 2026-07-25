# The trigger model — entities, matches, and one selector grammar

*Status: **partly built**. Steps 1–2 of the migration below have landed — `Match` exists as
the named leaf, `Trigger.Matches []entity.Pattern` is now `Condition{When, Subject}`, and
`action.setRef`/charter-set `spawn.subject` are gone. Flow-as-a-query (step 4) and set-valued
`Subject` with fan-out (step 3) have landed too: `flows[subject]` is gone, replaced by a
subject-`Match` over live processes (so `flow pause demo:pr:*` works), and a `Subject` naming
a `system:kind` set fans the action out one member at a time. Still to come: the tony match
leaf (step 5), and evaluating a *temporal* `When` (which needs a long-lived monitor rather
than per-change dispatch, and is rejected at install with an error saying so) — so a
set-valued `Subject` and a temporal `When` are rejected at install for now, with an error
that says why. The implementation plan is `trigger-model-plan.md`. The substrate this leans
on — per-entity change events — is `change-events.md`.*

## Two layers: point-in-time matches, temporal formulas

There are two grammars, and keeping them apart is the whole game:

- **Match** — a *point-in-time* tony selector over the entity-verse: "which entities satisfy
  this **right now**." No time in it — no `until`, no `deadline`.
- **Formula** — a *temporal* composition of matches with operators (`quorum`, `fact`,
  `until`, `deadline`, `eventually`, …), evaluated over the verse's **evolution**. A
  formula's leaves are matches.

Every trigger concept lands on exactly one layer, and *which* is the thing to be
unambiguous about:

- **Entity** — a node `system:kind:id` with a small payload. The only noun.
- **Match** — a point-in-time selector over the verse. The atom.
- **Formula** — temporal operators over matches. The composition.
- **Process** — the unit of work, about exactly **one** subject entity.

| concept | grammar | what it expresses |
|---|---|---|
| **Subject** | **Match** — point-in-time | the entities a firing is *about* — const system/kind + an entity predicate; **no** temporal operators |
| **Flow** | **Match** — point-in-time | which *live processes* to steer, by their subject |
| **When** | **Formula** — temporal | *fire* when the verse's evolution satisfies this — an edge on a match, or `until`/`deadline`/… |
| **Gate** | **Formula** — temporal | *proceed* at a threshold — `quorum`/`fact`/`until`/`deadline` |
| **Goal `doneWhen`** | **Formula** — temporal | *done*, latched, when the witness appears |

The cut: **name entities at an instant → a `Match`; relate the verse over time → a
`Formula`.** `Subject` and flow-addressing are point-in-time — a subject is *named at the
instant a firing happens*, never accumulated — so they are plain matches (const system/kind
preferred), and giving them a `deadline` or an `until` would be a category error. `When`,
gates, and `doneWhen` all relate the verse's *evolution*, so they are formulas — and `When`
and a gate become the **same** grammar, differing only in consequence: a satisfied `When`
*fires*, a satisfied gate *proceeds*. (This is what the first draft glossed as a vague
"rule"; the rule is a temporal formula, and only `When`/gates/`doneWhen` have one.)

## The trigger

```go
type Trigger struct {
    Name      string
    Condition Condition   // the cause: what fires it, and what it is about
    Gates     []Gate      // the guard — conjunctive resolutions, unchanged
    Action    Action      // set / spawn — declarative data, unchanged
}

// Condition is the ECA "cause". When and Subject are grouped, not nested, because the
// Subject is resolved at the instant When fires — there is no subject before the match —
// yet neither field owns the other. Two halves of "when X, about Y" — and two DIFFERENT
// grammars: When is temporal, Subject is point-in-time.
type Condition struct {
    When    Formula   // temporal — fires when the verse's evolution satisfies this
    Subject Match     // point-in-time — the entities this firing is about, at the fire instant
}

// Match is a POINT-IN-TIME selector over the entity-verse — the atom, and the leaf of every
// Formula. Today it is a plain map (system/kind/op/fields, interpreted by the existing typed
// matcher); it will become a real tony match. This type is the SINGLE place that swap
// happens. Subject and flow-addressing are bare Matches — no temporal operators.
type Match map[string]any

// Formula is the TEMPORAL grammar: matches composed with operators over the verse's
// evolution (quorum/fact/until/deadline/eventually/…). This is the existing formula grammar
// (its leaves are Matches); When, Gate, and Goal.doneWhen are Formulas. A single match is
// its degenerate form — an atom, fired on the edge into satisfaction.
type Formula struct{ /* Op; leaf Match; operands []Formula — see formula.go */ }
```

Two deletions come with it:

- **`Matches []entity.Pattern` → one `When`.** The list was a conjunction of point-in-time
  patterns over *one* event, so it always either collapsed to a single pattern or was
  unsatisfiable — it bought no expressiveness and invited reading a cross-entity/temporal
  join into the "when." Relating over time is a real need, but it belongs in the temporal
  grammar: `When` is now a single **`Formula`** (whose atom form is one match), so `until`,
  `deadline`, and thresholds compose there — where they compose for a gate — instead of
  being faked with a list.
- **`action.setRef` and `spawn.subject` → `Subject`.** Both were "what this firing acts on,"
  expressed twice and by side channel. They are the `Subject`.

## Subject — what a firing is about

`Subject` is a **point-in-time `Match`** — never a formula. It *selects* the entities a
firing acts on, evaluated once, at the instant `When` fires. There is no `until` or
`deadline` here: a subject is *named*, not *awaited*. It resolves in one of three ways, all
the same match grammar:

| `Subject` | resolves to | example |
|---|---|---|
| *(omitted)* | the matched entity | `review` — a change to `demo:pr:1` is about `demo:pr:1` |
| a concrete address (`"demo:pr:1"`) | that one entity, **named** — a `set` creates it if absent | publishing a fact nothing has written yet |
| an address glob (`"demo:pr:*"`, `"demo:pr:hotfix-*"`) | the **set** it selects | the whole kind, or a family within it |
| a `system:kind` selector (+ entity predicate) | the **set** of matching entities | broadcast — every `demo:pr` with `stage:reviewed` |
| a deferred `$var` (`"$flow"`) | the address the firing expands it to | `reviewed` — the PR the completed process carried |

The engine **fans out one action/process per resolved entity**, and **each is about exactly
one subject** — the original invariant, intact. A *firing* can yield 0, 1, or N; a
*process / flow / goal* always has one subject.

This is why `Subject` is bounded where a `When` formula ranges freely: it pins `system:kind`
(it must name a definite kind to select and spawn for) and predicates over the single
entity's payload. "all open PRs" is just `{system: demo, kind: pr, entity: {stage: open}}`.

### The string form is an address, in one grammar

There is **one** string grammar for addressing entities by identity, and both the charter
wire and the CLI (`verse flow <addr>`) parse it with `ParseExpr`: three segments, `*` a
wildcard per segment, and an id that may glob (`hotfix-*`). So `"subject": "demo:pr:*"` in a
charter and `verse flow demo:pr:*` on the command line mean the same set, as they read like
they should.

Only a `$var` stays deferred, as a `ref` — it is an address that no amount of parsing can
resolve, because the entity is not known until a firing expands it against the changed
payload. An expansion is then re-read as an address, so it is held to the same grammar; a
partial result is a no-op rather than a set of unset dimensions (which, being wildcards,
would *widen* the firing across a kind or a system).

The one distinction inside the address form is **name vs select**. A glob-free address
*names* its entity, so an action's `set` may bring it into existence — that is how a trigger
publishes a fact nothing has written yet. A glob anywhere *selects* over what exists, so it
can only act on entities that are really there (and can never conjure one called `*`).

> This replaced a parse where *every* string became a `ref` — and `ref` is not a matcher
> dimension, so nothing read it. The sugar silently misbehaved in both positions: `"when":
> "demo:pr:1"` constrained nothing and fired on every event in the verse, and `"subject":
> "demo:pr:*"` named an entity literally called `*`, which the action's `set` then created.
> Routing the string through the address grammar fixes both, and is the natural seat for the
> id glob.

## Flow — a subject-match over live processes

A flow is **not a stored key.** It is a `Match` evaluated over the subjects of *live*
processes:

```sh
verse flow pause demo:pr:1      # processes whose subject matches demo:pr:1
verse flow pause demo:pr:*      # …the whole kind
verse flow pause 'demo:pr:1/**' # …a related subtree (once Match grows references/globs)
```

Relating processes is what a match *does*, so "relational flows" are not a second primitive
to add later — they are broader subject-matches, available the moment the selector grammar
is. `flow list` is the distinct subjects with live processes; steering applies the match and
acts on the matched processes. This is *smaller* than the current `flows[subject]` registry
(a query, not a maintained index) and strictly more expressive.

Goals sit here too, and split the same way: a goal is a `Subject` (a point-in-time `Match` —
the flow it owns and steers) plus a `doneWhen` (a temporal `Formula` that latches). One of
each layer, no new machinery.

## The substrate that makes a subject well-defined

None of this needs a match to "select one entity" — it needs the **change stream to be
per-entity**, and it is. A docd commit is atomic across many branches, but `LogdStore`
normalises each commit into one `Event` per entity at path-depth `system/kind/id`
(`walkEntities`; see `change-events.md`). So:

- A firing is driven by one event → one `Ref` → `When`'s matched entity is exactly one. The
  subject is well-defined **structurally**, not by constraining the pattern.
- **Case 1** — one commit changes `…:a` and `…:b` → two events → two firings → subjects `a`
  and `b`, for free, with no fan-out logic; just the per-entity grain.
- **Case 2** — a change unrelated to the subject → the `Subject` selector picks the *set* →
  the engine fans out. This is the one genuinely new dispatch path.

(The known cost, recorded in `change-events.md`: transaction atomicity is flattened — the
reactive grain is the entity, not the commit. A shared `Rev` is the only trace, and nothing
groups by it.)

## `Match` — the swap point (and `Formula` above it)

`Match` is the placeholder, and it is the **leaf** grammar. Today it is a map the engine
interprets with the existing typed matcher (`system/kind/op/fields`); tomorrow it is a real
tony match. Naming it as one type means the swap lands **squarely, in one place**. `Formula`
— the temporal layer — already exists and composes those leaves; it grows only as `Match`
does. Two decisions stay quarantined behind the leaf swap:

- **the match grammar** — globs (the `id` glob has landed), references, `!and`/`!or`, nested
  payload matches. (The temporal operators — `until`/`deadline`/… — live in `Formula` and are
  already settled; only the leaf is in flux.)
- **fan-out** — a set-valued `Subject` spawns per entity; `spawnID` must be deterministic
  *per subject entity* (an idempotent re-fire per member), and the runaway backstop meters
  *entities*, not firings.

### The shape the leaf swap lands: `entity: {id, value}`

The flat `id` + `fields` pair (with `entity` a confusing *alias* for `fields` — it sounds
like the whole entity but means only its payload) becomes one nested per-entity match:

```json
"subject": { "system": "demo", "kind": "pr",
             "entity": { "id": <string-or-glob-match>, "value": <tony match> } }
```

`entity` then means the entity: its identity and its content. Two things fall out:

- **`ref` disappears.** With `id` a typed match slot, a glob is a glob because the *slot*
  says so, not because a parser inferred it from the string's shape — and a concrete id is
  just its degenerate case. Nothing is left for a `ref` escape hatch to carry, so `subject:`
  is a pattern end to end.
- **the completion join stops needing a `$var` at all.** A tony match has a `let` binding, so
  "the PR this completed process carried" is expressible *inside* the match language rather
  than by substituting a payload field into the pattern slot from outside. That requires the
  firing's matched entity to be a **value in the match environment** — today `resolveSubject`
  only ever string-expands a template — so what that binding is called in scope is a wire
  contract to settle with the swap.

And then the naming resolves. `$flow` exists only because `$subject` is reserved for the
firing's own resolved subject, so the mirrored process entity could not call its subject
`subject` (`mirror.go`, `goal.go` each carry a comment apologizing for this). With no flat
payload bindings in the subject slot, that collision stops applying: the mirrored key becomes
**`subject`** — which is what it is — and `flow` goes back to naming only the pattern over
live processes that this document says it is. Landing order: `id` glob (done) → `entity:
{id, value}` + `let` → rename `flow` → `subject`.

The earlier "does `When` read one change or the whole verse?" question is **not** an open
fork — the two layers answer it. `When` is a `Formula`, so it is `Monitor`ed over the verse's
evolution and fires on the transition into satisfaction, exactly as a gate does; a `When`
that watches a single change is just its *atom* form. `When` and a gate are therefore one
grammar with two consequences — *fire* vs *proceed* — and nothing there is left to decide,
only whether a given `When` happens to use temporal operators.

## The two demo rules, before and after

```json
// before
{ "name": "review",
  "matches": [ { "system": "demo", "fields": { "stage": "open" } } ],
  "gates":   [ { "name": "review", "mode": "ask" } ],
  "action":  { "set": { "stage": "reviewing" }, "spawn": { "driver": "scripted", "task": "review $subject" } } }
{ "name": "reviewed",
  "matches": [ { "system": "process", "kind": "instance", "fields": { "status": "completed" } } ],
  "action":  { "setRef": "$flow", "set": { "stage": "reviewed" } } }
```

```json
// after
{ "name": "review",
  "condition": { "when": { "system": "demo", "fields": { "stage": "open" } } },
  "gates":     [ { "name": "review", "mode": "ask" } ],
  "action":    { "set": { "stage": "reviewing" }, "spawn": { "driver": "scripted", "task": "review $subject" } } }
{ "name": "reviewed",
  "condition": { "when":    { "system": "process", "kind": "instance", "fields": { "status": "completed" } },
                 "subject": "$flow" },
  "action":    { "set": { "stage": "reviewed" } } }
```

`reviewed` now reads as it means: *when a process completes, about the PR it carried, set
reviewed* — the `setRef` was only ever compensating for `$subject` being wired to the
matched entity.

## Migration

The shape can land before the grammar. In order, each step green + smoke-tested:

1. **Name the placeholder.** ✅ Introduce `Match` (map-backed); the engine parses it into
   today's typed matcher — no behaviour change, no lost coverage. `Requirement.Facts` became
   a `Match` too, so a formula's leaves really are matches.
2. **Restructure.** ✅ `Trigger.Matches []entity.Pattern` → `Condition{When, Subject}`, where
   `When` is a `Formula` (its atom form is a single match, carried by the new `match`
   requirement kind — one atom carrier, so the leaf stays a `Match`) and `Subject` is a
   point-in-time `Match`; `setRef`/charter-set `spawn.subject` folded into `Subject`. Today's
   semantics kept: `Subject` resolves to the matched ref or a `$flow` binding, no fan-out.
   `$subject` now binds the *resolved* subject (set last, so a payload field cannot shadow
   it), and a goal latch publishes `flow` so a reacting trigger says `"subject": "$flow"`
   like any completion. Charter wire, tutorial §3–4/§8–9, overview, and tests updated.
3. **Set-valued `Subject` + fan-out.** ✅ `Subject` may select a `system:kind` set → one
   action/process per member; `spawnID` is deterministic per subject (so a re-fire is
   idempotent per member, enforced by the engine's live-id dedupe); the budget meters
   entities acted on, truncating a too-wide fan-out rather than dropping it. Gates stay per
   firing — a broadcast asks once.
4. **Flow as a query.** ✅ `flows[subject]` replaced by an id-keyed live registry plus a
   subject-`Match` query over it; `flow <cmd> <address>` where `*` is a per-segment wildcard,
   and a payload predicate works too. Spawn is now idempotent per id, which is what step 3's
   fan-out needs.
5. **Grow the leaf.** Swap `Match`'s map backing for a real tony match; the richer leaf
   (globs, references) flows into `When`/gate formulas, `Subject` selectors, and flow
   addressing alike — the temporal `Formula` layer above is unchanged.

Steps 1–2 are the scaffolding refactor; 3–5 are the model filling in, each expressible in the
grammar already named.

## What this buys

One noun (entity), two layers (a point-in-time `Match`, a temporal `Formula` composed over
it), one work-unit (process). `When`, `Subject`, gates, goals, and flows stop being five
mechanisms: the point-in-time ones (`Subject`, flow) are bare matches; the temporal ones
(`When`, gate, `doneWhen`) are formulas over matches. A reader learns *match* once and
*formula* once, and then relational flows, broadcast fan-out, and store-level conditions are
not new features but those two layers at a wider setting. Small, and — because `Subject`
cannot smuggle in a `deadline` and `When` cannot pretend a list is a join — unambiguous
about which layer each concept lives on.

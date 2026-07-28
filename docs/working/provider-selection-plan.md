# Provider selection: development plan

Status: **commits 1–3 are landed on `provider-selection`; commit 4 is not started and needs a
decision, not a keystroke.** Kept because the "decisions this plan hardcodes" list below is
the review surface for what was built — each item is a place the design left a question open
and the code answers it one way. Two items changed on contact and are marked ✎.

The design and its argument are in [provider-selection.md](provider-selection.md). This is
the build order: four commits, what each one touches, what it hardcodes, and how it is
verified. Branch `provider-selection`, worktree `.worktrees/provider-selection`.

Read the "decisions this plan hardcodes" list first — the design doc leaves several questions
open, and this plan answers each one in a particular direction. Disagreeing with one of those
answers is cheaper now than after the commit that assumes it.

## Decisions this plan hardcodes

1. **A provider fragment is read field-by-field, not decoded into a `launch.Spec`.** A
   provider entity's payload mixes selection attributes (`tier`, `risk`) with fragment fields
   (`driver`, `frame`, `tools`, `grant`) in one namespace. Decoding the whole payload into a
   Spec would make every unknown attribute a codec question; reading the four known keys the
   way `eligibleProviders` already reads `driver` (`entity.Str`) keeps attributes free-form.
2. **The merge rule lives in `launch`, as one function with one table test** — not spread
   across the engine. It is the piece most likely to be got subtly wrong (absent vs empty on
   both sides), so it should be testable without an engine.
3. **A spawn carrying `provider:` must declare `grant:`, enforced at install** — in
   `Action.validate`, alongside the existing spawn/seq contradictions. This follows
   `Condition.validate`'s stated stance: a charter that cannot mean what it says fails at
   install rather than silently doing something else. Without it the ceiling is ⊤ (see the
   design doc, "The ceiling is ⊤ unless the spawn says otherwise").
4. **A provider that violates the ceiling — or sets a field the engine does not honor — is
   made INELIGIBLE, not silently trimmed.** It never reaches a ballot, and `whyNoEligible`
   says how many were excluded and why. Trimming would mean a doer that runs under an
   envelope different from the one its entity states, which is the failure mode
   `process/authority.go:9-25` was written to end.
5. **`vector` is not settable by a provider in this cycle.** Setting it makes the provider
   ineligible per (4). An ordering over `auto|ask|park` that would let a provider tighten
   autonomy is deferred, not rejected.
6. ✎ **No schema regeneration** — *this one changed.* The rule as written: nothing adds a
   field to `launch.Spec` or `launch.Provider`, because the fragment lives in an entity
   payload, which has no schema. That held for commits 1–2. Commit 3 adds exactly one field,
   `Spec.Doer`, and regenerates. What the rule missed is that the ANSWER to a selection has to
   reach the process entity, `mirror.Reflect` owns that payload and re-Puts it on every event,
   so the spec is the only channel — the same reason `Chain` rides it. One field, engine-owned,
   for provenance rather than for anything a charter writes.

## Commit 1 — a selection binds a fragment

Mechanical: what a selection returns stops being a string. No authority change, so it is
reviewable on its own.

**`launch/`** — a fragment type and the merge:

- `type Fragment struct { Driver, Frame, Grant string; Tools []string }` — the provider-settable
  subset of a Spec, named the same as the Spec fields it overlays.
- `func (s Spec) Overlay(f Fragment) Spec` implementing the table from the design doc:
  `driver`/`frame`/`grant` replace when set and inherit when absent; `tools` **intersect**,
  with spawn-empty meaning all (so a provider list narrows) and provider-empty meaning do not
  narrow further.

**`trigger/engine.go`**:

- `eligibleProviders` returns `([]string, map[string]launch.Fragment)` — it already reads
  `driver` off each payload at line 969; it now reads `frame`, `tools`, `grant` too. Inline
  `Eligible` ids yield `Fragment{Driver: id}`, preserving today's option-is-the-driver
  behaviour.
- `driverFor` becomes `fragmentFor`, keeping the same fallback: an option no longer in the
  registry yields `Fragment{Driver: option}`.
- `selectProvider` returns `(launch.Fragment, bool)`. Both exits that return `("", false)`
  today return the zero fragment.

**`trigger/trigger.go:390`** — `applyOne` applies the fragment: `spec = spec.Overlay(frag)`
instead of `spec.Driver = driver`.

**Tests**: `launch/launch_test.go` gets the overlay table (each field × absent/empty/set on
both sides). The six `selectProvider` call sites in `trigger/provider_test.go` change shape;
add one asserting a registry provider's `frame`/`tools` reach the bound spec.

Verify: `go build ./... && go test ./launch/... ./trigger/...`

## Commit 2 — the ceiling

The authority commit. Nothing here is optional relative to commit 1 shipping — a fragment
that can carry `grant` without this is the escalation the design doc opens with, so **do not
merge 1 without 2**.

**`trigger/trigger.go`** — `Action.validate`: a spawn with `Provider != nil` and an empty
`Grant` is an install error, worded so it says what to write and why (the ceiling exists
because a provider entity is writable by anyone who can write entities).

**`trigger/engine.go`** — `eligibleProviders` filters:

- resolve the spawn's grant once (`process.NamedGrant`, importable from `trigger`; note it
  currently resolves in `versed.launchOf` at `versed/server.go:245` — this is a second
  resolution site, and consolidating them is a follow-on, not this commit);
- for each candidate, resolve its `grant` and drop it unless `ceiling.Subset(candidate)`;
- drop any candidate setting `vector`, `resolver` or `environment`;
- count the drops for the message below.

This requires `eligibleProviders` to see the spawn, not just the `Provider` — signature grows
a `launch.Spec` (or the resolved ceiling). `selectProvider` likewise.

**`whyNoEligible`** gains a fourth cause: registered, matched, and excluded by the ceiling.
The existing three are "no provider declared", "nothing registered", "all excluded by match";
this one is fixed in a different place again (widen the spawn's grant, or narrow the
provider's), so it earns its own sentence.

**Tests** (`trigger/provider_test.go`, or a new `trigger/provider_authority_test.go`):

- a provider whose grant exceeds the spawn's ceiling never appears in the option set — assert
  on `eligibleProviders`, not on the spawn, so the vote path is covered by construction;
- the ballot for `select: vote` contains only seatable candidates (the design doc's "a
  decision with no seat");
- `read-only` under a `no-delegate` ceiling **passes** — this is the case that works only
  because `namedGrants` shares atoms, and a test pins it before `Grant.Subset` is ever made
  more complete;
- ✎ *the plan wanted a path-scoped provider grant refused under a coarser ceiling, to
  document `Subset`'s incompleteness. It is unwritable: a provider's `grant` is a NAME from
  `namedGrants`, so there is no way to express a structured one and no reachable case where
  the incompleteness bites. What landed instead is the full 16-cell ceiling matrix over the
  four names, which pins every pair the vocabulary can express — so whichever arrives first,
  a structured grant or subsumption, the change is visible;*
- tools intersection: spawn `[read_file]` + provider `[read_file, run_bash]` binds
  `[read_file]`;
- a spawn with `provider:` and no `grant:` fails `AddTrigger`.

Verify: `go build ./... && go test ./trigger/... ./process/...`

## Commit 3 — the envelope is visible

Selection can now change what a process may do, so what it may do has to appear somewhere.

**`mirror/mirror.go`** — `Meta` gains the resolved envelope (`Grant`, `Tools`, and the
selected provider id, so the entity says *who* was picked and not only which driver ran).
Reflected on **every** select mode, including `auto` — the design doc's point that `auto`
opens no channel by design (`trigger/engine.go:882`) and therefore has no other surface. This
is provenance of a spawn, not a decision anyone took, which is where the existing comment
says such things belong.

**`cmd/verse/vote.go`** — `optionLabels` (line 131) renders what differs between options. Bare
names are right when the options differ only by name; once they differ by envelope, a voter
picking between them is choosing an envelope. Render the differing fields only, so the common
case stays as terse as it is today.

`verse providers` needs no change: it already prints every non-`driver` payload key, so
`grant=` and `tools=` appear for free. Confirm that in a test rather than assuming it.

**Docs**: `docs/tutorial.md` §6 (the registry section around line 690) gains the fragment and
the ceiling; `docs/trigger-model.md:343` mentions the who-axis and should say a selection binds
an envelope. Fold `provider-selection.md` down to what remains open once 1–3 land.

Verify: `go build ./... && go test ./...` (short; the long suite stays skipped)

## Commit 4 — the human row

Sketch only; the design doc argues this is separable and should follow, not accompany.

A brokered doer is not a driver name: `Server.runner` is one field set once
(`versed/server.go:44`), `Runner.Spawn(Launch, ContentReader)` takes no runner selector, and
the runner reaches the process entity only via `mirror.Meta.Runner`. Two shapes to weigh
before writing anything:

- a **runner axis on the launch spec** — a `runner:` field a fragment can set, resolved
  against a registry of runners the way `grant:` resolves against `namedGrants`. Symmetric
  with the other axes; costs the Server a runner registry where it has one runner;
- a **driver that resolves to a brokered handle** — `driver: broker` returning a handle from
  the broker rather than the local runtime. Cheaper, and it keeps `Runner` a property of the
  verse; pays for that by making "which substrate" a driver name, which is the conflation this
  whole design is undoing one axis over.

The first is more likely right for the same reason the rest of this is: the substrate a doer
runs on is a property of the doer, and a registry of doers should be able to state it. Decide
with a written comparison, not in the commit.

## Order and dependencies

```
  1 fragment ──▶ 2 ceiling ──▶ 3 visible ──▶ (docs fold)
                     │
                     └──▶ 4 human row (independent of 3)
```

1 and 2 land together or not at all. 3 can follow at leisure but before the tutorial claims
the feature. 4 needs 2's ceiling to exist (a human doer has an envelope too) and nothing from
3.

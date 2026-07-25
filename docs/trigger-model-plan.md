# The trigger model — implementation plan

The incremental build-out of `docs/trigger-model.md`, which is design, not code. It replaces
`Trigger.Matches []entity.Pattern` with a single `Condition{When Formula, Subject Match}`,
folds `action.setRef` and `spawn.subject` into `Subject`, and puts flows, goals, and gates on
**two** grammars instead of five mechanisms:

- **`Match`** — point-in-time selector over the entity-verse. The atom, and the leaf of every
  `Formula`. Today map-backed over the existing typed matcher; later a real tony match.
- **`Formula`** — temporal composition of matches (`quorum`/`fact`/`until`/`deadline`/…).
  `When`, `Gate.Formula`, and `Goal.doneWhen` are all Formulas.

`Subject` and flow-addressing are bare Matches (naming entities at an instant); `When`, gates,
and `doneWhen` are Formulas (relating the verse over time). The payoff is that broadcast
fan-out and relational flows stop being new features and become those two layers at a wider
setting.

The work is a refactor of `trigger/` plus mechanical updates in `cmd/verse/`, `versed/`, and
the docs. Nothing outside `trigger/` reads the match grammar except `cmd/verse/connect.go:240`.

**Decisions taken up front:** a clean break on the old wire form (no `matches`/`setRef`
alias — the docs and tests move with it); steps 1–4 are executable here and step 5 is a
sketch; a `when` carrying temporal operators is *parsed but rejected at install* until the
step that gives it a subject to be about.

## Invariants this must not break

Each has a named test or tutorial section that must stay green:

| Fundamental | Where | Must survive |
|---|---|---|
| **`up` / `down`** | `cmd/verse/up.go`, `down.go` | untouched; `status` still reports `triggers`/`flows` counts (`versed/server.go:654`) |
| **The reactive grain is the entity** | `docs/change-events.md` | atom `When` still dispatches **per change event**, so `$subject` is structurally one entity. One commit touching `…:a` and `…:b` still yields two firings. |
| **Emergent chain + completion pattern** | tutorial §3, `trigger/engine_test.go:70` | `review` → process → `reviewed` advances `demo:pr:1` to `reviewed` |
| **Ignition guard** | `engine_test.go:103` | `process:*` changes need explicit opt-in |
| **Budget backstop** | `engine_test.go:22,137` | denials don't spend budget; runaway caps |
| **`decisions` / `vote`** | tutorial §8, `monitor_test.go`, `provider_test.go` | whether-vote (quorum, deny-quorum, deadline) and who-select (inline + `provider:*` registry) unchanged |
| **`processes`** | tutorial §5/§7, `process/`, `broker/` | `run/list/get/say/cancel/attach/do/inbox` untouched; `launch.Spec.Subject` stays the runtime transport |
| **`connect`** | tutorial §10, `cmd/verse/connect.go` | scoped per-file validator still fires, `$path` still binds, still uses a **subtree** watch (no root watch) |
| **`flow` / `goals` steering** | tutorial §6/§9, `flow_test.go`, `goal_test.go` | pause/resume/cancel/say by subject; goal latches + `waitingOn` |

## Recommended order — **0** → 1 → 2 → **4** → **3**

**Step 0 (prerequisite) — DONE.** Two parts, both landed:

1. **Mounts moved to their own root.** `entity.MountRoot` (`versemnt`) holds controller-mounted
   slices, disjoint from the entity root, so no watch is ever an ancestor of a mount. `Since`
   keeps working with a `verse connect` content controller live, and the mount no longer stalls
   on the root watch.
2. **The stale single-watch-per-path workarounds are gone.** The store's second `-replay`
   session and the `replayMu` that serialized replays against it are deleted (one session now
   multiplexes reads, writes, the root watch, subtree watches, and replays), and `goal.go`'s
   rationale comment is corrected to say the single watch is a simplification, not a constraint.

Details in *Substrate check* below.

**Steps 4 before 3**, deviating from the doc: **fan-out needs an id-keyed live-process registry
first.**
`processd.Daemon.spawnHosted` does `d.proc[id] = h` (`process/processd/daemon.go:133`), which
silently clobbers on a duplicate id while the old process keeps running. Step 3 requires
"deterministic `spawnID` per subject entity (an idempotent re-fire per member)", so the dedupe
has to exist — and step 4's flat registry is exactly where it belongs. Doing 4 first makes 3's
dedupe a one-line check instead of a throwaway guard.

---

## Step 1 — Name the placeholder: `Match`

**New file `trigger/match.go`.** No behaviour change.

```go
// Match is a POINT-IN-TIME selector over the entity-verse — the atom, and the leaf of
// every Formula. Map-backed today (system/kind/id/op/fields, interpreted by the existing
// typed matcher, entity.Pattern); a real tony match later. THE single swap point.
type Match map[string]any
```

Methods (all delegating to the existing `entity.Pattern` matcher in `entity/pattern.go` — do
not reimplement matching):

- `pattern() (entity.Pattern, error)` — reads `system`/`kind`/`op`/`fields`; accepts `entity`
  as an alias for `fields` (the doc spells the Subject predicate `entity:`). Unknown keys are
  an error, so a typo'd charter fails at install, not silently at runtime.
- `MatchEvent(ev entity.Event) bool` / `Select(store entity.Store) []entity.Ref` — delegate to
  `Pattern.MatchEvent` / `Pattern.Select`, ANDing the `id` dimension (see below).
- `System() string`, `Kind() string`, `Fields() map[string]any` — typed reads, so the ~14
  `r.Facts.System`-style sites stay one-liners.
- `Ref() (string, bool)` — the degenerate single-entity form.
- `UnmarshalJSON` / `MarshalJSON` — **string form is sugar**: `"$flow"` or `"demo:pr:1"`
  parses to `Match{"ref": "$flow"}` and marshals back to the string, so `trigger show` reads
  the way it was authored. An object parses as the map.
- `id` dimension owned by `Match`, not `entity.Pattern` — `*` is a wildcard. Keeps
  `entity.Pattern` stable while giving step 4 `demo:pr:*` addressing.

**Migrate `Requirement.Facts entity.Pattern` → `Match`** so "the leaf of every Formula" is
literally true — and so that step 2's `when` atom has somewhere to live without a second
carrier. ~14 mechanical sites, all inside `trigger/`: `formula.go:126,152-170,183,
260-263,285,325,347,352`, `decision.go:145,155`, `engine.go:574`. Constructors like
`quorumFormula` become `Match{"system": "vote", "kind": did, "fields": {"choice": opt}}`.
The `"facts"` JSON key and its emitted shape are unchanged, so `verse decisions` output and
`goals add --done-when '{"fact":"ci:status"}'` are wire-identical.

**Green gate:** `go test ./...` unchanged; `verse decisions`/`vote` smoke per tutorial §8.

---

## Step 2 — Restructure: `Condition{When, Subject}`

### `trigger/trigger.go`

```go
type Trigger struct {
    Name      string    `json:"name"`
    Condition Condition `json:"condition"`
    Gates     []Gate    `json:"gates,omitempty"`
    Action    Action    `json:"action"`
}

// Condition is the ECA cause: two halves of "when X, about Y", two DIFFERENT grammars.
type Condition struct {
    When    *Formula `json:"when"`              // temporal
    Subject Match    `json:"subject,omitempty"` // point-in-time; omitted = the matched entity
}

type Action struct {                            // SetRef deleted
    Set   map[string]any `json:"set,omitempty"`
    Spawn *launch.Spec   `json:"spawn,omitempty"`
}
```

`launch.Spec.Subject` **stays** — it is the runtime transport of the resolved subject into
`mirror.Reflect` and the flow registry. What is deleted is *charter authors setting it*: the
engine always overwrites it from the resolved `Condition.Subject`.

### `Formula` grows the `when` atom — as a requirement kind, not a second carrier

**`Formula` keeps exactly one atom carrier: `Req *Requirement`, whose `Facts` is the `Match`.**
Do *not* add a parallel `Formula.Match` field. An atom is **a Match plus how it is evaluated**,
and `RequirementKind` is already that "how":

| kind | leaf | evaluated as |
|---|---|---|
| `match` *(new)* | `Facts` | **edge** — `MatchEvent` on the change delta (the `when` face); as a store predicate, presence |
| `fact` | `Facts` | presence — `Select(store)` ≥ 1 |
| `quorum` | `Facts` | tally — `Select(store)` ≥ `N`, on `choice` |
| `deadline` | *(none)* | the clock — a time derivation, already Match-less |

So `trigger/formula.go` adds one constant and two small cases, and `Formula` is untouched:

```go
const ReqMatch RequirementKind = "match" // a point-in-time Match leaf: the `when` edge
```

- `Requirement.eval` — `ReqMatch` → presence (`len(Facts.Select(store)) >= 1`), same as
  `ReqFact`, so a `when` leaf composes inside a future temporal `When`/`doneWhen`.
- `Formula.MatchEvent(ev) bool` — the **edge** face: true iff
  `Op == OpAtom && Req != nil && Req.Facts.MatchEvent(ev)`. A temporal formula returns false,
  which is what routes it to the (rejected, for now) monitor path instead of the edge path.
- `Subtrees()` (`formula.go:314`) — **no change needed.** It already walks atoms and reads
  `Req.Facts.System/Kind` for any non-`deadline` kind, so a `match` leaf is picked up for free.
  This is the payoff of one carrier: the subtree-watch generalisation below costs nothing.
- `Requirement.Label()` — add a `ReqMatch` case (`match:demo:pr`) for goals' `waitingOn`.
- `compactValue()` — `ReqMatch` returns the bare `Facts` map, and `Formula.MarshalJSON`
  special-cases it to emit that map *unwrapped*, so `trigger show` renders
  `"when": {"system":"demo","fields":{"stage":"open"}}` as authored rather than
  `{"match":{…}}`. The wrapped `{"match": {…}}` form is still accepted on input.

*(Why this and not the parallel field: two carriers would make "an atom" two things and leave
`Req.Facts → Match` a retype that bought nothing. One carrier keeps the doc's claim — a
formula's leaves are Matches — literally true, and `deadline` was already the Match-less leaf
that proves "atom = Match + eval mode" is the honest model.)*

### Parsing: a *validated* disjoint-key rule (fixes a live misparse)

A `when` value can be a match object (`{system,…}`), a temporal op (`{until:[…]}`), a
requirement tag (`{quorum:2}`), or the verbose `{op,req,args}` node. Today
`Formula.UnmarshalJSON` (`formula.go:51`) disambiguates by branching on `raw["op"]` — and
**`op` is also a Match key.** So `{"system":"local","kind":"dir-status-x","op":"set"}` —
precisely `cmd/verse/connect.go:242`'s trigger — takes the verbose branch and silently decodes
to `Formula{Op:"set"}`, matching nothing. This must be fixed as part of step 2, not left
implicit.

Replace the branch with an **ordered classification**, and make the disjointness an asserted
invariant rather than an assumption:

```
matchKeys = {system, kind, id, op, fields, entity, ref}
tagKeys   = FormulaOps {until, eventually, always, sequence}
          ∪ RequirementKinds {match, quorum, fact, deadline, judgment, ask, default}
```

1. `{}` → a `match` atom matching everything (today's `[{}]`).
2. Any key in `matchKeys \ {op}` present → a `match` atom. Every other key must also be in
   `matchKeys`, else **error naming the offending keys** — a mixed object like
   `{"system":"demo","quorum":2}` is rejected, never silently resolved one way.
3. Else keys ⊆ `{op, req, args}` **and** `op` is a known `FormulaOp` → the verbose node
   (`formula_test.go:27` covers this; keep it green).
4. Else exactly one key, in `tagKeys` → that tag. An unknown single key errors instead of
   falling through to a requirement (today's `default:` branch accepts any tag name).
5. Else → error: *want a single tag, a match object, or {op,req,args}*.

`matchKeys ∩ tagKeys == {op}`, and rule 2-before-3 is what resolves it. Add a **unit test that
computes both sets from the declared constants and asserts the intersection is exactly
`{"op"}`** — so adding a `RequirementKind` named `fields` (or a match key named `quorum`)
fails loudly at test time instead of quietly reclassifying charters. `Match.pattern()`'s
unknown-key error is the second line of defence, not the first.

### Engine wiring — one edit each, all reusing what exists

| Today | After |
|---|---|
| `Trigger.matchesEvent` (`trigger.go:32`) | `t.Condition.When.MatchEvent(ev)` |
| `Trigger.opensTo` (`trigger.go:43`) | walk `When.Subtrees()` for `st[0] == mirror.System` |
| `watchTriggerSubtree` reads `t.Matches[0]` (`engine.go:143-155`) | **open a watch per subtree in `When.Subtrees()`** — generalises for free and drops the "exactly one match" restriction. Safe on docd only since go-tony **v0.0.85** (multi-watch per path); see the substrate check below. |
| `Action.apply(name, ev, e)` (`trigger.go:91`) | `apply(name, ev, subjects []entity.Ref, e)` |

### `AddTrigger` validation (`trigger/trigger_api.go:16`)

- `When` required. An **omitted** `when` is an install error (`"trigger %q: condition.when
  required (use {} to match everything)"`) — today's silently-everything-matching nil is a
  footgun, and "the when is the complexity sink". An explicit `when: {}` still matches
  everything, exactly as today's `[{}]`.
- `When` carrying a temporal operator → error: *"temporal `when` is not evaluated yet — it
  needs a Subject to name what it is about (see docs/trigger-model.md step 3)."* Parsed,
  round-trips, rejected at install.
- `Subject` must parse as a Match; if it is a `system:kind` selector it must name both (it
  has to name a definite kind to select and spawn for).

### Subject resolution (new `resolveSubject` in `trigger/trigger.go`)

Called in `fire`, after gates proceed, before `apply`:

1. `Subject` empty → `[]entity.Ref{ev.Ref}` (the matched entity).
2. `Subject` is the ref form → `entity.ParseRef(launch.Expand(ref, binding))`. Covers both
   `"$flow"` and a literal `"change:pr:merged"`. Unresolvable (e.g. no `flow` field on this
   event) → trace and skip the firing, matching today's `SetRef` behaviour (`trigger.go:97`).
3. `Subject` is a selector → `Match.Select(store)`. **Step 3.** In step 2, error at install.

Then `apply` uses the resolved subject for *both* the `Set` target and `spawn.Subject`, and
**`$subject` binds to the resolved subject, not the matched ref.** That is the whole point of
the `reviewed` rewrite — `setRef` only ever existed to compensate for `$subject` being wired
to the matched entity. Payload-field bindings (`$flow`, `$stage`, `$path`) still come from the
matched event's payload, so `connect`'s `$path` is untouched.

Set `$subject` **last** in `bindingFor` (`trigger_api.go:111`) so the reserved binding can no
longer be shadowed by a payload field named `subject`.

### One consequence worth fixing here: goal latch payloads

`goal.go:274-287` writes `subject` onto `goal:done`/`goal:failed`. Tutorial §9's trigger uses
`"setRef": "$subject"` and works today only because that payload field shadows the reserved
`$subject`. With `$subject` now authoritative, that idiom is circular. **Add `flow: <subject>`
to the goal latch payloads** (keep `subject` for display), matching `mirror.Reflect`'s existing
`flow` convention (`mirror/mirror.go:52`). §9's trigger then reads `"subject": "$flow"` — the
identical idiom as the completion pattern. One convention, not two.

### Call sites outside `trigger/`

- `cmd/verse/connect.go:240` — `Condition: trigger.Condition{When: matchAtom(trigger.Match{
  "system": source.System, "kind": statusKind, "op": "set"})}`. Keep the `op: set` dimension.
- `versed/server.go:965` `addTrigger` — decoding is `json.Decode` into `trigger.Trigger`; only
  add a `condition.when required` 400 alongside the existing name check.
- Tests: `trigger/{control,engine,monitor,goal,replay,logd_e2e,spawn}_test.go`,
  `versed/status_test.go` — mechanical `Matches: […]` → `Condition: …`, `SetRef: x` →
  `Subject: Match(x)`.

**Green gate:** `go test ./...`; tutorial §3–§4 by hand with the rewritten charter (watch the
double stage transition); tutorial §10 `connect .` fires a validator per file.

**New tests this step owes:** the disjoint-key assertion above; a `when` JSON round-trip
(`{"system":"demo","fields":{"stage":"open"}}` → `Formula` → the same JSON); the mixed-object
rejection; and a **regression test for the `op` misparse** — a `when` carrying `op: "set"`
must produce a `match` atom that `MatchEvent`s a `Set` event, not a `Formula{Op:"set"}`.

---

## Step 4 (do third) — Flow as a query, not a stored index

Replace the maintained `flows map[string][]flowProc` index (`engine.go:61,739-893`) with a
flat live registry plus a query. Strictly smaller code and strictly more expressive.

```go
type flowProc struct {
    id      string
    subject string   // the resolved subject this process is about
    h       process.ProcHandle
}

liveMu sync.Mutex
live   map[string]flowProc   // by process id — reap is a delete, dedupe is a lookup
```

- `Spawn` (`engine.go:732`) — **dedupe:** if `live[id]` exists, trace and return the existing
  handle instead of spawning. This is the guard step 3 needs.
- `Adopt` (`engine.go:754`) — same registration path.
- `reap(ev)` (`engine.go:867`) and `reapOnDone` (`engine.go:773`) — `delete(e.live, id)`. Both
  the O(subjects×procs) rebuild loop and the "never filter in place" hazard
  (`flow_test.go:101`) disappear; keep that test as a race guard over the new registry.
- `Flows()` — distinct subjects over `live`, sorted.
- `matchFlow(m Match) []flowProc` — parse each live subject with `entity.ParseRef` and
  `m.MatchEvent`-style match it. `PauseFlow`/`ResumeFlow`/`CancelFlow`/`SteerFlow` take a
  `Match` and act on the matched set, returning the count (the `{"paused": 1}` shape the
  tutorial documents).
- `goalSubject` (`goal.go:337`) → a literal-ref Match, so goal steering is unchanged.

**CLI + wire.** `cmd/verse/flow.go` and `versed/server.go:1114-1177`: the `subject` field
becomes a match expression, parsed by a new `Match.ParseExpr(s string)`:

```
demo:pr:1   → {system: demo, kind: pr, id: 1}     one subject (today's behaviour)
demo:pr:*   → {system: demo, kind: pr}            the whole kind — "relational flows"
demo:*:*    → {system: demo}
```

Keep the JSON body key `subject` (a ref is still a valid expression) so no client breaks; the
CLI synopsis becomes `pause <subject|match>`. `demo:pr:1/**` (subtree addressing) waits on
step 5's reference grammar.

**Green gate:** `flow_test.go` + `goal_test.go`; tutorial §6 by hand — `flow list`, then
`flow pause demo:pr:2` **and** `flow pause 'demo:pr:*'`.

---

## Step 3 (do fourth) — Set-valued `Subject` + fan-out

Enable `resolveSubject` case 3 and lift the step-2 install rejection.

- `Match.Select(store)` returns the member set; sort by ref string for deterministic order.
- **Gates resolve once per firing, then the action fans out.** A broadcast should raise one
  approval, not N — and a temporal gate's decision channel is already keyed on the matched
  `ev.Ref` (`engine.go:404`), so it stays one channel per firing. `gate list` stays legible.
- `apply` loops the members: one `Set` and/or one `Spawn` each, `$subject` bound per member.
- **`spawnID` keys off the subject ref, not the matched ref** (`trigger_api.go:84`, called at
  `trigger.go:128`) — `spawnID(t.Name, subjectRef)`. Deterministic per subject entity, so a
  re-fire is idempotent per member via step 4's `live[id]` dedupe. `spawn_test.go:130`
  (distinct refs → distinct ids) still holds.
- **Budget meters entities.** `reserve()` (`engine.go:321`) → `reserveN(n int)`, called once
  with `len(subjects)`, partially satisfiable (fan out as far as the budget allows and trace
  the shortfall). Keeps `engine_test.go:22,137` green while honouring "the runaway backstop
  meters entities, not firings."
- Also lift the temporal-`When` rejection **only** when `Subject` is present and set-valued —
  a temporal `When` then gets a per-trigger `Monitor` over `When.Subtrees()` and fires on the
  transition into satisfaction. This is optional within step 3; if it slips, keep the install
  error and land it separately. Atom `When` must keep using the per-event edge path either way
  — that is what preserves the reactive grain.

**New tests:** broadcast (`when: {system: ci, kind: status}` + `subject: {system: demo, kind:
pr, fields: {stage: reviewed}}` → one process per matching PR); re-fire idempotence (same
change twice → one process per member); budget metering by entity count.

---

## Step 5 — Grow the leaf (sketch)

`Match`'s map backing becomes a real tony match, using `tony.Match` over an `ir.Node` — the
pattern `providerMatches` (`engine.go:720`) already established for provider attributes. The
richer leaf (globs, references, `!and`/`!or`, nested payload matches) then flows into `When`,
gates, `Subject`, and flow addressing at once, because they all bottom out in `Match`. The
temporal `Formula` layer above is unchanged. Settle the `fields:` vs `entity:` spelling here.

---

## Substrate check: what docd/logd watches actually support (go-tony v0.0.87)

This plan multiplies watches — one per subtree in `When.Subtrees()` per trigger, plus each
`Monitor` and decision channel — so what docd guarantees per *session* is load-bearing. Checked
against the pinned `go-tony v0.0.87` (which is the tip of `tony-format` `main`).

### Multi-watch per session — supported since v0.0.85

`2d459d6 logd/docd/libctl: support multiple watches per path, keyed by request id`
(2026-07-23, first in **v0.0.85**) is the commit that unblocks this plan. Before it a session
could hold **one watch per path**, and a second silently replaced the first.

- docd keys each watch by `watchKeyFor(id, path)` — `"id:"+*req.ID` when the request carries
  one, else `"path:"+path` (`docd/server/watch.go:24`).
- **Admission rule:** a path is *either* one id-less watch *or* N distinct id-bearing watches
  — never mixed.
- `libctl.LogdSession.Watch` **always** stamps a fresh id (`libctl/watch.go:118-135`), so verse
  is permanently in the N-id regime. Two triggers matching the same `(system,kind)`, or a
  `Monitor` and a decision channel over the same vote log, are legal and independent.

**Three verse workarounds for the old limit were therefore obsolete — now cleared:**

| Workaround | Where | Done |
|---|---|---|
| A **second logd session** (`replay`) existing only so `Since` could watch the root path the live watch already held | `entity/logdstore.go` | deleted; `sess` serves everything. Visible in the logs: the store opens **one** logd session where it used to open two. |
| `replayMu` serializing every `Since` call — "one watch per path per session" | `entity/logdstore.go` | deleted; replays are independent id-keyed watches. Guarded by `TestLogdStore_ConcurrentSince` (8 concurrent replays on one path, direct-logd *and* through-docd, under `-race`). |
| `runGoal` driving status *and* terminal "from ONE watch … so a docd session never opens two watches on the same path" | `trigger/goal.go` | rationale corrected: the single watch is a *simplification* (status and terminal come from one evaluation), not a constraint. |
| `seen` map deduping subtree watches across decision options | `trigger/decision.go:232` | kept — now an efficiency, not a correctness requirement. |

### Watch behaviour by mount position

`MountsUnder(p)` is **strictly** below `p` (excludes `p` itself and siblings —
`docd/server/compose_read_test.go:64`), and `coordOverlap` is field-prefix in either direction:

| Mount relative to watch path | Behaviour |
|---|---|
| **Descendant** (watch is a strict ancestor of ≥1 mount) | **Composed watch**: sub-watches on the base owner + each mount, multiplexed into one client watch, deltas re-stamped to the watch path. Event-preserving only while membership is stable. |
| **Ancestor / exact** (watch at or below a mount) | Single-route to the owning controller; `ErrCodeUnavailable` if the mount is tombstoned (controller crashed). |
| **Sibling** (disjoint subtree) | Unaffected — no overlap, no coordination. |
| `.meta` | Cannot be watched (`ErrCodeUnsupported`). |

Mount/unmount are **writers**, watches are **readers**, serialized with **writer priority**: a
pending mount blocks *new* overlapping watches, waits `MountForceAfter` for active ones to
drain, then force-ends the stragglers with `EndReason: "membership_changed"` — carrying the
highest commit delivered, so the client re-watches and resumes. `verse connect`/`disconnect`
are exactly such writers.

### The one live landmine: `Since` over a composed root watch

`LogdStore` watches the root path `verse` (`DefaultRoot`), and `verse connect` mounts its
content controller at `verse.local.dir-content-<scope>` (`entity.MountKPath`, via
`cmd/verse/connect.go:108`) — **strictly below the root**. So once any connect scope is
mounted, the baseline store's root watch becomes a *composed* watch. And a composed watch:

- **drops `FromCommit`** — `startComposedWatch` builds each sub-watch as
  `{Path: p, NoInit: true}` with no `FromCommit` (`docd/server/watch.go:126-128`), and says so:
  *"FromCommit is therefore not honored here"* (`watch.go:106`). docd names it an open gap:
  *"best-effort for composed watches … **until** composed watches honor FromCommit on
  re-establish"* (`client_session.go:64-67`).
- **never emits `ReplayComplete`** — that event is constructed only by logd
  (`logd/api/session.go:471`); no docd path references it.

`LogdStore.Since` (`entity/logdstore.go:798`) asks for exactly that: a `FromCommit` + `NoInit`
watch on the root, looping until `ReplayComplete`. Composed, neither arrives, so it runs to its
15s `context.WithTimeout` and returns `(partial, true)` — a *successful-looking* but incomplete
catch-up. `Engine.catchUp` runs on the dispatch-loop goroutine, so the loop would also stall for
that timeout on every event while a scope is mounted.

This is **pre-existing, not caused by this plan** — but the plan leans harder on `Since`
(step 3's fan-out re-fires, step 4's reap) and the trigger-model doc cites `Since` as what makes
"a hold cost a cursor, not a buffer", so it should not stay unrecorded.

**Confirmed by test** — `entity/composedwatch_test.go` (coverage `versewatch_test.go` lacks: it
has scoped-sibling, overlapping, crosstalk and same-path multi-watch, but no *ancestor watch +
FromCommit with a mount below*). Both tests **fail today**, which is the point — they are step
0's gate. Measured against go-tony v0.0.87:

| Test | Result |
|---|---|
| `TestLogdStore_SinceAcrossContentMount` — control phase, no mount | `Since(0)` replays 1 event ✓ (harness sound) |
| …same, with the content controller mounted below `verse` | **`Since(1) → ok=true, events=0, elapsed=15.002s`** |
| `TestDocdComposedWatchHonorsFromCommit` — docd directly, no `LogdStore` | **no `ReplayComplete`, 0 patch events in 10s** |
| `TestLogdStore_SinceWithMountOutsideRoot` — the fix | passes: 1 event in **2.4ms**, mount live in **11ms** |

The second row is the whole fault in one line: **`ok=true` with zero events after a 15-second
stall** — a silent, successful-looking under-delivery that `Engine.catchUp` would take at face
value, on the dispatch-loop goroutine. The third row pins the blame *below* verse: it is docd's
composed-watch path, not `Since`'s loop.

**Third finding, from the same run: the mount itself blocks ~5s.** `mount became live in
5.0096s` when the store held an active root watch, versus **8ms** in the test with no store.
Mount/unmount are coordinator *writers*; the root watch is an overlapping *reader*, so docd
waits its full `MountForceAfter` window (default 5s) before force-ending it. **`verse connect`
pays this on every connect**, blocking in `waitContentReady` (`cmd/verse/connect.go:119`). Same
root cause, same fix.

### Why this is a modelling error, not a docd bug

**A mount is process-driven, so it has no history.** A controller is a live process backing its
subtree from something that is not the log (a filesystem, an API); across a mount/unmount cycle
there is no durable commit sequence to replay, and for a *self-backed* controller there is no
"past" to ask for at all — which is exactly why `libctl.Handler` warns that such a controller
*"has NO way to honor \[a historical read\] correctly … and should reject a historical read
rather than answer from an uncoordinated timeline"* (`libctl/controller.go:42-49`).

So *"make composed watches honor `FromCommit` upstream"* is not a fix — it is unachievable in
general (at best partial, for logd-backed mounts only). The error is on **verse's** side: it asked
a process for its past. The correct model is a client-side split verse already controls:

> **History belongs to the logd-backed base; mounts get live watches only.** Never open a
> history-bearing watch (`FromCommit`) on a path that spans a mount.

And verse controls the layout that makes this checkable, because verse owns docd exclusively:
`entity.MountKPath` is the **only** mount-path producer (one call site,
`cmd/verse/connect.go:108`), and it is `<root>.<system>.<kind>` — depth 3 by construction.

### The fix: give mounts their own root — **confirmed by test**

Mount controllers on a **sibling top-level segment** rather than under the store root. `"verse"`
is not a field-prefix of `"versemnt"`, so the two never overlap: the root watch is not an ancestor
of any mount, stays single-route, and does not collide with the mount as a coordinator writer.
`TestLogdStore_SinceWithMountOutsideRoot` measures it:

| | mount **under** root | mount **outside** root |
|---|---|---|
| mount became live | 5.0096s | **11ms** |
| `Since` after the mount | `ok=true`, **0 events**, 15.002s | `ok=true`, **1 event**, **2.4ms** |
| reads of the mount | work | work |

Both faults gone, and it is the *smallest* change of any option: **one root watch, no subtree
enumeration, `Since` and `Observe` untouched.** The verse-level namespace is unchanged — a ref
stays `local:dir-content-<scope>:<path>`; only `refPath`'s *rendering* differs for mount-owned
`(system,kind)` slices, and verse knows that set because verse is what mounted them.

**Implemented** (all in `entity/logdstore.go`, ~50 lines including the rationale comment):

- `MountRoot = DefaultRoot + "mnt"`, carrying the *why* (a process has no history).
- `mountedKinds` + `IsMountOwned(system, kind)` — the mount-owned set. Named here rather than
  imported from `source` (which imports `entity`), and held in step by
  `source.TestFederationKindsAreMountOwned`.
- `refPath` → `rootFor(ref)`: `MountRoot` for a mount-owned slice, the entity root otherwise.
  The single place the two roots are chosen between.
- `MountKPath` renders under `MountRoot`, and **panics** if handed a slice that is not
  `IsMountOwned` — otherwise the store would render its refs under the other root and reads
  would miss the mount, silently.
- `source.Content` needed no change: it is constructed with its own mount kpath and does
  `CutPrefix(path, c.mount+".")`. `cmd/verse/connect.go` needed none either — it goes through
  `MountKPath`.

Verified end-to-end on a live docd-backed `verse up` + `verse connect`, not just in unit tests:
content served on demand through the mount (`local:dir-content-<scope>:README.md` →
`{"content":"hello from verse\n","size":17}`), a validator spawned and completed per file, and —
the regression that matters — **the baseline emergent chain `open → reviewing → reviewed` ran in
under 2s with the mount live** (`fired: 2`), which is exactly what the old layout broke silently.

Alternatives, both strictly more work:

- **(a) Replay per matched `(system,kind)` subtree.** Also correct — depth 3 is never an ancestor
  of a mount — and it is where per-trigger observation heads anyway. But it turns one watch into
  N, needs the subtree set enumerated, and needs `Since` to merge N replays by `Rev`. Worth doing
  on its own merits for the engine; not needed to fix this.
- **(b) Hold a root watch only while no mount sits below it.** Keeps the root design but makes
  observation silently mode-dependent on mount state — the fragile option.

Both tests run ~60s wall-clock (three 15s `Since` attempts plus two mount waits). If that is too
slow for the default `make test`, guard them behind a build tag or `testing.Short()` rather than
deleting the evidence.

## Docs to rewrite (clean break, so these are not optional)

- **`docs/tutorial.md`** — §3 charter (both rules), §4 anatomy (`matches`→`condition.when`,
  drop `setRef`, add `condition.subject` and the bindings note), §8 the `merge` and `review`
  charters (lines 420, 495), §9 the `on-ship` trigger (line 603 — now `"subject": "$flow"`),
  §12 command reference (`flow … <subject|match>`), §13 add a trigger-model bullet.
- **`docs/overview.md`** lines 35, 40.
- **`docs/trigger-model.md`** — flip the status banner from *design* to implemented per step;
  keep the migration section as the record.

## Verification

Per step, in order:

1. `make test` (`go test ./...`) and `go test -race ./trigger/... ./process/...` — the flow
   registry change is concurrency-sensitive.
2. `go vet ./...`.
3. **Tutorial smoke, docd-backed** (the default path, so scopes and CAS are real):
   `verse up` → §3 two-rule charter → `verse entity put demo:pr:1 '{"stage":"open"}'` →
   `gate list` / `gate resolve … --approve` → `verse entity get demo:pr:1` is
   `{"stage":"reviewed"}`, i.e. the emergent second transition. Then `verse status` (charter
   count), `verse down`.
4. **Decisions:** §8 whether-vote (`decisions`, `vote … --as alice/bob`, `deny`-quorum,
   `deadline`), then who-select both inline and via the `provider:*` registry — confirm
   `cheapo` is off the ballot.
5. **Processes:** §5 `process run --wait`, `--attach` with an inline gate, `say`, `cancel`;
   §7 `up --broker` + `process do note|gate|complete --as human:scott`.
6. **Flow (steps 3–4):** §6 pause/resume/say/cancel by ref, then by `demo:pr:*`.
7. **Goals:** §9 `goals add ship --done-when … --deadline 4h`, `goals list` shows
   `waitingOn`, make the fact true, `entity get goal:done:ship`, and the `on-ship` trigger
   advances the subject via `$flow`.
8. **Connect:** §10 `verse connect . --driver llm --task '…$path…'` — a validator per file,
   `entity list --system local --scope <scope>`, `entity get local:dir-content-<scope>:README.md`,
   Ctrl-C tears the scope down. This is the one path exercising per-trigger subtree watches,
   so it is the real test of `watchTriggerSubtree` moving to `When.Subtrees()`.
9. `--store memory` run of steps 3–4 as well, since the memory store has no
   `SubtreeWatcher`/`ConditionalCommitter` and takes the fallback paths.

## Risks

- **`$subject` semantics change** (matched ref → resolved subject). They coincide whenever
  `Subject` is omitted, so tutorial §3's `"review $subject"` is unaffected; the goal-latch
  `flow` field above closes the one place they diverged silently.
- **`Requirement.Facts` retype** touches the decision/vote machinery, and is now load-bearing
  (the `when` atom rides on it). Mitigated by keeping the wire shape identical and by
  `provider_test.go`/`monitor_test.go` covering both decision axes.
- **`ReqMatch` evaluates two ways** — edge via `MatchEvent`, presence via `Select`. That is
  deliberate ("atom = Match + eval mode"), but the *dispatch path* picks which, so a leaf's
  meaning depends on where it sits. Keep it honest in the type doc, and note that only the
  edge face is reachable in step 2 (a temporal `when` is rejected at install).
- **Temporal `When`** is deliberately deferred. If a charter needs it before step 3, the
  install error names the reason rather than misfiring.
- **Formula key classification** is now the single point where a charter's `when` is
  interpreted. The disjointness test is what keeps it safe as `RequirementKind` grows; without
  it, the `op` overlap that misparses `connect`'s trigger today could reappear under a new name.
- **Fan-out + gates** — one gate per firing is a judgement call, not something the doc states.
  If per-member approval is ever wanted, it is a gate-scope flag, not a re-architecture.

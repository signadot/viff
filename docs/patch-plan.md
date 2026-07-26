# Plan: `patch` replaces `set` — the write half of the tony vocabulary

*Status: design, not implemented. It is a prerequisite for the reserved progress root in
`docs/seq-plan.md`, and the write-side counterpart of the predicate unification in the
`payload`/`fields` commit.*

## Context

verse now **matches** with tony: a payload predicate is a tony match evaluated at the payload
root, with operators inside it (`fields: {stage: !or [open, merged]}`). It still **writes** with
a hand-rolled shallow overlay:

```go
// trigger/trigger.go — Action.apply
p := clonePayload(cur)
for k, v := range a.Set { p[k] = v }
```

`docs/trigger-model.md` cites the argument that *the same operators serve matching, patching and
diffing* — and verse adopted only the matching half. `tony.Patch(doc, patch, …)` is right there,
with a `mergeop` vocabulary (`delete`, `replace`, `insert`, `rename`, `dive`, `keyed_list`, `if`,
`and`/`or`/`not`, `glob`) several of whose operators are shared with the match side.

And since a payload became an `ir.Node`, `tony.Patch(payload, patch)` needs **no conversion** —
the same collapse that made `fields` a match makes `set` a patch. The write side got cheaper at
the moment the read side did; nobody has spent it yet.

## What the overlay cannot do

Measured against go-tony v0.0.92:

| document | patch | `tony.Patch` |
|---|---|---|
| `{stage: open, verse: {hold: {step: 1}}}` | `{verse: {review: {step: 2}}}` | `{stage: open, verse: {hold: {step: 1}, review: {step: 2}}}` — **deep merge, sibling preserved** |
| `{stage: open, n: 1}` | `{stage: reviewing}` | `{n: 1, stage: reviewing}` — the common case, unchanged |
| `{stage: open, tmp: 1}` | `{tmp: !delete null}` | `{stage: open}` — **deletion**, which `set` cannot express at all |

Row 1 is the one that matters: today's overlay replaces the whole `verse` key and loses `hold`.
Row 3 is a capability gap — there is currently no way for a trigger to *unset* a field.

## Why this is a prerequisite, not a tidy-up

`seq-plan.md` publishes progress under a reserved root, `verse: {<trigger>: {…}}`. That is
literally row 1: under the shallow overlay, the second trigger to publish wipes the first. The
engine writes progress itself, so it could deep-merge privately — but then any *author* who
writes nested data hits the same clobber, silently, and the reserved root quietly depends on
nobody nesting.

## The shape: one name, `patch`

*(Decided with Scott: `patch`, not `set` kept alongside it.)*

`set` is **renamed**, not kept as a second spelling:

```tony
action:
  patch: { stage: reviewing }                     # a record — the common case
  patch: { verse: { review: { done: true } } }    # nested, merging
  patch: { tmp: !delete null }                    # and things set could never say
```

### Why not keep `set` as the object-shaped spelling

The read side keeps `fields`/`entity` beside `payload`, so the symmetry would suggest keeping
`set` beside `patch`. It does not carry over, and the reason is worth stating because it is what
makes this asymmetry principled rather than sloppy:

`fields` is **load-bearing**. Requiring an object under that name is what keeps
`fields: "stage=open"` an install-time error rather than a predicate that matches almost
nothing. The write side has no equivalent — `patch: 42` is not a mistake to catch, it is a
legitimate whole-value replace. So the object-shaped spelling buys real safety when reading and
only familiarity when writing.

And the name would be actively wrong: this thing deep-merges, deletes, and replaces. "Set"
describes exactly one of those.

### On migration: there isn't one

Nothing persists across a push at this stage — wire and store data are clobbered every time — so
there is no compatibility story to write. The only charters in the tree are `docs/tutorial.md`
(5 uses) and `smoke.sh` (1), all flat, and a top-level object patch over flat keys is identical
to the overlay (row 2 above). So the change is textual: rename the word, update those two.

One small thing is worth keeping, and it is a habit guard rather than a migration: an unknown
charter key is **silently dropped**, so a `set:` typed from muscle memory would parse cleanly and
write nothing — the trigger fires, spawns, and quietly stops advancing state. A three-line reject
in `Action.validate` naming the new spelling costs nothing and can be deleted once nobody types
it. It is the one failure here that is otherwise invisible.

## What does **not** change: `verse entity put`

It is named put, and it replaces — that is how an entity is reset. Making it merge silently
turns every existing invocation into the complement of what it says, which is the same class of
hazard as the null-idiom reversal (`{x: null}` used to mean "has no x" and now means "has an
x"), and worse in one way: nothing errors, so it is found late or never. Add `verse entity
patch` if the CLI wants it; do not reinterpret `put`.

## The error path is the real work

`Modify`'s callback cannot fail — `func(cur Payload) Payload` — and `apply` ignores the result.
A patch **can** fail. So the write path grows an error, and what it does with it matters more
than the plumbing:

**A failed patch must fail the firing, loudly** — not skip the write and continue. The action
runs `set` *then* `spawn`; skipping a failed write and spawning anyway starts a process about a
state the trigger failed to establish, which is worse than not firing at all.

## Scalar payloads: still a policy decision, and patch does not make it

I expected patch to turn this into a diagnostic. It does not — measured:

| document | patch | result |
|---|---|---|
| `42` | `{stage: open}` | `{stage: open}` — the scalar is **replaced**, no error |
| `{stage: open}` | `42` | `42` — the object is replaced, no error |

So tony's semantics agree with today's behaviour, and adopting patch changes nothing here; it
only makes the rule *inherited* rather than invented. The choice remains verse's:

- **(a) accept replace** — the status quo, now with a stated pedigree;
- **(b) refuse an object patch against a scalar payload** and fail the firing.

I lean (b) *for a write onto a subject*: a flow's subject silently losing its value is the bad
case, and it is exactly what a sequence publishing progress onto a scalar-payload entity would
do (`seq-plan.md`). But it is a policy verse chooses, not something the patch vocabulary
decides — so state it out loud either way.

## Charter grammar: tags have to reach the patch

`Action.Set` is `map[string]any`, decoded as such. A patch carrying operators (`!delete`,
`!replace`) needs tags to survive to the node — the same journey `fields` just took, from
refusing a tag with a diagnostic to being a match. So the action's write field becomes
node-carrying, and codegen/schema follow.

## Implementation

- `trigger/trigger.go` — `Action.Set map[string]any` becomes `Action.Patch` as a node; `apply`
  calls `tony.Patch` inside `Modify`. The `set` reject needs the RAW document (the decoder drops
  unknown keys before `validate` ever sees them), so it lives in the charter codec rather than in
  `validate` — worth confirming early, it is the only structural surprise here.
- `entity/store.go` — an error-returning `Modify` (or a `CommitIf` path), so a patch failure
  reaches the caller.
- `trigger/trigger_gen.go`, `schema_gen.tony` — regenerate.
- `cmd/verse/entity.go` — `verse entity patch`.
- Docs: `entity-values.md` (whose status line still says "not yet implemented" though the
  payload-as-node commit implemented it), `trigger-model.md`'s action grammar, and the tutorial's
  `action` section.

## Tests

1. Deep merge preserves a sibling subtree — the reserved-root case, written as the two-trigger
   scenario it comes from rather than as an abstract patch.
1b. A charter still saying `set:` is rejected at install with the new spelling named — cheap, and
   the one failure mode that is otherwise silent (an unknown key is dropped, so it would write
   nothing while still firing).
2. The record case is byte-identical to today: `set: {stage: reviewing}` on a flat payload.
3. Deletion: `!delete` removes a key that `set` could only ever have overwritten.
4. A failing patch fails the firing **and does not spawn** — the ordering hazard, not just the
   error.
5. Scalar policy, whichever is chosen, pinned with the reason in the test name.
6. Charter round-trip: a patch carrying an operator survives tony and json charter forms.
7. Concurrency: two triggers patching disjoint subtrees of one entity both survive — the
   `Modify` CAS property, now exercised at depth rather than at the top level.

## Open questions

- ~~**Does `set` stay a field?**~~ **Settled: renamed to `patch`,** with `set` an install-time
  error rather than an alias or a silently dropped key.
- **Scalar policy** — (a) or (b) above. This is now the only open decision.
- **`verse entity patch` ergonomics.** `verse entity patch demo:pr:1 '{tmp: !delete null}'`
  parses fine, but an operator on a command line is a new thing for the CLI to teach.
- Hooks and `seq` elements get patch for free, since both are `Action`s — worth a test, not a
  decision.

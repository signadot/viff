# Entity values are `any`, not `map[string]any`

Status: design note / decision. Not yet implemented; the surface below is a sketch,
not a spec — the implementation is a moving target.

## The over-constraint

`entity.Payload` is `map[string]any` today. That bakes in three assumptions an
entity has no business requiring: that its value is an *object*, that its keys are
*strings*, and that it is *field-addressable*. None of that is intrinsic. An entity's
value is just **whatever the encoding can serialize** — it could be `1`, `"1"`,
`[1, 2, 3]`, or, in tony, `{1: [], 17: "twenty-three"}` (non-string keys). Forcing it
through `map[string]any` is a modeling error.

It is a *common* error — so common it often reads as a non-issue — but it is real,
and in this codebase it already bit us: the "numeric key comes back quoted" confusion
was not a tony quirk. tony models the string `"1"` and the number `1` as distinct
values (a bare `1` is not a valid string literal); `gomap.FromTonyIR` then had to
cram a precisely-typed key into `map[string]any`, and the only way to preserve "this
is the string, not the number" through that projection is to stringify it to its
source form. The artifact was the projection's fault — manufactured by insisting on
`map[string]any`.

## The decision

**Model an entity's value as `any`, both at the serialization boundary and within
verse. No `map[string]any` anywhere as the representation of a generic value.**

- **Serialization:** an `any` is encoded via the encoding in use; a value the
  encoding cannot represent simply errors — which is the correct outcome, not
  something to pre-empt by narrowing the type.
- **Within verse:** the generic value type is `any`. `map[string]any` may still
  appear as a *concrete* known object (e.g. building a specific payload), but never
  as the type that stands for "an entity's value."

## What this changes semantically

verse's matching and mutation are **field-centric** (`EventMatch.Fields`,
`Set stage=…`). With `any` values that becomes a *convention that applies when the
value is an object*, not a law:

- **Field match / field set** operate on object-shaped values; a scalar or list
  entity simply does not satisfy a field predicate (and has no field to set).
- **Value-equality match** becomes meaningful for non-object entities (match the
  whole value: a clock entity that is a bare number, a flag that is a bool).
- **Delete** is unchanged (a tombstone is a tombstone regardless of value shape).
- Entities verse *manages* still carry a field-map by **convention** (a `stage`, a
  `status`) — that convention is unchanged; it just stops being encoded in the type.

This is what lets a "clock" entity be `1697040000` instead of `{value: 1697040000}`,
a flag be `true`, a set be `[…]` — without verse pretending everything is a map.

## What it touches (sketch — not a spec)

- **`entity`** — the value type (`Payload map[string]any` → a value `any`; likely a
  rename to `Value`). `Event.To`/`From` carry `any`. Entity *ids* stay strings
  (`fieldName` unquoting for ids is unrelated and stays).
- **Stores** — MemStore holds `any` directly. The logd store projects `any`↔tony at
  its boundary (via `gomap`), which is where any exotic-key lossiness lives and is
  documented; the core never sees tony. (A more radical variant — value = `*ir.Node`,
  deleting the projection entirely — was considered and set aside because it couples
  the core to tony and breaks the storage-agnostic Store interface.)
- **trigger matching** — `EventMatch.Fields` becomes "object-shaped match," plus a
  value-equality path for scalar entities. The "when" grows a small type-switch.
- **actions** (`def.go`) — `Set`/overlay operate on object values; setting a field on
  a non-object value is a no-op or an error (decide during impl).
- **mirror** — already builds object values (status/pending/result); mostly a type
  change, no behavior change.
- **versed / CLI** — the HTTP/JSON boundary carries `any`; a non-object entity
  serializes as its JSON value. `entity put`/`get` accept/emit any JSON value, not
  only objects.

## Notes

- This is **conceptual correctness, not an active bug** — verse's live data is all
  string-keyed objects, so nothing is broken today. It is worth doing to stop the
  type from lying and to allow scalar/list entities, but it is not a fire.
- The exotic-key lossiness (`gomap` stringifying integer-keyed tony objects) is a
  logd-store-boundary concern for *foreign* data verse doesn't operate on — keep it
  documented there, don't let it leak into the core model.
- Keep the surface above loose until implementation; expect the field-match /
  value-match split and the action-on-non-object behavior to shift as we build it.

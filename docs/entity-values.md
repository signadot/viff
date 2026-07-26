# Entity values are a tony node, not a Go map

Status: settled, and settled differently than this note first proposed. `entity.Payload` is
`*ir.Node` (`entity/payload.go`). The note is kept for the argument, which outlived the
resolution.

## The over-constraint

`entity.Payload` was `map[string]any`. That baked in three assumptions an entity has no
business requiring: that its value is an *object*, that its keys are *strings*, and that it
is *field-addressable*. None of that is intrinsic. An entity's value is whatever the encoding
can serialize — `1`, `"1"`, `[1, 2, 3]`, or, in tony, `{1: [], 17: "twenty-three"}` with
non-string keys.

It is a common error, so common it often reads as a non-issue, but it was real and it bit
this codebase: the "numeric key comes back quoted" confusion was not a tony quirk. tony
models the string `"1"` and the number `1` as distinct values — a bare `1` is not a valid
string literal — and
[`gomap`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/gomap)`.FromTonyIR` then
had to cram a precisely-typed key into `map[string]any`. The only way to preserve "this is
the string, not the number" through that projection is to stringify it to its source form.
The artifact was the projection's fault, manufactured by insisting on `map[string]any`.

## What was proposed, and what landed

The proposal was to model an entity's value as `any` — encoding-agnostic within verse, with
the logd store projecting `any`↔tony at its boundary via `gomap`. A more radical variant,
making the value `*ir.Node` and deleting the projection entirely, was considered and set
aside for coupling the core to tony and breaking a storage-agnostic `Store`.

The radical variant is what landed. `Payload` is a tony node, the projection is gone, and
with it the exotic-key lossiness that motivated the note in the first place — there is no
longer a lossy hop for a non-string key to be flattened by.

The coupling that argument feared is real and was accepted: `entity` depends on tony's `ir`.
What bought it was that the same collapse made a payload predicate a `tony.Match` and an
action's `patch` a `tony.Patch`, so matching, patching and diffing all became one vocabulary
instead of three hand-rolled ones. That was worth more than storage-agnosticism verse was not
using.

## What it changed semantically

verse's matching and mutation used to be field-centric. With node-valued payloads that
became a convention that applies when the value is an object, rather than a law:

- A field predicate operates on object-shaped values; a scalar or list entity does not
  satisfy one, and has no field to set.
- A whole-value match is meaningful for non-object entities — a clock entity that is a bare
  number, a flag that is a bool. This is the `payload:` spelling in
  [the trigger model](trigger-model.md#what-a-match-can-say), alongside the object-shaped
  `fields:`.
- Writing a scalar (`patch: 42`) replaces the whole payload, since there is nothing to merge
  into.
- Delete is unchanged: a tombstone is a tombstone regardless of value shape.
- Entities verse *manages* still carry a field-map by convention — a `stage`, a `status`.
  That convention is unchanged; it just stopped being encoded in the type.

Which is what lets a clock entity be `1697040000` rather than `{value: 1697040000}`, a flag
be `true`, and a set be a list — without verse pretending everything is a map.

# A rule's gate takes no `then` or `else`: what the rule does is its action

A rule's gate carries `then` and `else` (faxkagtmh12krs61vxn0): what the question's yes, or its no, starts before the firing goes on. A rule already says what it does about a yes, in `action:`, and about a no, in a gate's `onDenied`. So a gate's `then` is a second way to say the same thing, and the two are not alike. A spawn in the action runs with what a rule's spawn is given, credentials included, while a spawn in a gate's `then` runs as an ask's `then` does, without them.

Remove `then` and `else` from a rule's gate:

- `trigger.Gate` loses `Then` and `Else`; a gate writing either is refused at install as an unknown field.
- The install-time check of them goes (`Engine.SetThenCheck`, versed's `thenCheck`), and with it the `install` flag `vet`, `place` and `addTrigger` take, whose only reader it was.
- A rule's question carries no `then`, so `askerOf` no longer reads a rule from `from`.
- docs/reference/gates.md and docs/asking.md say a rule's gate starts nothing of its own.

A person's ask keeps `then` and `else`, and so does an autonomy row's ask. The settle path they share is unchanged.
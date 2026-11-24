# Why: The charter

The reasons behind [The charter](../reference/charter.md). They are for somebody changing viff; the page itself says what viff does.

## Confinement belongs to the engine

Isolation by convention holds until somebody edits a rule.

The isolation of a package holds after that edit too. The confinement is a property of the
engine, and not of the rules that are installed in the engine at one moment.

## A rule is stored under `!raw`

A write to the store is a tony *patch*. This property makes the store good at everything else.
It also means that the store *executes* the operator tags in a payload, and does not store
them.

A charter is full of operator tags: `value: {branch: !glob "hotfix-*"}`,
`patch: {tmp: !delete null}`.

A rule stored as bare structure does not read back wrong. It cannot be read back at all. The
write leaves an entity whose own patch log does not apply
(`glob patching "null" gave cannot patch with glob operation`).

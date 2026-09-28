# Why: The condition of a rule

The reasons behind [The condition of a rule](../reference/condition.md). They are for somebody changing verse; the page itself says what verse does.

## The subject is required

Nothing else in a rule can answer for the subject.

The action has no target of its own. A `when` need not pin one place: `{path: "demo.*.*"}`
spans everything two segments under `demo`.

"The entity the `when` matched" answers *which entity fired*. The question is *what is this
rule about*. A rule that acts on whichever entity made the `when` hold is not a rule that
anybody wrote down.

The subject is therefore always stated.

## A subject is a selection

A rule is a statement about the world. So the entities that a rule is about cannot depend on
which entity was written last.

There is also no changed entity to inherit from. Verse asks the rule once for each commit, and
not once for each change.

## A name is a set of one

Suppose "every entity this selects" were read literally for a name. Then no rule could create
an entity: to create one, the rule would already need it.

## A `flow` field does not redirect a subject

Suppose the `flow` field resolved the firing onto the address that the field holds. The firing
would then advance the single PR that a completed task was about.

The price is this. Whether a firing acts on one entity or on a whole set would depend on
whether some entity carries a key with that name. No charter says that, and no reader of a
charter could predict it.

## A match has no `op` key

An `op` key would classify a *change*, as `set` or `delete`. Verse asks a match only of
*state*, at a revision. State has no such class.

The refusal explains this, and does not say "unknown key". The author wants a distinction that
the model leaves out on purpose, and "unknown key" would not say so.

## `value` asserts no shape

A separate key that asserted an object would make `value: "stage=open"` an install error.

Such a key would still catch only the wrong *shape*. It would never catch `{stge: open}`. A
misspelled key is the mistake that people make.

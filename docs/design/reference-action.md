# Why: The action of a rule

The reasons behind [The action of a rule](../reference/action.md). They are for somebody changing verse; the page itself says what verse does.

## An install acts on the world

An install evaluates the rules against what is already there, so that an install acts on the
world and not only on what happens next. This is why a derive must write only the fields it owns.

## `at:` and `path:` in a read

The address is `at:` in a read and `path:` in a condition. This looks inconsistent, and is not.
In a read, `path:` is already the field path inside the payload, and two things cannot both be
`path`.

A store is one document. So an address is a path to a node in it, and `path:` continues it into
the payload. `!get-path` and `!list-paths` follow the split of go-tony, where `GetKPath` returns
one node and rejects a wildcard, and `ListKPath` collects.

## Why the tag is the whole spelling

A magic key would turn a payload somebody wrote into a lookup that nobody asked for. So only the
tag makes a path read, and a plain key stays data.

## The answer contract

A charter that patches `.[data.class]` onto its subject depends on the task producing a `class`.
Without a contract, verse checks that dependency at patch time. The work is done then, and the
tokens are spent. What is left is a hook error on the task entity, and a subject stuck at a stage
that no rule matches.

A prompt that asks for a shape does not prevent this. An agent that has just read nine files
narrates, and puts its answer in a fence. A prompt is advice that a model may ignore. `answer`
is the same request, made enforceable.

## One kind of expression

The strings of a spawn and the values of a `patch` and a `derive` are tony expressions, evaluated
by one evaluator against the same names. An author learns one grammar. What a firing reads, and
what a derive's install check allows, are asked of the expressions themselves, so what is decided
and what is evaluated cannot drift apart.

The whole-value form is what keeps a charter's meaning fixed. `.[result]` writes the object a
task returned, as an object. `"saw $[result]"` asks for text, and a document renders as tony on
one line, its fields in name order, in a patch as in a prompt. So the only question text raises
is *which* text, and leaves that do not change matter more than the feature.

## The hooks are named fields

The four hooks are named fields, and not an `on map[status]*Action`. So an unknown status cannot
be written, and nothing needs validating. The cost is that a fifth terminal status is a change of
schema, and a misspelled `onDoen` is dropped like any other unknown field.

`onOrphaned` is inert, and restoring hooks at resume would not change that. Reconcile either
adopts a task or orphans it, never both.

## A sequence, and no `par`

Nested `onDone`s express a chain. But they drift to the right and, more importantly, have no
arity. The engine knows that a next hook exists, and never how many remain. So a sequence written
that way cannot say where it is. `seq` states the depth.

Breadth is what independent rules that match the same condition already do. A task id carries
both directions. `review@demo.pr.1` and `hold@demo.pr.1` are two roots about one subject.
`review.onDone@demo.pr.1` extends one chain. Breadth *inside* a chain would lose that reading.

`seq` and `spawn` exclude each other on one action. Otherwise "does the sequence wait for the
spawn?" would have no answer.

A chain holds at most one `seq`. So "the sequence this task belongs to" is one ancestor, and not a
stack. This keeps the idempotence of a re-fire a predicate, and not a search.

## Ids drop the brackets

Brackets break on a shell. `zsh` rejects `review.seq[2]@demo.pr.1` with `no matches found` before
verse sees it, and people paste ids onto command lines all the time. The join is `@` and not `-`,
because rule names use `-` too, and a `-` join could not be decoded back.

## One reserved name

The progress facts use one reserved name, not a family. Flat keys for each rule, like
`review.step`, would reserve an open set of names out of a payload that is the author's own
document. A single `verse` root reserves one name, and documents it once, as `flow` is one
reserved key on task entities. Nested predicates keep it readable.

The two halves under it are separate because they grow differently. `charter` gains a key for
each rule. `meta` gains a field for each concern of the lifecycle. Flat, they would collide: a
rule *named* `vanishing` would write its progress exactly where the marker of the lifecycle goes.

The name is plain `verse`, and that is not settled. A sigil would say "reserved" at a glance, and
give the word back to authors. `@verse`, `~verse` and `$verse` all parse unquoted, address through
kpath, and round-trip through the yaml renderer. `.verse` does not, and is the one to avoid:
kpath reads `.verse.meta.vanishing` back as `verse.meta.vanishing`, and drops the sigil without a
word instead of refusing it.

## `done` and `ended` are published

A match compares a value against a pattern. It never compares one field of a payload against
another. A join spelled `step == steps` cannot be written. A join spelled `{step: 3}` stops
meaning "finished" without a word, as soon as the sequence gains an element. So the engine latches
`done`, and the join matches that.

`done` and `ended` are two fields, because they are two facts. A join that asks for `done` must
not start to match a run that gave up.

`shape` is a hash and not a stored copy, because these sequences can run for a long time. A
snapshot kept for each live sequence is a floor of retention that the store can never compact
past. To refuse to continue, verse needs to detect the change, not to reconstruct it.

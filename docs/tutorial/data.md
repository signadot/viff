# [Tutorial](README.md) -- Data

An [entity](../glossary.md#entity) is anything the work refers to, such as a pull request, a
build, a person or a CI status. An entity has an [address](../glossary.md#address), and it holds
a document. That is the whole data model. The other three parts of the tutorial are built on it.

On the command line you type the segments of an address as separate words:
`verse-tutorial pr 1`. In a document the same address is one path: `verse-tutorial.pr."1"`.
[Addressing](../reference/entities.md#addressing) has the whole grammar.

This page needs [a verse running](README.md#start). Seed some data to read. It goes under
`verse-tutorial`, where everything the tutorial makes lives:

```sh
./scripts/seed-entities.sh       # a dozen entities, nothing watching them
```

## Read

The address narrows a read by *where* an entity lives. `-if` narrows it by *what the entity
says*, with a [tony match](https://signadot.github.io/tony-format/) against the payload.

```sh
verse data                                                          # the whole verse, one document
verse data verse-tutorial pr '*'                                    # by where it lives
verse data verse-tutorial pr 1                                      # one entity, whole
verse data verse-tutorial pr '*' -if '{stage: open}'                # by what it says
verse data verse-tutorial pr '*' -if '{stage: !or [open, merged]}'  # …with an operator
verse data verse-tutorial pr '*' -trim '{stage}'                    # …and how much of each to print
verse data -paths verse-tutorial pr '*'                             # just the addresses
verse data -paths verse-tutorial pr '*' | xargs -L1 verse data      # …passed straight back
verse data -at 'verse-tutorial.pr.*.stage'                          # a whole address as one word
```

Each segment of an address is its own shell word, so nothing needs escaping. A word is always one
segment: `verse data verse-tutorial.pr.1` names one segment called `verse-tutorial.pr.1`. To give
a whole address as one word, put it after `-at`.

The match operators, such as `!or`, `!glob` and `!not`, are tony's. They mean the same thing in a
read, in a charter and in an answer contract. [Reading](../reference/entities.md#reading) has
the rest.

## Write

A put **merges**: the fields you do not name stay as they are. Verse answers with the entity as
it stands after the write:

```sh
verse data verse-tutorial pr 1 -put '{owner: dee}'        # changes the owner, keeps the stage
verse data verse-tutorial pr 1 labels urgent -put true    # one field, deep
verse data verse-tutorial pr 1 labels urgent -rm          # and gone again
```

A write can carry a condition. The store checks it and makes the write in one step:

```sh
verse data verse-tutorial pr 1 -put '{stage: open}' -create                   # only if nothing is there yet: refused
verse data verse-tutorial pr 4 -put '{owner: dee}' -expect '{stage: merged}'  # only if it still looks like this
```

[Writing](../reference/entities.md#writing) and
[conditional writes](../reference/entities.md#conditional-writes) have the detail.

## Watch

```sh
verse watch -if '{stage: open}' verse-tutorial pr '*'
```

```tony
{at: 'verse-tutorial.pr."1"' op: set rev: 97 to: {labels: {area: auth urgent: true} owner: ana stage: open …}}
{at: 'verse-tutorial.pr."5"' op: set rev: 97 to: {labels: {area: engine} owner: cy stage: open …}}
```

The watch **replays**: what already matches arrives first, then each new change. Each change is
one line, and `to` is the whole entity at that commit. In another terminal, run
`verse data verse-tutorial pr 2 -put '{stage: open}'`, and the line arrives. Then write
`'{stage: reviewing}'`. Nothing arrives, because the filter tests the state, and the new state does
not match. [Watching](../reference/entities.md) has cursors, `-since` and the rest.

## Everything verse keeps is data

Verse keeps its own state in the same store, at addresses of its own, so the same three verbs
read it:

| Address | What is there |
|---|---|
| `verse.ask.<id>` | a [question](questions.md), its answers and what it settled on |
| `verse.task.instance.<id>` | a [task](tasks.md): its status, its prompt and its answer |
| `verse.trigger.rule.<charter>:<name>` | a rule of a [charter](charters.md), as it was installed |

So a rule can react to an answered question or to a finished task in the same way it reacts to a
pull request. Nothing in verse is hidden from `verse data` and `verse watch`. Next,
[questions](questions.md).

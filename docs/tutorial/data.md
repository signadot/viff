# [Tutorial](README.md) -- Data

[Data](../glossary.md#data) is anything the work refers to, such as a pull request, a build,
a person or a CI status. Each piece of data has an [address](../glossary.md#address), and it is a
document. That is the whole data model. The other three parts of the tutorial are built on it.

On the command line you type the segments of an address as separate words:
`viff-tutorial pr 1`. In a document the same address is one path: `viff-tutorial.pr."1"`.
[Addressing](../reference/entities.md#addressing) has the whole grammar.

This page needs [a viff instance running](README.md#start). Seed some data to read. It goes under
`viff-tutorial`, where everything the tutorial makes lives:

```sh
./scripts/seed-entities.sh       # a dozen documents, nothing watching them
```

## Read

The address narrows a read by *where* the data lives. `-if` narrows it by *what the data
says*, with a [tony match](https://signadot.github.io/tony-format/) against the payload.

```sh
viff data                                                          # the whole viff instance, one document
viff data viff-tutorial pr '*'                                    # by where it lives
viff data viff-tutorial pr 1                                      # one pull request, whole
viff data viff-tutorial pr '*' -if '{stage: open}'                # by what it says
viff data viff-tutorial pr '*' -if '{stage: !or [open, merged]}'  # …with an operator
viff data viff-tutorial pr '*' -trim '{stage}'                    # …and how much of each to print
viff data -paths viff-tutorial pr '*'                             # just the addresses
viff data -paths viff-tutorial pr '*' | xargs -L1 viff data      # …passed straight back
viff data -at 'viff-tutorial.pr.*.stage'                          # a whole address as one word
```

Each segment of an address is its own shell word, so nothing needs escaping. A word is always one
segment: `viff data viff-tutorial.pr.1` names one segment called `viff-tutorial.pr.1`. To give
a whole address as one word, put it after `-at`.

The match operators, such as `!or`, `!glob` and `!not`, are tony's. They mean the same thing in a
read, in a charter and in an answer contract. [Reading](../reference/entities.md#reading) has
the rest.

## Write

A put **merges**: the fields you do not name stay as they are. Viff answers with the data as
it stands after the write:

```sh
viff data viff-tutorial pr 1 -put '{owner: dee}'        # changes the owner, keeps the stage
viff data viff-tutorial pr 1 labels urgent -put true    # one field, deep
viff data viff-tutorial pr 1 labels urgent -rm          # and gone again
```

A write can carry a condition. The store checks it and makes the write in one step:

```sh
viff data viff-tutorial pr 1 -put '{stage: open}' -create                   # only if nothing is there yet: refused
viff data viff-tutorial pr 4 -put '{owner: dee}' -expect '{stage: merged}'  # only if it still looks like this
```

[Writing](../reference/entities.md#writing) and
[conditional writes](../reference/entities.md#conditional-writes) have the detail.

## Watch

```sh
viff watch -if '{stage: open}' viff-tutorial pr '*'
```

```tony
{at: 'viff-tutorial.pr."1"' op: set rev: 97 to: {labels: {area: auth} owner: dee stage: open …}}
{at: 'viff-tutorial.pr."3"' op: set rev: 97 to: {labels: {area: store urgent: true} owner: ana stage: open …}}
{at: 'viff-tutorial.pr."5"' op: set rev: 97 to: {labels: {area: engine} owner: cy stage: open …}}
```

The watch **replays**: what already matches arrives first, then each new change. Each change is
one line, and `to` is the whole document at that commit. In another terminal, run
`viff data viff-tutorial pr 2 -put '{stage: open}'`, and the line arrives. Then write
`'{stage: reviewing}'`. Nothing arrives, because the filter tests the state, and the new state does
not match. [Watching](../reference/entities.md) has cursors, `-since` and the rest.

## Everything viff keeps is data

Viff keeps its own state in the same store, at addresses of its own, so the same three verbs
read it:

| Address | What is there |
|---|---|
| `mirror.ask.<id>` | a [question](questions.md), its answers and what it settled on |
| `mirror.task.instance.<id>` | a [task](tasks.md): its status, its prompt and its answer |
| `mirror.trigger.rule.<charter>:<name>` | a rule of a [charter](charters.md), as it was installed |

So a rule can react to an answered question or to a finished task as it reacts to any data.
Nothing in viff is hidden from `viff data` and `viff watch`. Next, [questions](questions.md).

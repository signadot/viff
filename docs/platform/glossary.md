# Platform glossary

This page defines the terms that somebody who runs, extends or changes verse meets. The terms
a user meets are in [the glossary](../glossary.md).

## Tasks and where they run

### frame (of an event stream)

A frame is one event in the event stream of a task: a status, a pending gate, a decision, a
`say`. Each frame has a `kind` and a `cursor`.

See [Task control](../task-control.md#the-event-envelope-and-the-cursor).

### root (of verse up)

`-root` on `verse up` names the directory in which the tasks of the verse process act. It is a
directory on disk, and not an address.

Also written: the agent root.

See [Starting a verse](../reference/up.md#the-agent-root).

## Ios

### reflection

A reflection is one answer of a source. Its `entities` are what the source read. Its `covers`
say where the answer is complete.

See [Ios (reference)](../reference/io.md#the-contract-an-io-serves).

### cover (of a reflection)

A cover of a reflection is a pattern under which the reflection is complete, such as
`git.verse.ref.*`. Verse removes a stored entity that a cover reaches and that the reflection
does not hold.

See [Ios (reference)](../reference/io.md#the-contract-an-io-serves).

### converge

To converge is to make the store match a reflection. Verse writes what changed, and tombstones
what a cover reaches and the reflection does not hold.

The page [Convergence](../convergence.md) is about how rules come to rest.

See [Ios](../io.md#the-seam-a-source-answers-verse-converges).

### tombstone

To tombstone an entity is to remove it from the store because its external system does not
hold it any more.

See [Ios](../io.md#the-seam-a-source-answers-verse-converges).

### reflector

The reflector is the part of verse that drives one installed source. It asks for a full
reflection every minute, and for a part of one when a hint arrives. It converges the store
with each answer.

See [Ios (reference)](../reference/io.md#the-reflector).

### iod

`iod` is the binary that holds the five ios that verse ships. Each is a subcommand: `git`,
`git-issue`, `slack`, `linear` and `github`.

See [iod](../reference/iod.md).

### mirror (of a git repository)

A mirror of a git repository is a copy of a remote repository, kept fetched.
`iod git -mirror <url>` reflects from one, and the coderunner example clones from one.

In a page about git, "mirror" has this meaning.

See [iod](../reference/iod.md#git).

## Steering

### observation plane

The observation plane is the part of verse that reads: `get`, `list` and `watch`. A read may be
late.

See [Deployment and steering](../deployment-and-steering.md#two-planes).

## Index

+ [converge](#converge)

+ [cover (of a reflection)](#cover-of-a-reflection)

+ [frame (of an event stream)](#frame-of-an-event-stream)

+ [iod](#iod)

+ [mirror (of a git repository)](#mirror-of-a-git-repository)

+ [observation plane](#observation-plane)

+ [reflection](#reflection)

+ [reflector](#reflector)

+ [root (of verse up)](#root-of-verse-up)

+ [tombstone](#tombstone)

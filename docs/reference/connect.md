# Federating a working tree, and scopes

`verse connect` federates your working tree into the [store](../glossary.md#store). To
[federate](../glossary.md#federation) a tree is to make its files readable through the store.
`verse connect` does this under a private [scope](../glossary.md#scope) for this session, and it
connects a validator to every file. This is the loop for local development. `verse connect` is
a process of its own, so give it a terminal and leave it running.

[Content federation](../content-federation.md) says how verse splits a tree into an index and
the bytes that it serves.

## What one command does

`verse connect` does three things.

| What | Where | What it holds |
|---|---|---|
| writes the metadata of each file | `local.dir-status-<scope>.<path>` entities | the full fstat and a hash of the content. It is an index: the stat and the hash, and not the bytes. |
| serves the contents of files on request | `local.dir-content-<scope>.*` | the bytes. A validator reads them through the verse, and needs no shared filesystem. |
| installs a scoped [rule](../glossary.md#rule) | the scope | a rule that spawns a validator for each file |

All of it lives in a scope with a random name. The scope is isolated from
[baseline](../glossary.md#baseline) and from other developers. When `connect` exits, it drops
the scope.

## Driving it

First configure the model credentials ([up.md](up.md#the-driver)), so that the validators run
as real agents. In one terminal:

```sh
verse up
```

In a second terminal, connect your tree and give the validator a prompt. `$path` binds to the
relative path of each file. `connect` prints the scope that it chose, such as `scope "a1b2c3"`:

```sh
verse connect . -driver llm \
  -prompt 'Read the file at path "$path" and report any problems in one sentence.'
```

In a third terminal, use the scope that `connect` printed:

```sh
verse task                                           # a validator per file
verse task get <id>                                  # the agent's read-and-validated report
verse -scope <scope> data local                    # your files, as dir-status entities
verse data local dir-content-<scope> README.md # a file's bytes, served on demand
```

Touch or add a file, and its validator fires. The validator reads the file through the verse.
To remove the scope, press Ctrl-c in `connect`, or run `verse disconnect -scope <scope>`. The
rule, the overlay and the content mount all go.

For a test without credentials, leave out the credentials and `-driver llm`. A validator still
fires for each file, on the scripted driver. It does not read the file.

The runtime need not share a filesystem with your tree, and the validator can run on another
machine.
*Verse truncates file reads, so an agent validates a large file from a partial view.*

<a id="scopes"></a>
## Scopes

Every client command takes `-scope <name>`. The default is baseline. A scope is a
copy-on-write overlay:

- a read sees the scope on top of the shared baseline;
- a write lands in the scope, and does not touch baseline.

So your local changes and your flows stay isolated.

```sh
verse connect . -scope me
verse watch -scope me
verse data -scope me demo x 1 -put '{k: v}' # in the scope only
verse data demo x 1                          # baseline: not there
verse disconnect -scope me                         # drop the whole overlay
```

`disconnect -scope me` removes the overlay in one step. It removes every write of the scope,
not only the `local.*` files. First, what the scope started
stops:

1. its sources stop;
2. its engine stops;
3. verse cancels every live task that the scope owns ([below](#what-a-scope-owns)). For the
   tasks that its rules started, verse skips `onCancelled`, because that hook would act
   inside the scope that is being dropped.

How the store makes a scope is in [connect.md](../platform/connect.md), in the repository.

## The charter a scope runs

A scope runs **the rules installed into it**, and only those. `verse c -a -scope me` writes
the rule into the layer of the scope. There it is durable, and you can read it at its address
(`verse data verse trigger rule x -scope me`). The record carries `scope: me`, and that field
says whose rule it is.

A scope does **not** run the rules of baseline, although a scoped read sees them. The store
puts baseline under the scope. So `verse data verse trigger rule x -scope me` answers for a
rule that the scope never installed. The `scope` field on the record tells the two apart.

`verse charter -scope me` lists the same set. It answers from the engine of the scope, and not
from the layered store. So it shows what runs in the scope and nothing else: the rules that
you installed, without the rules of baseline.

To run the behaviour of a baseline rule in your scope, install the rule there. To
**replace** a baseline rule, install yours under the same name. A scoped install lands in your
layer. There it is the only rule of that name in the engine of the scope, and baseline does
not change. You do not need to remove anything first, and you cannot:
`verse c -r rules <a baseline rule> -scope me` is a write to the charter of baseline from a
scope, and verse refuses it as one (see [what a scope owns](#what-a-scope-owns)). A rule that
never ran in the scope needs no hiding.

After a restart of verse, the charter of a scope comes back when verse next builds the engine
of the scope. That happens when you connect again. Until something asks for the scope, its
engine does not exist and its rules do not run. A restart cannot list the scopes that have a
charter. The layer of a scope is not visible from baseline, and nothing registers a scope.

*Left to do: a scope is not a first-class, listable, durable thing. A registry of scopes would
let a restart bring back the charter of every scope without a new connection.*

Whether a scope should also run the rules of baseline is an open question. See
[Why it is this way](#why-it-is-this-way).

<a id="what-a-scope-owns"></a>
## What a scope owns

A scope has two halves, and they layer differently.

**The world is copied.** The entities under `demo`, `git`, `notes`, or whatever the charter is
about, use the copy-on-write of logd:

- a scoped write lands in the overlay;
- a scoped read sees the overlay on top of baseline;
- baseline never sees the overlay.

**The program is referenced.** Rules, tasks, gates and flows are records in the store. A scope
owns what it created:

- the rules installed into it;
- the tasks that those rules and `task run -scope` spawned;
- the gates that those tasks raised.

Everything else that the scope can see belongs to baseline, and the scope reads its record
through the overlay. How the runtime keeps each one is in
[connect.md](../platform/connect.md#the-program-of-a-scope), in the repository.

So a write of the [control plane](../glossary.md#control-plane) in a scope reaches only what the
scope created. These are the writes of the control plane:
`flow suspend|resume|say|cancel`, `task cancel|say|vector`, `gate answer|escalate`
and `charter -r`.

| A suspension made | What it covers |
|---|---|
| with `-scope me` | the rules that the scope installed, the tasks and questions they started, and the tasks run in the scope. Nothing of baseline. |
| without `-scope` | the work of baseline and of every package. Nothing of any scope. |

Verse **refuses** such a write aimed at an inherited object. The refusal names the scope
that holds the object. Baseline is the scope with no name:

```
task review@demo.pr.1 is baseline's; scope me did not create it — act on it without --scope
```

The rule is the same from baseline. A task that a scope spawned belongs to that scope.
Baseline is told which scope to name, and it cannot cancel the task without knowing.

Creating is what "the scope's own" means, so verse never refuses a creation:
`charter -a -scope me`, `task run -scope me`, and everything that a scoped rule fires. To
replace a baseline rule in a scope, install yours under its name (above). Removing the rule of
baseline from the scope is a write to the program of baseline, and verse refuses it as one.

**Grants are copied, like the world.** Baseline's grants govern a scope: opening a scope does
not get around them. A grant made with `-scope me` lands in the overlay. It governs the writes
of the scope, on top of the grants of baseline, and the checks of baseline never see it.
[Permissions](../perms.md#scopes) has the detail.

## Why it is this way

The reasons for this design are in [reference-connect.md](../design/reference-connect.md), in the repository.

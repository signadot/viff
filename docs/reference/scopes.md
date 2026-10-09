# Scopes

A scope is a private copy of the [store](../glossary.md#store) for one party: a developer, a
sandbox, a trial. A write under a scope lands in the scope and does not touch
[baseline](../glossary.md#baseline). A read under a scope sees the scope on top of baseline. So
your local changes and your flows stay isolated from other developers and from the shared verse.

<a id="scopes"></a>
## Using a scope

Every client command takes `-scope <name>`. The default is baseline. A scope is a copy-on-write
overlay:

- a read sees the scope on top of the shared baseline;
- a write lands in the scope, and does not touch baseline.

A scope exists from its first scoped write. Nothing creates one ahead of time.

```sh
verse data -scope me demo x 1 -put '{k: v}' # in the scope only
verse data -scope me demo x 1                # the scope: there
verse data demo x 1                          # baseline: not there
verse watch -scope me                        # the scope's changes, baseline's beneath
verse charter -a -f rules.tony -scope me     # a charter that runs in the scope alone
```

Dropping a scope is `POST /scope/delete?scope=<name>` on the operator API. It removes every write
of the scope, in one step. First, what the scope started stops:

1. its sources stop;
2. its engine stops;
3. verse cancels every live task that the scope owns ([below](#what-a-scope-owns)). For the
   tasks that its rules started, verse skips `onCancelled`, because that hook would act
   inside the scope that is being dropped.

How the store makes a scope is in [scopes.md](../platform/scopes.md), in the repository.

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
of the scope. That happens on the first scoped request. Until something asks for the scope, its
engine does not exist and its rules do not run. A restart cannot list the scopes that have a
charter. The layer of a scope is not visible from baseline, and nothing registers a scope.

*Left to do: a scope is not a first-class, listable, durable thing. A registry of scopes would
let a restart bring back the charter of every scope without a new request.*

Whether a scope should also run the rules of baseline is an open question. See
[Why it is this way](#why-it-is-this-way).

<a id="what-a-scope-owns"></a>
## What a scope owns

A scope has two halves, and they layer differently.

**The world is copied.** The entities under `demo`, `git`, `notes`, or whatever the charter is
about, use the copy-on-write overlay of [scopes](#scopes).

**The program is referenced.** Rules, tasks, gates and flows are records in the store. A scope
owns what it created:

- the rules installed into it;
- the tasks that those rules and `task run -scope` spawned;
- the gates that those tasks raised.

Everything else that the scope can see belongs to baseline, and the scope reads its record
through the overlay. How the runtime keeps each one is in
[scopes.md](../platform/scopes.md#the-program-of-a-scope), in the repository.

So a write of the [control plane](../glossary.md#control-plane) in a scope reaches only what the
scope created. These are the writes of the control plane:
`flow suspend|resume|say|cancel`, `task cancel|say|vector`, `question <id> --answer`
and `charter -r`. What a suspension covers in a scope is in [control](../control.md#scopes).

Verse **refuses** such a write aimed at an inherited object. The refusal names the scope
that holds the object. Baseline is the scope with no name:

```
task review@demo.pr.1 is baseline's; scope me did not create it — act on it without --scope
```

The rule is the same from baseline. A task that a scope spawned belongs to that scope.
Baseline is told which scope to name, and it cannot cancel the task without knowing.

Creating is what "the scope's own" means, so verse never refuses a creation:
`charter -a -scope me`, `task run -scope me`, and everything that a scoped rule fires. To
replace a baseline rule in a scope, install yours under its name
([above](#the-charter-a-scope-runs)).

**Grants are copied, like the world** ([permissions](../perms.md#scopes)).

## Why it is this way

The reasons for this design are in [reference-scopes.md](../design/reference-scopes.md), in the repository.

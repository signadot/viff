# Scopes

A scope is a private copy of the [store](../glossary.md#store) for one party: a developer, a
sandbox, a trial. A write in a scope lands in the scope. It does not touch
[baseline](../glossary.md#baseline), the store everybody shares. A read in a scope sees the
scope on top of baseline. So your changes and your flows stay apart from other people's work.

<a id="scopes"></a>
## Using a scope

Every client command takes `-scope <name>`. Without it, the command works on baseline. A scope
is a copy-on-write overlay:

- a read sees the scope on top of baseline;
- a write lands in the scope, and baseline does not change.

A scope exists from its first write. Nothing creates one ahead of time.

```sh
viff data -scope me demo x 1 -put '{k: v}' # in the scope only
viff data -scope me demo x 1                # the scope: there
viff data demo x 1                          # baseline: not there
viff watch -scope me                        # the scope's changes, baseline's beneath
viff charter -a -f rules.tony -scope me     # a charter that runs in the scope alone
```

An operator drops a scope with `POST /scope/delete?scope=<name>`. That removes every write of
the scope in one step. Before it does, viff stops what the scope started:

1. its sources stop;
2. its engine stops;
3. viff cancels every live task that the scope owns ([below](#what-a-scope-owns)). For a task
   that one of the scope's rules started, viff does not fire `onCancelled`, because that hook
   would act inside the scope that is being dropped.

How the store makes a scope is in [scopes.md](../platform/scopes.md), in the repository.

## The charter a scope runs

A scope runs **the rules installed into it**, and only those. `viff c -a -scope me` writes the
rule into the scope. The rule is durable there, and you can read it at its address
(`viff data mirror trigger rule x -scope me`). Its record carries `scope: me`, which says whose
rule it is.

A scope does **not** run the rules of baseline, even though a read in the scope sees them. So
`viff data mirror trigger rule x -scope me` also answers for a rule that the scope never
installed. The `scope` field on the record tells the two apart.

`viff charter -scope me` asks the scope's engine, not the store. So it shows only what runs in
the scope: the rules you installed there, and none of baseline's.

To run a baseline rule in your scope, install it there. To **replace** a baseline rule in your
scope, install yours under the same name. Your rule is then the only rule of that name in the
scope, and baseline does not change. You do not remove the baseline rule first, and you cannot:
`viff c -r rules <a baseline rule> -scope me` writes to baseline's charter from a scope, and
viff refuses it ([what a scope owns](#what-a-scope-owns)).

After viff restarts, a scope's charter comes back the first time something asks for the scope.
Until then, the scope has no engine and its rules do not run. Viff keeps no list of scopes, so
a restart cannot start them all. The tasks the scope's rules started are attached again, and
its claims are paid, at the same moment. A task run by hand in the scope is not attached again.

Whether a scope should also run the rules of baseline is an open question
([why it is this way](#why-it-is-this-way)).

<a id="what-a-scope-owns"></a>
## What a scope owns

A scope has two halves, and they layer in two different ways.

**The world is copied.** The data a charter is about, under `demo`, `git`, `notes` or any
other place, use the copy-on-write overlay ([using a scope](#scopes)).

**The program is referenced.** Rules, tasks, gates and flows are records in the store. A scope
owns what it created:

- the rules installed into it;
- the tasks that those rules and `task run -scope` started;
- the gates that those tasks raised.

Everything else the scope can see belongs to baseline. The scope reads that record through the
overlay ([the program of a scope](../platform/scopes.md#the-program-of-a-scope), in the
repository).

So a write of the [control plane](../glossary.md#control-plane) in a scope reaches only what the
scope created. These are the control writes: `flow suspend|resume|say|cancel`,
`task cancel|say|autonomy`, `question <id> --answer` and `charter -r`. What a suspension covers
in a scope is in [control](../control.md#scopes).

Viff **refuses** a control write in a scope that names something the scope did not create. The
refusal names the scope that holds it, and baseline is the scope with no name:

```
task review@demo.pr.1 is baseline's; scope me did not create it — act on it without --scope
```

The same holds from baseline: a task that a scope started belongs to that scope, and baseline
cannot cancel it without naming the scope.

Viff never refuses a creation, because creating is what makes a thing the scope's own:
`charter -a -scope me`, `task run -scope me`, and everything that a scope's rule fires.

**Grants are copied, like the world** ([permissions](../perms.md#scopes)).

## Why it is this way

The reasons for this design are in [reference-scopes.md](../design/reference-scopes.md), in the
repository.

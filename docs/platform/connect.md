# Connect and scopes on the platform

This page is how `verse connect` and a scope work inside verse: docd, logd and the engine of a
scope. What a user types and sees is in [connect.md](../reference/connect.md).

## What one command does

`verse connect` serves the contents of files on request, through docd. A content
[controller](../glossary.md#controller) serves them at `local.dir-content-<scope>.*`.

## How the contents travel

The contents pass through docd, and not through a `-root` on the server. So the runtime need
not share a filesystem with your tree, and the validator can run on another machine.

## Dropping a scope

`disconnect -scope me` removes the overlay in one step, through the `DeleteScope` of logd.

The store provides scopes, and the surface of verse does not. logd and docd make the
copy-on-write overlay, and `disconnect -scope` asks the store to drop one. A store without
real scopes would answer 501. The client would then remove the `local.*` entities one at a
time.

The engine of a scope watches subtrees, one watch for each rule. Each watch covers exactly the
literal prefix of the [pattern](../glossary.md#pattern) that its rule matches. It does not
watch the whole tree.

## The program of a scope

**The program is referenced.** Rules, tasks, gates and flows are records in the store. Each one
is also a live thing:

| Record | Its live thing |
|---|---|
| a rule | a watch |
| a task | a handle |
| a gate | an awaiter, which waits for the answer |
| a flow | the set of handles about a subject |

A live thing runs in exactly one engine. The engine of every scope shares one task runtime and
one gate registry with baseline. The engine of a scope holds what the scope created:

- the rules installed into it;
- the tasks that those rules and `task run -scope` spawned;
- the gates that those tasks raised.

Everything else that the scope can see belongs to baseline, and the scope reads it through
the overlay. The record is there. The live thing is not.

## Grants in a scope

A grant is live too, because the check reads a mirror of the records and not the store. But a
grant has no live half that runs anywhere. The mirror is a cache of records, and it layers as
records do.

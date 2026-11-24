# Scopes on the platform

This page is how a scope works inside viff: docd, logd and the engine of a scope. What a user
types and sees is in [scopes.md](../reference/scopes.md).

## Dropping a scope

`POST /scope/delete?scope=<name>` removes the overlay in one step, through the `DeleteScope` of
logd.

The store provides scopes, and the surface of viff does not. logd and docd make the
copy-on-write overlay, and the route asks the store to drop one. A store without real scopes
answers 501.

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

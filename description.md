# perms seed: verse.trigger is everybody's to read, so the charter's records are read and watched from one address

A person can read every kind under `verse.trigger` and not `verse.trigger` itself, so the charter's records cannot be read or watched as one tree.

## What happens

The seed holds out `read` to everyone at each of the four kinds that stand under `verse.trigger`: `rule`, `strata`, `package`, `suspension`. It has no row at `verse.trigger`. A person with no grants gets:

- `GET /state?at=verse.trigger.rule` → 200, and the same at `.strata`
- `GET /state?at=verse.trigger` → 403
- `GET /watch?at=verse.trigger..` → 403

So a client that wants every install and removal watches three or four addresses, and has to know the kinds to name them. Charters are trees; the watch that follows one is `verse.trigger..`.

## Fix

One seed row: `read` to everyone at `verse.trigger`.

It shows nothing that is not shown already. The four kinds are all that stands under `verse.trigger`, each has its own record and its own `read` row, and a grant does not cross a record. The new row reaches the node `verse.trigger` and anything put under it later with no record of its own, which is the one thing to weigh when a fifth kind is added.

An existing verse gets it at its next start: the seed writes a record that is absent, and none stands at `verse.trigger`. The record above it, `verse`'s, holds out nothing, so the new record stops no grant that reached this place before.

## Done when

- A person with no grants reads `verse.trigger` and watches `verse.trigger..`.
- `docs/platform/perms.md` lists the row.
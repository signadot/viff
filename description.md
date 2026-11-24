# perms: a node emptied by its owner stays the owner's, and whoever creates beneath it next does not own it

Removing the last thing under a node leaves the node standing, empty, and still owned as it was. A principal who holds `child-owner` above it and creates beneath it again owns only what it created, not the node. Nothing a reader sees says the empty node is there or whose it is, and an install at it is refused.

## How it showed up

A place was seeded by an admin before the principal that installs charters there had created anything in it. The install was refused:

    refused: agent:<name> holds read at <place>.<child> and installing a charter at it needs owner

The admin removed the seed and the installing principal wrote it again. The records were then that principal's, but the nodes above them still stood, empty and the admin's, and the install was refused as before.

## To reproduce

As the admin, with `agent:git-iod` holding `child-owner` at `zz`:

    viff perms zz --grants '[{child-owner: ["agent:git-iod"]}, {read: ["*"]}, {below: true, read: ["*"]}]'
    viff data zz bug a --put '{x: 1}'      # zz.bug and zz.bug.a have no record of their own: the admin's
    viff data zz bug a --rm                # `viff data zz --paths` still lists `zz bug`

As the git iod:

    viff data zz bug a --put '{x: 2}'      # zz.bug.a: owner agent:git-iod
    viff perms zz bug                      # no record of its own: still the admin's
    viff data zz bug --rm                  # refused: deleting it needs owner

For comparison, where `zz.ctl` never stood, the git iod's `data zz ctl a --put` leaves `zz.ctl` with a record, `owner: agent:git-iod`. And where the admin also removes `zz.bug` itself, the git iod's next create owns it.

## What might change

One of:

- removing the last child of a node that has no record and no value of its own removes the node too;
- `data --rm` says the parent it leaves standing, empty, and whose it is;
- the refusal names the empty node and how to clear it.

## Worked around

The installing principal takes the place with `viff perms … --grants` before anything stands in it, and writes the seed itself.
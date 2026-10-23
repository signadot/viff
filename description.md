# versed: the perms migrations read every mirror record twice at every start

`NewWithStore` runs `migratePerms` before any startup phase: `entity.MigrateChildGrants` then `entity.MigrateChildOwner`. Each lists the perms mirror and then reads every record one at a time, to find the old spellings (`default` for `child-grants`, `owner` held out below for `child-owner`). That is two reads per record on every start, issued before the mirror is loaded into memory (`phasePerms`). On staging both migrations ran long ago and now find nothing: the last start logged no `perms: renamed` / `perms: rewrote` line.

Found while counting the store reads of an unchanged restart (TestAnUnchangedSystemCharterIsNotReplayedAtARestart, in the system-skip work): the two reads left over came from here.

Decided (Scott): delete both migrations. That covers `entity/migrate.go` with its tests, `migratePerms` and its call, and the way back, `verse up --unmigrate-perms`, with its docs. A store still holding the old spellings is not one verse reads, and no deployment holds one.
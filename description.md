# versed: a task installs and removes charters, each spawn held within the task less charter-install

The verse system gives a task `verse-charter-get` and `-check`. It does not yet give install and remove, which `verse charter -a` / `-r` and `verse_charter_install` / `verse_charter_remove` have.

A charter a task installs runs as the task's owner and outlives the task. Unheld, a task given only `verse` could install a rule whose spawn reaches everything the owner can.

Design, as agreed:
- The door carries `POST /charter` and `DELETE /charter` for a task.
- At install, and at a check carried for a task, each rule's spawns (`Action.EachSpawn`) are held within the task:
  - environment, as written (`WithinAsWritten`), within the task's less `verse.charter-install`;
  - autonomy no looser than the task's current autonomy;
  - the task's credential;
  - any runner.
- The rule records the task (`Trigger.Task`, written on the rule record beside the spec, read back at restore). A person reinstalling the rule clears it.
- At each firing, the bound environment is held to the task's recorded environment (`mirror.EnvironmentOf`), less charter-install. A missing record refuses the spawn.
- A readback note in `verse charter`.
- The verse system document gets the verbs `charter-install` and `charter-remove`.

The runner's tools are a follow-up commit.
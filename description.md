# Uninstalling a charter package leaves its tasks running and its claims standing

## What happens

Uninstalling a package (`takeDown`, `internal/versed/packages.go`) removes its rules, strata, schedules and records and drops its engine. It does not touch the work the package had in progress:

- its live tasks keep running on their runners;
- the claims those tasks hold (`verse.task.claim.<id>`) stand;
- launches a suspension parked for it stay recorded (`verse.task.parked.<id>`, in scratch).

Nothing takes the end of such a task while no engine is at the address, so its hooks do not run and its claim is not released. The state lasts until one of:

- the package is installed at the address again: the new engine is handed the running tasks before it opens and takes their ends (`Engine.Leave`, `handTasks`, since 0864e098);
- verse restarts with the package still gone: the tasks are cancelled and the package's claims removed (`settleTasks`).

So what an uninstall does to running work depends on whether verse restarts afterwards.

## The question

Should uninstalling a package stop its work then, as a restart does for a package that is gone: cancel its live tasks, remove its claims, and remove its parked launches?

## Things a change has to cover

- **A cancel that fails.** At start, a task whose cancel fails keeps its claims for the next start (9a4ca9d4). An uninstall needs the same answer, and a reinstall in between must still be handed the task.
- **Uninstall then install as an upgrade path.** If anything relies on uninstalling and installing again to change a package while its tasks run, stopping the tasks breaks it. An install over a standing package reuses the engine and is not affected.
- **Prune paths.** `charterprune.go` and `systemprune.go` call `takeDown` too.
- **Scopes.** Deleting a scope already cancels its live work (`Engine.CancelLive`), with no hooks run. Whether a package's uninstall should run hooks for the tasks it cancels is part of the question.

## Where this came from

Review of the task-end work (zm4kqgzsh12krkmtznn0). That work fixed the engine made again failing its own running tasks, and left what uninstall does to running work as it is.
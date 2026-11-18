# Uninstalling a charter package does not stop the tasks its rules started

## What happens

Uninstalling a package (`takeDown`, `internal/versed/packages.go`) removes its rules, strata, schedules and records. It does not stop the work the package had in progress:

- its live tasks keep running on their runners;
- the claims those tasks hold (`verse.task.claim.<id>`) stand until each task ends;
- launches a suspension parked for it stay parked, and start when the suspension lifts.

The package's engine stays at the address, with no rule, until that work is done, and is then dropped (`retirePackageEngine`, `Engine.Idle`, since fb520adc). Meanwhile:

- a task that ends has its end taken by that engine. A task started in this verse carries the action it was spawned with, so its `onDone` / `onFailed` runs as written when it was spawned, and its claim comes off. The hook writes as the rule's record (`Engine.Principal`), which the uninstall removed, so on a store with a policy its writes are expected to be refused and recorded as a hook failure. That is from reading the code, not from a run.
- a package installed at the address again finds the engine and keeps it, tasks and all.
- verse restarting with the package still gone cancels the tasks and removes the package's claims (`settleTasks`).

So what an uninstall does to running work still depends on whether verse restarts afterwards: without a restart the tasks run to their end; with one they are cancelled.

## The question

Should uninstalling a package stop its work then, as a restart does for a package that is gone: cancel its live tasks, remove its claims, and drop its parked launches?

## Things a change has to cover

- **A cancel that fails.** At start, a task whose cancel fails keeps its claims for the next start (9a4ca9d4). An uninstall needs the same answer.
- **Uninstall then install as an upgrade path.** If anything relies on uninstalling and installing again to change a package while its tasks run, stopping the tasks breaks it. An install over a standing package is not affected.
- **Prune paths.** `charterprune.go` and `systemprune.go` call `takeDown` too.
- **Hooks.** Deleting a scope cancels its live work with no hooks run (`Engine.CancelLive`). Whether a package's uninstall should run hooks for the tasks it cancels is part of the question, and so is whose authority a hook of a removed rule writes with.
- **A parked launch.** It keeps a retired engine for as long as its suspension stands.

## Where this came from

Review of the task-end work (zm4kqgzsh12krkmtznn0). That work keeps an uninstalled package's engine until its tasks are done, and left what uninstall does to running work as it is.

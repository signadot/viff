# A task's launch state is durable on its record, and versed's copy is kept for a retention after the task ends

## The problem

Every task versed launches (a rule's spawn, `verse task run`, an ask's `then`) gets an entry in an in-memory side table beside its record (`s.hosted`, `hostedTask`): its scope, subject, whom it acts for, answerers, its autonomy as slid, and its launch (environment, runner, credential). Two things are wrong with it:

- **A restart loses it.** Only tasks still running on a remote runner get an entry again, rebuilt from their records, and the record carries no environment and no credential. So after a restart a raise whose `then` spawns is refused ("the raising task's launch is not known"), though the task is running fine behind its runner.
- **Entries are never dropped.** Every task versed ever launched stays in memory until the process ends.

## The change

- The task record carries the launch: `environment`, as the spawn's environment, and `virtualKey`, the credential's ADDRESS as the spawn named it, never the key. `runnerName` is there already.
- A restart rebuilds the entry of a task it resumes with its launch, from the record.
- What a `then`'s spawn is held to compares the credential by address: the one the task's spawn named.
- versed drops a task's entry a retention period after the task ends. Its readers after the end find the record (a task's scope, by where its record is), so nothing reads the entry past it. A new run of the same id keeps its own entry.

## Not covered

- Scoped tasks are not resumed at a restart at all: reconcile reads only the baseline store. Their scope is where their record is, and resuming them is its own change.
- The task record itself has no retention.

## Part of

A prerequisite in the plan for verse system work (verse/verse-system-base), after the door and before endpoints open for tools.
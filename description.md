# The door admits a runner relaying a task's owner, through the task's record

A task's verb on verse is the task owner's act, carried by the runner that hosts it ("Verse as a system, v0", Notion 40408fc654be8049879edeafe483abcc, "The door"). Today `asRunner` refuses any runner request naming somebody else (the stop-gap pointing at 3gf9wqb3h12kr2ymtnn0).

The relay:
- A request whose bearer proves a registered runner and that names a task (`Verse-Task`) is admitted as the task's owner when the task's record says `runnerName` is this registration, the record's `runnerSubject` is this registration's subject, and the task is running. Otherwise it is refused, naming which failed.
- The owner is the record's (entity.OwnerOf), so the runner names nobody: a `Verse-Principal` beside it must name the same owner or nobody. A task the system owns is relayed for nobody.
- The spawn records the registration's subject on the task (`runnerSubject`), so a registration rewritten under the same name with another subject relays nothing of tasks it did not host.
- `whoami` says `via: runner:<identity>`; the handler knows the task (for the raise verb's containment, next issue).
- An account to relay (`Verse-For-Principal`) beside a runner token stays refused: a runner relays a task, never an account.

Out of scope: the embedded runner, whose in-process tasks reach versed with no request; that is the embedded re-do in ENG-1297.
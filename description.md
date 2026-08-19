# spawns complete at about one per second however many fire at once

Measured locally with the comment-drift charter over 131 commits landing at once
(scratchpad/stress-width.sh, and docs/working/drift-per-commit-plan.md for why):

    A  131 commits land, no charter installed     2s        (15ms each)
    B  install the charter, 131 fire at once
         records (derive + gather)                14s       (~9/s)
         tasks                                    0 at 14s, 0 at 49s, 22 at 68s,
                                                  41 at 90s, 77 at 144s, 131 at 174s
    C  131 more with the charter running          abandoned — minutes, and reads
                                                  time out while it runs

Nothing is lost: all 131 records derived, all 131 gathered, all 131 tasks eventually
spawned. The rate is the problem, and it is specific to the SPAWN half — the derive half
does ~9/s on the same engine, same store, same commits.

Two hypotheses checked and eliminated:

  - dispatch is not serial. `fireSubjects` does `go e.fireOne(...)` per selected subject.
  - the cascade throttle did not trip. `DefaultTripPace` is exactly 1s and matches the
    observed rate, but there is no `cycle:` line in the log — chain depth 3 against a cap
    of 4 (one per trigger).

A single spawn is ~1s end to end (land → task, drive-drift.sh scenario 1). So ~1s of work
per spawn, dispatched concurrently, still completing at ~1/s: something inside the spawn
path serializes what the dispatcher already parallelized.

Not yet isolated. The cheap next step is the same charter with the review's `spawn`
replaced by a plain `patch`: if 131 then complete in ~15s like the derives, the cost is
inside the spawn path (task entity, broker registration, frames) rather than in the engine
loop.

Operator-visible symptom while this runs: `verse data <path>` answers "the store could not
be read … context deadline exceeded". Nothing is wrong with the store afterwards.
# settle races a re-firing: an answer lands on the ask a firing just joined

Found on `verse/ask-next`, while adding "a settled rule gate that fires again is a new ask with a new id" (44c499d9). It predates that change.

## The race

`Server.settle` (versed/server.go) delivers an answer to the waiter first (`Reg.Deliver`, or the task's runner), then writes the record (`mirror.AnswerQuestion`). Between the two, the record still says `pending`.

A firing of the same gate in that window runs `Engine.askingID` (trigger/asking.go). It sees an open ask at the derived id and joins it: `Registry.Resolve` posts it (the entry was just removed by `Deliver`, so a new one is made) and waits. Then `AnswerQuestion` writes `answered` onto that record. The new asking's waiter waits on a record that says answered, and no answer will come.

**Precondition:** a re-firing of the same gate about the same subject in the milliseconds between delivery and the record write. Rare, but it strands a firing when it happens.

## Why not a mutex here

For a lock to close it, the id decision and the post have to happen under the same lock that `settle` holds across delivery and the record write. Two things are in the way:
- **The post isn't separable from waiting.** `Registry.Resolve` posts and blocks until answered in one call, so it can't be held under a lock. The registry would need a post-then-wait split.
- **The lock is versed's and the id decision is the engine's.** The trigger engine would have to take a versed-owned per-question lock across `askingID` and the post, and `settle` the same lock across `Deliver` and `AnswerQuestion`. That is three packages sharing a lock around a call that releases a waiter. Doing it without a deadlock needs care, so it isn't done in passing.

## Options

1. **The post-then-wait split,** with a per-id lock, as above.
2. **Write the record first, then deliver.** A re-firing then sees `answered` and takes a new id. But a delivery that fails (runner unreachable, stale edit) leaves a record saying answered that nobody received. Settle would have to roll the record back, or mark it undelivered, as the broker path already does with `delivered: false`.
3. **Make the record write conditional** on the registry entry it answered, so an answer never lands on a newer asking. The record would need to carry a token of the asking (a posedAt or a sequence), and `AnswerQuestion` would require it.

Option 3 looks smallest, and fits the edit that person asks already carry.
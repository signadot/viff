# The task record says which address it is about

A spawned task's id carries the address it was fired about, but only in flattened form. `spawnID` (`internal/trigger/trigger_api.go`) is `owner + chain + "@" + entity.SegToken(ref.Segs())`. The record itself (`api.Task`, and the mirror at `verse.task.instance.<id>`) has no field for that address.

So a client that wants "the tasks about this place" has to parse the id. PR #4 (`verse tui`) does exactly that: `taskParts` splits on `@`, and `tokenUnder` strips the `+hash` that `SegToken` adds when the flattening is lossy, so a place's token is a prefix of its children's. Stripping the hash makes distinct addresses collide. `a."b.c"` and `a.b.c` both become `a.b.c`, so the first picks up the tasks of the second and of everything under it. Keeping the hash doesn't fix this either: the token for `a."b.c"` is then not a prefix of its children's tokens, because each child's token carries its own hash.

The id should stay an id. The address should be a field.

## Proposal

The task record carries `subject`: the address of the firing, in its ordinary text form. `api.Task` exposes it, and `/tasks` can filter on it. Clients then compare it as segments (`entity.PathSegs`), never as text. A task with no subject (a direct `task do`, say) leaves the field empty.

## Done when

- A spawn writes `subject` on the task's record. `GET /tasks` and `GET /tasks/{id}` return it.
- A test fires a rule on an address whose segment contains a dot (`a."b.c"`). The test checks that the task's `subject` round-trips, and that it does not match `a.b.c`.
- The TUI (or whatever lands from PR #4) reads `subject` instead of parsing the id.
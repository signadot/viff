# verse watch: a frame over 1 MiB ends the stream silently, exit 0

`verse watch verse task instance` against staging returns after ~3s with no output and exit 0.

The opening state of a watch at a node is ONE frame: the whole node. At `verse.task.instance` (218 tasks) that is ~1.4 MB. `streamChanges` (`cmd/verse/client.go`) scans with a 1 MiB line limit and never reads `sc.Err()`, so the scan stops with `bufio.ErrTooLong` and the command returns nil — the same output and status as a server that closed a quiet stream.

Probes, staging, 2026-11-07:

| watch | result |
|---|---|
| `verse task instance` | exit 0 after ~3s, no lines |
| `verse task instance --init=false` | stays open |
| `verse task instance <one id>` | opening state, stays open |
| `verse task instance '*'` | stays open, 218 frames, largest 18 KB |

Fix: the stream says why it stopped when a frame is too long, naming the limit and the `'*'` form that sends one frame per child; and the limit is raised, since the frame is what the person asked to watch.

`verse question --watch` (`cmd/verse/ask.go`) scans with the same 1 MiB limit but returns `sc.Err()`, so it fails loudly rather than silently; its slice is ~21 KB today.
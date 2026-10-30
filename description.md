# Tests for routing and the local runner that #59 doesn't have

Follow-up from the #59 review ("tests that would help"). Not yet covered at e586855c2:

Routing:
- two fitting runners where one is passed over by its ceiling and the other takes the spawn;
- a handshake that succeeds while the runner's current description no longer takes the launch (the re-check after the handshake);
- a `then` held to its task's runner while another runner also offers the doer;
- recovery after a failed handshake.

Local runner:
- adoption of a recorded port held by a non-runner, and that no bearer reaches it;
- `stop()` against a runner that ignores the stop;
- a stop racing a respawn, and shutdown before `lr` is set;
- `resolveRunnerd`'s order and version checks;
- docd adoption with a stale `docd.addr`;
- two `verse up` on one data dir.

Already covered by 6de161210: a re-launch of a live id; replays of a pre-PR record and from below a second asking.
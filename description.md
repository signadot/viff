# streams: a silent stream is not a healthy one — no keepalive, no dead-stream detection

Nothing writes a keepalive on any stream (codec/sse.go writes frames only; no ticker in versed/ or broker/), so a reader cannot tell a quiet stream from a dead one. The proposal: a keepalive comment every N seconds, and a reader that has heard nothing in 2N treats the stream as dead and reconnects.

The other half of this closed already: every non-streaming call to a runner is bounded by runner.ControlTimeout on a dedicated client, never http.DefaultClient (runner/http.go).

From docs/working/work/seam-repair.md, mechanism 2, answering R2 of an earlier census.
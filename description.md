# codex: the wire's pump can block forever after Close, and Close waits for stdout before cmd.Wait

Found by the review of the claude agent (0t4bdajwh12krvwatxn0), whose wire was a copy of runner/runnerd/codex/rpc.go; fixed there, the same two remain here.

1. `pump` sends on the unbuffered `Events` channel; after Close nobody reads it, so a pump holding an event (or draining the queue on `done`) blocks forever, pinning the goroutine and the queued events. Per task, in a long-lived process (`verse up`). The claude wire closes a `stop` channel in Close and selects on it in every send.
2. `Close` waits for `<-r.done` (stdout EOF) before `cmd.Wait`, so `cmd.WaitDelay` never applies: a process codex started that inherited its stdout keeps the pipe open after codex is killed, the reader never sees EOF, and Close hangs. The claude wire kills, then `cmd.Wait()` (WaitDelay closes the pipe), then waits for the reader.

The review also noted the two wires and drivers share most of their plumbing (tail/firstLine/lastLine, Fold/drain/Result, stepOf, Next's shape, askPerson, Close); lifting that into one package under runner/runnerd would fix both at once and leave each agent its wire and propose/Execute. Likewise the runner's per-agent dispatch (inner systems, Describe, modelHosts, Build, the key wiring in cmd/runnerd) is five hand-kept sites; an agent registration would make them loops.
# verse account -link github: ctrl-c stops the wait, as the output says it does

`verse account -link github --account <login>` prints "(ctrl-c stops waiting)" and has a branch for the context ending that says "stopped waiting; <login> is not linked" (cmd/verse/login.go, awaitGitHub). The CLI installs no signal handler, so ctrl-c kills the process before the branch runs: the hint is true, the message is never printed, and the branch is dead code (review of c611c15a3 on zr5vx3z2h12kr6dnvhn0).

Install a signal.NotifyContext for SIGINT and SIGTERM around the wait, as `verse up` does around its serving, and test the branch by signalling the test process once the claim is made.
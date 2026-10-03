# smoke leaves `verse ask --watch` running forever against a verse that is gone

Found 2026-10-04: 25 processes of the form `/var/folders/…/tmp.XXXX/verse --addr 127.0.0.1:<port> ask --watch --since 0`, the oldest from Thursday, each watching a smoke verse that ended minutes after it started.

Two defects, one in the script and one in the command:

1. **smoke.sh backgrounds a wrapper, not verse.** Line 486 runs `VERSE_USER=user:alice v ask --watch --since 0 … &` and line 492 kills `$wpid`, which is the pid of the shell function `v`, not of the verse it started; the comment at line 95 names this very hazard for `connect` and launches it directly. The watch outlives the script and the server.

2. **`verse ask --watch` retries a dead server forever.** The loop in `cmd/verse/ask.go` (`watch`, `waitOut`) backs off and dials again on every error except a refusal with a status below 500; a connection refused is retried without end, so a watch whose server has gone sits in the process table for days. A watch should give up when the server it was watching is gone: after the backoff has reached its ceiling and the dial has been refused for a bounded time, say so and exit non-zero, rather than wait for a verse that will never answer on that port again.

Fix shape: smoke launches the watch by direct invocation (as it does `connect`) and kills that pid, or kills the process group; `ask --watch` exits after a bounded run of connection failures. Until then, `pkill -f 'ask --watch --since 0'` clears them.
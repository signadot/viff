# secret: secret-exec answers at the start, with secret-exec-wait and an exit file, and verse-secret-exec runs a command as if in place

`secret-exec` blocks until the command ends (pa35grmz, step 5), and the run's id is in its answer
only. So nothing can read a run's sockets while it runs: an agent's loop is in the call, and a
script's calls go through the loop one at a time (`scriptTask.await` takes the next call after
`Execute` returns), so a backgrounded `verse-call-tool secret-exec … &` has no id to read at and a
`secret-exec-kill` beside it waits until the run is over. Step 5 chose the blocking shape on the
premise that a script runs two calls at once; it does not.

The shape step 5 set aside:

- `secret-exec` answers once the runner has started the command: the run's id and the directory
  its sockets and files are in. A call refused, or one that could not start, answers as now.
- `secret-exec-wait <id>` blocks until the run ends and answers what the blocking call answered:
  the end, each stream's file, size and tail. No step.
- The end is written to `.verse/exec/<id>.exit` before the output sockets close, so a shell reads
  the status with no call in front of the loop.
- A reader at `<id>.sock` / `<id>.err.sock` reads the stream from its start (the file's part is
  sent first), so what the command wrote before the reader connected is not lost; a reader that
  connects after the file was cut is closed.
- A run lives until it ends, its timeout, `secret-exec-kill`, or the task's process going. The
  call's own context bounds it only until it has started.

`cmd/verse-secret-exec`: one command run with a secret from a shell as if in place. It makes the
call, copies its stdin to `<id>.in.sock` (`-stdin`) and the two output sockets to its stdout and
stderr, exits with the command's status from `<id>.exit`, and on SIGINT/SIGTERM/SIGHUP kills the
run (`secret-exec-kill`), or gives the call up where it has not started. Found and bound beside
`verse-call-tool`, built into the same images.
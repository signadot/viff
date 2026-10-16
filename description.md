# gates: what a gate on a command or a file write can honestly promise, and the kernel seams that keep it

**Status:** design, for after the cut to runnerv2. Nothing here is a condition of the cut: main
promises no gate of this kind today, so a cut without one oversells nothing. This is the shape
of the work that would let a spawn say "ask before a push, a `make ship`, or a write to
CHARTER.md" and have the runner mean it.

## The problem

A gate is spelled on what the agent SAYS it is doing, and what it says is not what happens:

- `files.write` matches the `write_file` tool. A write through the shell, `echo x > db/x.sql`,
  is a `run_bash` and matches `commands.run` only, so under `auto` with a gate on
  `files.write path: db/**` it proceeds unasked. (PR #15 review, comment 1.)
- `commands.run` matches a pattern against the whole command string. `git push*` misses
  `cd x && git push origin main`; `*git push*` is what an author must write, and even that is
  advisory, since the shell, a script, `make`, or a `subprocess` call exec whatever they like
  without the string ever saying so. (PR #15 review, comment 2.) Not fixable by matching.

Main's runner gates TOOLS (`push`, `create-pr`, `run_bash` as a whole) and says nothing finer, which
is honest and coarse. The step gates on runnerv2 say finer things and cannot keep them. Either
is admissible for the cut under the criterion "don't oversell the guarantee more than what's on
main now"; the second only with its docs saying the match is advisory.

## Two levels

1. **The ceiling:** what a task may do at all, set by the sandbox and the task's set-up. The
   only full-proof thing. Never an ask: a refusal, in-kernel, with no process to trust.
2. **The gates:** which of the permitted actions ask, and whom. An ask needs a supervisor in the
   loop, and so a seam that puts one there and cannot be stepped around.

The two want different kernel mechanisms, and both exist.

## Commands: one syscall

Every command the agent runs, through any shell, script, `make` or interpreter, reaches the
kernel as `execve` or `execveat`. That is the seam.

- **Not a prescribed shell.** Agents bring their own; `claude` and `codex` spawn whatever they
  were built with, and a runner that works only under `/bin/sh` of its choosing is a runner
  agents route around.
- **Not `LD_PRELOAD`.** A libc override reaches dynamically linked programs that call libc, so a
  Go binary the agent builds (static, raw syscalls) is invisible after its own exec, and the
  variable is dropped by `env -i` or `unset` by accident as easily as on purpose. The stronger
  form, bwrap binding a file at `/etc/ld.so.preload` inside the sandbox, cannot be unset from
  inside and still misses static binaries. Fine for a prototype, advisory as a gate.
- **seccomp user notification on `execve`/`execveat`.** A filter installed in the task's child
  before it execs the program, with `SECCOMP_FILTER_FLAG_NEW_LISTENER` (Linux 5.0), applies to
  every descendant, cannot be removed from inside, and covers static binaries and raw syscalls
  alike. The supervisor (the task process; it is already the parent under bwrap) receives the
  pid and reads argv from the tracee, decides or asks, and answers: continue, or fail the exec
  with `EACCES`, which a shell reports as "permission denied" and an agent already knows how to
  read. A gate names a pattern over argv, not over a line of shell text, so `cd x && git push`
  and a `git push` inside a Makefile are the same exec.

**Cost.** A build execs thousands of times. The filter must let everything through in-kernel
except what a gate names, and BPF cannot read argv, so EVERY exec round-trips to the supervisor
when any exec gate is set. Measured before relied on; the expectation is tens of microseconds
per exec and an ask-by-default on exec being unusable, which argues for `auto` with named gates
and against `ask` covering execs at all.

**What it does not say.** `python -c ...` is one exec whose argv says nothing about what the
code does. An exec gate sees the interpreter, not the program.

## Files: one syscall for content, a dozen for the rest

Every read and write of file CONTENT goes through the open family, `openat` and `openat2` in
practice; a shell's `>` is an `openat` with `O_WRONLY|O_CREAT|O_TRUNC`, which is exactly the hole
in comment 1. Writes that are not content have about a dozen siblings: `rename`, `unlink`,
`mkdir`, `rmdir`, `link`, `symlink`, `truncate`, `chmod`, `chown`. The list is finite and already
enumerated: it is Landlock's access-rights list.

- **The ceiling is Landlock** (Linux 5.13; `truncate` as a right from 6.2). Path-based read and
  write restriction enforced in-kernel, unprivileged, stackable under bwrap, no supervisor and
  no race. "This task writes only under its workspace" and "this task never reads
  `.verse/credential`" become refusals, which is level 1 for files done properly, and the
  containment the model key rests on today (ptrace_scope, undumpable) gets a second wall.
- **The asks are seccomp notification with `addfd`** (`SECCOMP_IOCTL_NOTIF_ADDFD`, Linux 5.9).
  The filter sends write-flagged opens to the supervisor, which resolves the path (dirfd plus
  pathname read from the tracee), decides or asks, and on approval OPENS THE FILE ITSELF and
  injects the descriptor into the task. The task never performs the open, so the race of
  checking one path and letting the process open another does not exist. A denied open is
  `EACCES`.
- **Reads are not gated.** BPF cannot filter on the path, so a read gate means every open in
  the task round-trips, and a process opens dozens of files to start. Read gates are ruled out
  on cost, which is a firmer reason than a sandbox making them pointless, and it is the same
  answer. What a task may not read is Landlock's (level 1).

## What this is, and is not

- Linux only, with kernel floors (5.9 for addfd, 5.13 for Landlock). Staging's kernels clear
  them; the pod already runs with `seccompProfile: Unconfined` for bwrap (deploy/README.md), which
  is what lets a task install its own filter. `verse up` stays unenforced, consistent with the
  decision that local does not sandbox (f4gyjepr, 2026-10-12 comment).
- It makes `commands.run` and `files.write` mean what they say, in argv and paths. It does not
  look inside an interpreter, and it does not gate reads.
- It is level 2 work with a measured cost and a kernel floor, which is why it comes after the
  cut and not before it. The agreed WHAT for the cut is narrower and already decided: a tool the
  runner cannot provide is not shown to the agent, and a launch asking for one fails early.

## Order, when it is picked up

1. Landlock under bwrap for the workspace and the credential file: a ceiling, no asks, no new
   spelling in the spawn. Smallest, and it closes a real gap on its own.
2. An exec gate over argv by seccomp notification, measured on a `go build` and a `make check`
   inside a task before the gate is offered.
3. Write asks by `addfd`, on named paths only.
4. Then the spelling: whether the spawn says steps, tools, or both, which is the gate-refactor
   question and is answerable once the three above say what can be kept.

Relates to f4gyjeprha9xr8z2shn0 (step 2c, gate matching), sjw7pv0wh12ksxvysxn0, km5mzagqh12kspdenxn0
(the coding runner's residual: the principal), PR #15 review comments 1 and 2, and PR #19's tools
contract (level 1 for tools).

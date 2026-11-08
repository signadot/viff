# pkg-nix step 0c (2b5art3x): can the runner own the task's userns and inject mounts at runtime?

The chosen direction (over 0b's dead end): the runner builds the task's user+mount namespace
itself and holds it, so it is the namespace's owner and can re-enter with capabilities to bind
store paths in while the task runs. 0c measures whether that actually yields usable capabilities,
at the syscall (not the `mount(8)` CLI, which refuses on euid != 0 before the kernel, the error
that made 0c-i inconclusive). On the staging pod, uid 65532, every capability dropped, as the
runner runs.

The task namespace is built by `clone(CLONE_NEWUSER|CLONE_NEWNS)` with the uid map written by the
runner — the real shape, not an in-process `unshare` (which a multithreaded Go runtime cannot do
for a user namespace). Two maps measured.

## Result

**Single non-root map (65532 -> 65532), the map bwrap uses so nothing in the sandbox is
privileged:**
- The namespace's creator, after `execve`-ing its program, has `CapEff 0000000000000000` and
  cannot mount a tmpfs: `operation not permitted`.
- A helper entered through `nsenter` (keeping euid 65532) likewise has `CapEff 0` and its
  `mount(2)` bind fails `operation not permitted`.

**Root-in-userns map (0 -> 65532), the map rootless containers use:**
- The creator, as uid 0 inside, has `CapEff 000001ffffffffff` and mounts the tmpfs fine.
- But a second process entering as uid 0 — standing for the task — also has
  `CapEff 000001ffffffffff` and can write the store. A root map makes everything that reaches
  uid 0 in the namespace fully privileged.

## What it means

The obstacle is **`execve`, not the kernel's willingness to grant the owner capabilities.** A
user-namespace creator (or a `setns` enterer) holds a full capability set, but the moment it
`execve`s a binary as a non-root uid with no file capabilities, the kernel zeroes the effective
set. This is why bwrap mounts fine (it never execs between `unshare` and its mounts; it does them
in-process, then execs the payload with caps dropped on purpose) and why every "the runner execs
a helper — `nsenter`, or a Go tool — to enter and bind" path fails: the helper loses its caps on
its own exec.

The two maps bracket the real constraint:
- The single non-root map keeps the task unprivileged (the point of the sandbox) but leaves no
  exec'd helper able to mount.
- The root map lets an entering helper mount, but a task that reaches uid 0 in the namespace is
  as privileged as the helper — the sandbox's isolation is gone.

**So runtime injection that keeps the task unprivileged needs an in-namespace helper that holds
capabilities without `execve`-ing as non-root: a process forked (never exec'd) from the
namespace's capable creator, kept resident to perform mounts in-process on command.** That is a
small **C namespace-bootstrap resident in every task's sandbox** — the same class of code as
runc's `nsenter` bootstrap or bwrap itself. Go cannot be it: it drops caps on exec and cannot
in-process `setns` a user namespace from a multithreaded runtime.

## The decision, now measured rather than assumed

The userns-ownership rework is feasible, and its honest cost is: verse ships and maintains a
privileged C namespace-bootstrap in the runner, resident inside each task's sandbox, as a
security-critical part of the trusted base that isolates untrusted agent code. That is a large
step beyond "rework `Sandbox.Wrap`."

Weighed against **whole-store-read-only** (the LRU-cache shape: whole store bound read-only at
`/nix/store` at start, runtime installs by the runner realising into the store and writing a
profile symlink through a bound directory — no capabilities, no C, no sandbox-TCB change, every
piece already proven in step 0), whose only cost is that a task can `ls /nix/store` and see the
union of realised paths (but runs only what its profile `PATH` names, nix's hashing keeping
versions from colliding) — the measurement argues for the fallback, unless per-task store
invisibility is worth a resident privileged bootstrap in the sandbox.

## Artifacts

`nstest/main.go` (the harness), run on the step-0 pod. Pod still up:
`kubectl --context my-cluster.example.ts.net delete namespace pkgnix-try` removes it.

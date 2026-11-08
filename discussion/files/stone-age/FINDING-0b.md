# pkg-nix step 0b (2b5art3x): can the runner add store paths to a running task's sandbox?

The runtime-install shape needs the runner to bind store paths into a task's sandbox *after*
the task's process has started, with the store reachable by no path the task holds. The only
way to add a mount to a running process's mount namespace is to be in that namespace with
`CAP_SYS_ADMIN`. The question: can the runner (uid 65532, every capability dropped, as it runs)
get there, given the task's sandbox is a bwrap the runner started?

## Measured, on the staging pod (the step-0 pod, same settings)

A real task sandbox: bwrap 0.13.1, `--unshare-pid --unshare-ipc --unshare-uts --unshare-net`,
the runner's state bound at `/.verse/state`, an empty ro `/nix/store`, a profile dir on `PATH`.
The runner then tried to enter the sandbox's namespaces to add binds.

**The runner cannot enter the sandbox's namespaces with capabilities.**
- bwrap's child's uid_map is `65532 65532 1`: a single identity uid, no in-namespace root.
- The runner joining that user namespace comes in with `CapEff 0000000000000000` — no
  capabilities. So the user that *created* the namespace holds the caps, and that is bwrap's
  forked child, not the runner; the runner is only a same-uid process in the parent, and the
  kernel grants it nothing on entry.
- With no caps in the user namespace, joining the mount namespace fails
  (`reassociate to namespace 'ns/mnt' failed: Operation not permitted`), so no bind can be
  added. Entering bwrap's own pid gives `ns/user ... Invalid argument` (already its own userns).

**Conclusion: runtime injection into a bwrap-made sandbox does not work as the runner runs
today.** The task-isolated runtime install (each task sees only what it installed, installs
mid-run) is not reachable by the runner stepping into the sandbox.

## The one path that could still give task isolation at runtime

For the runner to hold capabilities in the sandbox's user namespace, the runner must be the
namespace's **owner**: it would create the user (and mount) namespace itself and have bwrap
*reuse* it (bwrap `--userns <fd>`), rather than let bwrap create its own. Then the runner, as
creator, can re-enter with full capabilities and bind. This is a real change to how
`Sandbox.Wrap` builds every sandbox (not just pkg-nix's), it changes who owns the task's
namespaces, and it needs its own measurement before anything is planned on it. Not done here.

## The decision this forces

Runtime, per-task-isolated installs are either (a) that userns-ownership rework, measured
first, or (b) given up. If given up, the runtime-install shape falls back to **the whole store
bound read-only** at `/nix/store` (every task sees the union of realised paths; a task runs only
what its own profile `PATH` names; the store is an LRU cache no task reads by name) — which needs
no capabilities and is what step 0 already proved works. Or the design stays with the
**provided-at-start** shape (bind the task's own closure once, before the process starts, as the
current plan and step 0 measured), and drops mid-run installs.

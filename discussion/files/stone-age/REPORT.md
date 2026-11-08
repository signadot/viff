# pkg (the stone-age model): measurements behind the issue

Run on the staging pod of 2b5art3x's step 0 (the runner's image and pod settings, bwrap 0.13.1
built from source, a gp3 claim for the runner's state), uid 65532, every capability dropped.

## Why 2b5art3x's shape was dropped (`FINDING-0b.md`, `FINDING-0c.md`)

2b5art3x binds each nix store path as its own mount, so installing a tool mid-run means adding a
mount to a running sandbox, which needs `CAP_SYS_ADMIN` in the task's user namespace.

- **0b (`p9-nsenter.sh`):** the runner cannot enter a bwrap-made sandbox's namespaces with
  capabilities. bwrap creates the user namespace in a forked child, so the runner is only a
  same-uid process alongside it and enters with `CapEff 0`; the mount namespace will not join.
- **0c (`p10-owner.sh`, `src/nstest.go`):** even when the runner builds the namespace itself, a
  non-root uid loses every capability on `execve` — so an exec'd helper (nsenter, or a Go tool)
  cannot mount. A root-in-userns map restores the helper's caps but also privileges the task. The
  only form that keeps the task unprivileged is a resident privileged C bootstrap in every
  sandbox (runc/bwrap class). Rejected.

## The model that works, no capabilities (`p11-robind.sh`, `p12-brew.sh`)

- **`p11`:** a runner-owned directory bound read-only into the task once at start; the runner then
  writes new tools into it from its own side. The task sees them appear through the read-only bind
  (one mount, made at start), and still cannot write. No capability, no new mount.
- **`p12`:** a working Debian example, non-root. An install toolchain (dpkg-deb, xz, patchelf;
  here from nix, standing for what the runner image bakes in) fetches a package and its library
  dependencies as `.debs` (curl), extracts them (`dpkg-deb -x`, no maintainer scripts), assembles
  a self-contained versioned prefix, and sets `DT_RUNPATH` to `$ORIGIN/../lib` on each binary and
  `$ORIGIN` on each bundled library (needed: `DT_RUNPATH` does not reach a transitive dependency).
  The prefix runs with no `LD_LIBRARY_PATH`. Materialised into a task's read-only root under two
  names (`jq`, `jq-v2`, the install-name override), `echo '{"a":1}' | jq .a` prints `1` inside the
  sandbox; the task cannot write the root and cannot see the cellar or other tasks.

## Files

`in/` the scripts, `logs/` their output, `src/` the Go harnesses (nstest for 0c, mounter for the
0b agent probe), `FINDING-0b.md` and `FINDING-0c.md` the capability findings.

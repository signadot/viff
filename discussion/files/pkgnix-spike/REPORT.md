# pkg-nix feasibility spike: findings

Proposal: git-issue `1f5art3x`. Everything ran in local Docker on this Mac: Docker Desktop 29.6.1,
linuxkit kernel 6.12.76, aarch64. No cluster, registry, git push or git-issue push was touched.

## Verdict

**Feasible, with a newer bwrap than the image ships.**

- **Nix as uid 65532:** nix 2.35.4 works with no root, no daemon and no capability. It pulls real
  aarch64 toolchains from cache.nixos.org without building:
  - nodejs_22: 32 store paths, 209 MiB, 11.4 s from an empty store;
  - go: 9 paths, 266 MiB, 3.1 s.
- **Building can be turned off.** `max-jobs = 0` makes an uncached derivation fail with "local
  builds are disabled".
  - You also need `always-allow-substitutes = true`. nodejs_22's top level is a wrapper marked
    `allowSubstitutes = false`, and so is every `buildEnv`/profile. Under `max-jobs = 0` alone it
    fails even though the cache has it.
  - So the runner should build `PATH` itself from the closures' `bin` directories, not have nix
    make a profile. A profile is a build.
- **One `--ro-bind` per store path works, and the programs run from the binds.** Two blockers,
  both in bwrap rather than the kernel:
  - **bwrap stops at 9000 arguments.** That holds in every version up to 0.13.1, and through
    `--args <fd>` too. With the runner's flags that is about 2,986 store paths, so 3,000 already
    fails: `Exceeded maximum number of arguments 9000`.
  - **bookworm's 0.8.0 (and trixie's 0.12.0) get quadratically slower** with each path: 0.19 s at
    471 paths, 0.75 s at 1,000, 6.1 s at 2,980.
  - bwrap 0.13.x is linear: 8 / 14 / 32 ms at those sizes. With the 9000 limit patched out, it
    handled 10,000 paths in 110 ms and 30,471 in 324 ms. 50,000 mounts were still under the
    kernel's limit of 100,000.
- **Option 1, hard links:** links made after the sandbox starts show up inside it, and the task
  can't hard-link out ("Invalid cross-device link"). The hazard is that the store's files are
  owned by the task's own uid.
- **Option 3, `--store`:** substitutes from cache.nixos.org at identical hashes, needing no user
  namespace.
- **Option 4, overlay:** works unprivileged, and nix inside the task substitutes into its own
  layer. It needs bwrap ≥0.9, not ≥0.8 as the brief said: 0.8.0 answers "Unknown option
  --overlay-src". Under this option a task can always switch building back on, because
  single-user nix obeys whoever runs it.
- **GC roots:** per-task roots work. A task that is idle and has no root loses its bound paths to
  garbage collection.

## Setup and how it differs from the pod

**Images**

- `pkgnix-spike:local` (`Dockerfile`, `build.sh`): bookworm-slim, bubblewrap 0.8.0, uid/gid
  65532. `/nix` is created and chowned to 65532 as root at build time; then 65532 installs nix
  2.35.4 with the official `--no-daemon` installer.
- `pkgnix-spike:bwrapnew` (`build-bwrap-new.sh`, `bwrap-new/Dockerfile`): adds bwrap 0.13.1 built
  from source as `bwrap-new`, and the same build with `MAX_ARGS` raised from 9000 to 200000 as
  `bwrap-big`.
- `pkgnix-spike:trixie`: trixie-slim with its packaged bwrap 0.12.0.

**Container options** (`common.sh`): `--user 65532 --cap-drop ALL --security-opt no-new-privileges
--security-opt seccomp=unconfined --security-opt systempaths=unconfined`. `/nix` and the home
directory are named volumes (ext4). Scripts are piped in on stdin, because `/private/tmp` is not a
Docker Desktop file share. Each `NN-*.sh` runs `in/NN-*.sh` in the container and writes
`logs/NN-*.log`.

**Q0: what plain bwrap needs** (`00-bwrap-probe.sh`). These are exactly `Wrap`'s flags plus
`--unshare-net`, and the results reproduce both of the pod's documented findings:

- Docker's default seccomp: "No permissions to create new namespace…".
- `seccomp=unconfined` alone: "Can't mount proc on /newroot/proc: Operation not permitted".
- `seccomp=unconfined` plus `systempaths=unconfined` (Docker's counterpart of
  `procMount: Unmasked`) works. Inside: uid 65532, CapEff 0, NoNewPrivs 1.
- `apparmor=unconfined` changes nothing; this VM has no AppArmor. Nothing privileged was needed.

**Differences from the real pod**

- The kernel is linuxkit, not the node's.
- There is no AppArmor or SELinux.
- `hostUsers: false` is not reproduced: the container runs in the initial user namespace, so here
  bwrap's user namespace is one level shallower than in the pod.
- `systempaths=unconfined` is close to, but not the same as, `procMount: Unmasked`.
- Storage is local ext4, not a PVC.
- Downloads go over a home connection, so their times are only indicative.
- Kernel settings here: `max_user_namespaces` 49796, `fs.mount-max` 100000.

## Q1: single-user nix as 65532 (01, 01b, 01c, 01d)

It works: nix 2.35.4, CapEff 0, no daemon, `/nix` owned by 65532. The installer needs nothing but
a writable `/nix`; `sandbox = false` silences its sandbox warning. nixpkgs used:
`4fe25e12eb9f4c744966dd3de5a05246cf098601` (nixpkgs-unstable).

**Clean-store timings** (01d: fresh volume, `max-jobs = 0`, `always-allow-substitutes = true`)

| Step | Time | Store paths | Closure size (`nix path-info -S`) |
|---|---|---|---|
| Fetch nixpkgs and evaluate `nodejs_22.outPath` | 21.0 s | – | – |
| Evaluate `go.outPath` (source already fetched) | 0.18 s | – | – |
| Substitute nodejs_22 | 11.4 s | 32 | 209 MiB (230 MB on disk) |
| Substitute go (4 of its 9 paths new after node) | 3.1 s | 9 | 266 MiB (305 MB on disk) |
| `nix-store --realise` of go's output path after a GC (no flake evaluation) | 4.1 s | 9 | – |

**Other closures**, substituted from a warm store:

| Closure | Store paths | Size |
|---|---|---|
| python3 3.14 | 23 | 229 MiB |
| git | 87 | 404 MiB |
| gcc wrapper | 27 | 391 MiB |
| postgresql | 40 | – |
| imagemagick | 89 | – |
| ffmpeg-full | 369 | about 1.6 GB |
| rustc | 15 | – |
| cargo | 26 | – |
| jq | 8 | – |

Together they come to 471 distinct paths and a 4.3 GB store.

**Key finding:** `nix build …#nodejs_22 --option max-jobs 0` fails with `Cannot build
'…nodejs-22.23.4.drv'. Reason: local builds are disabled (max-jobs = 0)`, although the output is in
cache.nixos.org. The derivation has `allowSubstitutes = ""` and `preferLocalBuild = "1"`. Adding
`--option always-allow-substitutes true` fixes it. Every `buildEnv`, `symlinkJoin` and
`runCommandLocal` carries the same attributes, so a profile composed for a task can't be realised
without a build unless a trusted cache already holds that exact profile.

**Option 3, `--store`**

- `nix build --store /home/nonroot/s3 …#go` substitutes in 4.1 s, at the same path
  `/nix/store/k2vfr5…-go-1.26.10`. Physically it lands in `s3/nix/store`, with its own database.
- It needs no capability and no user namespace: it worked identically in a default-seccomp
  container where `unshare -U` fails with EPERM.
- Running a program from it needs something to mount it at `/nix/store`.
  `bwrap --ro-bind s3/nix/store /nix/store` works (`go1.26.10`), and `nix shell --store` works
  too. In the container without user namespaces, both fail: "setting up a private mount
  namespace: Operation not permitted".
- The alternative store held 417 entries for a 9-path closure. Evaluation writes go's build-graph
  `.drv` files into it; GC collects them.

Confidence: high.

## Q2: building is off (02-nobuild.sh)

The test used a uniquely named `derivation { builder = "/bin/sh"; … }`, so no cache can have it.

| Settings | Result |
|---|---|
| `max-jobs 0` (`builders` defaults to `@/etc/nix/machines`, which is absent) | Fails: "local builds are disabled (max-jobs = 0)" |
| `max-jobs 0` plus `builders ""` | Fails, same message |
| `--offline` alone | **Builds** (rc 0) |
| `max-jobs 0` plus `--offline` | Fails |
| `max-jobs = 0` in a `NIX_CONF_DIR` config | Fails |
| Same config, caller passes `--option max-jobs 1` | **Builds** |
| `nix-store --realise` of the uncached `.drv` | Fails |
| A cached package (jq) under the same config | Substitutes fine |

Single-user nix has no "trusted users", so any caller can override any setting. Building is
enforceably off only where the task never runs nix itself (options 1–3); under option 4 the task
can always build. Evaluation-time fetches are not builds and are not stopped by these settings.
Confidence: high.

## Q3: one bind per store path (03, 03a, 03b, 03c, 03d)

The setup:

- the 40 base arguments from `Wrap`, plus `--unshare-net`;
- the 471 real paths first, then synthetic directories under `/nix/synth` to reach larger counts;
- the command `/usr/bin/true`, timed as the median of 5–7 runs.

| Binds | 0.8.0 (bookworm) | 0.12.0 (trixie) | 0.13.1 | 0.13.1, `MAX_ARGS` patched |
|---|---|---|---|---|
| 0 | 2–3 ms | | | |
| 32 (node) | 4–5 ms | | | |
| 471 (real union) | 185–190 ms | 193 ms | 9 ms | 8 ms |
| 1000 | about 750 ms | 742 ms | 14 ms | |
| 2000 | 2.8 s | 2.9 s | 24 ms | |
| 2980 | 6.1 s | 6.2 s | 32 ms | 37 ms |
| 2990 / 3000 | **Fails**: "Exceeded maximum number of arguments 9000" | Fails | Fails | 33 ms |
| 10000 | Fails | | Fails | 110 ms |
| 20000 | Fails; passed directly in argv: "Argument list too long" | | | 218 ms |
| 30471 | Fails | | | 324 ms |

- **argv:** binds passed directly in argv are fine up to about 10,000 (1.1 MB). At 20,000 (2.2 MB)
  the exec fails with E2BIG. `--args <fd>` avoids the exec limit, but bwrap's `MAX_ARGS = 9000`
  (`bubblewrap.c:1771`, unchanged in 0.13.1) counts the arguments it reads from `--args` as well.
  With today's flags the ceiling is (9000 − 40) / 3 ≈ 2,986 binds.
- **Why it is quadratic:** up to 0.12, bwrap re-parses the whole of `/proc/self/mountinfo` after
  every bind. 0.13.0 (released 2026-09-22) uses `mount_setattr` on kernels ≥5.12 and is linear,
  about 11 ms per 1,000 binds here. Read-only and writable binds cost the same.
- **Kernel limit:** 50,000 mounts in one sandbox worked (498 ms). `fs.mount-max` (100,000) was not
  reached, because the patched build only allowed about 66,000 binds. No other kernel limit was
  hit.
- **The programs run** (471 binds, 0.8.0):
  - `ls /nix/store` shows 471 entries; the task sees only its closure.
  - By store path, `node --version` gives v22.23.4 and `go version` gives go1.26.10 linux/arm64.
  - With a `PATH` of the 138 `bin` directories (8,864 bytes), all of these ran: node, go, python3
    with ssl, git, ffmpeg, psql, rustc, cargo, jq and magick.
  - `go run` of a hello-world worked.
  - At 10,000 binds (patched 0.13.1), node and go still run, with 10,021 mounts in the sandbox.
- **Each store bind must be read-only:**
  - With `--bind` instead of `--ro-bind`, the task ran `chmod u+w` on the node path and **wrote
    into the shared store** (same uid). With `--ro-bind` the same attempt fails: "Read-only file
    system".
  - `--tmpfs /nix/store`, then plain `--bind`s, then `--remount-ro /nix/store` does not help: 494
    writable mounts remain.
  - The task can also create entries in its own root tmpfs (`touch /nix/store/planted`
    succeeded). Adding `--remount-ro /` at the end stops that, and the workspace stays writable.

Confidence: high for the argument ceiling and the 0.8/0.12 quadratic cost, since both are in
bwrap's code. Medium for the absolute 0.13 times and for the mount ceiling under
`hostUsers: false`.

## Q4: hard links, option 1 (04-hardlinks.sh)

The setup:

- the per-task directory `T = /nix/tasks/t1`, on the store's filesystem;
- in the sandbox, `--bind T /nix/store`;
- then a `--ro-bind` for each of go's 9 paths;
- then `--remount-ro /nix/store`.

Results:

- **Binding paths over T and then making it read-only works.** bwrap leaves the 9 mountpoints as
  empty directories in T on the host, which the runner has to clean up.
- **Links made after the start appear inside.** The runner ran `cp -al` for the 4 jq paths not in
  the base (6 files, 19 directories, 23 ms). Inside, `/nix/store` went from 9 to 13 entries and
  `jq --version` ran. Hard links exist only for files, so linking a store path means copying its
  directory tree with linked files; the cost is per file and directory, not per path.
- **The task cannot link out:**
  - `ln` from the store to the workspace gives "Invalid cross-device link", even with the
    workspace on the same filesystem, because it is a different mount;
  - `ln` inside `/nix/store` gives "Read-only file system";
  - `chmod` on a linked file is also refused;
  - a symlink into the workspace is allowed, and only points at the read-only view.
- **The runner can make the links:** it owns every inode, and `protected_hardlinks=1` doesn't
  restrict an owner.
- **The hazard: every link is a store inode owned by the task's uid.**
  - A link the runner makes in any writable directory on `/nix`, then `chmod u+w`, changed the
    store file to mode 755.
  - **This spike's first cleanup did exactly that by accident.** `chmod -R u+w T` made the store
    files of jq's closure writable. They were restored by hand, and `nix-store --verify-path`
    passed.
  - Correct removal: `find T -type d -exec chmod u+w {} +; rm -rf T`. A plain `rm -rf T` fails
    with "Permission denied".
- Not tested: `nix-store --optimise` and `auto-optimise-store` with links present.

Confidence: high.

## Q5: overlay, option 4 (05-overlay.sh)

**bwrap 0.8.0 can't do it:** "Unknown option --overlay-src". The overlay options arrived in 0.9.0.

**With 0.13.1 it works, unprivileged.** The setup:

- lower: the option-3 store `s3/nix`, with nix itself copied in via `nix copy --no-check-sigs`
  (the installer's paths are unsigned);
- upper and work directories per task;
- the bwrap argument: `--overlay-src s3/nix --overlay upper work /nix`.

Inside the sandbox:

- `/nix` is an overlay;
- the base's go runs;
- `nix-store --option max-jobs 0 --realise <ripgrep>` substituted from cache.nixos.org into the
  task's own layer;
- `rg --version` gives ripgrep 15.2.0, and `nix path-info` sees both the base's paths and the
  task's.

The whole sandbox run took 0.7–1.1 s. Afterwards:

- the upper held 20 MB: ripgrep, pcre2, gcc-lib, libgcc, and a go directory copied up by the
  task's own `chmod` (which succeeded, in its own layer);
- the database had been copied up (352 KB);
- the base was unchanged.

`--tmp-overlay /nix` (upper on tmpfs, gone at exit) also works. With `--unshare-net` the install
fails cleanly ("no substituter that can build it"), so the network really is the bound.

Not tested:

- a lower that changes while overlays sit on it (the kernel documents this as undefined);
- two tasks at once over one lower;
- an upper on PVC filesystems;
- building inside the sandbox.

Confidence: high that bwrap ≥0.9 does this on a 6.x kernel; medium for the pod (one more
user-namespace level, and the upper's filesystem).

## Q6: GC roots (06-gcroots.sh, run last because it empties the store)

`nix-store --add-root /home/nonroot/roots/task-1-go --indirect --realise <go>` works as 65532 and
registers the root under `gcroots/auto`. `nix-store --gc` then deleted 4,639 paths (3.7 GiB) in
2.2 s.

| Path | How it was held | After GC |
|---|---|---|
| go (9 paths) | Per-task root | Kept, all 9 |
| jq-bin's closure | Running inside bwrap, no root | Kept, via `/proc/<pid>/exe` and `/proc/<pid>/maps`. The runner sees sandboxed processes, so runtime roots work under bwrap. |
| imagemagick | Nothing | Dropped |
| postgresql | Bound into an idle sandbox, no root | Dropped. Inside, the 39 bind mounts remained but `psql` gave "No such file or directory". |

Removing go's root and collecting again dropped go. So a live task must have a root; runtime roots
only cover what is mapped or executing at the moment of the GC. Confidence: high.

## What this does not settle

- **The real pod.** Not tested: the node's kernel, the extra user namespace from
  `hostUsers: false`, `procMount: Unmasked` versus Docker's `systempaths`, SELinux on
  Bottlerocket, and the PVC's filesystem (hard links, the overlay upper, sqlite locking). The bwrap
  conclusions come from bwrap's own code and should carry over; the absolute times and the overlay
  result should be re-measured there.
- **Vendoring bwrap.** Whether shipping a vendored bwrap ≥0.13 with a patched `MAX_ARGS` is
  acceptable. Upstream was not asked.
- **Splitting the binds.** Nested bwrap, or any other way to stay under 9000 arguments, was not
  tried.
- **Concurrency on one single-user store** (substituting while GC runs, several pods on one PVC)
  was not tested.
- **Private caches and flakes.** Caches with tokens, private flakes, and flakes other than nixpkgs
  were not tested.
- **Throughput under real load.** Substitution speed from inside the cluster or through the egress
  proxy was not measured, nor was disk growth from `.drv` files and flake sources.
- **Scoping.** Under every option the ceiling still bounds what a task asks for, not its whole
  closure.

## What a plan issue for pkg-nix should take from this

1. **bwrap:** the image needs bwrap ≥0.13, not bookworm's 0.8.0. Above about 2,900 paths it needs
   `MAX_ARGS` raised, through a vendored patch (tested to 30,000 binds) or an upstream change.
   Pass binds via `--args <fd>`. The `Bwrap()` probe should check the version and refuse a closure
   past the ceiling with a clear error.
2. **Read-only:** every store bind is `--ro-bind`, plus `--remount-ro /` once the binds are made.
3. **The runner's nix config**, used only where the runner alone runs nix: `max-jobs = 0`, empty
   `builders`, `always-allow-substitutes = true`, `allow-import-from-derivation = false`,
   `require-sigs = true`, and the operator's `substituters` and `trusted-public-keys`. Build `PATH`
   from `bin` directories rather than a nix profile.
4. **Resolve once, substitute by store path:** evaluate a flake to locked store paths (about 21 s
   cold, 0.2 s warm), then `nix-store --realise` them.
5. **GC roots:** one per live task and one per base, made before the sandbox starts and removed
   after it.
6. **If option 1 (links):**
   - T on the store's filesystem;
   - clean up the mountpoints bwrap leaves in T;
   - expect a cost per file;
   - remove T by `chmod`-ing directories only;
   - note that store and task share a uid.
7. **If option 4 (overlay):**
   - bwrap ≥0.9;
   - a frozen or versioned lower;
   - an upper on a filesystem overlayfs supports;
   - builds can only be starved by the network, not forbidden;
   - the lower must be a store with its own database (option-3 style), not the runner's `/nix`.
8. **Option 3** costs nothing extra to substitute, and can serve as option 4's base store.
9. **Re-run in a staging pod:** scripts 00, 03b/03c, 05 and 06, on the node's kernel and the real
   PVC.

## Files

All in this directory:

- `Dockerfile`, `build.sh`, `build-bwrap-new.sh`, `common.sh`
- `bwrap-new/Dockerfile` and `trixie/Dockerfile` for the other two images
- experiments `00-…` to `06-…`, each with its in-container half under `in/` and its output under
  `logs/`

To re-run, go in this order: `build.sh`, `build-bwrap-new.sh`, then 00, 01, 01b, 01c, 02, 03a, 03,
03b, 03c, 03d, 04, 05, 06. 01d is independent. To get the 0.13.1 run of 03b:
`IMG=pkgnix-spike:bwrapnew C=pkgnix-pod-new ./03b-bind-shapes.sh /usr/local/bin/bwrap-new`.

All containers and the named volumes have been removed, so a re-run starts from 01. The three
`pkgnix-spike` images (`local`, `bwrapnew`, `trixie`) remain.

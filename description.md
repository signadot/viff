# pkg-nix: packages from nix, provided when a task starts, read-only, one bind per store path

**Status:** a plan, not yet reviewed. Nothing is built. It blocks 1f5art3x. It rests on a
feasibility spike run in local Docker (the findings are summarized under "What the spike
measured"); step 0 re-runs the parts of it that only the pod can settle.

## What it is

The `pkg-nix` system, as 1f5art3x describes it, at its first version: **packages the runner's
image does not carry, provided when the task starts.** Each doer has a base, and a spawn may add
one flake. The runner realises both into its store before the task's process starts, and binds
their closures into the sandbox, read-only. The packages are fixed for the run. The environment
and the ceiling bound them; no row decides them, as no row decides a checkout.

- **Provided at the start,** a kind of system the model has not had: no verb, no step, no op.
- **Substitution only.** The runner fetches from caches the operator trusts, and builds nothing.
- **A task sees its own closure and no other's.** One `--ro-bind` per store path, never the
  store whole.
- **Installing during a run is not in this issue.** 1f5art3x's open question stands; the spike
  narrowed it (below).

## The shape

```tony
# the runner's configuration
nix:
  store: /state/nix                       # the runner's store, on a volume that outlives the pod
  substituters: ["https://cache.nixos.org"]
  trusted-public-keys: ["cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="]
agents:
  claude:
    systems:
      pkg-nix:
        base: "github:acme/tools/<rev>#base"          # every claude task starts with it
        ceiling: {flake: !regexp "^github:acme/"}

# a spawn's environment
environment:
  systems:
    pkg-nix: {flake: "github:acme/tools/<rev>#ci"}
```

- **`flake` is one attribute,** a scalar, as a block's attributes are (`readBlock`). The list
  of packages lives in the flake.
- **A reference is remote.** The ceiling's pattern admits none that is local (`path:`, `.`,
  `/`, `file:`), and the runner refuses one whatever the pattern says: given a local path, it
  would copy a task's files into a store other tasks draw on.
- **What the task's `PATH` names:** the flake's `bin` directories, then the base's, then the
  doer's `PATH` as today. The runner builds it from the closures; it does not ask nix for a
  profile, since a profile is a build (below).

## What the spike measured

Local Docker (Docker Desktop, linuxkit 6.12, aarch64), as uid 65532 with no capabilities,
`seccomp=unconfined` and `systempaths=unconfined` (the counterparts of the pod's settings). Not
the pod: its kernel, `hostUsers: false`, Bottlerocket's SELinux and a PVC were not reproduced.

- **Single-user nix as 65532 works,** with no root, no daemon and no capability: nix 2.35.4 with
  `/nix` owned by 65532. nodejs_22 is 32 paths and 209 MiB, substituted in 11.4 s into an empty
  store; go is 9 paths in 3.1 s. Evaluating nixpkgs cold took 21 s, warm 0.2 s.
- **Building is off only with two settings together:** `max-jobs = 0` and
  `always-allow-substitutes = true`. With the first alone, nodejs_22 fails though the cache has
  it: its top-level wrapper, and every `buildEnv` and `symlinkJoin`, is marked
  `allowSubstitutes = false`. So the runner composes no profile.
- **Single-user nix obeys whoever runs it.** A caller's `--option max-jobs 1` builds. So
  building is off only where the task never runs nix itself, which is this issue's shape.
- **One `--ro-bind` per store path works,** and the programs run from the binds: with 471 paths,
  node, go, python3, git, ffmpeg, psql, rustc, cargo and jq ran, by store path and by a `PATH` of
  138 `bin` directories. The task's `/nix/store` lists its closure alone.
- **bwrap, not the kernel, is the limit.**
  - bookworm's 0.8.0, and trixie's 0.12.0, re-read the mount table after every bind: 0.19 s at
    471 binds, 0.75 s at 1,000, 6.1 s at 2,980. 0.13 (`mount_setattr`) is linear: 9, 14 and
    32 ms.
  - Every version stops at 9000 arguments, `--args <fd>` included: about 2,986 binds with the
    runner's flags. A build with the limit raised took 30,471 binds in 324 ms. 50,000 mounts in
    one sandbox were under the kernel's limit.
- **Every store bind must be read-only,** and the root remounted read-only after the binds: with
  `--bind`, the task (same uid) wrote into the shared store; without `--remount-ro /`, it could
  create entries in its own `/nix/store`.
- **GC roots:** a per-task root (`nix-store --add-root --indirect`) keeps its closure through a
  collection; an idle task with no root loses its bound paths, though the binds remain.

## The plan

Each step builds and passes on its own.

0. **The spike in a staging pod.** Its scripts 00 (what bwrap needs), 03b/03c (the binds at
   size), 06 (GC roots), on the node's kernel, under `hostUsers: false`, with the store on the
   PVC. What it finds is said here before step 3 is built.
1. **The document.** `pkg/systems/documents/pkg-nix.tony`: the attribute `flake` (required,
   form `text`), no verb. **The model's first system with no verb:** `Environment.Gives`,
   `Ceiling.Check`, `Describe` and the offer's `held` read a block of it as given; no step is
   claimed in `pkg/task/step.go`. The document says its kind, `provided` (shoring item 6 of
   1f5art3x), and the description publishes it, so a charter's author reads that no row decides
   it. `docs/systems/pkg-nix.md`.
2. **The configuration.** `nix: {store, substituters, trusted-public-keys}` in `stock.Config`,
   and `systems.pkg-nix: {base, ceiling}` per doer. `Config.Check` takes:
   - a store that is absolute, and none the existing kept-directory checks refuse (it is the
     runner's: no `readOnly` path reaches it, no sandbox binds it whole);
   - `https` substituters, each with a key;
   - a base and every ceiling pattern that admit no local reference.

   `performs`/`held` gain the provided kind: `pkg-nix` is a doer's where nix and a store are
   there, and closed, and said, where not, as `secret` is under `-sandbox none`.
3. **The image.** bwrap 0.13 built from source in `deploy/verse-runner/Dockerfile` (no Debian
   release ships it); nix installed single-user for uid 65532, with `/nix` prepared at build
   time. The runner's `Bwrap()` probe reads bwrap's version and refuses `pkg-nix` below 0.13,
   saying why. The deployment gains a volume for the store (`deploy/build.tony` has PVC knobs
   for docd alone today).
4. **The runner's nix.** One `nix.conf` the runner writes and nothing else reads: `max-jobs = 0`,
   `builders =`, `always-allow-substitutes = true`, `allow-import-from-derivation = false`,
   `require-sigs = true`, `sandbox = false`, and the operator's substituters and keys. Run with
   `NIX_CONF_DIR` naming it, and `NIX_USER_CONF_FILES` empty.
5. **Resolve, realise, root.** At a launch that gives `pkg-nix`, before the task's process
   starts:
   - evaluate the base and the flake to their output paths (`nix eval … outPath`), cached by the
     locked reference, so a warm launch pays 0.2 s and not 21 s;
   - realise them (`nix-store --realise`), substituting and never building;
   - add a root for the task, and one for each base, under the runner's own roots directory;
   - read each closure (`nix-store -qR`) into the list of paths to bind.

   The task's root is removed when the task ends, and a base's when no doer names it. A failure
   fails the launch, naming the reference and nix's words, before any process starts.
6. **The sandbox.** `Sandbox.Wrap` takes the store paths beside its programs:
   - one `--ro-bind p p` each, passed with `--args <fd>` rather than on the command line;
   - `--remount-ro /` after the binds, so the task writes its workspace and `/tmp` and nothing
     else;
   - a closure past what bwrap takes (its 9000 arguments, less the runner's own) refuses the
     launch, saying how many paths and the bound.

   `PATH` is built from the flake's `bin` directories, then the base's, then the doer's.
7. **`secret-exec`.** `Confiner` counts a program under the task's closure as one the runner
   keeps, since the runner chose and bound it read-only; the command's sandbox has the same
   binds and `PATH` as the task's.
8. **The words.** `docs/systems/pkg-nix.md`, `docs/reference/runnerd.md` (the `nix` section, the
   doer's `pkg-nix`, the store's volume), the row in `docs/systems/README.md`, and the deploy
   manifest's example. They say:
   - a flake is evaluated on the runner's side, so the ceiling's pattern is a list of sources the
     operator trusts to evaluate, not only to run;
   - a ceiling bounds what a task names, and the task gets the whole closure of it;
   - the store is single-user and the runner's: a task never runs nix against it.

## Tests

- **The configuration.** A local reference in a base, in a ceiling's pattern, and in a spawn
  that a pattern would admit: each refused. A substituter with no key: refused.
- **The document.** `TestTheRunnerImplementsTheDocumentedSystems` holds a system with no verb;
  an environment with a `pkg-nix` block gives no step and asks no row.
- **The sandbox's arguments.** For a list of paths: one `--ro-bind` each, on the `--args` fd,
  `--remount-ro /` after the binds, the workspace still writable, and a list past the bound
  refused. `PATH`'s order.
- **The launch, against a fake nix** (a script that answers `eval`, `--realise`, `-qR` and
  `--add-root` from fixtures): the paths bound are the closure's, the roots are added before
  the process and removed after, and a failure fails the launch before any process.
- **`secret-exec`** admits a program under the closure and refuses one beside it.
- **End to end, where nix and bwrap 0.13 are there** (skipped otherwise, and said): a flake
  realised from a cache, its program run in a task, the store unwritable from inside.

## Not in this issue

- **Installing during a run.** The spike tested two of 1f5art3x's four ways:
  - **1, links into the task's own directory:** links made after the start appear inside, and
    the task cannot link out ("Invalid cross-device link"). But the store's files and the task
    share a uid, so the runner's every `chmod` near a link is a write to the store. The spike's
    own cleanup did that once, by accident.
  - **4, an overlay `/nix`:** works unprivileged with bwrap ≥0.9, and nix inside the task
    installs into its own layer. Building cannot be forbidden there; only the network bounds it.

  Which, if either, is the next issue's.
- **A private cache's token,** and a private flake's host credential, from the secret store.
  Bound by the operator, as 1f5art3x says; untested by the spike.
- **More than about 2,900 store paths.** A bwrap with its limit raised (a vendored patch, or
  upstream), or nested sandboxes. Refused until then.
- **A build.** Substitution only.

## Open, for review

1. **The name.** `pkg-nix`, or `nix-pkg` as `linear-issue` and `github-issue` put the provider
   first (1f5art3x's open question 2).
2. **bwrap from source.** Building 0.13 in the image is a maintenance cost of its own; the
   alternative is a newer base image whenever one ships it.
3. **The store's volume.** Its size, its class, and what happens to a store on a node the pod
   moves off.
4. **The launch's latency.** A cold evaluation is 21 s; whether the runner evaluates the bases
   at its start, ahead of any launch.
5. **Concurrency on one store.** Two launches substituting at once, and a collection while one
   does. Untested.
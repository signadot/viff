# pkg-nix: packages from nix, provided when a task starts, read-only, one bind per store path

**Status:** a plan, reviewed once (2026-11-07, the review is the first comment) and revised for
it. Nothing is built. It blocks 1f5art3x. It rests on a feasibility spike run in local Docker
(the findings are summarized under "What the spike measured"); step 0 re-runs the parts of it
that only the pod can settle, and tests what the review added.

## What it is

The `pkg-nix` system, as 1f5art3x describes it, at its first version: **packages the runner's
image does not carry, provided when the task starts.** Each doer has a base, and a spawn may add
one flake. The runner realises both into its store before the task's process starts, and binds
their closures into the sandbox, read-only. The packages are fixed for the run. The environment
and the ceiling bound them; no row decides them, as no row decides a checkout.

- **Provided at the start,** a kind of system the model has not had: no verb, no step, no op.
- **Substitution only.** The runner fetches from caches the operator trusts, and builds nothing.
- **Unmodified packages only, at first.** With no build and no private cache, a launch can
  provide only what the operator's caches already hold. A package acme patched, or composed with
  an overlay, misses the cache and fails the launch, saying which path.
- **A task sees its own closure and no other's.** One `--ro-bind` per store path, never the
  store whole.
- **Installing during a run is not in this issue.** 1f5art3x's open question stands; the spike
  narrowed it (below).

## The shape

```tony
# the runner's configuration
nix:
  store: /state/nix                       # a store of the runner's own (`nix --store`), on a volume
                                          # that outlives the pod; its paths are /nix/store/… in it
  substituters: ["https://cache.nixos.org"]
  trusted-public-keys: ["cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="]
  collect-above: 50Gi                     # the store's size past which the runner collects
agents:
  claude:
    systems:
      pkg-nix:
        base: "github:acme/tools/<rev>#base"          # every claude task starts with it
        ceiling: {flake: !regexp "^github:acme/tools(/|$)"}
        kept: base                                    # what secret-exec counts as the runner's:
                                                      # base (the default), or base-and-flake

# a spawn's environment
environment:
  systems:
    pkg-nix: {flake: "github:acme/tools/<rev>#ci"}
```

- **The store is the runner's own, apart from the image's nix.** nix itself is installed in the
  image's `/nix`; the store a task draws on is `nix --store <store>`, the spike's option 3, with
  its own database, on the volume. Its paths are `/nix/store/…` by name (the spike substituted
  at identical hashes) and lie at `<store>/nix/store/…` on disk. A task's sandbox binds neither
  `/nix` nor the store whole: `Wrap`'s system directories are `/usr /bin /sbin /lib /lib64 /etc
  /opt`.
- **`flake` is one attribute,** a scalar, as a block's attributes are (`readBlock`).
- **What `#ci` names is a list of packages:** the flake's output `verse.<system>.ci`, the system
  the runner's. Each element is a derivation a trusted cache holds. For each, the runner binds
  the outputs its `meta.outputsToInstall` names, as `nix profile` would (`bin` where a package
  has one, which is where jq's program is), and their closures. Not a `buildEnv` or
  `symlinkJoin`: a composition is a build, and no public cache holds acme's.
- **A reference is parsed before anything is fetched, and the pattern matches what was parsed.**
  The runner reads the reference itself:
  - schemes `github:`, `git+https:` and `https:` only. Anything local (`path:`, `.`, `/`,
    `file:`, `git+file:`, `tarball+file:`) is refused whatever the pattern says: given one, the
    runner would copy a task's files into a store other tasks draw on. So is an indirect
    reference, which the flake registry would resolve.
  - attributes other than `rev`, `ref` and `dir` are refused: `github:`'s `host`, for one, would
    fetch from another server under a name the pattern admits.

  Only then does nix fetch and lock it.
- **A flake's inputs come from wherever its lock file names.** The pattern bounds the reference
  a task names, not the inputs the runner then fetches and evaluates.
- **What the task's `PATH` names:** the `bin` directories of the flake's packages' outputs
  (above), then the base's, then the doer's `PATH` as today. Not every path of the closures. The
  runner builds it; it does not ask nix for a profile, since a profile is a build.

## What the spike measured

Local Docker (Docker Desktop, linuxkit 6.12, aarch64), as uid 65532 with no capabilities,
`seccomp=unconfined` and `systempaths=unconfined` (the counterparts of the pod's settings). Not
the pod: its kernel, `hostUsers: false`, Bottlerocket's SELinux and a PVC were not reproduced.

- **Single-user nix as 65532 works,** with no root, no daemon and no capability: nix 2.35.4 with
  `/nix` owned by 65532. nodejs_22 is 32 paths and 209 MiB, substituted in 11.4 s into an empty
  store; go is 9 paths in 3.1 s. Evaluating nixpkgs cold took 21 s, warm 0.2 s.
- **A store apart (`--store`) substitutes at the same paths,** needing no user namespace, and a
  bwrap bind from it to `/nix/store` runs its programs. GC roots in such a store were not tested.
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
    runner's flags. Binds on the command line are fine to about 10,000, so below bwrap's cap
    `--args` buys nothing. A build with the cap raised took 30,471 binds in 324 ms. 50,000
    mounts in one sandbox were under the kernel's limit.
- **Every store bind must be read-only,** and the root remounted read-only after the binds: with
  `--bind`, the task (same uid) wrote into the shared store; without `--remount-ro /`, it could
  create entries in its own `/nix/store`.
- **GC roots:** a per-task root (`nix-store --add-root --indirect`) keeps its closure through a
  collection; an idle task with no root loses its bound paths, though the binds remain.

## The plan

Each step builds and passes on its own.

0. **The spike in a staging pod.** Its scripts 00 (what bwrap needs), 03b/03c (the binds at
   size), 06 (GC roots), on the node's kernel, under `hostUsers: false`, with the store on the
   PVC. And what the review added:
   - roots and a collection in a `--store` store;
   - two realises at once, and a collection during one;
   - a flake evaluated under step 4's settings, `restrict-eval` among them;
   - the memory an evaluation takes.

   What it finds is said here before step 3 is built.
1. **The document.** `pkg/systems/documents/pkg-nix.tony`: the attribute `flake` (required,
   form `text`), no verb. **The model's first system with no verb:** `Environment.Gives`,
   `Ceiling.Check`, `Describe` and the offer's `held` read a block of it as given; no step is
   claimed in `pkg/task/step.go`. The document says its kind, `provided` (shoring item 6 of
   1f5art3x), and the description publishes it, so a charter's author reads that no row decides
   it. `docs/systems/pkg-nix.md`.
2. **The configuration.** `nix: {store, substituters, trusted-public-keys, collect-above}` in
   `stock.Config`, and `systems.pkg-nix: {base, ceiling, kept}` per doer. `Config.Check` takes:
   - a store that is absolute, and none the existing kept-directory checks refuse (it is the
     runner's: no `readOnly` path reaches it, no sandbox binds it whole);
   - `https` substituters, each with a key;
   - a base the reference parser (above) admits. A ceiling's pattern is a regexp, whose language
     the check cannot read, so a spawn's reference is parsed at the launch whatever the pattern
     admits.

   `performs`/`held` gain the provided kind. `pkg-nix` is a doer's where nix, a store and
   bwrap 0.13 are there; it is closed, and said, where any is not, and under `-sandbox none`, as
   `secret` is.
3. **The image.** bwrap 0.13: from nixpkgs, pinned and installed at build time, if nixpkgs ships
   it (0.13.0 was released 2026-09-22); built from source in `deploy/verse-runner/Dockerfile`
   otherwise. nix installed single-user for uid 65532 in the image's `/nix`. The runner's
   `Bwrap()` probe reads bwrap's version and closes `pkg-nix` below 0.13, saying why. The
   deployment gains a volume for the store, ReadWriteOnce: a single-user store is one runner's,
   so replicas each have their own (`deploy/build.tony` has PVC knobs for docd alone today). The
   network policy lets the runner pod reach the substituters and the flakes' hosts.
4. **The runner's nix.** One `nix.conf` the runner writes and nothing else reads:
   - `max-jobs = 0`, `builders =`, `always-allow-substitutes = true`,
     `allow-import-from-derivation = false`, `require-sigs = true`, `sandbox = false`, and the
     operator's substituters and keys;
   - `accept-flake-config = false`: a flake's `nixConfig` can ask for substituters and keys, and
     single-user nix trusts its caller;
   - `pure-eval = true`, `restrict-eval = true` (if step 0 finds flakes evaluate under it),
     `use-registries = false`, `flake-registry =`.

   Run with `NIX_CONF_DIR` naming it, `NIX_USER_CONF_FILES` empty, `NIX_CONFIG` unset, and
   `HOME` and the `XDG_*` directories the runner's own, on the store's volume. So no
   `trusted-settings.json` accepted elsewhere applies, and fetched flake sources survive a
   restart.

   **An evaluation is a flake's code run on the runner's side.** It runs under a timeout and a
   memory bound, in a bwrap with the network and without the secret store or the runner's state.
5. **Resolve, realise, root.** At a launch that gives `pkg-nix`, before the task's process
   starts:
   - parse the reference (above), then evaluate the base and the flake to their packages'
     outputs, cached by the locked reference, so a warm launch pays 0.2 s and not 21 s. The
     locked reference is recorded with the task;
   - realise and root them in one command, `nix-store --add-root … --indirect --realise`, so no
     collection falls between the two: a root for the task, and one for each base, under the
     runner's own roots directory. Substituting, never building;
   - read each closure (`nix-store -qR`) into the list of paths to bind.

   The task's root is removed when the task ends, and a base's when no doer names it. At its
   start, the runner removes the roots of tasks that are not live, which a crash left. A failure
   fails the launch, naming the reference and nix's words, before any process starts.
6. **Collection.** The runner collects (`nix-store --gc`) at its start, and whenever the store
   passes `collect-above`. Never while a launch realises: one lock in the runner orders them.
7. **The sandbox.** `Sandbox.Wrap` takes the store paths beside its programs:
   - one `--ro-bind <store>/nix/store/p /nix/store/p` each, on the command line;
   - `--remount-ro /` after the binds, where there are any, so the task writes its workspace and
     `/tmp` and nothing else. A sandbox with no store binds is as today;
   - a closure past what bwrap takes (its 9000 arguments, less the runner's own) refuses the
     launch, saying how many paths and the bound.

   `PATH` as above.
8. **`secret-exec`.** Needs pa35grmz merged. The `Confiner` counts a program under the **base's**
   closure as one the runner keeps: the operator chose the base, as it chose the image. A program
   of the spawn's flake counts only where the doer's `kept` is `base-and-flake`. A row admitting
   `git push` otherwise means whatever the flake calls `git`, which the flake's author chose
   within the ceiling, not the operator. The step carries the resolved program's path, so the
   ask shows which `git`. The command's sandbox has the same binds and `PATH` as the task's.
9. **The words.** `docs/systems/pkg-nix.md`, `docs/reference/runnerd.md` (the `nix` section, the
   doer's `pkg-nix`, the store's volume), the row in `docs/systems/README.md`, and the deploy
   manifest's example. They say:
   - a flake is evaluated on the runner's side, so the ceiling's pattern is a list of sources the
     operator trusts to evaluate, not only to run, and that trust reaches the flake's inputs too;
   - a ceiling bounds what a task names, and the task gets the whole closure of it;
   - only packages the caches hold can be provided;
   - the store is single-user and one runner's: a task never runs nix against it, and replicas
     do not share it;
   - the runner pod reaches the substituters and the flakes' hosts.

## Tests

- **The configuration.** A local reference in a base, in a ceiling's pattern, and in a spawn
  that a pattern would admit: each refused. So are `git+file:`, an indirect reference, and
  `github:acme/tools?host=…` under a pattern that admits `github:acme/`. A substituter with no
  key: refused.
- **The document.** `TestTheRunnerImplementsTheDocumentedSystems` holds a system with no verb;
  an environment with a `pkg-nix` block gives no step and asks no row.
- **The runner's nix.** The `nix.conf` written holds every setting of step 4; nix's environment
  names no configuration but it.
- **The sandbox's arguments.** For a list of paths: one `--ro-bind` each from the store to
  `/nix/store`, `--remount-ro /` after the binds and none without them, the workspace still
  writable, and a list past the bound refused. `PATH`'s order.
- **The launch, against a fake nix** (a script that answers `eval`, `--realise`, `-qR` and
  `--add-root` from fixtures):
  - the paths bound are the closure's;
  - a package with a `bin` output has that output bound and on `PATH`;
  - the roots are added before the process and removed after;
  - roots of tasks not live are removed at the start;
  - a collection waits for a realise;
  - a failure fails the launch before any process.
- **`secret-exec`** admits a program under the base's closure, refuses one of the spawn's flake
  under `kept: base` and admits it under `base-and-flake`, and refuses one beside both. The ask
  carries the program's path.
- **End to end, where nix and bwrap 0.13 are there** (skipped otherwise, and said): a flake
  realised from a cache, its program run in a task, the store unwritable from inside, a flake's
  `nixConfig` substituter ignored.

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
  Bound by the operator, as 1f5art3x says; untested by the spike. Until then, a package only a
  private cache holds (acme's own, a patch, an overlay) fails the launch.
- **More than about 2,900 store paths.** A bwrap with its limit raised (a vendored patch, or
  upstream), or nested sandboxes, and `--args <fd>` with it, from a memfd or a file (a pipe
  fills before bwrap reads it). Refused until then.
- **A build.** Substitution only.

## Open, for review

1. **The name.** `pkg-nix`, or `nix-pkg` as `linear-issue` and `github-issue` put the provider
   first (1f5art3x's open question 2). The review proposes `nix-pkg`.
2. **The output's name.** `verse.<system>.<name>` is this plan's convention for a list of
   packages; flakes have no standard output for one.
3. **`kept`.** Its name and values, and whether `base` is the right default.
4. **bwrap's source.** nixpkgs if it ships 0.13, from source otherwise; either is a maintenance
   cost the image did not have.
5. **The store's volume.** Its size, its class, and what happens to a store on a node the pod
   moves off.
6. **The launch's latency.** A cold evaluation is 21 s. The review proposes the runner evaluate
   the bases at its start, ahead of any launch.

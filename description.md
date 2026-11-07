# pkg-nix: packages from nix, provided when a task starts, read-only, one bind per store path

**Status:** a plan, reviewed once (2026-11-07, the first comment) and revised for it. **Step 0
is done** (2026-11-07, a pod on staging, the third comment; report, scripts and logs attached as
`pkgnix-pod/`), and the plan is revised for what it found. The bases are prepared at the
runner's start, ahead of any launch (step 5; the fourth comment). Nothing else is built. It
blocks 1f5art3x.

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
- **Installing during a run is not in this issue.** 1f5art3x's open question stands; the local
  run narrowed it (below).

## The shape

```tony
# the runner's configuration
nix:
  store: /state/nix                       # a store of the runner's own (`nix --store`), on a volume
                                          # that outlives the pod; its paths are /nix/store/… in it
  substituters: ["https://cache.nixos.org"]
  trusted-public-keys: ["cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="]
  sources: ["github:acme", "github:NixOS/nixpkgs"]   # what a flake and its inputs may come from
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
  image's `/nix`; the store a task draws on is `nix --store <store>`, with its own database, on
  the volume. Its paths are `/nix/store/…` by name and lie at `<store>/nix/store/…` on disk. A
  task's sandbox binds neither `/nix` nor the store whole: `Wrap`'s system directories are `/usr
  /bin /sbin /lib /lib64 /etc /opt`.
- **`flake` is one attribute,** a scalar, as a block's attributes are (`readBlock`).
- **What `#ci` names is a list of packages:** the flake's output `verse.<system>.ci`, the system
  the runner's. Each element is a derivation a trusted cache holds. For each, the runner binds
  the outputs its `meta.outputsToInstall` names, as `nix profile` would (`bin` and `man` for jq
  and ffmpeg, `out` for most), and their closures. Not a `buildEnv` or `symlinkJoin`: a
  composition is a build, and no public cache holds acme's.
- **A reference is parsed before anything is fetched, and the pattern matches what was parsed.**
  The runner reads the reference itself:
  - schemes `github:`, `git+https:` and `https:` only. Anything local (`path:`, `.`, `/`,
    `file:`, `git+file:`, `tarball+file:`) is refused whatever the pattern says: given one, the
    runner would copy a task's files into a store other tasks draw on. So is an indirect
    reference, which the flake registry would resolve.
  - attributes other than `rev`, `ref` and `dir` are refused. `github:`'s `host` is real: given
    one, nix fetched from the host it named (step 0).

  Only then does nix fetch and lock it.
- **`sources` bounds everything an evaluation fetches,** the flake and its inputs. It is
  `allowed-uris` under `restrict-eval`: a prefix by path segment (`github:NixOS/nix` does not
  admit `github:NixOS/nixpkgs`), and an input outside it fails the evaluation, naming the input.
  The ceiling's pattern bounds the reference a task names; `sources` bounds what that reference
  draws on.
- **What the task's `PATH` names:** the `bin` directories of the flake's packages' outputs
  (above), then the base's, then the doer's `PATH` as today. Not every path of the closures. The
  runner builds it; it does not ask nix for a profile, since a profile is a build.

## What the runs measured

**Locally** (Docker Desktop, linuxkit 6.12, aarch64), as uid 65532 with no capabilities,
`seccomp=unconfined` and `systempaths=unconfined`:

- **Single-user nix as 65532 works,** with no root, no daemon and no capability.
- **Building is off only with two settings together:** `max-jobs = 0` and
  `always-allow-substitutes = true`. With the first alone, nodejs_22 fails though the cache has
  it: its top-level wrapper, and every `buildEnv` and `symlinkJoin`, is marked
  `allowSubstitutes = false`. So the runner composes no profile.
- **Single-user nix obeys whoever runs it.** A caller's `--option max-jobs 1` builds. So
  building is off only where the task never runs nix itself, which is this issue's shape.
- **bwrap's 9000-argument cap,** `--args <fd>` included: about 2,986 binds with the runner's
  flags. A build with the cap raised took 30,471 binds in 324 ms.
- **Every store bind must be read-only,** and the root remounted read-only after the binds: with
  `--bind`, the task (same uid) wrote into the shared store; without `--remount-ro /`, it could
  create entries in its own `/nix/store`.

**On staging** (step 0: the runner's image and pod settings, `hostUsers: false`, t3.large,
Bottlerocket, kernel 6.18.54, the store apart on a gp3 claim):

- **Wrap's flags run** with bwrap 0.8.0 and 0.13.1; `--remount-ro /` refuses `/` and leaves the
  workspace and `/tmp` writable.
- **The store apart:** 11 packages, 471 paths, 4.2 GB, substituted in 52 s from inside the
  cluster. Bound to `/nix/store`, every program ran from a `PATH` of 13 `bin` directories; the
  store refused writes; `/state` was not there.
- **Binds:** 0.13.1 is linear, 45 ms at 471 and 216 ms at 2,980; 0.8.0 is quadratic, 9.8 s at
  2,980. The cap is the same. **2 of 471 paths are symlinks** into `/nix/store/…`, which names
  nothing on the runner's side: `--ro-bind` of one fails.
- **No nixpkgs ships bwrap 0.13:** the pinned rev, `nixpkgs-unstable` and `nixos-unstable` all
  have 0.12.0.
- **Evaluation:** five nixpkgs packages through `--apply`, cold 36–41 s and 578 MiB, warm 3.2 s
  and 462 MiB (no evaluation cache for `--apply`). `restrict-eval` refused the flake's own input
  until `allowed-uris` named it.
- **`trusted-settings.json` overrides `accept-flake-config = false`:** a saved yes in the
  runner's `XDG_DATA_HOME` applied a flake's extra cache and key.
- **Roots** in the store apart work as in the default store, realised and rooted in one
  command; a running task keeps its paths with no root. **Concurrency:** two realises at once
  both complete; a collection during a realise takes nothing of it and does not wait.
- **nix refuses a `HOME` its uid does not own,** and 65532 has no passwd entry. The claim is
  writable to 65532 with `fsGroup: 65532`.

## The plan

Each step builds and passes on its own.

0. **Done:** the pod on staging (above, and the third comment).
1. **The document.** `pkg/systems/documents/pkg-nix.tony`: the attribute `flake` (required,
   form `text`), no verb. **The model's first system with no verb:** `Environment.Gives`,
   `Ceiling.Check`, `Describe` and the offer's `held` read a block of it as given; no step is
   claimed in `pkg/task/step.go`. The document says its kind, `provided` (shoring item 6 of
   1f5art3x), and the description publishes it, so a charter's author reads that no row decides
   it. `docs/systems/pkg-nix.md`.
2. **The configuration.** `nix: {store, substituters, trusted-public-keys, sources,
   collect-above}` in `stock.Config`, and `systems.pkg-nix: {base, ceiling, kept}` per doer.
   `Config.Check` takes:
   - a store that is absolute, and none the existing kept-directory checks refuse (it is the
     runner's: no `readOnly` path reaches it, no sandbox binds it whole);
   - `https` substituters, each with a key;
   - `sources` each a reference the parser (above) admits, with no attributes;
   - a base the parser admits and `sources` covers. A ceiling's pattern is a regexp, whose
     language the check cannot read, so a spawn's reference is parsed, and held to `sources`, at
     the launch whatever the pattern admits.

   `performs`/`held` gain the provided kind. `pkg-nix` is a doer's where nix, a store and
   bwrap 0.13 are there; it is closed, and said, where any is not, and under `-sandbox none`, as
   `secret` is.
3. **The image.** bwrap 0.13 built from source in `deploy/verse-runner/Dockerfile`, as no Debian
   release and no nixpkgs ships it. nix installed single-user for uid 65532 in the image's
   `/nix` (from the release tarball; the image needs `xz` to unpack it). The runner's `Bwrap()`
   probe reads bwrap's version and closes `pkg-nix` below 0.13, saying why. The deployment gains
   a volume for the store, ReadWriteOnce, and `fsGroup: 65532` so the runner's uid can write it:
   a single-user store is one runner's, so replicas each have their own (`deploy/build.tony` has
   PVC knobs for docd alone today). The network policy lets the runner pod reach the
   substituters and `sources`.
4. **The runner's nix.** One `nix.conf` the runner writes and nothing else reads:
   - `max-jobs = 0`, `builders =`, `always-allow-substitutes = true`,
     `allow-import-from-derivation = false`, `require-sigs = true`, `sandbox = false`, and the
     operator's substituters and keys;
   - `accept-flake-config = false`: a flake's `nixConfig` can ask for substituters and keys, and
     single-user nix trusts its caller;
   - `pure-eval = true`, `restrict-eval = true`, `allowed-uris` = `sources`,
     `use-registries = false`, `flake-registry =`.

   Run with `NIX_CONF_DIR` naming it, `NIX_USER_CONF_FILES` empty, `NIX_CONFIG` unset, and
   `HOME` and the `XDG_*` directories the runner's own, on the store's volume, made by the
   runner's uid (nix refuses a `HOME` another uid owns). Fetched flake sources survive a restart.
   **`$XDG_DATA_HOME/nix/trusted-settings.json` is removed at the runner's start and before each
   evaluation:** a saved setting there applies whatever `accept-flake-config` says.

   **An evaluation is a flake's code run on the runner's side.** It runs under a timeout (2
   minutes; a cold one is about 40 s) and a memory bound (1 GiB; one takes about 580 MiB), in a
   bwrap with the network and without the secret store or the runner's state.
5. **Resolve, realise, root: the bases at the runner's start, a spawn's flake at its launch.**
   Each is the same three acts:
   - parse the reference (above), then evaluate it to its packages' outputs. The runner keeps
     the answer by the locked reference and the system, on the store's volume, so it outlives a
     restart: nix caches no evaluation through `--apply`, so without it a warm one still costs
     3 s, and a cold one about 40 s;
   - realise and root them in one command, `nix-store --add-root … --indirect --realise`, so no
     collection falls between the two, under the runner's own roots directory. Given several
     paths, nix names the roots `<name>-1`, `<name>-2`, …, so a root is removed by its prefix.
     Substituting, never building;
   - read each closure (`nix path-info -r`) into the list of paths to bind.

   **The bases, at the runner's start.** Once the start's collection is done (step 6), the
   runner prepares every base a doer names, a root for each, so no launch pays a base's
   evaluation or its substitution:
   - **It serves while they are prepared.** A launch that gives `pkg-nix` for a doer whose base
     is not ready waits for it, as long as an evaluation's timeout (step 4), rather than doing
     the work a second time or being refused.
   - **A base is locked once.** A base with no `rev` is locked when it is prepared, and every
     launch until the runner restarts, or its configuration names another base, uses that lock.
     The locked reference is in the runner's log and recorded with each task. A base moves only
     when the operator moves it; the words (step 9) say to name a `rev`.
   - **A base that fails** is said in the runner's log with nix's words, and prepared again by
     the next launch that needs it. A launch whose base fails again fails before any process,
     naming the base and nix's words. A network that fails at the start closes nothing for
     good.
   - A base's root is removed when no doer names it.

   **A spawn's flake, at its launch,** before the task's process starts. Its locked reference is
   recorded with the task, and a flake evaluated before costs nothing to evaluate again. The
   task's roots are removed when the task ends. A failure fails the launch, naming the reference
   and nix's words, before any process starts.
6. **Collection.** The runner collects (`nix-store --gc`) at its start, and whenever the store
   passes `collect-above`. At the start, in this order: the roots of tasks that are not live,
   which a crash left, are removed; then the roots of bases no doer names; then the collection;
   then the bases are prepared (step 5). A base whose root outlived the restart is kept through
   the collection, so preparing it again is warm. A collection during a realise takes nothing of
   it (step 0), so no lock orders them.
7. **The sandbox.** `Sandbox.Wrap` takes the store paths beside its programs:
   - one `--ro-bind <store>/nix/store/p /nix/store/p` each, on the command line;
   - a store path that is itself a symlink is recreated, `--symlink <target> /nix/store/p`: its
     target names `/nix/store/…`, which is nothing on the runner's side, and is in the closure,
     bound;
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
   - a flake is evaluated on the runner's side, so `sources` and the ceiling's pattern are lists
     of what the operator trusts to evaluate, not only to run;
   - a ceiling bounds what a task names, and the task gets the whole closure of it;
   - only packages the caches hold can be provided;
   - the store is single-user and one runner's: a task never runs nix against it, and replicas
     do not share it;
   - the runner pod reaches the substituters and `sources`.

## Tests

- **The configuration.** A local reference in a base, in a ceiling's pattern, and in a spawn
  that a pattern would admit: each refused. So are `git+file:`, an indirect reference, and
  `github:acme/tools?host=…` under a pattern that admits `github:acme/`. A base outside
  `sources`: refused. A substituter with no key: refused.
- **The document.** `TestTheRunnerImplementsTheDocumentedSystems` holds a system with no verb;
  an environment with a `pkg-nix` block gives no step and asks no row.
- **The runner's nix.** The `nix.conf` written holds every setting of step 4, `allowed-uris`
  among them; nix's environment names no configuration but it; a `trusted-settings.json` placed
  in its `XDG_DATA_HOME` is gone before an evaluation.
- **The sandbox's arguments.** For a list of paths: one `--ro-bind` each from the store to
  `/nix/store`, a symlinked path as `--symlink`, `--remount-ro /` after the binds and none
  without them, the workspace still writable, and a list past the bound refused. `PATH`'s order.
- **The launch, against a fake nix** (a script that answers `eval`, `--realise`, `path-info` and
  `--add-root` from fixtures):
  - the paths bound are the closure's;
  - a package with a `bin` output has that output bound and on `PATH`;
  - the roots are added before the process and removed, by prefix, after;
  - roots of tasks not live are removed at the start;
  - a second launch of the same locked reference does not evaluate;
  - a failure fails the launch before any process.
- **The bases at the start,** against the same fake nix:
  - every base a doer names is evaluated, realised and rooted at the start, in step 6's order,
    and a launch afterwards evaluates and realises only its flake;
  - a launch while its base is being prepared waits, then starts, and the base is prepared once;
  - a base with no `rev` is locked once: two launches record the same locked reference;
  - a restart with the same locked base does not evaluate it (the answer is on the volume);
  - a base that fails at the start is prepared again by the next launch that needs it; failing
    again, that launch fails before any process, naming the base;
  - a base no doer names any more loses its root.
- **`secret-exec`** admits a program under the base's closure, refuses one of the spawn's flake
  under `kept: base` and admits it under `base-and-flake`, and refuses one beside both. The ask
  carries the program's path.
- **End to end, where nix and bwrap 0.13 are there** (skipped otherwise, and said): a flake
  realised from a cache, its program run in a task, the store unwritable from inside, a flake's
  `nixConfig` substituter ignored, an input outside `sources` refused.

## Not in this issue

- **Installing during a run.** The local run tested two of 1f5art3x's four ways:
  - **1, links into the task's own directory:** links made after the start appear inside, and
    the task cannot link out ("Invalid cross-device link"). But the store's files and the task
    share a uid, so the runner's every `chmod` near a link is a write to the store. That run's
    own cleanup did that once, by accident.
  - **4, an overlay `/nix`:** works unprivileged with bwrap ≥0.9, and nix inside the task
    installs into its own layer. Building cannot be forbidden there; only the network bounds it.

  Which, if either, is the next issue's.
- **A private cache's token,** and a private flake's host credential, from the secret store.
  Bound by the operator, as 1f5art3x says; untested. Until then, a package only a private cache
  holds (acme's own, a patch, an overlay) fails the launch.
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
4. **The store's volume.** Its size, its class, and what happens to a store on a node the pod
   moves off (step 0 did not move one).
5. **A spawn's flake's latency.** The bases are prepared at the start (step 5), so what a launch
   still pays is its flake's: nothing for one evaluated before, about 3 s for a new one over a
   nixpkgs the store has, about 40 s for one over a nixpkgs it has not, and the substitution of
   whatever its closure adds. Whether that wants more (the ceiling's flakes prepared ahead too,
   say) is the measure of the first users' launches.

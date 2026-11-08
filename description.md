# pkg: tools a runner installs into a per-task read-only prefix, by trusted scripts, multi-version and LRU-cached

**Status:** a plan, superseding 2b5art3x, whose nix shape reached a dead end (below, and
`FINDING-0b.md`/`FINDING-0c.md` on that issue's and this one's attachments). Nothing is built.
It blocks 1f5art3x, as 2b5art3x did.

**Where it is built:** on `verse/secret-exec`, as a pull request on top of it, stacked where
2b5art3x was. The team is shown secret-exec (pa35grmz) as one pull request and what it enables;
this and mcp (1v5art3x) as far as each is done cleanly, as one or two on top; so secret-exec does
not merge to main first. mcp's configuration steps settle the shape this one's configuration
follows (`stock.Config`, `performs`/`held`, `Describe`, the offer).

## Why 2b5art3x was dropped

2b5art3x provided nix packages by binding **each store path as its own read-only mount** into the
task (`--ro-bind <store>/nix/store/p /nix/store/p`). That gives a task only its own closure's
paths, which is the isolation we want, but it makes *installing a tool while the task runs* mean
*adding a mount to a running sandbox* — and that needs `CAP_SYS_ADMIN` in the task's user
namespace. Two staging measurements closed that off (attached):

- **The runner cannot inject mounts into a bwrap-made sandbox** (`FINDING-0b.md`). bwrap creates
  the user namespace in a forked child, so the runner, a same-uid process alongside it, enters
  with no capabilities and cannot join the mount namespace.
- **Owning the namespace does not rescue it cheaply** (`FINDING-0c.md`). A non-root uid loses
  every capability on `execve`, so any exec'd helper that enters to mount has none; a
  root-in-userns map restores the helper's caps but equally privileges the task. The only form
  that keeps the task unprivileged is a **resident privileged C namespace-bootstrap in every
  sandbox**, runc/bwrap class, a security-critical addition to the trusted base. Rejected.

The lesson: per-store-path binding is the only thing that ever forced a runtime *mount*. Drop it,
and the capability problem disappears.

## What it is

The `pkg` system, 1f5art3x's "packages a runner provides", reshaped: **a runner installs tools
into a directory it owns, bound read-only into the task; installing is the runner writing files
into that directory, which the task sees through the bind.** No nix in the framework, no per-path
mount, no capability. A task may ask for an install while it runs; the runner, not the task,
performs it, deciding each by the environment, the autonomy and the ceiling, as every act is
decided. An install is substitution of prebuilt artifacts — the runner fetches and lays out, it
does not build.

- **A read-only prefix, bound once at the task's start.** `$I/{bin,opt}`, the runner's, bound
  `--ro-bind` at the task's start (bwrap, no capability). The task reads it and cannot write it.
- **Installing is a file write, not a mount.** The runner adds a tool to `$I` from its own side;
  the task sees it at once through the bind. Proven on staging (`p11-robind.sh`): the task saw a
  tool the runner wrote after the task started, through the read-only bind, with exactly one
  mount, made at start, and could not write it.
- **The task only asks.** An `install` step reaches the runner over the delivery channel, like
  `mcp.call` or `secret.exec`. The runner runs the operator's install script, non-root. The task
  never runs install logic.

## The layout and the dynamic-library discipline

The hard part of a flat prefix is making a binary find its libraries without a global
`LD_LIBRARY_PATH` (which collides across tools) and without a shared image. The discipline, proven
end to end on Debian in `p12-brew.sh`:

**The cellar** — the runner's, shared across tasks, the LRU cache:

```
<cellar>/<name>/<version>/
  bin/<cmd>            an executable, DT_RUNPATH = $ORIGIN/../lib
  lib/<lib>.so.N       the package's own libraries, each DT_RUNPATH = $ORIGIN
  share/...
```

- **Each package is self-contained.** At install, the runner sets `DT_RUNPATH` (patchelf, in the
  install toolchain, never in the sandbox): `$ORIGIN/../lib` on each executable, so it finds its
  siblings, and `$ORIGIN` on each bundled library, so a library finds *its* siblings — needed,
  since `DT_RUNPATH` does not reach a transitive dependency (`jq` -> `libjq` -> `libonig`: `jq`'s
  runpath finds `libjq`, and `libjq`'s own runpath finds `libonig`). `p12` ran `jq` from the
  cellar with no `LD_LIBRARY_PATH`, both libraries resolved through `$ORIGIN`.
- **Only the package's own libraries are bundled.** libc, libm and the loader are the base
  image's, found as always; one base image, one libc family. So a package brings `libjq` and
  `libonig`, not `libc`.
- **A version is immutable once installed,** so two versions of a name never share a path.

**The task's root** — one per task, bound read-only:

```
$I/opt/<name>-<version>/   the prefix, hardlinked from the cellar (near-free, same volume)
$I/bin/<as>                a relative symlink ../opt/<name>-<version>/bin/<cmd>
```

- **`bin` is a symlink farm** of relative links, so a link resolves wherever `$I` is bound; `PATH`
  is `$I/bin` first.
- **The prefix is hardlinked, not copied,** from the cellar: near-free on one volume, and a
  running task keeps its inodes even when the cellar entry is LRU-evicted (the hardlink holds the
  inode until the task ends — eviction is safe for running tasks for free).

## Multi-version, and the install-name override

- **The cellar holds many versions of a name** (`<name>/<version>`); the runner side is
  multi-version by construction.
- **A task may install more than one version, each under a name it chooses.** `as` overrides the
  command name: `install {pkg: jq, version: "1.6", as: jq-v1}` and
  `{pkg: jq, version: "1.7", as: jq-v2}` give `$I/bin/jq-v1` and `$I/bin/jq-v2`, both on `PATH`,
  no collision (separate prefixes, `$ORIGIN` libraries). Default `as` is the package's canonical
  command. `p12` ran `jq` and `jq-v2` side by side from one prefix.

## The catalog, and the equivalent of `brew update`

- **The catalog is a manifest** the runner reads: `name -> {version -> {source, digest, layout}}`
  — where to fetch each version (an apt pool `Filename` and the index's hash, or a release URL and
  a sha256), its digest, and which artifacts make the prefix (a package and its library debs).
  This is what a flake and `sources` were in 2b5art3x, written as data the runner acts on rather
  than code it evaluates.
- **Two update models, the operator's choice:**
  - **Baked into the runner image.** The manifest ships with the image; a new version is a new
    image. Most auditable, matches "the operator chose the image".
  - **A tap the runner pulls.** The operator names a manifest source (a git repo, a URL, an OCI
    artifact); the runner refreshes it at start or on a schedule. A pull changes only the
    available `(name, version) -> (source, digest)` rows; it installs nothing.
- **`brew update` is refreshing the manifest; `brew install` is a per-task act** by the trusted
  script, fetching by the pinned digest. There is no `brew upgrade` as a task action: a task gets
  the version its spawn or ceiling names, or the catalog's default. Moving the default is an
  operator act (edit the manifest, or a new image), never a task's.
- **Fetching is the runner's, by pinned digest, from operator-approved sources** — the trust shape
  of nix substituters and signatures. A fetch whose digest does not match the manifest fails the
  install.

## A working example, non-root, on Debian (`p12-brew.sh`)

The install toolchain (dpkg-deb, xz, patchelf — here from nix, standing for what the runner image
bakes in) on Debian bookworm, uid 65532, no capabilities:

1. Resolved `jq` to `1.6-2.1+deb12u2` from the suite's `Packages` index (the manifest).
2. Fetched `jq`, `libjq1`, `libonig5` as `.debs` with `curl` (non-root), extracted each with
   `dpkg-deb -x` (non-root, no maintainer scripts, no dpkg database).
3. Assembled `<cellar>/jq/1.6.../{bin,lib}`, set the runpaths, and ran the cellar `jq` with **no
   `LD_LIBRARY_PATH`**: `jq-1.6`, both libraries resolved through `$ORIGIN`.
4. Materialised it into a task root under `jq` and `jq-v2`, bound it read-only in a bwrap, and
   inside the sandbox ran `echo '{"a":1}' | jq .a` -> `1`; the task could not write the root and
   could not see the cellar or other tasks.

**apt/dpkg non-root, settled:** `apt-get`/`apt-get update` want root (they write root-owned
lists), but the two acts that matter — fetch a `.deb` (`curl` the pool `Filename`) and extract it
(`dpkg-deb -x` into a prefix) — do not. The cost: maintainer scripts do not run and dependencies
are not auto-resolved, so the manifest enumerates a tool's deb set (the package and its library
debs). Fine for CLI tools; a package needing post-install configuration or setuid does not fit.
**apk** (Alpine) installs to `--root <dir>` and may be a lighter toolchain, untested here; its
libc is musl, so it is a different base image, not mixable with glibc tools.

## The shape

```tony
# the runner's configuration
pkg:
  cellar: /state/cellar          # the runner's store of installed prefixes, on a volume
  catalog: {baked: true}         # or {tap: "https://.../catalog.git", refresh: 1h}
  collect-above: 50Gi            # the cellar's size past which the runner evicts, LRU
agents:
  claude:
    systems:
      pkg:
        base: [ripgrep, jq]                 # installed into every claude task's root at start
        ceiling: {pkg: !glob "*", version: !any}   # which catalog names and versions a task may install
```

```tony
# a spawn's environment
environment:
  systems:
    pkg:
    - {pkg: jq, version: "1.7", as: jq-v2}   # installed before the process starts
autonomy:
- {match: {system: pkg, verb: install}, then: ask}   # the person is asked which tool and version
- {then: allow}
```

- **`base`** is what a doer's every task gets at start (the stone-age analog of 2b5art3x's base,
  but just a list of catalog names). A spawn adds installs for its task.
- **The ceiling names which catalog entries a doer may install,** by name and version, as a shape.
  The install step carries the name, the version and the `as`; a row decides by them and an answer
  is bound to them, as for every step.

## Security model

- **The install script is the operator's, run by the runner, non-root, never by the task.** The
  task requests; the runner decides (environment, autonomy, ceiling) and acts.
- **The ceiling bounds which names and versions a doer may install,** and the autonomy decides
  which installs are asked about. A row asks about the name, the version and the `as`.
- **Fetching is by pinned digest from operator-approved sources.** A mismatch fails the install.
- **secret-exec (pa35grmz):** a program under an installed prefix counts as the runner's for the
  `Confiner`, since everything in the cellar came through the doer's ceiling from an approved
  source — so, unlike 2b5art3x, there is no base/flake split and no `kept`. The step carries the
  resolved program's path, so the ask shows which binary a row admits.

## Open questions

1. **The name.** `pkg`, or something that is not nix-specific since nix is gone (an install script
   may use apt, apk, a release tarball, or nix internally — the framework does not know).
2. **The step/verb.** `install`, and whether a task may also *list* what is installed.
3. **The manifest format,** and whether `base` and a spawn name a version or a channel.
4. **musl vs glibc.** One base image is one libc family; whether a second base (Alpine) is worth
   it, or glibc-only.
5. **Interpreted tools.** python-with-site-packages, node-with-modules: a single prebuilt
   interpreter fits the prefix model; an environment of packages is its own layout question.
6. **The cellar on a node the pod moves off,** and one cellar per replica (ReadWriteOnce), as
   2b5art3x's step 0 left open for the store.

## Not in this issue

- **A build.** The runner fetches prebuilt artifacts and lays them out; it does not compile.
- **Interpreted-language environments** beyond a single prebuilt interpreter (open question 5).
- **Cross-libc tools in one base** (open question 4).
- **`upgrade` as a task action.** A task gets the version it names; the default is the operator's.
- **Private sources' credentials,** from the secret store, as 1f5art3x says; until then, only
  what an approved public source holds.
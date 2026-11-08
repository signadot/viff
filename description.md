# pkg: tools a runner installs into a per-task read-only prefix, by trusted scripts, multi-version and LRU-cached

**Status:** a plan, superseding 2b5art3x, whose nix shape reached a dead end (below, and
`FINDING-0b.md`/`FINDING-0c.md`). The pieces are measured on staging (`FINDING-0d.md`, and
`p11`/`p12`/`p13` under the attachments); nothing is built. It blocks 1f5art3x, as 2b5art3x did.

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
  `mcp.call` or `secret.exec`. The runner runs the install, non-root. The task never runs install
  logic.

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
  multi-version by construction. The versions come from the index (below) holding more than one —
  several suites (bookworm/bullseye/trixie) or snapshot.debian.org.
- **A task may install more than one version, each under a name it chooses.** `as` overrides the
  command name: `install {pkg: jq, version: "1.6", as: jq-v1}` and
  `{pkg: jq, version: "1.7", as: jq-v2}` give `$I/bin/jq-v1` and `$I/bin/jq-v2`, both on `PATH`,
  no collision (separate prefixes, `$ORIGIN` libraries). Default `as` is the package's canonical
  command. `p12` ran `jq` and `jq-v2` side by side from one prefix.

## The catalog: wrap a package manager, do not write one

The operator does not list installable tools — that is re-deriving a package manager's index by
hand. The distro already publishes it: Debian's `Packages` index (and Alpine's `APKINDEX`) is
`name -> version -> {url, hash, dependency graph}`, signed. The runner **wraps the manager**: an
`install` resolves the tool's dependency closure against that index, fetches each package by its
hash, extracts, and lays out the prefix. Proven non-root on staging (`FINDING-0d.md`,
`p13-resolve.sh`):

- **`apt-get update` runs non-root with signatures verified** — `gpgv` and the Debian keyrings are
  in the runner image; the state and cache dirs are redirected to a writable path.
- **Resolution subtracts the base with no curated list.** Pointed at the runner image's own
  `/var/lib/dpkg/status`, `apt-get install -s jq` resolved to exactly `libonig5, libjq1, jq` —
  libc and friends were not in the closure, since the image provides them and the sandbox binds
  the image's `/usr` and `/lib`. `git` resolved to an empty closure: already in the image. **The
  base-provides set is the runner image's dpkg database, already there.**
- **`--print-uris` is the fetch plan** — a url and hash per package of the closure; integrity rides
  the signed `Release -> Packages -> SHA256` chain that `update` verified.

So the operator configures a **manager and the repos to trust**, not a catalog. What a task may
install is a **ceiling pattern over names**, not an enumeration: `{pkg: !glob "*"}` over a signed
repo is every tool in it, bounded by the autonomy asking. The "list" the first draft feared
collapses to "trust Debian main, ask on install".

**The equivalent of `brew update`** is refreshing the index (`apt-get update`); installing is a
per-task act by the runner, by the index's pinned hash. There is no `brew upgrade` as a task
action: a task gets the version its spawn or ceiling names, or the suite's default; moving the
default is the operator's (another suite, snapshot pin, or a new image), never a task's.

**Two bind times for resolution, the operator's choice** — the same wrapper, run now or frozen:

- **Resolve at runtime (dynamic).** The runner resolves and fetches against the live index when a
  task installs. The widest catalog, the smallest config; needs the index and the pool reachable
  from the runner pod.
- **Freeze a manifest at build (pinned).** The build resolves a named set once and freezes the
  closure and hashes into a manifest baked in the image. Auditable, offline-installable, no
  resolver at run time; adding a tool is a rebuild. This is the dynamic path run once and pinned,
  and the two compose (a frozen default set, resolve on demand past it).

**The caveat, unchanged:** extract-only (`dpkg-deb -x`, no maintainer scripts), so packages needing
postinst, alternatives or setuid do not work — fine for CLI tools, libraries, compilers and
interpreters; the wrapper detects and refuses the rest rather than installing something
half-configured.

## A working example, non-root, on Debian (`p12`, `p13`)

Debian bookworm, uid 65532, no capabilities, the install toolchain (dpkg-deb, xz, patchelf — here
from nix, standing for what the runner image bakes in):

1. **Resolved** `jq`'s closure against the signed index, minus the image's base
   (`apt-get install -s` -> `libonig5, libjq1, jq`), and read the fetch plan (`--print-uris`).
2. **Fetched** each package with `curl` (non-root) and **extracted** it with `dpkg-deb -x`
   (non-root, no maintainer scripts, no dpkg database).
3. **Assembled** `<cellar>/jq/<ver>/{bin,lib}`, set the runpaths, and ran the cellar `jq` with **no
   `LD_LIBRARY_PATH`**: `jq-1.6`, both libraries resolved through `$ORIGIN`.
4. **Materialised** it into a task root under `jq` and `jq-v2`, bound it read-only in a bwrap, and
   inside the sandbox ran `echo '{"a":1}' | jq .a` -> `1`; the task could not write the root and
   could not see the cellar or other tasks.

**apk** (Alpine) installs to `--root <dir>` and may be a lighter wrapper, untested here; its libc
is musl, so it is a different base image, not mixable with glibc tools.

## The shape

```tony
# the runner's configuration
pkg:
  cellar: /state/cellar                 # the runner's store of installed prefixes, on a volume
  manager: apt                          # the wrapper: apt (glibc) or apk (musl)
  repos: ["deb http://deb.debian.org/debian bookworm main"]   # the signed index(es) to trust
  collect-above: 50Gi                   # the cellar's size past which the runner evicts, LRU
  # resolve: runtime   | or a frozen manifest baked in the image
agents:
  claude:
    systems:
      pkg:
        base: [ripgrep, jq]                 # installed into every claude task's root at start
        ceiling: {pkg: !glob "*"}           # which names a task may install; a pattern, not a list
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

## Security model

- **The install is the runner's, non-root, never the task's.** The task requests; the runner
  resolves, fetches by the index's pinned hash from the trusted repos, extracts, and lays out.
- **The ceiling bounds which names (and versions) a doer may install,** as a pattern, and the
  autonomy decides which installs are asked about. A row asks about the name, the version and the
  `as`, and the answer is bound to them, as for every step.
- **Provenance is the distro's signed index:** `apt-get update` verifies the Release signature, and
  a package is fetched by the index's hash. A wide ceiling is defensible because an install runs
  non-root, extract-only (no scripts), and lands only in the task's own read-only `$I` — a task
  installing a library is no more than it fetching a binary itself, and it cannot touch the system.

## Open questions

1. **The name.** `pkg`, or something not tied to one manager (the wrapper may be apt, apk, or a
   release tarball — the framework does not know).
2. **The step/verb.** `install`, and whether a task may also *list* what is installed or available.
3. **Runtime vs frozen** as the default, and whether a deployment offers both.
4. **musl vs glibc.** One base image is one libc family; whether a second base (Alpine/apk) is
   worth it, or glibc-only.
5. **Interpreted tools.** python-with-site-packages, node-with-modules: a single prebuilt
   interpreter fits the prefix model; an environment of language packages (pip/npm) is its own
   layout and resolver question, a separate issue.
6. **The cellar on a node the pod moves off,** and one cellar per replica (ReadWriteOnce), as
   2b5art3x's step 0 left open for the store.

## Not in this issue

- **A build.** The runner fetches prebuilt artifacts and lays them out; it does not compile.
- **Interpreted-language environments** beyond a single prebuilt interpreter (open question 5).
- **Cross-libc tools in one base** (open question 4).
- **`upgrade` as a task action.** A task gets the version it names; the default is the operator's.
- **Private repos' credentials,** from the secret store, as 1f5art3x says; until then, only what an
  approved signed repo holds.

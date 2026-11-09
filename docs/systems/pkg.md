# pkg

The `pkg` system is tools the runner installs into a task's prefix: a directory of the runner's,
bound read-only into the sandbox at `/inst` when the task starts. The runner wraps a package
manager — it resolves a package's name against the signed index of the repositories its operator
trusts, fetches each file by the index's hash, extracts it with none of the package's own scripts
run, and writes the tool into the prefix, where the task sees it through the bind and finds it on
its PATH. The task only asks: the environment says which packages, the autonomy decides each
install, and the ceiling bounds both.

Installing is a file write on the runner's side, never a new mount and never a capability: the
one bind is made at the task's start, and a tool installed mid-run appears through it. Nothing
outside the task's own prefix changes, and an install runs no code of the package's — a task
installing a library is no more than it fetching a binary itself, from a source the operator
signed off on, laid where the task cannot write.

## What a task can do

A `pkg` block's `install` shapes say which packages, versions and names:

```tony
spawn:
  agent: {name: claude}
  environment:
    systems:
      commands: {}
      files: {}
      pkg:
        install:
        - {pkg: jq, version: "1.7.1-1", as: jq-v2}   # pinned: installed before the process starts
        - {pkg: !glob "lib*"}                        # a pattern: installs the task may ask for
  prompt: "Compare jq 1.6's and 1.7's output for our filters."
  autonomy:
  - {match: {system: pkg, verb: install}, then: ask}
  - {then: allow}
```

The task's tool is `pkg-install`, and each use of it is the step `pkg.install`, with `pkg`,
`version` and `as`.

**A shape that pins the package is installed before the process starts.** A shape whose fields
are single literals, the package named, is the spawner's own word: the runner installs it into
the task's root during the launch, no row asked, and a failure refuses the launch, naming the
package. A shape with a pattern anywhere pins nothing: it admits installs the task asks for as it
runs, each decided by the autonomy.

**The step carries what the call asked and no more.** An install that names no `version` takes
the index's candidate, and the answer says exactly what landed — but the step a row or a shape
decides has no `version` field then, so a shape that pins the version refuses a call that names
another or none. A ceiling that must pin versions forces the task to name them.

**`as` renames one command, so two versions sit side by side.** `install {pkg: jq, version:
"1.6-2.1+deb12u2", as: jq-v1}` and `{pkg: jq, version: "1.7.1-1", as: jq-v2}` give `jq-v1` and
`jq-v2`, both on PATH, no collision: each version is its own prefix, and each prefix's libraries
are its own. With no `as`, every command the package ships lands under its own name (`ripgrep`
lands `rg`). A package of several commands refuses an `as`, naming them: it renames exactly one —
the one named as the package, or the only one.

**A package the image provides is answered as on PATH already.** The base image's packages are
the resolver's base, so `install {pkg: git}` on an image that ships git installs nothing and says
so; its libraries are never bundled twice. An `as` on one is refused: there is no installed
command to rename.

## How an install lands

The runner keeps a **cellar**, one immutable prefix per package and version, shared across tasks
and filled once:

- **Resolution is the manager's own, non-root.** The closure is resolved against the index
  `apt-get update` fetched and signature-checked with the image's keyrings, with the image's own
  package database as the base — so the closure is only the delta past the image. A closure
  that would move libc is refused: the prefix runs on the image's loader and libc, which move
  together, so a tool needing a newer libc needs a newer image.
- **Each file is fetched by the index's SHA256**, never the plan's MD5: integrity rides the
  signed `Release → Packages → SHA256` chain.
- **Extraction runs no maintainer script.** A package whose install scripts do more than the
  stock ldconfig stanza is refused by name: it would land half-configured, and nothing of a
  package's runs here. (Runpaths do ldconfig's work in a prefix.) So packages needing postinst,
  alternatives or setuid do not work — CLI tools and libraries do.
- **A prefix is flat: commands, their libraries and share.** Every package of the closure is
  held to it: one whose programs live in a tree of their own (`usr/libexec`, `usr/games`, `opt`,
  or a file that is no shared object under `usr/lib`, `lib` or `lib64`) does not work laid flat,
  and is refused by the path that gives it away rather than installed broken.
- **Each prefix is self-contained by its runpaths:** `$ORIGIN/../lib` on each executable,
  `$ORIGIN` on each bundled library (a runpath does not reach a transitive dependency, so a
  library carries its own). A prefix runs with no `LD_LIBRARY_PATH` and collides with nothing.
- **A task's root is hardlinked from the cellar,** near-free on one volume, with `bin` a farm of
  relative links. Above the operator's bound the least recently used prefixes are evicted; a
  running task keeps an evicted prefix by its root's hardlinks until it ends, so eviction never
  breaks a run. The root is removed with the task: installed tools are the cellar's to keep,
  never a task's.

## The question

A row that asks about an install asks about the package, the version and the name, and the
answer performs that install and no other. What the act resolved beyond the call — the candidate
version, the commands' names — is the answer's to report, not the row's to decide.

## The runner's configuration

The runner names the manager and the repositories under `pkg:`, and a doer's `pkg` ceiling
bounds what its tasks may install, with `base` the packages every task of the doer starts with
([the verse runner](../reference/runnerd.md#configuration)):

```tony
pkg:
  cellar: /state/cellar          # the prefixes and the tasks' roots, on a volume
  manager: apt                   # the manager the runner wraps
  repos: ["deb http://deb.debian.org/debian bookworm main"]
  collect-above: 50Gi            # evict least recently used past this; absent, never
agents:
  claude:
    systems:
      pkg:
        base: [ripgrep, jq]      # installed into every claude task's root at start
        ceiling: {install: {pkg: !glob "*"}}   # a pattern over the index, not a list
```

- **The index is the catalog.** The operator names a manager and repositories, never a list of
  installable tools: the ceiling is a pattern over the index's names, and `{install: {pkg: !glob
  "*"}}` over a signed repository is every tool in it, bounded by the autonomy asking. (Every
  shape of `install` names `pkg`.) A wide ceiling is
  defensible because an install runs non-root, extract-only, and lands only in the asking task's
  own read-only prefix.
- **`base` is the operator's installs,** laid into every task's root before its process starts,
  each at the index's candidate under its own commands' names; the ceiling does not bound them
  and no row is asked. It is `pkg`'s key alone.
- **The equivalent of `brew update`** is the runner refreshing the index, which it does when the
  lists are a day old; a version is pinned by naming it, and moving the default is the
  operator's (another suite, a snapshot repository, a new image), never a task's.
- **One base image is one libc family.** The manager is `apt`, Debian, glibc: the prefix model
  bundles a package's own libraries and leans on the image's libc. Alpine/apk (musl) would be a
  different base image, not mixable with glibc tools, and is not wrapped.

The system is performed where the `pkg:` section and a doer's ceiling naming it are both there.
It closes whole, and the runner says so, where tasks run unconfined (`-sandbox none`: there is no
bind to make the root read-only through) and where the toolchain (`apt-get`, `apt-cache`,
`dpkg-deb`, `patchelf`) is not in the image. A command run with a secret ([`secret`](secret.md))
is confined with no install root: it starts from the runner's own programs, never an installed
one. An installed tool runs inside the task, as the task's own; a remote service's tools stay
outside, called by the runner ([`mcp`](mcp.md)).

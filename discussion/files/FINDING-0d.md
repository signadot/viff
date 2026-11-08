# pkg step 0d (2k5art3x): non-root dependency resolution against the distro index

2k's first draft had the operator hand-write a catalog (name -> version -> source, digest, deps).
That is re-deriving a package manager's index by hand. The distro already publishes it. 0d tests
whether the runner can wrap the package manager instead: resolve a tool's dependency closure
against the signed distro index, as non-root, with the runner image's own installed set as the
base so the closure is only the delta. On the step-0 pod (Debian bookworm, uid 65532, no
capabilities); script `p13-resolve.sh`, log `p13-resolve.log`.

## Result: it works

- **The toolchain is in the image:** `apt-get`, `dpkg`, `gpgv`, and the Debian archive keyrings
  (bookworm, bullseye, trixie). 117 packages installed (the base).
- **`apt-get update` non-root, signatures verified.** With `Dir::State::Lists` and
  `Dir::Cache::Archives` pointed at a writable dir and `APT::Sandbox::User=` (no privilege drop,
  there is no `_apt` user), the InRelease was fetched and its signature checked, exit 0.
- **Resolution minus base is automatic, and needs no curated list.** Pointing apt at the image's
  real `/var/lib/dpkg/status`, `apt-get install -s --no-install-recommends jq` resolved to exactly
  `libonig5, libjq1, jq`. libc6 and libc-bin were not in the closure — subtracted, because the
  image already provides them, and the sandbox binds the image's `/usr` and `/lib`. `git` resolved
  to an **empty** closure: the image already ships it. So the base-provides set is just the
  image's own dpkg database.
- **`--print-uris` is the fetch plan:** a URL, size and hash for each package of the closure, which
  the runner fetches by. The per-file hash printed is MD5, but integrity rides the signed
  `Release -> Packages -> SHA256` chain that `apt-get update` already verified.

## What this settles for the design

The operator does not list installable tools. They choose a **package manager and the repos to
trust** (a `sources.list` / `repositories`), and the runner resolves each install against that
index. The base-provides set is the runner image's dpkg status, already there. The allowed set is
a **ceiling pattern over names**, not an enumeration (`{pkg: !glob "*"}` over a signed repo is
every tool in it, bounded by the autonomy asking). Multi-version comes from the index holding
several versions — multiple suites (the keyrings for bookworm/bullseye/trixie are already present)
or snapshot.debian.org.

Proven across p12 + p13: resolve (p13) -> fetch by the index's hashes -> extract non-root
(`dpkg-deb -x`, p12) -> RUNPATH `$ORIGIN` discipline (p12) -> run in a read-only sandbox (p12).
The remaining work is chaining these, and the apk variant (musl base) is untested.

## Caveat unchanged

Extract-only: maintainer scripts do not run, so packages needing postinst/alternatives/setuid do
not work — fine for CLI tools, libraries, compilers, interpreters; the wrapper must detect and
refuse the rest rather than install something half-configured.

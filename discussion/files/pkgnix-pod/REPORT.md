# pkg-nix step 0: the staging pod

Plan: git-issue `2b5art3x`, step 0. Throwaway: a pod on staging
(`my-cluster.example.ts.net`, namespace `pkgnix-try`) with the runner's image
(`signadot/verse-runner:0cc21d35`) and the runner pod's settings, and a 30Gi gp3 claim for the
store. Run 2026-11-07, 17:35–18:01Z.

## Verdict

**The plan holds in the real pod, with six changes.** In order of weight:

1. **`restrict-eval` refuses a flake's own inputs** unless `allowed-uris` names them. Named, it
   bounds the inputs too: the ceiling's sources become the list of what a flake may draw on.
2. **`trusted-settings.json` overrides `accept-flake-config = false`.** A saved "yes" in the
   runner's `XDG_DATA_HOME` applies a flake's extra cache and key. The file must not exist.
3. **Some store paths are symlinks.** In a store apart they point at `/nix/store/…`, which is not
   there on the runner's side, so `--ro-bind` of one fails. The sandbox recreates them with
   `--symlink`.
4. **No nixpkgs ships bwrap 0.13** (pinned, `nixpkgs-unstable`, `nixos-unstable`: all 0.12.0). It
   is built from source.
5. **A warm evaluation is 3.2 s, not 0.2 s,** and takes 460–580 MiB: a list through `--apply`
   gets no evaluation cache. The runner caches its own answer by the locked reference.
6. **nix needs a `HOME` its uid owns,** and the claim needs `fsGroup: 65532` to be writable.

## The pod

| | |
|---|---|
| Node | t3.large, Bottlerocket 1.68.0, kernel 6.18.54, containerd 2.2.9, amd64 |
| Pod | `hostUsers: false`, `procMount: Unmasked`, seccomp `Unconfined`, uid/gid 65532, every capability dropped, no privilege escalation, `fsGroup: 65532` |
| Volumes | `/nix` an emptyDir (standing in for the image's `/nix`), `/home/nonroot` an emptyDir, `/state` the gp3 claim (ext4, idmapped) |
| Inside | uid_map `0 267517952 65536`; `max_user_namespaces` 2147483647; `fs.mount-max` 100000 |

The image has no `xz`, so nix's tarball (2.35.4, hash checked against releases.nixos.org) was
decompressed outside and streamed in.

## What it found

**nix as 65532 (p1).** The installer failed with "cannot determine user's home directory": 65532
has no passwd entry, and nix will not use a `HOME` the uid does not own (the emptyDir is
root's). With `HOME` a directory 65532 made, it installed in 37 s.

**bwrap (p2, p2b, p3).**
- nixpkgs at the pinned rev, `nixpkgs-unstable` and `nixos-unstable`: bubblewrap 0.12.0.
- 0.13.1 built by nixpkgs' recipe with the source moved on: 7 s, a closure of 10 paths. The build
  failed under `/nix/var/nix/builds` (`chmod … Operation not permitted`, on the setgid
  directories `fsGroup` makes) and worked with `build-dir` in `/tmp`. Only the image's build
  builds; the runner never does.
- Wrap's flags run with 0.8.0 and 0.13.1 under `hostUsers: false`: uid 65532, CapEff 0,
  NoNewPrivs 1. With `--remount-ro /`, `/planted` is refused and the workspace and `/tmp` stay
  writable.

**The store apart (p4).** `nix --store /state/nix` on the claim. Each package by
`meta.outputsToInstall`: jq and ffmpeg-full give `bin` and `man`, gcc, postgresql and rustc
`out` and `man`, the rest `out`. Eleven packages, 471 paths, 4.2 GB, substituted from
cache.nixos.org in **52 s** from inside the cluster.

**The binds (p5).** From `/state/nix/nix/store/p` to `/nix/store/p`, `--remount-ro /` after,
median of 5:

| Binds | 0.8.0 | 0.13.1, `--args` | 0.13.1, command line |
|---|---|---|---|
| 471 (real) | 429 ms | 45 ms | 51 ms |
| 1000 | 1.47 s | 82 ms | 94 ms |
| 2000 | 4.76 s | 154 ms | 158 ms |
| 2980 | 9.77 s | 216 ms | 235 ms |
| 2990 | "Exceeded maximum number of arguments 9000" | same | same |

0.13 is linear, about 70 µs a bind: 5–7 times the local machine's, still a fraction of a
launch. 0.8 is quadratic and slower here than locally.

- **2 of the 471 paths are symlinks** (`gcc-wrapper-16.2.0-man` → `gcc-16.2.0-man`, and rustc's
  wrapper's), into `/nix/store/…`. On the runner's side that names nothing, so bwrap answered
  "Can't find source path". `--symlink <target> <path>` recreates the link; the target is in the
  closure and bound.
- **Inside:** node, go, python3, git, gcc, psql, magick, ffmpeg, rustc, cargo and jq ran from a
  `PATH` of 13 `bin` directories (the packages' own outputs, not the closure's). `/nix/store`
  lists 471. A store path refuses `chmod` and `touch` ("Read-only file system"), so does
  `/nix/store/planted`. `/state` is not there. The workspace is writable.

**Evaluating (p8, p8b, p8c).** A `nix.conf` with step 4's settings, `NIX_CONF_DIR`,
`NIX_USER_CONF_FILES` empty, `HOME` and `XDG_*` on the claim; nix in a bwrap with the system
directories and `/nix` read-only, the store apart and the nix home writable, the network shared.
The flake standing in for acme's: `verse.x86_64-linux.ci = [ jq go ripgrep ]`, nixpkgs as its
input, a `nixConfig` asking for another cache and key.

- `nix config show` reads every setting as written.
- **`restrict-eval` refuses the flake's own input:** "access to URI
  'github:NixOS/nixpkgs/4fe25e…?narHash=…' is forbidden in restricted mode".
  - `allowed-uris = github:NixOS/nixpkgs`: the list evaluates, to the outputs above.
  - `allowed-uris = github:acme`: the input is refused.
  - `allowed-uris = github:NixOS/nix` does not admit `github:NixOS/nixpkgs`: a prefix by path
    segment, not by character.
  - A URL fetched at evaluation stays refused under all three.
- **pure-eval:** reading `/state/secrets/probe` gives "access to absolute path … is forbidden in
  pure evaluation mode". In the bwrap, `/state/secrets` does not exist.
- **`nixConfig`** under `accept-flake-config = false`: "ignoring untrusted flake configuration
  setting 'extra-substituters'", and the key.
- **With a `trusted-settings.json` in the runner's own `XDG_DATA_HOME`** saying yes to both:
  "Using saved setting for 'extra-substituters = https://cache.example.invalid' from
  ~/.local/share/nix/trusted-settings.json", and the key. `accept-flake-config = false` does
  not stop a saved setting.
- **`github:`'s `host`** (a reference nothing has cached): nix asked
  `https://example.invalid/api/v3/repos/NixOS/nixpkgs/…`. With a rev the git cache already
  held, the same reference resolved from the cache and fetched nothing.
- **Cost:** five nixpkgs packages through `--apply`, cold (the nix cache emptied, nixpkgs
  unpacked into its git cache): **36–41 s, 578 MiB**. Warm: **3.2 s, 462 MiB**.

**Concurrency (p7).** In the store apart, after a collection took everything unrooted:
- ffmpeg-full (367 paths) and imagemagick (89) realised at once: both rc 0, 12 s together, every
  path there, `nix store verify` clean.
- A collection 3 s into a realise of ffmpeg-full: rc 0 in 0.8 s, deleted nothing, did not wait
  for the realise. The realise finished (rc 0, 11.6 s) with all 367 paths.
- `nix-store --add-root during --realise` of two paths named its roots `during-1` and
  `during-2`.

**Roots (p6, p6b).** In the store apart:
- `nix-store --store /state/nix --add-root /state/roots/task-1 --indirect --realise <go>`: one
  command, rc 0, registered in `/state/nix/nix/var/nix/gcroots/auto`. `nix build --out-link`
  does the same.
- A collection: go and its closure (9 of 9) kept; jq, postgresql and imagemagick, unrooted,
  gone. The root removed, go goes.
- A task running jq from the store apart, with no root: kept through a collection (nix reads the
  process's `/nix/store/…` mappings, the names the store apart uses), collected after it ends.

## What it does not settle

- The store on a node the pod moves off (the claim's zone).
- A real acme flake, a private flake, a private cache.
- A whole launch through runnerd: this ran the pieces by hand.
- t3.large is burstable; the bind timings are one node's, at one moment.

## Files

`pod.yaml` (the namespace, the claim, the pod), `run.sh` (runs one script in the pod, naming
the context), `in/` (lib.sh, then p0–p8c), `logs/`. To clean up:
`kubectl --context my-cluster.example.ts.net delete namespace pkgnix-try`.

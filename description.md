# local deployments: a data dir is one verse, with one owner

There are two local layouts, and they share state without either knowing of the other.

- **A bare `verse up`**: one process. It derives the data dir from `--addr` (`~/.verse/<host>_<port>`, cmd/verse/localrunner.go), adopts a docd it finds there by a TCP dial (cmd/verse/docd.go), and starts a runner it registers as `local`.
- **PR #63 (pitchfork)**: the store (`o sys up --data ~/.verse/localhost_9797`), versed (`verse up --docd-addr … --local-runner=false`, the issuer in the config) and a runnerd, each a daemon of its own, as staging is.

They meet in `~/.verse/localhost_9797`. It is the same store, and the same `issuer.pem` (versed/issuer.go `IssuerKeyIn`). The issuer URL is the same too, so to a runner the two are one verse. A stale `verse.runners.local` can also be left in that store.

The fixes planned in the `verse up` issues each cover one corner of the bare layout, and none covers where the two meet:

- **vq2pp5j0** (data dir follows how `--addr` is spelled): normalising the key moves the dir PR #63 hardcodes, unless the key for loopback stays `localhost_<port>`.
- **tv2pp5j0** (one verse per data dir): a lock taken by `verse up` does not cover PR #63, where versed holds no data dir and `o sys up` holds the store. go-tony v0.0.249 takes no lock on it. A bare `verse up` beside a running pitchfork store finds no `docd.addr` (only `verse up` writes it). It starts a second logd and docd on the dir, and then fails to bind 9797.
- **tz2pp5j0** (local runners left running): a `local` registration left by a crashed bare `verse up`, or by a `stop()` that gave up, stays in the store. Nothing removes it under `--local-runner=false`, and routing does not set `local` apart: the only special case is a permission check, versed/runners.go:49. The orphan takes PR #63's versed's tokens, since the issuer is the same.
- **v32pp5j0** (runnerd version): PR #63 does not reach it. Its setup builds verse, runnerd and verse-call-tool from one tree, so the versions match.
- **pd32ag16** (an unreachable store at boot is the steady state's first iteration): what `/healthz` says before the store answers is then what a supervisor's readiness waits on. That is pitchfork's `ready_http`, and anything like it.

## The model

A data dir is one verse: its store, its issuer key and its registrations. It has one owner at a time. What the plan settles:

1. **Naming.** Is a data dir named for what it is, or derived from `--addr`? A derived default is derived one way (vq2).
2. **Ownership.** Whatever writes the store holds the lock: `o sys up` where it runs alone, so the lock is go-tony's, or a lock both honour (tv2).
3. **Who runs the runner.** Inside a bare `verse up`, verse does; in a split layout, the supervisor does. Where verse hosts no runner, `local` means nothing: it is cleared or not routed to. Whoever registers `local` stops it (tz2, v32).
4. **Boot.** What readiness says while the store is unreachable (pd32).
5. **The split layout is supported.** PR #63 is a second layout, held to the product's paths, and not a recipe page.

PR #63 with a data dir of its own conflicts with none of the planned fixes, so it can land ahead of the plan.
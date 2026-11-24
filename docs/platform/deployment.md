# What a deployment is

A deployment of viff is the same four parts wherever it runs: the store, viffd, the runners and
the iods. A kind of deployment is how it fills each one in: where the part runs, what it listens
on, where its state is, how it proves itself, and the name of the knob or setting that says so.

Two kinds exist, beside a plain `viff up`, which is no kind (see the end of this page):

- **Kubernetes**, in [`deploy/`](../../deploy/README.md): one `o build`, rendered with a profile
  (`deploy/profiles/<name>.tony`) and knobs (`build.tony`'s `env`, or `-e knob=value`), and
  applied with `make deploy PROFILE=<name>`. eks and minikube are its profiles.
- **Local**, in [A viff on your machine](local-viff.md): pitchfork daemons
  ([`pitchfork.toml`](../../pitchfork.toml)), from settings
  ([`scripts/viff-local.env`](../../scripts/viff-local.env), with yours in
  `~/.config/viff-local.env`) that `scripts/viff-local.sh setup` renders.

A new kind fills in the same rows. Nothing checks that it does: this page is the list, with names
only, and there is no schema shared by the kinds.

## The parts

**The store** is logd and docd (`o sys up`) on a data dir. It holds every document and every
patch, and keeps every patch back to its compaction cutoff. A read at a commit older than that is
refused, so the cutoff bounds how long a gate, a hook or a sequence step can wait and still read
what it fired on. It is the stateful part: losing its data dir loses the viff.

**viffd** is the engine and the API (`viff up`), attached to the store's client face. It is a
singleton, since two engines would fire every trigger twice. It signs the token it presents to
each runner, minted for that runner alone, as the issuer its config names, with a key the
deployment keeps. A runner proves itself with a token of its own, so the handshake runs both ways.

**A runner** is where a task runs: `runnerd`, with a config naming the agents and scripts it
offers and their ceilings, a token, and the credentials those agents need. Deploying one does not
install it: viffd routes to a runner registered with `viff runner add`, which probes it.

**An iod** reflects an outside system into the store and acts back out (`iod`). Each is a singleton
writer of its slice, and, like a runner, is installed with a verb (`viff iod add`), not by
deploying it.

## Filled in

| | Kubernetes: `build.tony` knob | Local: `scripts/viff-local.env` setting |
|---|---|---|
| **The deployment** | | |
| its description | a profile, `deploy/profiles/<name>.tony`, and `-e knob=value` | `~/.config/viff-local.env`, over the defaults |
| applied by | `make deploy PROFILE=<name>` | `scripts/viff-local.sh setup`, then `pitchfork start -l` |
| where | `kubeContext`, `namespace` | this machine, as you |
| the binaries | `image`, `imagePullPolicy`, `imagePullSecret` | built from the checkout into `VIFF_LOCAL_BIN` |
| **The store** | | |
| runs as | the `docd` StatefulSet | the `store` daemon |
| its data | a volume: `storageClassName`, `storageSize`, or `hostPV` and `hostPath` | `VIFF_LOCAL_DATA` |
| its faces | the `docd` Service (`:7071`), on the tailnet with `tailscaleExpose` | `VIFF_LOCAL_LOGD_ADDR`, `VIFF_LOCAL_DOCD_ADDR`, `VIFF_LOCAL_DOCD_MOUNT_ADDR`, `VIFF_LOCAL_STORE_ADMIN_ADDR` |
| compaction cutoff | `docdCompactionCutoff` (eks: `1d`) | `VIFF_LOCAL_COMPACTION_CUTOFF` (`1d`) |
| placement and memory | `docdPool`, `docdPoolLabel`, `docdMemoryRequest`, `docdMemoryLimit`, `docdGoMemLimit` | none |
| **viffd** | | |
| runs as | the `viffd` Deployment | the `viffd` daemon, `--local-runner=false` |
| its address | the `viff` Service (`:9797`), on the tailnet with `tailscaleExpose` | `VIFF_LOCAL_ADDR` |
| its config | `authConfig`, a file under `deploy/` | `VIFF_LOCAL_CONFIG` |
| the issuer | `issuer`; its key the `viff-issuer` Secret, from `issuerSecretKey` | `http://<VIFF_LOCAL_ADDR>`; its key `issuer.pem` in the data dir |
| the system charter | `systemCharter` | none |
| its LLM | `llmSecretKey`, `llmModel` | none |
| **The runners** | | |
| which | `runner`, `codexRunner`, `claudeRunner`, each with its `…Image` | one, the `runner` daemon: scripts and claude |
| its address | its Service (`viff-runner:9802`, …) | `VIFF_LOCAL_RUNNER_LISTEN` |
| its config | the ConfigMap in `deploy/<runner>/<runner>.yaml`, or a file of the deployment's: `runnerConfig`, `codexRunnerConfig`, `claudeRunnerConfig` | `runner.tony` in `VIFF_LOCAL_RUNNER_DIR` |
| how it proves itself | its pod's ServiceAccount token (`runnerTokens`) | `token` in `VIFF_LOCAL_RUNNER_DIR` |
| its credentials | `runnerSecretKey`, `codexSecretKey`, `claudeSecretKey` | `env` in `VIFF_LOCAL_RUNNER_DIR` |
| its [secret store](../reference/runnerd.md#the-secret-store) | a projected volume at `/secrets` in each runner: the Slack and Linear iods' tokens as the files `slack-bot` and `linear`, and `viff-runner-secrets` (`runnerStoreKey`) whole; a system acts with it where the runner's config names its secret (`secrets.systems`) | none: the runner reads its `env` |
| registered | by hand, `viff runner add` | by setup, as `VIFF_LOCAL_RUNNER_NAME` |
| its sandbox | bubblewrap, in the pod | bubblewrap on Linux, none on macOS; `VIFF_SANDBOX` in its `env` |
| **The iods** | | |
| which | `gitIod`, `gitIssueIod`, `slackIod`, `linearIod`, `githubIod` | none |
| how each proves itself | its pod's ServiceAccount token (`iodTokens`) | none |
| their credentials | `gitSecretKey`, `slackSecretKey`, `linearSecretKey`, `githubSecretKey` | none |
| installed | by hand, `viff iod add` | none |
| **Clients** | | |
| Claude Code | none | the MCP server `VIFF_LOCAL_MCP_NAME`, added by setup |

A "none" is a part the kind leaves out, not a value: the local kind runs no iod and gives viffd no
LLM, and a viff agent's model is reached from the runner's `env` there (`PORTKEY_API_KEY`). Every
Kubernetes knob is in [deploy/README.md](../../deploy/README.md#knobs-buildtony-env-override-with--e-or-a-profile),
and every local setting in `scripts/viff-local.env`, with its default.

A plain `viff up` is a third arrangement rather than a kind: one process starts the store and a
runner of its own, with its cutoff from `compactionCutoff` or `--compaction-cutoff`
([Starting a viff](../reference/up.md)). It is for a first try.

# What a deployment is

A deployment of verse is the same four parts wherever it runs: the store, versed, the runners and
the iods. A kind of deployment is how it fills each one in: where the part runs, what it listens
on, where its state is, how it proves itself, and the name of the knob or setting that says so.

Two kinds exist, beside a plain `verse up`, which is no kind (see the end of this page):

- **Kubernetes**, in [`deploy/`](../../deploy/README.md): one `o build`, rendered with a profile
  (`deploy/profiles/<name>.tony`) and knobs (`build.tony`'s `env`, or `-e knob=value`), and
  applied with `make deploy PROFILE=<name>`. eks and minikube are its profiles.
- **Local**, in [A verse on your machine](local-verse.md): pitchfork daemons
  ([`pitchfork.toml`](../../pitchfork.toml)), from settings
  ([`scripts/verse-local.env`](../../scripts/verse-local.env), with yours in
  `~/.config/verse-local.env`) that `scripts/verse-local.sh setup` renders.

A new kind fills in the same rows. Nothing checks that it does: this page is the list, with names
only, and there is no schema shared by the kinds.

## The parts

**The store** is logd and docd (`o sys up`) on a data dir. It holds every document and every
patch, and keeps every patch back to its compaction cutoff. A read at a commit older than that is
refused, so the cutoff bounds how long a gate, a hook or a sequence step can wait and still read
what it fired on. It is the stateful part: losing its data dir loses the verse.

**versed** is the engine and the API (`verse up`), attached to the store's client face. It is a
singleton, since two engines would fire every trigger twice. It signs the token it presents to
each runner, minted for that runner alone, as the issuer its config names, with a key the
deployment keeps. A runner proves itself with a token of its own, so the handshake runs both ways.

**A runner** is where a task runs: `runnerd`, with a config naming the agents and scripts it
offers and their ceilings, a token, and the credentials those agents need. Deploying one does not
install it: versed routes to a runner registered with `verse runner add`, which probes it.

**An iod** reflects an outside system into the store and acts back out (`iod`). Each is a singleton
writer of its slice, and, like a runner, is installed with a verb (`verse iod add`), not by
deploying it.

## Filled in

| | Kubernetes: `build.tony` knob | Local: `scripts/verse-local.env` setting |
|---|---|---|
| **The deployment** | | |
| its description | a profile, `deploy/profiles/<name>.tony`, and `-e knob=value` | `~/.config/verse-local.env`, over the defaults |
| applied by | `make deploy PROFILE=<name>` | `scripts/verse-local.sh setup`, then `pitchfork start -l` |
| where | `kubeContext`, `namespace` | this machine, as you |
| the binaries | `image`, `imagePullPolicy`, `imagePullSecret` | built from the checkout into `VERSE_LOCAL_BIN` |
| **The store** | | |
| runs as | the `docd` StatefulSet | the `store` daemon |
| its data | a volume: `storageClassName`, `storageSize`, or `hostPV` and `hostPath` | `VERSE_LOCAL_DATA` |
| its faces | the `docd` Service (`:7071`), on the tailnet with `tailscaleExpose` | `VERSE_LOCAL_LOGD_ADDR`, `VERSE_LOCAL_DOCD_ADDR`, `VERSE_LOCAL_DOCD_MOUNT_ADDR`, `VERSE_LOCAL_STORE_ADMIN_ADDR` |
| compaction cutoff | `docdCompactionCutoff` (eks: `1d`) | `VERSE_LOCAL_COMPACTION_CUTOFF` (`1d`) |
| placement and memory | `docdPool`, `docdPoolLabel`, `docdMemoryRequest`, `docdMemoryLimit`, `docdGoMemLimit` | none |
| **versed** | | |
| runs as | the `versed` Deployment | the `versed` daemon, `--local-runner=false` |
| its address | the `verse` Service (`:9797`), on the tailnet with `tailscaleExpose` | `VERSE_LOCAL_ADDR` |
| its config | `authConfig`, a file under `deploy/` | `VERSE_LOCAL_CONFIG` |
| the issuer | `issuer`; its key the `verse-issuer` Secret, from `issuerSecretKey` | `http://<VERSE_LOCAL_ADDR>`; its key `issuer.pem` in the data dir |
| the system charter | `systemCharter` | none |
| its LLM | `llmSecretKey`, `llmModel` | none |
| **The runners** | | |
| which | `runner`, `codexRunner`, `claudeRunner`, each with its `…Image` | one, the `runner` daemon: scripts and claude |
| its address | its Service (`verse-runner:9802`, …) | `VERSE_LOCAL_RUNNER_LISTEN` |
| its config | the ConfigMap in `deploy/<runner>/<runner>.yaml`, or a file of the deployment's: `runnerConfig`, `codexRunnerConfig`, `claudeRunnerConfig` | `runner.tony` in `VERSE_LOCAL_RUNNER_DIR` |
| how it proves itself | its pod's ServiceAccount token (`runnerTokens`) | `token` in `VERSE_LOCAL_RUNNER_DIR` |
| its credentials | `runnerSecretKey`, `codexSecretKey`, `claudeSecretKey` | `env` in `VERSE_LOCAL_RUNNER_DIR` |
| its [secret store](../reference/runnerd.md#the-secret-store) | a projected volume at `/secrets` in each runner: the Slack and Linear iods' tokens as the files `slack-bot` and `linear`, and `verse-runner-secrets` (`runnerStoreKey`) whole; a system acts with it where the runner's config names its secret (`secrets.systems`) | none: the runner reads its `env` |
| registered | by hand, `verse runner add` | by setup, as `VERSE_LOCAL_RUNNER_NAME` |
| its sandbox | bubblewrap, in the pod | bubblewrap on Linux, none on macOS; `VERSE_SANDBOX` in its `env` |
| **The iods** | | |
| which | `gitIod`, `gitIssueIod`, `slackIod`, `linearIod`, `githubIod` | none |
| how each proves itself | its pod's ServiceAccount token (`iodTokens`) | none |
| their credentials | `gitSecretKey`, `slackSecretKey`, `linearSecretKey`, `githubSecretKey` | none |
| installed | by hand, `verse iod add` | none |
| **Clients** | | |
| Claude Code | none | the MCP server `VERSE_LOCAL_MCP_NAME`, added by setup |

A "none" is a part the kind leaves out, not a value: the local kind runs no iod and gives versed no
LLM, and a verse agent's model is reached from the runner's `env` there (`PORTKEY_API_KEY`). Every
Kubernetes knob is in [deploy/README.md](../../deploy/README.md#knobs-buildtony-env-override-with--e-or-a-profile),
and every local setting in `scripts/verse-local.env`, with its default.

A plain `verse up` is a third arrangement rather than a kind: one process starts the store and a
runner of its own, with its cutoff from `compactionCutoff` or `--compaction-cutoff`
([Starting a verse](../reference/up.md)). It is for a first try.

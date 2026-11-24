# Starting a viff

`viff up` runs the engine and the task runtime, and it brings up the
[store](../glossary.md#store). Every other subcommand is a client of `viff up`.
[The tutorial](../tutorial/README.md#start) starts one with no flags. This page says what those
defaults are, and what to give instead.

```sh
export VIFF_ADDR=localhost:9797   # where clients reach the viff (this is the default)
viff up
```

To point a client at another address, `export VIFF_ADDR=host:port`, or pass
`-addr host:port` to any command. `viff down` stops the viff on `-addr`, and it stops the
daemons that the viff started.

```sh
VIFF_LOG=debug viff up            # the engine's trace: which rule fired, on what, and why not
```

**Use `VIFF_LOG` when a [rule](../glossary.md#rule) does not fire.** At `debug`, the engine
logs these things:

- which rules it evaluated, at which [commit](../glossary.md#commit);
- which [subjects](../glossary.md#subject) each rule selected;
- why a [firing](../glossary.md#firing) stopped: a read that did not resolve, a write that
  viff refused, or a cycle that the engine threw.

No other output tells a rule that selects nothing from a rule that selects and fails.
`VIFF_LOG` takes any slog level: `debug`, `info`, `warn` or `error`. Viff refuses any other
value at startup. It does not ignore it.

`up` decides two things for the whole runtime: the store, and the local runner it starts beside
itself. `up` decides them because it hosts the runtime, and the clients do not.

## The store

By default, `up` uses docd. It brings up logd and docd on its **data directory**, through
`o sys up`: `~/.viff/<host>_<port>` for its `--addr` (`~/.viff/localhost_9797` by default), or
under `$VIFF_HOME` where that is set, made on the first run and kept, so a bare `viff up` comes
back to the same viff. `--data` names another. It prints the directory. The data dir also holds
the local runner's configuration and token, and the issuer's key; `rm -rf ~/.viff` is the whole
wipe. The store is then
[docd](https://signadot.github.io/tony-format/docd/), the tony-format document store.

You need to know nothing about docd to try viff, because `up` starts it for you. docd gives
viff its deeper properties:

- copy-on-write [scopes](../glossary.md#scope);
- conditional and coordinated writes;
- watches that report every edge;
- compactions.

See [store.md](store.md).

```sh
viff up -data /srv/viff                    # a data dir of your choosing
viff up -docd-addr host:port                # attach to an already-running docd
viff up -compaction-cutoff 36h              # keep every patch for 36 hours, not a day
viff up -logd-config logd.tony              # start logd with this configuration
```

**The store keeps every patch for a day.** `up` writes the logd configuration for the store it
starts into the data dir, as `logd.viff.tony`, on every start. It sets one thing, the
compaction cutoff: inside it a read at a commit is exact, and past it history thins to logd's
snapshots. viff reads at a firing's commit when the action runs, after a gate, at a hook, at a
sequence step, so the cutoff has to be longer than any of those waits. A completion hook waits
as long as its task. The default is `1d`, as on staging. `-compaction-cutoff`, or
`compactionCutoff` in the config, sets another, as logd reads a duration (`36h`, `2d`, `1w`).
The file names where its cutoff came from.

`-logd-config` replaces that file with yours, cutoff included, so viff refuses it beside
`-compaction-cutoff`. Both configure a logd that `up` starts, so viff refuses them beside
`-docd-addr`: a store that `up` does not start is configured where it runs.

**A store call has a time limit, and reads and writes have different limits.**

| Call | How it runs | Its limit |
|---|---|---|
| a read | beside the request loop of logd | a limit of its own |
| a write | on the request loop of logd, behind every other write of the session | a separate number |

Both limits are constants in `pkg/entity/logdstore.go`. When the connection goes quiet, the
session connects again. It does not fail the caller.

**The readiness probe answers from this process alone.** `/healthz` reports three things:
`ok`, whether the engine is restored, and the head revision this process has. It reads
nothing from the store. `/healthz` answers `503` until the engine is restored. A viff that is
restoring is not stuck: viffd serves requests before the restore finishes. So the probe that
says "no traffic yet" also says which of the two cases it is. `/status` reads the store, and it
is not a probe.

The reason `/healthz` reads nothing: a question about readiness comes under load, and that
load would delay an answer that reads the store.

### When a client cannot reach docd where viff does

`up` publishes the client address of docd on `GET /status`. A store client that runs as its own
process beside viff finds docd there.

The client reads the address from where the client runs. That is not always where viff
runs. In a cluster, viff reaches docd by the name of a Service, and that name resolves only
inside the cluster. A laptop that gets that name gets a true address that it cannot use.

`-docd-advertise-addr` says what to publish. Viff still connects to `-docd-addr`. Only the
published answer changes:

```sh
viff up -docd-addr docd:7071 -docd-advertise-addr viff-docd:7071
```

The advertise flag needs the address flag that it replaces. Viff refuses
`-docd-advertise-addr` without `-docd-addr`. When viff starts docd itself, docd listens on
temporary ports, and a published address for them would not stay true.

Viff publishes one address for docd. So you choose which clients the address is
for. A deployment that clients reach in two ways publishes the address that its controllers
use. `deploy/README.md` is a worked example. There, the pair is the tailnet names of the two
Services that viff connects to inside the cluster.

## The local runner

`up` starts a [runner](../glossary.md#runner) beside itself, as it starts logd and docd: a
`runnerd` ([runnerd.md](runnerd.md)), registered as `mirror.runners.local`. It is a runner like any
other: viff routes a spawn to it when it offers the spawn's doer and holds what the spawn asks
([where a spawn runs](../runners.md#where-a-spawn-runs)), beside any runner registered with this
viff. What it offers depends on the model credentials.

| Credentials | On offer |
|---|---|
| configured: an `llm` block in `~/.config/viff.tony`, or `PORTKEY_*`/`OPENAI_MODEL` in the environment | the `viff` agent, and scripts |
| none | scripts |

The **server** reads the credentials and hands them to the runner. So set them before `up`, and
not in the environment of the client. `viff status` shows an `llm` block with `available` and a
masked key.

Each spawn says who does its work: a `script`, or a `prompt` for an [agent](../glossary.md#agent),
the one its `agent` names or else viff's default agent (`defaultAgent`, the `viff` agent unless
the config says another). Viff checks a spawn against what the runners offer where it is
written: with no credentials configured, and no runner registered that offers the `viff` agent,
a charter whose prompt is for it is refused at its install, and `task run` at the prompt, each
saying no runner offers it. The engine keeps running. What decides each step is in
[tasks.md](tasks.md#the-dial).

**How it runs.** Each task works in a workspace of its own, as on a deployed runner. On Linux,
where bubblewrap runs, its tasks are confined as a deployed runner's are; elsewhere (a Mac has no
bubblewrap), or where bubblewrap does not run, they run unconfined, as you, and `up` says so at
start. Unconfined, a runner's `shellEnv` is a declaration and nothing more. The runner builds the
host's network alone where it is unconfined, so a spawn it runs says `network: {mode: open}`.

**How it proves itself.** `up` makes a static token on the first run on a data dir
(`local-runner.token`, 0600, 64 hex digits) and keeps it; the registration names it by its hash,
and the runner reads it ([who the runner is](../runners.md#who-the-runner-is)). Its
configuration, `local-runner.tony`, is rewritten on every `up`: the viff it serves, at this
run's `--addr`, and the port it listens on, the one the last run recorded unless something else
holds it. The runner says hello when it starts, and is described within seconds. Rotating the
token means removing the file and restarting `up`: a runner holding the old one fails its
handshake, and is passed over until it is restarted on the new.

**Which `runnerd`.** `up` looks beside the `viff` executable first (`make bin` puts both
there), then in `~/.viff/bin/<version>/`, which counts once it holds both `runnerd` and
`viff-call-tool`, then on `PATH`; where none is found and this viff
is a release, it installs `runnerd` and `viff-call-tool` of that release into
`~/.viff/bin/<version>/` with `go install`, as it installs `o`. A `viff` built from a checkout
is no release: Go stamps it a pseudo-version, which names a commit, with `+dirty` where the tree
had changes, and nothing installs it by that. `up` refuses it with how to build the runner
beside it. `--runnerd-bin` names one.

A `runnerd` found beside `viff` or on `PATH` has to be the same build as `viff`: the same
release, or built from the same commit of the same tree. One of another build is refused, with
both builds named, since it may describe itself in a form this viff refuses, and the runner
would then fail to register a step away from the cause. Build both together (`make bin`, or
`go build -o <dir> ./cmd/viff ./cmd/runnerd ./cmd/viff-call-tool`). A `runnerd` that
`--runnerd-bin` names is used whatever its build, and `up` says so when it is another.

**Across a crash.** `up` finds what a crashed run left by dialing, not by a pid. The runner that
answers at the recorded port and proves this data dir's token is used as it stands, its tasks
with it, and is asked every 30 seconds whether it still answers, so one is started in its place
when it does not; nothing answers, or something else does, and a runner is started, and `up` says
why it did not use what answered. A runner left serving another `--addr` on the same `--data`
does not prove itself to this viff, takes no stop from it, and runs on until it is stopped by
hand. The store pair is found
the same way, at the docd address the last run recorded in the data dir (`docd.addr`). A runner
that exits while `up` runs is started again, waiting longer each time it exits quickly.

**When it stops.** `viff down`, or ctrl-c, asks the runner to stop (`POST /stop`, which the
runner takes from its viff alone), waits for it, and removes its registration (a runner this run
did not start is waited on by dialing, and its process can outlive `viff down` by a few seconds
while it drains its connections); logd and docd
are stopped with it. A store pair `up` found running rather than started is left running, and
`up` says so: the next `up` on that data dir uses it. After a crash, with no `up` running,
`viff down` finds nothing to stop; the next `up` on the data dir finds what the crashed one left,
and its `viff down` stops the runner.

`viff status` says how the local runner is, asked of it then and never stored: under `runner`,
its registration, its url, whether it is `described` and what it `offers`, and where it is not
described, `why`.

```sh
viff up --local-runner=false     # start no local runner: every spawn goes to a registered runner
```

`--local-runner=false`, or `localRunner: false` in the config, is a central viff's: `up` starts
no runner, and hosts nothing. A prompt that names no agent is for `defaultAgent`, `viff` unless
the config says another.

## The issuer

A registered runner takes this viff's calls by the tokens it signs as an issuer
(`auth.issuer`, below), each minted for that runner alone
([who the runner is](../runners.md#who-the-runner-is)). So `viff up` is always an issuer:
where its config names no `auth.issuer`, it is one at its own address (`http://<--addr>`, with
`localhost` for an address that binds every interface), signing with a key it makes in its data
dir as `issuer.pem` and keeps there, so a restart signs as the same issuer. With `--docd-addr`
it has no data dir, and the key is made for that process alone: a restart replaces it, and
every token signed by the last. `viff status` says which, under `issuer`: its `url`, and `key`,
which is `config`, the file, or `memory`; `viff up` prints the same at start. A runner holds
viffd's tokens to that url exactly, so its `viff.url` spells it the same way (`localhost`, not
`127.0.0.1`), or its `viff.issuer` names it ([runnerd.md](runnerd.md#configuration)).

Every runner uses the same seam, and every `viff task` verb works on each. See also
[task control](../task-control.md) and [tasks.md](tasks.md).

## Configuration

Defaults come from layers. Each layer overrides the one before it:

1. `/etc/viff.{tony,json,yaml}` — system base
2. `~/.config/viff.{tony,json,yaml}` — per user. It patches the system file. The patch is a
   merge, so you set only the keys that you want to change, and the other keys come from the
   layer below.
3. environment variables
4. CLI flags

The file name of a layer gives its format: `.tony`, `.json`, `.yaml` or `.yml`. The two layers
may use different formats. `/etc/viff.json` patched by `~/.config/viff.tony` merges
correctly, because viff parses both to the same tree before the merge. A config file cannot
declare its own format, so its name declares it. Two files for one layer are an error. Viff
does not pick one by a hidden rule. `VIFF_CONFIG=<file>` replaces both file layers.

A missing file is a layer that is absent. An example `~/.config/viff.tony`:

```tony
addr:  "localhost:9797"
scope: "me"
user:  "you@example.com"
llm: {
  model:      "gpt-4o"
  apiKey:     "pk-…"
  virtualKey: "vk-…"
}
```

The table below lists every key. The paragraphs that follow say what the table does not.

**`addr` and `scope`** say which viff to talk to, so every command reads them.

**`user`** is sent with each request, as `Viff-Principal`. The server writes it on every write
that the request makes. It checks each write and each read against what the
[address](../glossary.md#address) permits. The user owns what the request creates.

`user` is a [principal](../glossary.md#principal) in the
[grammar](../perms.md#principals-and-grantees). A bare email goes on the wire as
`user:<email>`. A runner's identity is `runner:<identity>`. Viff refuses a bare word.

Viff takes `user` as given. `-as` overrides it on the commands that take `-as`. When the
config names a user, those commands do not require `-as`. Where the viff names an identity
provider, `viff account login` proves who you are instead, and the CLI then acts as that person
whatever `user` says ([your account](perms.md#your-account)).

With no `user` and no `agent`, the CLI acts as the local user, `user:<login>@localhost`,
toward a viff on this machine (`localhost`, `127.0.0.1`, `::1`). Toward any other viff, it
names nobody.

**`agent`** names an operator agent instead of a user. An operator agent is a harness that
drives viff through the API. It is a principal of its own kind, and not the person whose
terminal it runs in. When `agent` is set, every request is `agent:<name>`, and `-as` still
names somebody else.

**`systemCharter`** is read by the server. It is the path of a charter file that `viff up`
installs at start as `system:charter`. These are the rules that the deployment itself stands
behind. A request cannot be made as the system, so this key is the way to install them.

**`repoCharters`** is read by the server. Each entry has `viff up` install the
[repo-charters reconciler](examples/repo-charters/repo-charters.tony) for one repository, as
the system, beside the file above: the charter that keeps what runs in viff equal to the last
state of the repository's charters directory on its branch that the team said yes to. `repo` is the
git iod's id for the repository (`git.<repo>`), `github` is the repository as GitHub names it
(`org/name`), `branch` is the branch the charters follow (`main` by default), `at` is the address the repository's charters install at, `tree` is the name the
git iod and the github iod report the charters directory's tree under (each one's `-tree`;
`charters` by default), and `answerers` is who may answer the question a change raises, as a
gate's `answerers` names them; the rendering adds the git iod (`agent:git-iod`), whose minute
sweep settles a question nothing proposes any more as `obsolete`, and an entry without
`answerers` asks the docket, whose questions are not swept. The git iod installs at `at` as
itself, so it owns that address or holds `child-owner` above it. The question is asked on a pull request onto the branch that changes the
charters, which the github iod reports only with its `-tree`; without it, a change is asked about
once it reaches the branch.

```tony
repoCharters:
- repo: app
  github: acme/app
  at: repos.app
  answerers: ["user:*"]
```

The config is what the system runs. At start, viff takes down each charter package the
system installed that neither `systemCharter` nor `repoCharters` names, with what it kept at its
address. So dropping an entry from `repoCharters` turns that repository's reconciler off at the
next start: the reconciler goes, with its state under `repo-charters.<repo>`, and the charters it
installed at `at` stay as they are.

**`auth: {principal: {…}}`** says how people are authenticated. The server reads it to verify a
request's token ([who a request is from](perms.md#who-a-request-is-from)). Without it, a
request is whoever it says it is. The CLI reads the same `providers` to know where
`viff account login` can log in, so a person's config carries the block as the server's does. It
ignores `mode`.

```tony
auth:
  principal:
    mode: optional
    providers:
      corp:
        issuer: https://login.example.com
        clientId: viff-cli
        domains: [example.com]
      github:
        iods: [github]
      slack:
        iods: [workspace]
        domains: [example.com]
```

- `mode` is `off`, `optional` or `required`. Unset is `off`: no token is verified. Under
  `optional`, a token is verified where a request carries one, and a request without one is
  still whoever it says it is. Under `required`, a request that names a person and carries no
  token is refused. The modes verify people. A runner is verified where its registration names
  its token's subject ([runners at the door](perms.md#runners-at-the-door)); an agent is taken
  as given ([who a request is from](perms.md#who-a-request-is-from)).
- `providers` are the parties whose word says which account a person is. Each is under a name
  the deployment gives it. The name is where its accounts are bound (`mirror.identity.<name>`),
  so it is one plain word, and `iod` is the iods'. A provider is one of two kinds:
    - **An issuer**, with `issuer`, the OpenID Connect issuer's URL, and `clientId`, the OAuth
      client registered there for this viff: a public client. Quote a `clientId` that starts
      with a digit. The viff accepts its id_tokens. `clientSecret` is for an issuer that hands
      a public client a secret and wants it back at its token endpoint, as Google's device flow
      does; it is not confidential (every copy of the CLI carries it), the CLI sends it where it
      is set, and the server never reads it.
    - **Iods**, with `iods`, the installed iods the viff trusts for the provider's accounts, by the
      name each is installed under. `github`'s iods reflect the comments that prove a GitHub
      account, and `slack`'s iod relays the member who typed
      ([a person relayed by a trusted iod](perms.md#a-person-relayed-by-a-trusted-iod)). GitHub
      is no issuer for people, so `github` is always of this kind.
- `domains` are the email domains a person may arrive from the first time, through a token or
  relayed by a trusted iod. With none, only an account that is already bound is admitted.
- `acts`, on a provider with iods, is what a trusted iod may do as a person it relays: `control`
  (unset), the reads and the acts that answer, say and steer, which is all an iod does; or `any`.

`viff up` refuses to start on an `auth` block it cannot serve. It does not read a provider at
start, so a provider that is down stops only the requests that carry its tokens.

**`auth: {iod: {…}}`** says how the deployment's own iods are authenticated
([iods at the door](perms.md#iods-at-the-door)). The server reads it.

```tony
auth:
  iod:
    issuer: https://oidc.eks.us-west-2.amazonaws.com/id/EXAMPLED539D4633E53DE1B71EXAMPLE
    audience: viff
    accounts:
      "system:serviceaccount:viff:slack-iod":  {principal: "agent:slack-iod", iod: workspace}
      "system:serviceaccount:viff:github-iod": {principal: "agent:github-iod", iod: github}
    static:
      - {file: /var/run/viff/git-iod.token, principal: "agent:git-iod"}
```

- `issuer` is the issuer of the iods' workload tokens. On Kubernetes it is the cluster's own,
  which signs a pod's projected service-account token; `kubectl get --raw
  /.well-known/openid-configuration` prints it, and on EKS it is
  `aws eks describe-cluster --query cluster.identity.oidc.issuer`. Viff reads its keys as it
  reads a provider's.
- `audience` is what the tokens' `aud` holds, which is what the pod's token was asked for.
  Unset is `viff`.
- `ca` is the path of the PEM bundle the issuer's certificate is checked against, for an issuer
  no public root signed: a cluster whose issuer is its own API server (minikube, kubeadm), whose
  certificate a pod has at `/var/run/secrets/kubernetes.io/serviceaccount/ca.crt`. Such a
  cluster also grants the discovery document to service accounts alone by default, and viffd
  reads it as nobody, so the deploy's `issuerDiscoveryOpen` opens it. Unset trusts the system's
  roots, which a public issuer (EKS's) needs and nothing more.
- `accounts` maps a token's subject to the iod it is: the `agent:` it acts as, and `iod`, the
  name it is installed under, which is how a provider's `iods` names it. An iod that relays nobody
  needs no `iod`. The accounts are written at start, never at first sight.
- `static` is the iods of a deployment with no issuer, a laptop or CI: the path of a file holding
  the token each presents, read at start. The viff keeps the hash and never the token.

**`auth: {issuer: {url, key}}`** makes the viff an issuer of its own tokens. The server reads
it.

```tony
auth:
  issuer:
    url: https://viff.example.com
    key: /etc/viff/issuer.pem
```

- `url` is the viff as a system outside it reaches it. It is the `iss` of every token the viff
  signs. The viff serves its discovery document at `<url>/.well-known/openid-configuration` and
  its public keys at `<url>/.well-known/jwks.json`. Both are JSON, and are read without naming
  anybody.
- `key` is the path of the signing key: an RSA private key of 2048 bits or more, in PEM (PKCS#1
  or PKCS#8). `openssl genrsa -out issuer.pem 2048` makes one. `viff up` refuses to start on a
  key it cannot read.

Without the block, the viff signs nothing and answers 404 at both addresses. What it signs is
[a credential for what a gate let through](gates.md#a-credential-for-what-a-gate-let-through),
and the token it presents to each registered runner, minted for that runner
alone ([who the runner is](../runners.md#who-the-runner-is)); the runners read the keys at the
url they reach the viff at, and hold the tokens to `url` as the issuer's name.

**`perms: {child-grants: {…}}`** is read by the server. It is the `child-grants` of the
[root](../glossary.md#root-of-the-store): what a node that is created with no record above it
holds out. Unset, it is `{read: ["*"]}`. Set it to `{}` for a viff where what anybody creates
belongs to that creator alone.

**`perms: {admins: […]}`** lists who may take a **top-level** address. Its entries are
[grantees](../glossary.md#grantee), and a bare email reads as `user:`. Viff puts them in the
`admin` group, and the root names that group in its `child-owner` line. Unset, the viff is
open: anybody may take a top-level address.

Viff writes `child-grants` and `admins` into the root's record at start, under a precondition
that the record is absent. So an operator's later edit of that record stands. The **first**
value of the config stands too. A check reads the record, so a later change to `child-grants`
or `admins` in the config changes nothing. At every start where the record differs from the
config, viff logs `perms: the root's record is not what this config would write`.

`--reseed-perms` (or `VIFF_RESEED_PERMS`) is the one start that writes the config over the
record.

**The `llm` block** is read by the server (`viff up`). The server hosts the task runtime, so
the `viff` agent reads its model and credentials from the config and the environment of the
server. Setting it for a `task run` *client* does nothing. The variables `OPENAI_MODEL`,
`PORTKEY_API_KEY`, `PORTKEY_VIRTUAL_KEY` and `PORTKEY_BASE_URL` override the file.

The precedence for any value, from low to high, is
`/etc/viff.tony` → `~/.config/viff.tony` → environment → CLI flag.

Every key, and the variable that sets it:

| Key | Variable | Read by | What it is |
|---|---|---|---|
| `addr` | `VIFF_ADDR` | every command | the viff to talk to |
| `scope` | `VIFF_SCOPE` | every command | the scope to act in |
| `user` | `VIFF_USER` | every command | who the CLI acts as |
| `agent` | `VIFF_AGENT` | every command | the operator agent the CLI acts as, in place of `user` |
| `format: {default, output, wire}` | `VIFF_FORMAT`, `VIFF_OUTPUT_FORMAT`, `VIFF_WIRE_FORMAT` | every command | the format to write in: `tony` or `yaml` ([representation](representation.md)) |
| `systemCharter` | `VIFF_SYSTEM_CHARTER` | `viff up` | a charter file installed at start as the system |
| `defaultAgent` | `VIFF_DEFAULT_AGENT` | `viff up` | the agent a spawn's prompt is for where it names none, bound at the spawn. `viff` by default |
| `localRunner` | `VIFF_LOCAL_RUNNER` | `viff up` | whether `up` starts a local runner beside itself ([the local runner](#the-local-runner)); `--local-runner` overrides it. `true` by default |
| `repoCharters: [{repo, github, branch, at, tree, answerers}]` | | `viff up` | the repo-charters reconciler, installed as the system for each repository named |
| `auth: {principal: {mode, providers}}` | | `viff up`, `viff account login` | the identity providers whose tokens say who a request is from |
| `auth: {iod: {issuer, audience, accounts, static}}` | | `viff up` | the issuer of the iods' workload tokens, and which iod each is |
| `auth: {issuer: {url, key}}` | | `viff up` | the address and signing key the viff issues its own tokens with |
| `perms: {child-grants, admins}` | `VIFF_PERMS_ADMINS` for `admins`, comma-separated | `viff up` | what the root holds out, and who may take a top-level place |
| `compactionCutoff` | `VIFF_COMPACTION_CUTOFF` | `viff up` | how long the store `up` starts keeps every patch ([the store](#the-store)); `--compaction-cutoff` overrides it. `1d` by default |
| `tripDuration` | | `viff up` | how long a firing waits when the [cycle throttle](../cycle-throttle.md) trips. A duration, `1s` by default; `0` refuses the firing |
| `shellEnv` | | `viff up` | the variables a local task's shell passes through from the server's environment. Everything else is unset |
| `llm: {baseURL, apiKey, virtualKey, model}` | `PORTKEY_BASE_URL`, `PORTKEY_API_KEY`, `PORTKEY_VIRTUAL_KEY`, `OPENAI_MODEL` | `viff up` | the model and credentials the `viff` agent uses |

`viff account login` keeps its tokens in `viff-tokens.tony`, beside the per-user config, or beside
the file `VIFF_CONFIG` names. Only its owner can read it.

`VIFF_CONFIG=<file>` names one config file in place of both file layers. `VIFF_RESEED_PERMS`
is `-reseed-perms`. `VIFF_LOG` sets how much `viff up` logs: `debug`, `info`, `warn` or
`error`.

`viff up -admin-addr <addr>` is where viff serves its admin listener, which serves pprof. It is
the port of `-addr` plus one by default, and `off` turns it off.

## The o binary

`viff up` brings up logd and docd by running `o sys up`. So it needs `o`, the
[go-tony](https://signadot.github.io/tony-format/) CLI. `up` finds one itself, in this order:

1. `-o-bin <path>`. Viff uses this path, and looks nowhere else.
2. `~/.viff/bin/<version>/o`, an `o` that viff installed before, at the version that viff
   pins.
3. `o` on your `PATH`, **if** it was built from the pinned go-tony. Viff reads the version
   from the binary. It runs nothing to ask.
4. Otherwise, viff runs `go install <pinned cmd/o>` into `~/.viff/bin/<version>/`, once for
   each version.

So `up` works from any directory, not only from a checkout.

Because of the version check at step 3, viff may ignore an `o` that you already have. Viff
and `o` share the logd session protocol. A difference in version does not fail with a clear
error. It looks like a store that does not answer, or like `o sys up` refusing a flag.

| What viff finds | What it does |
|---|---|
| no `go`, and an `o` of another version | uses that `o`, and says so |
| no `go`, and no `o` | says what to do. It does not start daemons and wait. |

The version that viff pins is the one it was *built* against. Viff reads it from its own
build info. So an installed `viff` resolves `o` in the same way as one run from a checkout.
If you work on go-tony itself, point `-o-bin` at your build. The flag exists to mean "use this
one".

# Starting a verse

`verse up` runs the engine and the task runtime, and it brings up the
[store](../glossary.md#store). Every other subcommand is a client of `verse up`.
[Tutorial §1](../tutorial.md#1-start) starts one with no flags. This page says what those
defaults are, and what to give instead.

```sh
export VERSE_ADDR=localhost:9797   # where clients reach the verse (this is the default)
verse up
```

To point a client at another address, `export VERSE_ADDR=host:port`, or pass
`-addr host:port` to any command. `verse down` stops the verse on `-addr`, and it stops the
daemons that the verse started.

```sh
VERSE_LOG=debug verse up            # the engine's trace: which rule fired, on what, and why not
```

**Use `VERSE_LOG` when a [rule](../glossary.md#rule) does not fire.** At `debug`, the engine
logs these things:

- which rules it evaluated, at which [commit](../glossary.md#commit);
- which [subjects](../glossary.md#subject) each rule selected;
- why a [firing](../glossary.md#firing) stopped: a read that did not resolve, a write that
  verse refused, or a cycle that the engine threw.

No other output tells a rule that selects nothing from a rule that selects and fails.
`VERSE_LOG` takes any slog level: `debug`, `info`, `warn` or `error`. Verse refuses any other
value at startup. It does not ignore it.

`up` decides three things for the whole runtime: the store, the driver and the agent root.
`up` decides them because it hosts the task runtime, and the clients do not.

## The store

By default, `up` uses docd. It brings up logd and docd on a **fresh temporary data
directory**, through `o sys up`. It prints the directory, and it keeps it. The store is then
[docd](https://signadot.github.io/tony-format/docd/), the tony-format document store.

You need to know nothing about docd to try verse, because `up` starts it for you. docd gives
verse its deeper properties:

- copy-on-write [scopes](../glossary.md#scope);
- conditional and coordinated writes;
- watches that report every edge;
- compactions.

See [store.md](store.md).

```sh
verse up -data ~/.verse/data                        # a durable dir rather than a fresh temp one
verse up -docd-addr host:port -docd-mount-addr host:port     # attach to an already-running docd
```

**A store call has a time limit, and reads and writes have different limits.**

| Call | How it runs | Its limit |
|---|---|---|
| a read | beside the request loop of logd | a limit of its own |
| a write | on the request loop of logd, behind every other write of the session | a separate number |

Both limits are constants in `entity/logdstore.go`. When the connection goes quiet, the
session connects again. It does not fail the caller.

**The readiness probe answers from this process alone.** `/healthz` reports three things:
`ok`, whether the engine is restored, and the head revision this process has. It reads
nothing from the store. `/healthz` answers `503` until the engine is restored. A verse that is
restoring is not stuck: versed serves requests before the restore finishes. So the probe that
says "no traffic yet" also says which of the two cases it is. `/status` reads the store, and it
is not a probe.

The reason `/healthz` reads nothing: a question about readiness comes under load, and that
load would delay an answer that reads the store.

### When a client cannot reach docd where verse does

`up` publishes both addresses of docd on `GET /status`. A
[controller](../glossary.md#controller) is its own process, and it must find these addresses
itself. [`verse connect`](connect.md) finds the mount address of docd in this way.

The client reads the addresses from where the client runs. That is not always where verse
runs. In a cluster, verse reaches docd by the name of a Service, and that name resolves only
inside the cluster. A laptop that gets that name gets a true address that it cannot use.

`-docd-advertise-addr` and `-docd-mount-advertise-addr` say what to publish. Verse still
connects to `-docd-addr`. Only the published answer changes:

```sh
verse up -docd-addr docd:7071 -docd-mount-addr docd-mount:7072 \
         -docd-advertise-addr verse-docd:7071 \
         -docd-mount-advertise-addr verse-docd-mount:7072
```

Each advertise flag needs the address flag that it replaces. Verse refuses
`-docd-advertise-addr` without `-docd-addr`. When verse starts docd itself, docd listens on
temporary ports, and a published address for them would not stay true.

Verse publishes one address for each face of docd. So you choose which clients the address is
for. A deployment that clients reach in two ways publishes the address that its controllers
use. `deploy/README.md` is a worked example. There, the pair is the tailnet names of the two
Services that verse connects to inside the cluster.

## The driver

The [driver](../glossary.md#driver) runs a task when nothing names another. The default
depends on the model credentials.

| Credentials | Default driver |
|---|---|
| configured: an `llm` block in `~/.config/verse.tony`, or `PORTKEY_*`/`OPENAI_MODEL` in the environment | a real `llm` agent |
| none | `scripted`, which runs a prompt as a shell command |

The **server** reads the credentials. So set them before `up`, and not in the environment of
the client. `verse status` shows an `llm` block with `available` and a masked key.

Each spawn chooses its driver. A rule's `spawn.driver`, `connect -driver` and
`task run -driver` each select `scripted`, `calls` or `llm` for that one task. If a task asks
for `llm` and no credentials are configured, that one task fails. Its status is `failed`, and
its message tells you to set the credentials. The engine keeps running. What each driver
means for gates is in [tasks.md](tasks.md#the-dial).

## The agent root

The agent root is the directory where an llm agent acts. `verse up -root <dir>` names it, and
`-root .` is your own tree. With no flag, `up` makes an isolated temporary directory. When the
llm driver is available, `up` prints it at start (`agents rooted at …`). For this reason, a
file that an agent writes is *not* in your working directory.

An agent can also read a tree *through* the verse, with no shared filesystem, wherever the
agent runs. For that, use [`verse connect`](connect.md). `-root` is for an agent on a single
machine, without federation.

## Where tasks run

By default, the local supervisor in the verse process runs tasks.

```sh
verse up -runner http://other-verse:9797      # send unnamed work to a remote verse
verse up -broker -broker-addr :9090           # …and/or serve a dial-in broker
```

The two flags answer different questions, and you can use both.

- `-runner` sets the **default**: where a spawn that names no [runner](../glossary.md#runner)
  runs.
- `-broker` serves a [broker](../glossary.md#broker). A spawn reaches it by name.

| a spawn names | where it runs |
|---|---|
| nothing | this verse's own runtime, or `-runner` if one is set |
| `broker` | the dial-in broker: it lands `unclaimed` and waits for a worker |
| an installed runner | wherever that runner's url says ([runners.md](runners.md)) |

A broker adds a name, and it takes no name away. A charter that never names a runner still
runs its tasks, and they do not wait for a [worker](../glossary.md#worker). Verse refuses a
spawn that names `broker` when no broker is served. It refuses it by name, as it refuses any
runner that is not installed.

All three places use the same seam, and every `verse task` verb works on all three. See also
[task control](../task-control.md) and [tasks.md](tasks.md).

A task on a broker carries a `dialURL`. The worker connects to that URL. Verse builds it from
`-broker-addr`. A worker may reach the broker by a name that the process cannot bind, such as
a pod behind a Service, or a device on a tailnet. Then `-broker-advertise-addr` is what verse
publishes instead:

```sh
verse up -broker -broker-addr :9090 -broker-advertise-addr verse-broker:9090
```

Without the flag, a bind to all interfaces advertises `localhost:9090`. Every worker resolves
that name, and no worker reaches the broker by it. `-docd-advertise-addr` above makes the
same split. Where a face is bound and where clients reach it are two separate facts.

## Configuration

Defaults come from layers. Each layer overrides the one before it:

1. `/etc/verse.{tony,json,yaml}` — system base
2. `~/.config/verse.{tony,json,yaml}` — per user. It patches the system file. The patch is a
   merge, so you set only the keys that you want to change, and the other keys come from the
   layer below.
3. environment variables
4. CLI flags

The file name of a layer gives its format: `.tony`, `.json`, `.yaml` or `.yml`. The two layers
may use different formats. `/etc/verse.json` patched by `~/.config/verse.tony` merges
correctly, because verse parses both to the same tree before the merge. A config file cannot
declare its own format, so its name declares it. Two files for one layer are an error. Verse
does not pick one by a hidden rule. `VERSE_CONFIG=<file>` replaces both file layers.

A missing file is a layer that is absent. An example `~/.config/verse.tony`:

```tony
addr:  "localhost:9797"
scope: "me"
user:  "walter@example.com"
llm: {
  model:      "gpt-4o"
  apiKey:     "pk-…"
  virtualKey: "vk-…"
}
```

The table below lists every key. The paragraphs that follow say what the table does not.

**`addr` and `scope`** say which verse to talk to, so every command reads them.

**`user`** is sent with each request, as `Verse-Principal`. The server writes it on every write
that the request makes. It checks each write and each read against what the
[address](../glossary.md#address) permits. The user owns what the request creates.

`user` is a [principal](../glossary.md#principal) in the
[grammar](../perms.md#principals-and-grantees). A bare email goes on the wire as
`user:<email>`. A worker sets `runner:<identity>`. Verse refuses a bare word.

Verse takes `user` as given. `-as` overrides it on the commands that take `-as`. When the
config names a user, those commands do not require `-as`. Where the verse names an identity
provider, `verse login` proves who you are instead, and the CLI then acts as that person
whatever `user` says ([logging in](perms.md#logging-in)).

With no `user` and no `agent`, the CLI acts as the local user, `user:<login>@localhost`,
toward a verse on this machine (`localhost`, `127.0.0.1`, `::1`). Toward any other verse, it
names nobody.

**`agent`** names an operator agent instead of a user. An operator agent is a harness that
drives verse through the API. It is a principal of its own kind, and not the person whose
terminal it runs in. When `agent` is set, every request is `agent:<name>`, and `-as` still
names somebody else.

**`systemCharter`** is read by the server. It is the path of a charter file that `verse up`
installs at start as `system:charter`. These are the rules that the deployment itself stands
behind. A request cannot be made as the system, so this key is the way to install them.

**`auth: {principal: {…}}`** says how people are authenticated. The server reads it to verify a
request's token ([who a request is from](perms.md#who-a-request-is-from)). Without it, a
request is whoever it says it is. The CLI reads the same `providers` to know where
`verse login` can log in, so a person's config carries the block as the server's does. It
ignores `mode`.

```tony
auth:
  principal:
    mode: optional
    providers:
      corp:
        issuer: https://login.example.com
        clientId: verse-cli
        domains: [example.com]
      github:
        ios: [github]
      slack:
        ios: [workspace]
        domains: [example.com]
```

- `mode` is `off`, `optional` or `required`. Unset is `off`: no token is verified. Under
  `optional`, a token is verified where a request carries one, and a request without one is
  still whoever it says it is. Under `required`, a request that names a person and carries no
  token is refused. The modes verify people, who come first. Agents and runners are next, and
  are taken as given until then ([who a request is from](perms.md#who-a-request-is-from)).
- `providers` are the parties whose word says which account a person is. Each is under a name
  the deployment gives it. The name is where its accounts are bound (`verse.identity.<name>`),
  so it is one plain word, and `iod` is the ios'. A provider is one of two kinds:
    - **An issuer**, with `issuer`, the OpenID Connect issuer's URL, and `clientId`, the OAuth
      client registered there for this verse: a public client, with no secret. Quote a
      `clientId` that starts with a digit. The verse accepts its id_tokens.
    - **Ios**, with `ios`, the installed ios the verse trusts for the provider's accounts, by the
      name each is installed under. `github`'s ios reflect the comments that prove a GitHub
      account, and `slack`'s io relays the member who typed
      ([a person relayed by a trusted io](perms.md#a-person-relayed-by-a-trusted-io)). GitHub
      is no issuer for people, so `github` is always of this kind.
- `domains` are the email domains a person may arrive from the first time, through a token or
  relayed by a trusted io. With none, only an account that is already bound is admitted.
- `acts`, on a provider with ios, is what a trusted io may do as a person it relays: `control`
  (unset), the reads and the acts that answer, say and steer, which is all an io does; or `any`.

`verse up` refuses to start on an `auth` block it cannot serve. It does not read a provider at
start, so a provider that is down stops only the requests that carry its tokens.

**`auth: {iod: {…}}`** says how the deployment's own ios are authenticated
([ios at the door](perms.md#ios-at-the-door)). The server reads it.

```tony
auth:
  iod:
    issuer: https://oidc.eks.us-west-2.amazonaws.com/id/EXAMPLED539D4633E53DE1B71EXAMPLE
    audience: verse
    accounts:
      "system:serviceaccount:verse:slack-io":  {principal: "agent:slack-io", io: workspace}
      "system:serviceaccount:verse:github-io": {principal: "agent:github-io", io: github}
    static:
      - {file: /var/run/verse/git-io.token, principal: "agent:git-io"}
```

- `issuer` is the issuer of the ios' workload tokens. On Kubernetes it is the cluster's own,
  which signs a pod's projected service-account token; `kubectl get --raw
  /.well-known/openid-configuration` prints it, and on EKS it is
  `aws eks describe-cluster --query cluster.identity.oidc.issuer`. Verse reads its keys as it
  reads a provider's.
- `audience` is what the tokens' `aud` holds, which is what the pod's token was asked for.
  Unset is `verse`.
- `accounts` maps a token's subject to the io it is: the `agent:` it acts as, and `io`, the
  name it is installed under, which is how a provider's `ios` names it. An io that relays nobody
  needs no `io`. The accounts are written at start, never at first sight.
- `static` is the ios of a deployment with no issuer, a laptop or CI: the path of a file holding
  the token each presents, read at start. The verse keeps the hash and never the token.

**`auth: {issuer: {url, key}}`** makes the verse an issuer of its own tokens. The server reads
it.

```tony
auth:
  issuer:
    url: https://verse.example.com
    key: /etc/verse/issuer.pem
```

- `url` is the verse as a system outside it reaches it. It is the `iss` of every token the verse
  signs. The verse serves its discovery document at `<url>/.well-known/openid-configuration` and
  its public keys at `<url>/.well-known/jwks.json`. Both are JSON, and are read without naming
  anybody.
- `key` is the path of the signing key: an RSA private key of 2048 bits or more, in PEM (PKCS#1
  or PKCS#8). `openssl genrsa -out issuer.pem 2048` makes one. `verse up` refuses to start on a
  key it cannot read.

Without the block, the verse signs nothing and answers 404 at both addresses. What it signs is
[a credential for what a gate let through](gates.md#a-credential-for-what-a-gate-let-through).

**`perms: {child-grants: {…}}`** is read by the server. It is the `child-grants` of the
[root](../glossary.md#root-of-the-store): what a node that is created with no record above it
holds out. Unset, it is `{read: ["*"]}`. Set it to `{}` for a verse where what anybody creates
belongs to that creator alone.

**`perms: {admins: […]}`** lists who may take a **top-level** address. Its entries are
[grantees](../glossary.md#grantee), and a bare email reads as `user:`. Verse puts them in the
`admin` group, and the root names that group in its `child-owner` line. Unset, the verse is
open: anybody may take a top-level address.

Verse writes `child-grants` and `admins` into the root's record at start, under a precondition
that the record is absent. So an operator's later edit of that record stands. The **first**
value of the config stands too. A check reads the record, so a later change to `child-grants`
or `admins` in the config changes nothing. At every start where the record differs from the
config, verse logs `perms: the root's record is not what this config would write`.

`--reseed-perms` (or `VERSE_RESEED_PERMS`) is the one start that writes the config over the
record.

`--unmigrate-perms` rewrites the perms mirror in the spelling that a binary from before
`child-owner` reads. Then it exits, and it serves nothing. Run it before you roll an image
back past `child-owner`. See [perms.md](../perms.md).

**The `llm` block** is read by the server (`verse up`). The server hosts the task runtime, so
the agent driver reads its model and credentials from the config and the environment of the
server. Setting it for a `task run` *client* does nothing. The variables `OPENAI_MODEL`,
`PORTKEY_API_KEY`, `PORTKEY_VIRTUAL_KEY` and `PORTKEY_BASE_URL` override the file.

The precedence for any value, from low to high, is
`/etc/verse.tony` → `~/.config/verse.tony` → environment → CLI flag.

Every key, and the variable that sets it:

| Key | Variable | Read by | What it is |
|---|---|---|---|
| `addr` | `VERSE_ADDR` | every command | the verse to talk to |
| `scope` | `VERSE_SCOPE` | every command | the scope to act in |
| `user` | `VERSE_USER` | every command | who the CLI acts as |
| `agent` | `VERSE_AGENT` | every command | the operator agent the CLI acts as, in place of `user` |
| `format: {default, output, wire}` | `VERSE_FORMAT`, `VERSE_OUTPUT_FORMAT`, `VERSE_WIRE_FORMAT` | every command | the format to write in: `tony` or `yaml` ([representation](representation.md)) |
| `systemCharter` | `VERSE_SYSTEM_CHARTER` | `verse up` | a charter file installed at start as the system |
| `auth: {principal: {mode, providers}}` | | `verse up`, `verse login` | the identity providers whose tokens say who a request is from |
| `auth: {iod: {issuer, audience, accounts, static}}` | | `verse up` | the issuer of the ios' workload tokens, and which io each is |
| `auth: {issuer: {url, key}}` | | `verse up` | the address and signing key the verse issues its own tokens with |
| `perms: {child-grants, admins}` | `VERSE_PERMS_ADMINS` for `admins`, comma-separated | `verse up` | what the root holds out, and who may take a top-level place |
| `tripDuration` | | `verse up` | how long a firing waits when the [cycle throttle](../cycle-throttle.md) trips. A duration, `1s` by default; `0` refuses the firing |
| `shellEnv` | | `verse up` | the variables a local task's shell passes through from the server's environment. Everything else is unset |
| `llm: {baseURL, apiKey, virtualKey, model}` | `PORTKEY_BASE_URL`, `PORTKEY_API_KEY`, `PORTKEY_VIRTUAL_KEY`, `OPENAI_MODEL` | `verse up` | the model and credentials the agent driver uses |

`verse login` keeps its tokens in `verse-tokens.tony`, beside the per-user config, or beside
the file `VERSE_CONFIG` names. Only its owner can read it.

`VERSE_CONFIG=<file>` names one config file in place of both file layers. `VERSE_RESEED_PERMS`
is `-reseed-perms`. `VERSE_LOG` sets how much `verse up` logs: `debug`, `info`, `warn` or
`error`.

## The o binary

`verse up` brings up logd and docd by running `o sys up`. So it needs `o`, the
[go-tony](https://signadot.github.io/tony-format/) CLI. `up` finds one itself, in this order:

1. `-o-bin <path>`. Verse uses this path, and looks nowhere else.
2. `~/.verse/bin/<version>/o`, an `o` that verse installed before, at the version that verse
   pins.
3. `o` on your `PATH`, **if** it was built from the pinned go-tony. Verse reads the version
   from the binary. It runs nothing to ask.
4. Otherwise, verse runs `go install <pinned cmd/o>` into `~/.verse/bin/<version>/`, once for
   each version.

So `up` works from any directory, not only from a checkout.

Because of the version check at step 3, verse may ignore an `o` that you already have. Verse
and `o` share the logd session protocol. A difference in version does not fail with a clear
error. It looks like a store that does not answer, or like `o sys up` refusing a flag.

| What verse finds | What it does |
|---|---|
| no `go`, and an `o` of another version | uses that `o`, and says so |
| no `go`, and no `o` | says what to do. It does not start daemons and wait. |

The version that verse pins is the one it was *built* against. Verse reads it from its own
build info. So an installed `verse` resolves `o` in the same way as one run from a checkout.
If you work on go-tony itself, point `-o-bin` at your build. The flag exists to mean "use this
one".

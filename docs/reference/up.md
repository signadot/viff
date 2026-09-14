# Starting a verse

`verse up` runs the engine and the task runtime and brings up its store; every other
subcommand is a client of it. [Tutorial §1](../tutorial.md#1-start) starts one with no flags
at all — this is what those defaults are, and what to say instead.

```sh
export VERSE_ADDR=localhost:9797   # where clients reach the verse (this is the default)
verse up
```

To point a client at a non-default address, `export VERSE_ADDR=host:port` or pass
`--addr host:port` to any command. `verse down` stops the verse on `--addr`, and it reaps the
daemons it spawned.

```sh
VERSE_LOG=debug verse up            # the engine's trace: which rule fired, on what, and why not
```

**`VERSE_LOG` is what to reach for when a rule does not fire.** At `debug` the engine says which
rules it evaluated at which commit, which subjects each selected, and — the useful half — why a
firing stopped: a read that did not resolve, a write that was refused, a cycle it threw. Nothing
else tells you the difference between a rule that is not selecting and a rule that is selecting
and failing. It takes any slog level (`debug`, `info`, `warn`, `error`); anything else is refused
at startup rather than ignored.

`up` decides three things for the whole runtime, because it hosts the task runtime and the
clients do not.

## The store

By default `up` is docd-backed: it brings up logd and docd on a **fresh temp data dir**
(printed, and kept) via `o sys up`, and points the store at
[docd](https://signadot.github.io/tony-format/docd/), the tony-format document store.

You do not need to know anything about docd to *play* with verse, since `up` stands it up for
you, but it is where verse's deeper properties come from — copy-on-write scopes, conditional
and coordinated writes, edge-faithful watches, compactions. See [store.md](store.md).

```sh
verse up --data ~/.verse/data                       # a durable dir rather than a fresh temp one
verse up --docd-addr host:port --docd-mount-addr host:port   # attach to an already-running docd
```

### When a client cannot reach docd where verse does

`up` publishes both docd addresses on `GET /status`, because a controller is its own process
and has to find the faces itself — that is how [`verse connect`](connect.md) reaches the mount
face. It reads them from wherever *it* runs, which is not always where verse runs. In a
cluster verse reaches docd by a Service name that resolves nowhere else, so a laptop handed
that name has been told something true and useless.

`--docd-advertise-addr` and `--docd-mount-advertise-addr` say what to publish. Verse keeps
dialling `--docd-addr`; only the answer changes:

```sh
verse up --docd-addr docd:7071 --docd-mount-addr docd-mount:7072 \
         --docd-advertise-addr verse-docd:7071 \
         --docd-mount-advertise-addr verse-docd-mount:7072
```

Each needs the face it re-addresses — `--docd-advertise-addr` without `--docd-addr` is
refused, since the faces of a docd verse spawned itself are ephemeral ports, and publishing
some other address for them is a promise nothing keeps. There is one published address, so
this is a choice about *which* client matters; a deployment reachable both ways publishes the
one its controllers use. `deploy/README.md` is a worked example, where the pair is the
tailnet names of the two Services verse dials internally.

## The driver

Which driver runs a task when nothing says otherwise. With LLM creds configured — an `llm`
block in `~/.config/verse.tony`, or `PORTKEY_*`/`OPENAI_MODEL` in the environment — the
default is a real llm agent; with no creds it is `scripted`, which runs a prompt as a shell
command.

The creds are read by the **server**, so set them before `up`, not in the client's
environment. `verse status` shows an `llm` block with `available` and a masked key.

The driver is chosen per spawn, so a trigger's `spawn.driver`, `connect --driver` and
`task run --driver` each select `scripted`, `calls` or `llm` for that one task. Requesting `llm`
with no configured creds fails that one task — status `failed`, with a message telling you
to set the creds — and the engine keeps running. What each driver implies for gates is in
[tasks.md](tasks.md#the-dial).

## The agent root

Where an llm agent acts. `verse up --root <dir>` names it, `--root .` is your tree, and with
no flag `up` makes an isolated temp dir and prints it at start (`agents rooted at …`, when
the llm driver is available) — which is why a file an agent writes is *not* in your cwd.

To let an agent read a tree *through* the verse instead — no shared filesystem, wherever it
runs — use [`verse connect`](connect.md). `--root` remains for a non-federated,
single-machine agent.

## Where tasks run

Off by default: the local in-process supervisor runs them.

```sh
verse up --runner http://other-verse:9797     # send unnamed work to a remote verse
verse up --broker --broker-addr :9090         # …and/or serve a dial-in broker
```

They answer different questions and compose. `--runner` sets the DEFAULT — where a spawn that
names nobody runs. `--broker` SERVES a broker, which a spawn reaches by name:

| a spawn names | where it runs |
|---|---|
| nothing | this verse's own runtime, or `--runner` if one is set |
| `broker` | the dial-in broker: it lands `unclaimed` and waits for a worker |
| an installed runner | wherever that runner's url says ([runners.md](runners.md)) |

Serving a broker therefore adds a name and takes none away — a charter that never mentioned a
runner goes on running rather than waiting for somebody. A spawn naming `broker` where none is
served is refused by name, like any other runner that is not installed.

All of it is the same seam, and every `verse task` verb works across all three. See also
[task control](../task-control.md) and [tasks.md](tasks.md).

A brokered task carries a `dialURL`, which is what its worker dials in on, and it is built
from `--broker-addr`. Where a worker reaches the broker by a name the process cannot bind —
a pod behind a Service, anything on a tailnet — `--broker-advertise-addr` is what gets
published instead:

```sh
verse up --broker --broker-addr :9090 --broker-advertise-addr verse-broker:9090
```

Without it a wildcard bind advertises `localhost:9090`, which every worker resolves and none
of them reaches. This is the same split as `--docd-advertise-addr` below: where a face is
bound and where it is reached are two facts, and neither implies the other.

## Configuration

Defaults are resolved through layers, each overriding the one before:

1. `/etc/verse.{tony,json,yaml}` — system base
2. `~/.config/verse.{tony,json,yaml}` — per-user; it patches the system file (a merge,
   so you set only the keys you want to change; everything else falls through)
3. environment variables
4. CLI flags

A config layer's format comes from its file name — `.tony`, `.json`, `.yaml` or `.yml` —
and the two layers need not agree: `/etc/verse.json` patched by `~/.config/verse.tony`
merges fine, because both are parsed to the same tree before the merge. A config file
cannot declare its own format, so its name declares it. Two files for one layer is an error
rather than a silent precedence rule. `VERSE_CONFIG=<file>` replaces both layers outright.

A missing file is simply an absent layer. Example `~/.config/verse.tony`:

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

`addr` and `scope` are read by every command (they say which verse to talk to). `user` is who
every command acts as: it rides each request (`Verse-Principal`), the server stamps it on every
write the request makes, checks each write and read against what its place permits, and what
the request creates is owned by them. It is a principal in the
[grammar](../perms.md#principals-and-grantees): a bare email goes on the wire as `user:<email>`,
a worker sets `runner:<identity>`, and a bare word is refused. It is self-asserted — there is no
login — and `--as` overrides it on the commands that take one, which is also why those commands
no longer insist on the flag when the config names a user. Env: `VERSE_USER`. `agent` names an
operator agent instead — a harness driving verse through the api, which is a principal of its
own kind rather than the person whose terminal it runs in; when set, every request is
`agent:<name>` and `--as` still names somebody else. Env: `VERSE_AGENT`. `systemCharter` is
server-side: the path of a charter file `verse up` installs at start as `system:charter`, the rules
the deployment itself stands behind, since a request cannot be made as the system. Env: `VERSE_SYSTEM_CHARTER`. `perms: {default: {…}}` is server-side too:
the root's default, what a node created with no default above it holds out — `{read: ["*"]}`
when unset, and `{}` for a verse where what anybody creates is theirs alone. See
[perms.md](../perms.md). The `llm`
block, however, is read by the server (`verse up`) — it hosts the task runtime, so
that is whose config and environment the agent driver reads for its model + creds; setting it for
a `task run` *client* does nothing. The equivalent env is `OPENAI_MODEL`,
`PORTKEY_API_KEY`, `PORTKEY_VIRTUAL_KEY`, `PORTKEY_BASE_URL` (which override the file).
Precedence for any value is, low → high:
`/etc/verse.tony` → `~/.config/verse.tony` → environment → CLI flag.

## The o binary

`verse up` brings logd and docd up by running `o sys up`, so it needs an `o` — the
[go-tony](https://signadot.github.io/tony-format/) CLI — and it finds one itself. In order:

1. `--o-bin <path>`, which is an answer rather than a hint.
2. `~/.verse/bin/<version>/o`, an `o` verse installed earlier at the version it pins.
3. `o` on your `PATH`, **if** it was built from the pinned go-tony (verse reads that out of
   the binary; nothing is run to ask it).
4. otherwise `go install <pinned cmd/o>` into `~/.verse/bin/<version>/`, once per version.

So `up` works from any directory, not only from a checkout.

The version check at (3) is why an `o` you already have may be ignored: verse and `o` share
the logd session protocol, and a skew does not fail cleanly — it reads as a store that will
not talk, or as `o sys up` refusing a flag. With no `go` and no matching `o`, verse uses a
skewed one and says so; with neither, it says what to do rather than spawning and waiting.

The version verse pins is the one it was *built* against, read from its own build info, so
an installed `verse` resolves the same way as one run out of a checkout. If you are working
on go-tony itself, point `--o-bin` at your build — that flag exists to mean "this one".

# Examples

These are working programs that you can copy and change. They compile with the rest of the
tree.

| Example | What it is | The command that runs it |
|---|---|---|
| [runner/](runner/main.go) | a viff [runner](../../glossary.md#runner): the smallest thing that serves the task-control contract. The two functions that a drive changes are marked. | `go run ./docs/reference/examples/runner -addr :9800` |
| [io/](io/main.go) | a viff [iod](../../glossary.md#iod): the smallest thing that serves the reflection contract. The one function that a drive changes is marked. | `go run ./docs/reference/examples/io -addr :9801 -repo viff=/path/to/a/repo` |

And two charters a deployment copies and fills in, each installed against a real engine by the
viffd tests, with the one viffd renders itself beside them, for reading:

| Example | What it is | How a deployment uses it |
|---|---|---|
| [gate-ask/](gate-ask/gate-ask.tony) | one Slack message per person a pending question waits on, or one in the channel an ask's `slack` label names, through the Slack iod's out half | a copy with your workspace's place, installed at start as the system (`deploy/`'s `systemCharter`), since only the system reads every ask put to a person |
| [deploy-announce/](deploy-announce/deploy-announce.tony) | one message in a channel per new `deploy/<instance>.N` tag, sent through its own outbox, with the message as the latch and a seed for the tags that already exist | a copy with your channel and repository, installed after the Slack iod, seeded first |
| [repo-charters/](repo-charters/repo-charters.tony) | the reconciler of a repository's own `.viff/charters`, which viffd renders from its config's `repoCharters` and installs as the system | read, not copied: viffd renders and installs its own from `repoCharters` in the auth file ([deploy](../../../deploy/README.md#repo-charters)), and a copy installed by hand is a second reconciler beside it |

## Running the runner

```sh
openssl rand -hex 32 > runner.token
go run ./docs/reference/examples/runner -addr :9800 -token-file runner.token
```

The runner proves itself to its viff with that token, and the registration names it by its
hash. `-viff-url` says which viff the runner serves, `http://localhost:9797` by default, and is
spelled the way `viff up` prints its issuer: `localhost`, not `127.0.0.1`.

Then run these commands against a viff:

```sh
viff runner add gpu http://localhost:9800 -token-file runner.token
viff task run -runner mirror.runners.gpu -agent sample "fetch the data; retrain; report"
viff ps -runner mirror.runners.gpu
```

The runner has a placeholder driver. The driver splits the prompt on semicolons and does each
part. You can watch a task run from start to end, and then replace the two functions with your
own work.

The `viff runner add` above prints `reach: a runner`. That line is the install probe. It
confirms that the program at the URL serves the contract.

## Running the iod

```sh
go run ./docs/reference/examples/io -addr :9801 -repo viff=/path/to/a/repo
```

The iod serves two routes and nothing else:

| Route | What it says |
|---|---|
| `GET /io` | what the iod is |
| `GET /reflect` | what the iod sees |

So you can look at exactly what viff would be told:

```sh
curl localhost:9801/io
curl "localhost:9801/reflect?at=git.viff"
```

As shipped, the iod reflects real git repositories. So the code you copy works before you
change anything. One function is marked for you to replace. It reads your system's truth and
returns it. The function is never handed a [store](../../glossary.md#store), and it cannot
write. Viff converges the store with your answer.

## Why it is this way

### Programs, not fenced blocks

The examples compile with the tree. An example in a fenced block goes out of date the first
time a signature changes, and nobody notices.

### The iod does not write

The iod is never handed a store. Viff, not the iod, converges the store with the iod's answer.
This keeps a source short enough to be worth writing.

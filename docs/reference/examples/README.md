# Examples

These are working programs that you can copy and change. They compile with the rest of the
tree.

| Example | What it is | The command that runs it |
|---|---|---|
| [runner/](runner/main.go) | a verse [runner](../../glossary.md#runner): the smallest thing that serves the task-control contract. The two functions that a drive changes are marked. | `go run ./docs/reference/examples/runner -addr :9800` |
| [io/](io/main.go) | a verse [iod](../../glossary.md#iod): the smallest thing that serves the reflection contract. The one function that a drive changes is marked. | `go run ./docs/reference/examples/io -addr :9801 -repo verse=/path/to/a/repo` |

## Running the runner

```sh
openssl rand -hex 32 > runner.token
go run ./docs/reference/examples/runner -addr :9800 -token-file runner.token
```

The runner proves itself to its verse with that token, and the registration names it by its
hash. `-verse-url` says which verse the runner serves, `http://localhost:9797` by default, and is
spelled the way `verse up` prints its issuer: `localhost`, not `127.0.0.1`.

Then run these commands against a verse:

```sh
verse runner add gpu http://localhost:9800 -token-file runner.token
verse task run -runner verse.runners.gpu -agent sample "fetch the data; retrain; report"
verse ps -runner verse.runners.gpu
```

The runner has a placeholder driver. The driver splits the prompt on semicolons and does each
part. You can watch a task run from start to end, and then replace the two functions with your
own work.

The `verse runner add` above prints `reach: a runner`. That line is the install probe. It
confirms that the program at the URL serves the contract.

## Running the iod

```sh
go run ./docs/reference/examples/io -addr :9801 -repo verse=/path/to/a/repo
```

The iod serves two routes and nothing else:

| Route | What it says |
|---|---|
| `GET /io` | what the iod is |
| `GET /reflect` | what the iod sees |

So you can look at exactly what verse would be told:

```sh
curl localhost:9801/io
curl "localhost:9801/reflect?at=git.verse"
```

As shipped, the iod reflects real git repositories. So the code you copy works before you
change anything. One function is marked for you to replace. It reads your system's truth and
returns it. The function is never handed a [store](../../glossary.md#store), and it cannot
write. Verse converges the store with your answer.

## Why it is this way

### Programs, not fenced blocks

The examples compile with the tree. An example in a fenced block goes out of date the first
time a signature changes, and nobody notices.

### The iod does not write

The iod is never handed a store. Verse, not the iod, converges the store with the iod's answer.
This keeps a source short enough to be worth writing.

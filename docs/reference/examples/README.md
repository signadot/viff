# Examples

Working programs you can copy and change. They compile with the tree, which is the point: an
example in a fenced block goes stale the first time a signature moves and nobody finds out.

| | |
|---|---|
| [runner/](runner/main.go) | a verse runner: the smallest thing that serves the process-control contract, with the two functions a drive changes marked |
| [source/](source/main.go) | a verse source: the smallest thing that serves the reflection contract, with the one function a drive changes marked |

## Running the runner

```sh
go run ./docs/reference/examples/runner -addr :9800
```

Then, against a verse:

```sh
verse runner add pool:gpu http://localhost:9800
verse process run --runner runners:pool:gpu "fetch the data; retrain; report"
verse ps --runner runners:pool:gpu
```

It is a placeholder driver — it splits the task on semicolons and does each part — so you can
watch a process run end to end before replacing the two functions with the work you actually
have. The `verse runner add` above will print `reach: a runner`, which is the install probe
confirming that what is at that url really serves the contract.

## Running the source

```sh
go run ./docs/reference/examples/source -addr :9801 -repo verse=/path/to/a/repo
```

It serves two routes and nothing else — `GET /source` says what it is, `GET /reflect` says
what it sees — so you can look at exactly what verse would be told:

```sh
curl localhost:9801/source
curl "localhost:9801/reflect?ref=git:head:verse"
```

As shipped it reflects real git repositories, so what you copy already works before you change
anything. One function is marked to replace: read your system's truth and return it. It is
never handed a store and cannot write — converging the store with your answer stays on verse's
side, which is what keeps a source short enough to be worth writing.

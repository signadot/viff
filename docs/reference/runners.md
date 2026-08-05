# Runners

The surface. For what a runner *is* and why it is shaped this way, see
[../runners.md](../runners.md); for the contract a runner serves,
[../task-control.md](../task-control.md). To build one, copy
[examples/runner](examples/runner/main.go).

A runner is an entity at `runners:<kind>:<name>` carrying one field:

| | |
|---|---|
| `url` | the verse-facing task-control base — what `runner.HTTP` drives and what a restart reconstructs from. Required |

`kind` is the operator's own category (what this runner *is*: pool, host, sandbox); `name` is
its name within that kind. Neither may contain `/`, and `kind` may not contain `:` — both are
segments of the address and of the route that serves it.

## Commands

```
verse runners                              what is installed, with running/total/lastSpawn
        [--probe]                          …and what each one says right now (not recorded)
verse runner add <kind>:<name> <url>       install one, probing the url first
       add [--file <f>] | [file] | stdin   …or as a document (.tony/.json/.yaml)
           [--no-probe]                    install without asking the url what it is
       show <ref>                          the entity
       check <ref>                         ask this runner what it is, right now
       remove <ref>                        take it away; refused if work is running on it
              [--cancel "<why>"]           …the place is going away: end that work, with a reason
              [--leave]                    …the place stays: leave it running
```

A `<ref>` is `runners:pool:gpu` or `pool:gpu` — the prefix is optional everywhere, so an
operator reading `verse entities` and one typing `verse runner show` need not know two spellings.

On the task side:

```
verse ps  [--runner <ref>]                 only the work on that runner (`local` = named none)
verse task run --runner <ref> <prompt>    run this one there
```

## Routes

| | |
|---|---|
| `GET /runners` | list; `?probe=true` adds a live `reach` per runner |
| `POST /runners` | install; `?probe=false` skips the check. `201` with `{ref, reach}`, plus `warning` when unreachable |
| `GET /runners/{kind}/{name}` | the entity |
| `GET /runners/{kind}/{name}/check` | `{ref, url, reach}` and `why` when it is not a runner |
| `DELETE /runners/{kind}/{name}` | remove. `?cancel=<why>` or `?leave=true`; `409` with both named when work is running and neither is given |
| `GET /tasks?runner=<ref>` | the work on one runner |

`reach` is one of `a runner`, `not a runner`, `unreachable`. Nothing about it is written to the
store — it is answered live and discarded, so a reachability column is always as of now.

## On a spawn

`runner:` on a charter's `spawn:`, and `--runner` / `runner` on the ad-hoc face. Empty means the
verse's default runner, which `verse up --runner` / `--broker` set.

A spawn naming a runner that is not installed is **refused** on both paths — an ad-hoc run with
`400` and the name in the message, a charter spawn as a failed spawn whose reason travels onto
the flow. It is never quietly run somewhere else.

## On the task

`task:instance` carries both:

| | |
|---|---|
| `runner` | the resolved url (or `local`) — what a restart re-attaches with |
| `runnerName` | the `runners:<kind>:<name>` the spawn named, when it named one |
| `startedAt` | when the task began; carried across a re-attach rather than re-stamped |

Both, deliberately: the url is what a restart can act on with nothing else present, so removing
a runner entity cannot strand running work; the name is what a person reads and what
`verse ps --runner` selects on, which keeps answering after the entity is gone.

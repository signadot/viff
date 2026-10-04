# Tasks, from the worker side

This page holds the parts of [the task reference](../reference/tasks.md) for somebody who runs a
verse or works a task as its worker: the backends, the broker, and `verse task do`.

## Where a task runs is a seam

There are three backends and one handle. `verse task` and
`task get`/`attach`/`cancel`/`say`/`vector`/`approve`/`deny` work across all of them.

| Backend | What it is |
|---|---|
| local | the supervisor in the verse process. It is the default. |
| remote | `verse up -runner <url>` runs tasks on the task-control routes of another verse, and makes that the default |
| broker | `verse up -broker -broker-addr :9090` serves routes where a worker behind a firewall, or a person, dials in and claims. A spawn reaches it by name, `runner: broker`. The default does not change. |

`-runner` sets the default at startup. A spawn can also name its own place:
`runner: verse.runners.gpu` on a charter, or `-runner` on `verse task run`. So a drive can run
part of its work in a place that the rest of the deployment knows nothing about. See
[runners.md](../reference/runners.md) for that surface, and [../runners.md](../runners.md) for what a
runner is.

Nothing is inverted under a broker. Verse and the worker are both clients of the broker. So a
laptop or a pod behind a firewall works, and verse never reaches in. The contract and the
protocol are in [task control](../task-control.md). The reason for the seam at this point is
in [external tasks](../external-tasks.md).

## Executing one by hand

A task spawned onto the broker waits there for a worker, and does not run by itself.
`verse task` shows its runner and the URL where a worker dials in. You never type that URL;
verse resolves it from the task.

```sh
verse up -broker -broker-addr :9090
ID=$(verse task run -runner broker "review the auth change")
verse task
# - dialURL: http://localhost:9090/broker/<nonce>
#   id: run-…
#   runner: broker
#   status: unclaimed
#   prompt: "review the auth change"
```

You are the worker. Every action takes `-as runner:<identity>`, because a claim must name who
made it. The first action claims the task, and `task get` then reads `status: running`:

```sh
verse task do note "$ID" "reviewing the auth change" -as runner:scott
```

During the work, you reach a decision that you should not make alone. Raise it *up* to verse,
and do not decide it. Verse governs who resolves it, and a worker never resolves its own gate.
`-wait` waits until the gate has an outcome:

```sh
verse task do gate "$ID" deploy-approval -as runner:scott -reason prod -wait
verse task do gate "$ID" unsure -as runner:scott -to peggy@example.com -reason "which region?"
# gate raised: deploy-approval-a1b2c3d4
# awaiting resolution…
verse task approve "$ID" deploy-approval-a1b2c3d4 -as lead@example.com    # …from someone with the authority
verse task do complete "$ID" "LGTM — one nit on the token TTL" -as runner:scott
```

`-to` names who a question raised by a worker is held out to: a person, the address of a
group, or a kind. A worker that asks somebody is what raising a gate is for. The spawn's own
gates may carry a gate of the same name with `answerers`. Then the spawn's gate stands, and
verse ignores `-to`. That gate is the charter's policy, and the worker cannot make it weaker.

A gate raised this way is the same record that a charter gate poses. So it can take outcomes
of its own, a threshold for each outcome, and a clock:

```sh
verse task do gate "$ID" rollout -as runner:scott -options canary,full:2,abort -deadline 4h:abort -wait
verse question <id> --answer canary --as lead@example.com
# rollout-… settled on canary
```

The task is now `done`, and your result is on its `verse.task.instance` entity. So a
completion hook or a rule reacts to it exactly as if an agent had finished. A *person* did
the work, and the flow moves on.

So a task has two views, and every verb works whether the task runs locally or behind a
broker:

- **From outside**, the operator drives and watches: bare `verse task` lists, then `get`,
  `attach`, `cancel`, `say`, `vector`, `approve`/`deny`.
- **From inside**, on the worker side, the worker acts: `do complete|fail|note|gate`, plus
  `inbox` to see the control queued for the worker.

*Left to do: the inbox long-poll.*

## The runtime's hold

The runtime's `POST /tasks/{id}/pause|resume` are the same hold. Why a separate channel
matters, and how the engine serves it first, are in
[deployment and steering](../deployment-and-steering.md).

# Tasks: counting and listing them is fast at any number of tasks, and a task's record has a retention

## Problem

Every count and list of tasks reads every task record, whole. All of them go through `taskRows` (versed/server.go:4528), which is an `entity.Below` over the `task:instance` mirror:

| caller | what it needs | what it reads |
|---|---|---|
| `taskCount`, for `GET /status` | a number | every record, payload included |
| `taskSnapshots`, for `verse task` | the rows a person asks for | every record |
| `runnerRow` / `listRunners`, for `verse runner` | each runner's running/total | every record, per listing |
| `byLoad`, for routing (versed/route.go:243) | not-yet-ended counts per candidate | every record, once per launch with two or more candidates (vb2pp5j0h12ks9e0whn0) |

Staging has 148 task records today, and a record carries the launch, the result and the rest of what the mirror keeps. So the cost of `verse status` grows with the whole history of the verse, not with what's happening now. On 2026-10-31, a `verse status` took 54s while docd's session was churning and 1s when it was quiet (wf2pp5j0h12ksze9whn0 has the churn). There will be many more tasks: every rule firing that spawns, every ask's `then`, every person's `verse task run`. Nothing removes a record once its run has ended, so the read only grows.

## What it should be

- **A count is not a read of the rows.** `status` says how many tasks there are, and how many are running, without decoding a payload. A runner's running/total and routing's load are counts too. Either versed keeps them up to date as records are written (it writes every one: at the spawn, at each transition, at the end), or the store answers a count. Measure which against staging first.
- **A list is a page, and a filter is applied before the read.** `verse task` shows the recent or running ones by default, in an order, with a cursor for more. A filter (running, a runner, a rule, a subject) narrows what's read, not what's printed after reading everything.
- **A task has a retention.** A record past its run's end, plus a retention, leaves the mirror. That's like the launch copy versed already drops a retention after the run ends (657fcd127), but for the record itself. Open questions:
  - what a retired task leaves behind, if anything (its id and outcome where a rule or an ask still names it, as `vf2pp5j0h12ksg60whn0`'s `then` needs);
  - whether retention is per rule or charter, or one setting for the verse;
  - what happens to a task an open ask or a `then` still refers to.

## Done when

- `GET /status`, `verse runner` and a launch's routing each take time independent of how many ended tasks the verse holds. A test with a few thousand ended records shows it.
- `verse task` returns a page.
- Ended tasks past their retention are gone from the mirror, and nothing that names them breaks.
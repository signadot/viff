# Tasks

A task is a governed unit of work: an agent, a shell command, a worker behind a firewall,
or a person. [Tutorial §3](../tutorial.md#3-tasks) drives one; this is the whole surface,
including the two vantages and the seam that makes them the same commands.

## Running one

```sh
verse task run "echo summarizing; sleep 2"       # prints a run-… id
verse task run --wait "echo done"                # …and block for the final output
verse task run --attach 'Write a file hello.txt containing "hi".'   # …and stream it
verse task run --driver scripted "go test ./..."
verse tasks
verse task get <id>        # snapshot: status, prompt, driver, runner, result, pending gates
verse task say <id> "also mention the dial"
verse task vector <id> ask --as human:scott
verse task vector <id> --file vector.tony
verse task cancel <id>
```

`say` and `vector` are different levers. `say` re-prompts the agent — new instructions.
`vector` re-vectors it, swapping the gate vector that governs its autonomy, applied at the
next boundary with no restart. `--as` attributes the slide. Both work wherever the task
runs: the local runtime applies them directly, and a brokered or remote worker gets them
relayed through its inbox.

A trigger's `spawn` runs a task the same way, from the same `launch.Spec`; what a charter
may say about one is in [the trigger model](../trigger-model.md#action--what-fires).

## The dial

The two drivers carry different vectors, and the difference is the whole point of a gate: it
asks about what you have **not** already authorized.

`scripted` runs ungated: no gates, everything auto. Its prompt *is* the shell command, so you
authorized it by typing it, and asking "run a shell command?" after
`verse task run "go test ./..."` would be asking you to confirm the sentence you just
wrote. Spawned from a trigger the same holds one level up, since the trigger's own gates
already ran, per firing, before the action fired.

`llm` asks before effects, because the agent *chose* them. Its reads run freely; writing a
file, running a shell command, and delegating to a sub-agent block until approved. Approving
a command the agent thought up is a real decision.

```sh
verse gates                                  # engine + task gates, unified
# - gate: write
#   id: run-f3e31ed0390f~d1
#   mode: ASK
#   task: run-f3e31ed0390f
#   reason: "verb write"
#   tool: write_file
#   where: task
verse gate approve run-f3e31ed0390f~d1 --as human:you   # or: task approve run-… run-…~d1
```

The `~` that separates the gate from its task needs no quoting and no escaping, in a shell
or in a URL. On a terminal, `run --attach` prompts inline instead
(`⏸ gate "write" (write_file) — approve? [y/N]`); answer `y` to allow, anything else to deny,
and a denied gate comes back to the agent as an error it re-plans around. Piped (non-tty),
`attach` just streams. The gate is the same question either way — it stays in `verse gates`
and stays resolvable by id from anywhere.

<a id="who-you-addressed"></a>
### The rough edge: who you addressed

Which rule applies depends on how you addressed the runtime, and today the runtime cannot
tell.

Ask an *agent* to do something and being asked before the write is the convention you expect:
you opted into a loop and you are watching it. Hand it a *command* —
`verse task run "go test ./..."` — and you addressed a runner, not an agent; but with
creds configured the default driver is llm, so the command you just typed comes back as
`⏸ gate "shell" (run_bash) — approve?`. Nothing was decided that you had not already decided.
The prompt only reveals that verse chose to satisfy your command with an agent.

The vector is fixed by the driver and nothing overrides it at spawn (`run` takes only
`--wait/--attach/--driver`; the `vector` axis is one of the launch spec's "later" fields). So
today: pass `--driver scripted` when your prompt *is* a command — that is exactly what it is
for — or slide the vector once it starts (`task vector <id> auto`). A trigger hits the
same edge from the other side: its gates approve *the firing*, not the actions the spawned
agent then chooses, and it has no way to say it already decided. Where the permission *should*
come from is [authority](../roadmap/authority.md).

## The mirror

Every task is reflected into the store as `task:instance:<id>`, so observing the fleet
is observing entities — and a completion is an entity change, which is what lets a rule react
to it.

```sh
verse watch --system task
verse entity get task:instance:<id>
```

```tony
payload:
  lastAction: run_bash
  lastOutput: summarizing
  runner: local
  status: done
  vector: ASK
  vectorBy: human:scott
```

`observed: false` appears while the mirror cannot reach the runner holding the task. The
entity's other fields are then the last thing it heard rather than the current truth.

It outlives the task: the result, the last `say`, the last tool output and the terminal
`status` are all still there after the live registry has retired it, which is what
`verse flow show` reads for history. The mirror is an index, not a warehouse — it publishes
what an observer needs, not the transcript.

## The question record

A gate that stops for an answer is published as its own entity, `gate:question:<id>`, and the
task points at it (`pending: gate:question:<id>`).

```tony
payload:
  id:      review@demo:pr:1~d3
  origin:  review@demo:pr:1
  gate:    write-guard
  mode:    ASK
  status:  pending
  subject: demo:pr:1
  tool:    edit_file
  effects: {verbs: [write], paths: [cfg/app.tony]}
```

`effects` is what the action *would do*, derived before the gate was posed, so a rule can
select the questions it cares about without reading prose:

```sh
verse entities --system gate --if '{status: pending, effects: {verbs: [write]}}'
```

It is a record, not a mailbox: answering happens over the resolve API, and the entity settles
afterwards to `status: answered` with the verdict and who gave it. A question outlives its
answer, because a docket that forgets is one nobody can audit.

`resolver` names the binding that owns the question — what a spawn or a rule named, else the
standing binding covering its subject, else absent, which is the docket a person reads.
`reownedFrom` appears when a question changed hands, with `reownedWhy` saying what moved it —
`escalated` when its owner declined, `removed` when its owner was uninstalled. Either way it
went to the layer above: the standing binding covering its subject, else the docket a person
reads. The question itself is unchanged, because escalating changes who answers and not what
is being answered.

`delivered: false` appears while an answer has been accepted but not yet taken by the worker
it is for — a brokered task, where the answer waits on the inbox until somebody dials in.
The gate is closed to further answers the moment one is accepted; what is still in doubt is
whether the party who must act on it knows. It clears on the worker's acknowledgement.

```sh
verse entities --system gate --if '{delivered: false}'   # answers still in flight
```

Every answer names its answerer — `verse gate approve <id> --as human:you` — and two people
answering at once is normal rather than exceptional. Exactly one applies; the other is
recorded under `stale` and told so (`409`, naming who won), because a docket that keeps only
the winner cannot show that a question was contested. Answering an id nobody asked about is a
different error (`404`), and worth telling apart: one means look for your typo, the other
means somebody beat you to it.

The record is its own entity rather than a field because a gate is not always a task's. A
trigger gate is posed by a firing, before anything spawns — and if it is denied, nothing ever
does.

A restart settles what nobody is waiting on: a question survives it exactly as its task
does, and one a firing raised is settled outright, since what waited on that answer was a
goroutine in the verse. See [task control](../task-control.md).

A completion hook (`onDone` and friends) reads that task's own payload: `$status`,
`$result`, `$data`, `$err`, `$chain`. The task's flow is its subject, so the hook's
`$subject` already names it. Note that `status: done` means the task
*stopped*, not that the work succeeded — a hook that advances state should name evidence
(`patch: {stage: reviewed, severity: $data.severity}`) rather than trust a bare claim. See
[bindings](../trigger-model.md#bindings) and
[the answer contract](../trigger-model.md#the-answer-contract).

## Steering

A *flow* is not a stored thing: it is a query over the subjects of live tasks, which is
why steering takes an address rather than a ref, and why steering a whole set needs no new
machinery — it is the same selector at a wider setting.

```sh
verse flows                             # the distinct subjects with live work
verse flow show demo:pr:2               # the tree: what ran, what is running, what comes next
verse flow pause  demo:pr:2             # freeze its tasks at their next boundary
verse flow resume demo:pr:2
verse flow say    demo:pr:2 "hurry"
verse flow cancel demo:pr:2             # stop them (destructive)
verse flow pause 'demo:pr:*'            # a selector, per segment
```

Each command reports how many tasks it reached, echoes the address it reached them by so
you can see what a wildcard actually selected, and names the verb:

```tony
affected: 1
subject: demo:pr:2
verb: pause
```

All eight steer routes (four flow verbs, four goal verbs) answer in that one shape; a goal
reports `goal:` instead of `subject:`.

They land at each task's next boundary, so a flow can be held, redirected or stopped
mid-flight while every *other* flow keeps reacting. A scripted task is a single command,
so `cancel` stops it at once by killing the command while `pause` and `say` land at its
next boundary — which for a one-command task is when it ends. They take hold mid-run on a
multi-step task: the `llm` driver, whose every tool call is a boundary.

`pause` is the operator's ad-hoc freeze — "hold this, I want to look" — distinct from
`cancel`, which ends the work, and from a `park` gate, which is a hold the *charter* declares
per action. Why out-of-band matters, and the engine's priority servicing, are in
[deployment and steering](../deployment-and-steering.md).

## Where a task runs is a seam

Three backends, one handle. `verse tasks` and
`task get`/`attach`/`cancel`/`say`/`vector`/`approve`/`deny` work across all of them.

| | |
|---|---|
| local | the in-process supervisor — the default |
| remote | `verse up --runner <url>` hosts tasks on another verse's task-control face |
| broker | `verse up --broker --broker-addr :9090` hosts them behind a dial-in face a firewalled worker or a human claims |

Those three are the verse-wide DEFAULT, set at startup. A spawn can also name its own place —
`runner: runners:pool:gpu` on a charter, `--runner` on `verse task run` — so a drive can run
part of the work somewhere the rest of the deployment knows nothing about. See
[runners.md](runners.md) for that surface and [../runners.md](../runners.md) for what it is.

Nothing inverts under a broker: both verse and the worker are clients of it, so a laptop or a
pod behind a firewall works without verse reaching in. The contract and the protocol are in
[task control](../task-control.md); why the seam was cut there is
[external tasks](../external-tasks.md).

### Executing one by hand

With a broker runner, a spawned task lands on the broker awaiting an executor instead of
running itself, and `verse tasks` shows its runner and where to dial in. You never type
that URL; verse resolves it from the task for you.

```sh
verse up --broker --broker-addr :9090
ID=$(verse task run "review the auth change")
verse tasks
# - dialURL: http://localhost:9090/broker/<nonce>
#   id: run-…
#   runner: broker
#   status: unclaimed
#   prompt: "review the auth change"
```

You are the executor. Every action takes `--as <identity>`, because a claim must be
attributable, and the first act claims it (`task get` then reads `status: running`):

```sh
verse task do note "$ID" "reviewing the auth change" --as human:scott
```

Mid-work you reach a decision you should not make alone. Raise it *up* to verse rather than
deciding — the verse governs who resolves it, and the executor never resolves its own gate.
`--wait` blocks for the outcome:

```sh
verse task do gate "$ID" deploy-approval --as human:scott --reason prod --wait
# gate raised: deploy-approval-a1b2c3d4
# awaiting resolution…
verse task approve "$ID" deploy-approval-a1b2c3d4 --as human:lead   # …from someone with the authority
verse task do complete "$ID" "LGTM — one nit on the token TTL" --as human:scott
```

The task is now `done`, with your result on its `task:instance` entity, so a completion
hook or a rule reacts to it exactly as if an agent had finished. A *human* did the work; the
flow moves on.

So a task has two vantages, and every verb works whether it runs locally or behind a
broker. From outside, the operator drives and watches: `list`, `get`, `attach`, `cancel`,
`say`, `vector`, `approve`/`deny`. From inside, on the worker leg, the executor acts:
`do complete|fail|note|gate`, plus `inbox` to see the control queued for the worker.

*Left to do: the inbox long-poll.*

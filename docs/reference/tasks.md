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
verse task run --driver calls "$(cat merge.tony)"   # the actions themselves, no model
verse task run --answer shape.tony "…"   # …and declare the shape its answer must have
verse task run --gates gates.tony "…"    # …under gates of its own, each with its question
verse task
verse task get <id>        # snapshot: status, prompt, driver, runner, result, pending gates
verse task say <id> "also mention the dial"
verse task vector <id> ask --as walter@example.com
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

### `--answer`: what finished means

```sh
verse task run --answer shape.tony "check every reference in docs/runners.md"
```

The file is a tony match pattern — the ad-hoc face of a charter's `answer:` — and it is the
run's definition of **done**.

Without one, finished means only that the model stopped calling tools. That is also what a model
narrating its next step looks like, and the two are indistinguishable from outside: a run has ended
mid-investigation, with a page of working notes as its `result` and `done` on its read plane.
With a contract, the final message is checked the moment the model stops, a mismatch is fed back
naming what is wrong, and a run that never satisfies it **fails** — which is a branch every rule
already declares, unlike a charter that could not deref what it got.

It does a second job worth stating separately: it tells *nothing to do* from *something went
wrong*. Those are identical on disk and identical in an exit status, and a runner cannot infer
which it was.

Keep it small and let it govern only what will be matched on. A `summary: !irtype string` field
looks harmless and is a trap — a model writes `summary: nothing was wrong`, which is not valid
tony, and the run fails for a reason that has nothing to do with the work. The prose account belongs
outside the governed document; `answer.Extract` lifts a fenced block out of the final message, so
the writing can be writing.

## The drivers

Three, and the third is the one to reach for when the work has a right answer.

`scripted` runs its prompt as a shell command. `llm` gives the prompt to a model that decides
what to do a turn at a time. `calls` takes the actions themselves — the charter wrote them, and
nothing decides anything at run time:

```tony
spawn:
  vector: auto
  calls:
  - tool: run_bash
    input: {command: "git merge --ff-only origin/$branch"}
  - tool: push
    input: {destination: main}
```

The list is a field, so the engine checks it when the trigger fires, `verse c rules <name>` prints it,
and each argument is expanded on its own — a `$branch` with a quote in it is an argument rather
than text spliced into a document. Its presence selects the driver; `driver: calls` may be
written and must not contradict it.

Each entry is an ordinary action — derived, gated, narrated on the read plane — so a gate
written for an agent's push holds a list's push, and nothing downstream can tell which driver
proposed it. What a list does not get is the half that belongs to a model: no answer contract
(there is nobody to repair one, so a spawn carrying one fails), no delegation, and no re-planning
after a result.

An entry may say which of its outcomes end the list:

```tony
- tool: push
  input: {destination: retire}
  fatal: [err]        # a remote declining ONE ref is not a reason to stop
```

out of `err` (the step failed), `blocked` (refused, by a gate or by the far side) and `nonzero`
(a command exited non-zero). The field has three states, and the empty one is not the default:

| written | means |
|---|---|
| *(absent)* | all three end the list — a list is ordered because its calls depend on each other |
| `fatal: []` | nothing ends it; the list runs to the end whatever happens to this call |
| `fatal: [err]` | these |

`scripted` is this driver with a one-command list built for it. Typed by hand,
`verse task run --driver calls "$(cat merge.tony)"` reads the same document the charter grammar
does and sends the field, so a list that will not read is a message on your terminal.

## The dial

The drivers carry different vectors, and the difference is the whole point of a gate: it
asks about what you have **not** already authorized.

`scripted` **as you type it** runs ungated: `verse task run --driver scripted` sends `auto` on
your behalf, because the prompt *is* the shell command, and asking "run a shell command?" after
`verse task run "go test ./..."` would be asking you to confirm the sentence you just wrote.

That argument is about who addressed the request, so it stops where the typing does. **A charter
that spawns `driver: scripted` is asked**, per command, like anything else — it typed nothing,
and the reader of a charter is not the person who wrote it. A charter that means to run
unattended says so, in the same line an `llm` spawn uses:

```tony
spawn: { driver: scripted, vector: auto, prompt: "echo reviewing $subject" }
```

which is also how it says "the firing already decided this": a trigger's own `gates:` run per
firing, before the action, and `vector: auto` is what stops the same decision being taken twice. A
driver that ignored the spawn's `vector:` would disobey a charter asking to be asked, in silence.

`llm` asks before effects, because the agent *chose* them. Its reads run freely; writing a
file, running a shell command, and delegating to a sub-agent block until approved. Approving
a command the agent thought up is a real decision.

The set is not fixed at four. A **runtime may add an action of its own** — the coding runner adds
`push`, which sends work to a destination its environment declares — and what arrives at the gate
is an ordinary action with an ordinary typed effect, so a vector decides it the same way and
`verse gate` shows it the same way. That is what keeps a runtime's own capability inside the dial
rather than beside it: the one action with an effect outside the verse should not be the one
action nobody can gate.

```sh
verse gate                                  # task gates, and engine gates that ASK
# - gate: write
#   id: run-f3e31ed0390f~d1
#   mode: ASK
#   task: run-f3e31ed0390f
#   reason: "verb write"
#   tool: write_file
#   where: task
verse gate approve run-f3e31ed0390f~d1 --as you@example.com   # or: task approve run-… run-…~d1
```

**Who may answer is asked before the answer is delivered.** The task's owner, or the charter's,
answers what their own work asked; a gate's `answerers`, or the binding a question is held out
to, answer it; and a person answers an unbound question by the docket's default. The runner
working the task holds nothing on the question it raised, and is refused. A refusal is a 403
naming who holds what and which record decided ([perms.md](../perms.md)). Whoever may answer
may also hand the question on — `verse gate escalate <id> --as <who> [--say <why>]` — to the
next binding covering its subject, and to the docket when there is none.

**A charter gate is here whatever it takes to settle.** A gate with `need: 2` lists with its
`options` and, as answers arrive, its `ballots`; `verse gate approve <id> --as <who>` casts one, and
the record settles in the same write as the ballot that reaches a threshold. A gate with a
`deadline` lists it, and settles on its default when the clock passes.

**A provider selection is here too.** A `spawn` with a `provider` and `select: ask|vote` poses
a question whose options are the eligible doers, each carrying the envelope it would run under,
and `chain` naming the action it holds up. It is answered by option:

```sh
verse gate verse.trigger.treat.provider~aa92f8cc --answer fixer --as you@example.com
```

`within` is its deadline with no default: the clock passing with no pick settles it on `none`,
and nothing spawns.

The `~` that separates the gate from its task needs no quoting and no escaping, in a shell
or in a URL. On a terminal, `run --attach` prompts inline instead
(`⏸ gate "write" (write_file) — approve? [y/N]`); answer `y` to allow, anything else to deny,
and a denied gate comes back to the agent as an error it re-plans around. Piped (non-tty),
`attach` just streams. The gate is the same question either way — it stays in `verse gate`
and stays resolvable by id from anywhere.

### A gate's question

A task's own gate poses the same record a charter gate does, and says the same things about
it: how many ballots settle it, what it settles on, and how long it stands. A named vector is
a bare default — `auto`, `ask`, `park` — so a run that wants gates of its own writes them,
as a spawn's `gates:` or a file handed to `--gates`, in the shape `task vector --file` reads:

```tony
- name: shell
  mode: ask
  match: {verbs: [exec]}
  need: 2                                    # proceed takes two ballots; one deny refuses
  deadline: {within: "4h", default: deny}    # and the clock decides silence
- name: delegate
  mode: ask
  match: {verbs: [call_agent]}
```

`need`, `options` and `deadline` mean what they mean on a
[charter gate](../trigger-model.md#gates--whether-it-proceeds), and are refused the same way
when they cannot be posed as written — at the spawn, or at `task vector`, never at the boundary
a running task reaches. The gates ride over `--vector`'s default; gates with no default are
over `auto`, since the gates are what asks.

**The deadline is the question's own clock, and it settles.** `default: deny` is how an
unattended run stops parking on a question nobody will find: when the clock passes, the action
is refused as `by: deadline` and comes back to the agent as a refusal it re-plans around, the
same one a person's no would be. `default: proceed` lets the action through; no default settles
on `none`, which the loop also reads as a refusal — nobody made it, and the record says so. A
bound resolver's own `deadline` is a different clock: it hands the question up to the layer
above rather than settling it. Both instants sit on the record, and the earlier fires first.

**A delegation's question offers the child modes.** A gate over `call_agent` asks how the
child is supervised, so its options are the modes rather than a yes and a no: `proceed` runs
the child under the parent's gates and resolver, `careful` re-asks every action the child
takes, `autonomous` lets the child and its subtree answer their own gates within the granted
scope, and `deny` spawns nothing. Answer by option, as any question with options is answered:

```sh
verse gate run-f3e31ed0390f~d2 --answer careful --as you@example.com
```

A gate that wrote `options` of its own over a delegation keeps them; `need` on one is the
threshold every mode takes.

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

The driver picks the vector unless the spawn says otherwise, and what verse cannot read is
which of the two things you meant by the prompt. So today you say it: pass `--driver scripted`
when your prompt *is* a command — that is exactly what it is for — pass `--vector auto` to keep
the agent and drop the asking, or slide the vector once it starts (`task vector <id> auto`).
A trigger hits the same edge from the other side: its gates approve *the firing*, not the
actions the spawned agent then chooses. It can say it already decided — `vector: auto` on
the spawn — but that is a charter asserting the equivalence rather than the runtime deriving it,
and it is all-or-nothing where the firing may have authorized one action and not another. Where
the permission *should* come from is [authority](https://github.com/signadot/verse/blob/main/docs/roadmap/authority.md).

## The mirror

Every task is reflected into the store as `verse.task.instance.<id>`, so observing the fleet
is observing entities — and a completion is an entity change, which is what lets a rule react
to it.

```sh
verse watch verse task
verse data verse task instance <id>
```

```tony
payload:
  lastAction: run_bash
  lastOutput: summarizing
  runner: local
  status: done
  vector: ASK
  vectorBy: user:walter@example.com
```

`observed: false` appears while the mirror cannot reach the runner holding the task. The
entity's other fields are then the last thing it heard rather than the current truth.

It outlives the task: the result, the last `say`, the last tool output and the terminal
`status` are all still there after the live registry has retired it, which is what
`verse flow show` reads for history. The mirror is an index, not a warehouse — it publishes
what an observer needs, not the transcript.

### One id, many runs

A task id names the *work* rather than the run: it is derived from the spawning action and the subject,
so a rule that fires twice about one subject spawns the same id twice — which is what makes a
re-fire idempotent and what `verse flow demo.pr.1` addresses.

`verse.task.instance.<id>` is therefore the latest run, and each run has a record of its own:

```sh
verse data verse task run '<id>@<occurrence>'
verse data --if '{flow: "demo.pr.1"}' verse.task.run
```

`occurrence` is the store revision the run is about — the commit that woke the firing, or the
revision the record was created at when a person spawned it by hand. It is on both records, and
it is what tells one run from the next.

Read the instance to ask what this work is doing now; read the runs to ask what happened the
other times. A failed run followed by a successful one leaves both, which the instance alone
cannot do — it holds whatever the newest run has written and nothing of the one before.

Run records are written twice, at the run's start and at its end, so a run that never finishes
still leaves one. **Nothing prunes them.** They are small and bounded by runs rather than by events,
and removing them is `verse data --rm`, or a charter that marks the stale ones
[vanishing](../change-events.md) and acts on the edge.

## The question record

A gate that stops for an answer is published as its own entity, `verse.gate.question.<id>`, and
the task points at it (`pending: verse.gate.question.<id>`).

```tony
payload:
  id:      review@demo.pr.1~d3
  origin:  review@demo.pr.1
  gate:    write-guard
  mode:    ASK
  status:  pending
  subject: demo.pr.1
  tool:    edit_file
  effects: {verbs: [write], paths: [cfg/app.tony]}
```

`effects` is what the action *would do*, derived before the gate was posed, so a rule can
select the questions it cares about without reading prose:

```sh
verse data --if '{status: pending, effects: {verbs: [write]}}' gate
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
verse data --if '{delivered: false}' gate             # answers still in flight
```

Every answer names its answerer — `verse gate approve <id> --as you@example.com` — and two people
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

A *flow* is not a stored thing: it is a query over live tasks, which is why steering takes a
selection rather than a ref, and why steering a whole set needs no new machinery — it is the same
selector at a wider setting. A task run by hand is a flow too: of its `--subject`, or of its own
id when it names none. The verbs, their flags, and how a run's hold differs from a suspension are
[control](../control.md); this is what they do to a task.

```sh
verse flow                                     # live tasks and suspensions
verse flow show demo.pr.2                      # the tree: what ran, what is running, what comes next
verse flow suspend --task review@demo.pr.2     # hold that run at its next boundary
verse flow resume  --task review@demo.pr.2
verse flow say     --subject demo.pr.2 "hurry" # to every live task about it
verse flow cancel  --task review@demo.pr.2     # end that run (destructive)
```

`say` and `cancel` report how many tasks they reached, echo the selection they reached them by,
and name the verb; `suspend` and `resume` answer the row they held or lifted:

```tony
affected: 1
subject: demo.pr.2
verb: say
```

They land at each task's next boundary, so a run can be held, redirected or stopped mid-flight
while every *other* flow keeps reacting. A scripted task is a single command, so `cancel` stops it
at once by killing the command while a hold and a `say` land at its next boundary — which for a
one-command task is when it ends. They take hold mid-run on a multi-step task: the `llm` driver,
whose every tool call is a boundary.

A run's hold is the operator's ad-hoc freeze — "hold this, I want to look" — distinct from
`cancel`, which ends the work, and from a `park` gate, which is a hold the *charter* declares per
action. The runtime's `POST /tasks/{id}/pause|resume` are the same hold. Why out-of-band matters,
and the engine's priority servicing, are in [deployment and
steering](../deployment-and-steering.md).

A run's hold is also forgetful: it holds that task, and the rules go on firing beside it. To stop
the work about something and keep it stopped — no new firings, questions withdrawn from the
docket, hooks recording and starting nothing — suspend it instead:

```sh
verse flow suspend --subject demo.pr.2 --why "a person is reviewing it by hand"
verse flow show demo.pr.2                      # heldBy: [<the suspension>] on the live task
verse flow resume --task review@demo.pr.2      # refused, 409: a suspension holds it
verse flow resume --subject demo.pr.2
```

A task knows whose hold it is under — `heldBy` on its record and in `flow show` — so the two do
not undo each other: a lift resumes only what nothing else holds, and an operator's hold
outlives it whichever came first.

Every verb here asks `control` of each task it reaches, as `task cancel` does: the task's owner
holds it, and so does the runner its claim granted. A `say` refused for any one of its tasks is
refused whole, naming that task.

With `--scope`, every verb here — `verse flow suspend|resume|say|cancel`, `task cancel|say|vector`
and the runtime's `/tasks/{id}/pause|resume` — reaches the tasks that scope started and none it
merely sees. Addressed at baseline's work it refuses and says so, so `verse flow cancel --scope me
--task review@demo.pr.2` never stops baseline's review of that PR by accident; drop the flag to
address baseline. See [what a scope owns](connect.md#what-a-scope-owns).

## Where a task runs is a seam

Three backends, one handle. `verse task` and
`task get`/`attach`/`cancel`/`say`/`vector`/`approve`/`deny` work across all of them.

| | |
|---|---|
| local | the in-process supervisor — the default |
| remote | `verse up --runner <url>` hosts tasks on another verse's task-control face |
| broker | `verse up --broker --broker-addr :9090` hosts them behind a dial-in face a firewalled worker or a human claims |

Those three are the verse-wide default, set at startup. A spawn can also name its own place —
`runner: verse.runners.pool.gpu` on a charter, `--runner` on `verse task run` — so a drive can
run part of the work somewhere the rest of the deployment knows nothing about. See
[runners.md](runners.md) for that surface and [../runners.md](../runners.md) for what it is.

Nothing inverts under a broker: both verse and the worker are clients of it, so a laptop or a
pod behind a firewall works without verse reaching in. The contract and the protocol are in
[task control](../task-control.md); why the seam was cut there is
[external tasks](../external-tasks.md).

### Executing one by hand

With a broker runner, a spawned task lands on the broker awaiting an executor instead of
running itself, and `verse task` shows its runner and where to dial in. You never type
that URL; verse resolves it from the task for you.

```sh
verse up --broker --broker-addr :9090
ID=$(verse task run "review the auth change")
verse task
# - dialURL: http://localhost:9090/broker/<nonce>
#   id: run-…
#   runner: broker
#   status: unclaimed
#   prompt: "review the auth change"
```

You are the executor. Every action takes `--as runner:<identity>`, because a claim must be
attributable, and the first act claims it (`task get` then reads `status: running`):

```sh
verse task do note "$ID" "reviewing the auth change" --as runner:scott
```

Mid-work you reach a decision you should not make alone. Raise it *up* to verse rather than
deciding — the verse governs who resolves it, and the executor never resolves its own gate.
`--wait` blocks for the outcome:

```sh
verse task do gate "$ID" deploy-approval --as runner:scott --reason prod --wait
verse task do gate "$ID" unsure --as runner:scott --to peggy@example.com --reason "which region?"
# gate raised: deploy-approval-a1b2c3d4
# awaiting resolution…
verse task approve "$ID" deploy-approval-a1b2c3d4 --as lead@example.com   # …from someone with the authority
verse task do complete "$ID" "LGTM — one nit on the token TTL" --as runner:scott
```

`--to` is who a worker-raised question is held out to — a person, a group's address, a kind —
since a worker asking somebody is what raising a gate is for. Where the spawn's own gates carry
a gate of that name with `answerers`, the spawn's stand and `--to` is ignored: that gate is the
charter's policy, and the runner does not get to soften it.

A gate raised this way is the same record a charter gate poses, so it can take outcomes of its
own, a threshold per outcome, and a clock:

```sh
verse task do gate "$ID" rollout --as runner:scott --options canary,full:2,abort --deadline 4h:abort --wait
verse gate <id> --answer canary --as lead@example.com
# rollout-… settled on canary
```

The task is now `done`, with your result on its `verse.task.instance` entity, so a completion
hook or a rule reacts to it exactly as if an agent had finished. A *human* did the work; the
flow moves on.

So a task has two vantages, and every verb works whether it runs locally or behind a
broker. From outside, the operator drives and watches: `list`, `get`, `attach`, `cancel`,
`say`, `vector`, `approve`/`deny`. From inside, on the worker leg, the executor acts:
`do complete|fail|note|gate`, plus `inbox` to see the control queued for the worker.

*Left to do: the inbox long-poll.*

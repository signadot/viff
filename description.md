# task: a task ends on its record, a claim is what versed owes, and a runner keeps a lease

Status: implemented, as eleven commits on `task-end-5-runner-lease` over `main`: each stage
reviewed, then the stack reviewed whole, and green on `make check` and the full suite. It replaces the approach in PR #14. The
design is meant to be taken whole or dropped: every hole listed under "The problem" is closed by
the four statements of "The model", and what they do not cover is said under "Beyond the model".

## The problem

A task runs on a runner while versed, the runner and docd each go up and down on their own.
Three things depend on the task ending: its record, its completion hook, and the hold its rule
has on the subject. Today each is settled by a different mechanism, and they disagree in most
outages.

| # | What happens | Hook today | Subject today |
|---|---|---|---|
| 1 | Task ends, everything up | runs | freed after the hook |
| 2 | versed restarts; the runner kept the task or its end | runs | held until then |
| 3 | versed and the runner both restart | `onFailed` | freed |
| 4 | The runner restarts, versed stays up | lost | held until a later versed restart |
| 5 | The runner is gone for good | lost | held for good; the record says `running` for good |
| 6 | versed dies between the terminal write and the hook | lost | freed |
| 7 | A scope's task, versed restarts | lost | held for good |
| 8 | A 5xx on the event stream from in front of a healthy runner | `onFailed` | freed, with the task still running |
| 9 | versed and the runner are partitioned, and the task is fine | lost | held; the task runs on unwatched |
| 10 | The stream goes silent without closing (a half-open connection) | never | held for good; nothing times out |
| 11 | docd is down when the task ends | runs, its writes fail | held until a restart |
| 12 | versed dies while a sequence is between a task ending and its next element | lost; the claim is dropped and the run starts over if the rule fires again | freed |

Two causes cover all of it.

- **What versed owes lives in memory.** The only record that a hook is owed is an entry in
  `Engine.live`. Dropping the entry loses the hook; failing to rebuild it after a restart
  loses it too.
- **Nobody is responsible for a task versed cannot see.** The runner keeps running it, versed
  keeps waiting or stops waiting, and neither tells the other.

## The model

Four statements.

**1. A task ends when its record says so, and only then.**
The record's terminal status is the one fact versed acts on. The engine does not act on the
event stream. While a task is followed, the mirror is the only writer of that status, and that
write always lands. A record found at start with no end, and with nothing running its task, is
ended then: `failed`, `orphaned: true`.

**2. A claim on a subject means versed owes it something.**
A hooked spawn's claim is taken before the spawn starts and released after its hook has run. A
sequence's claim is taken before its first element and released when the run concludes. Both
are durable, so they are the record of what versed owes, and they outlive every process.

**3. A runner runs a task only while versed is watching it.**
Each task has a lease, four minutes unless its spawn says otherwise. versed asks for each
live task's events several times within its lease. A runner that has not been asked for
longer than the lease ends the task itself. versed's own wait for that task is longer than
its lease, so by the time versed fails a task for a runner it cannot reach, the runner has
already ended it.

**4. Every hole ends `failed`, and says it was orphaned.**
A task that ended because the two sides lost each other is `failed` with `orphaned: true` on
its record. It reaches `onFailed`, which reads `.[orphaned]`.

### The two invariants

> **Owed.** A claim that versed finds at start with no task resumed for it is an ended task
> whose hook is owed: the hook for the status on its record, or `onFailed` where the record
> has none.

> **Watched.** If versed has written `failed` for a task whose runner it could not reach, that
> task is not running on its runner.

"Owed" is versed's alone and needs nothing from the runner. "Watched" is what statement 3
buys, and it is why the subject can be freed safely.

While versed runs, "Owed" needs no checking: a task leaves the live registry only by its
record ending, and that is the event that runs its hook.

### The lease and its clocks

The lease is the task's own. A spawn sets it with `lease`, a duration beside `timeout`
(`docs/reference/action.md`), and `verse task run` takes the same. With none said it is 4m.
The other two clocks are worked out from it, per task, so they cannot be set apart from it.

| Clock | Length | At the default | Who keeps it | Counts from |
|---|---|---|---|---|
| Resubscribe | lease / 4 | 1m | versed | its last subscribe |
| Lease | as the spawn says | 4m | the runner | the last subscribe it accepted for the task, or its launch |
| Wait | lease + 1m | 5m | versed | the last subscribe the runner accepted |

All three count requests, not open connections, so a half-open connection renews nothing. The
runner's count starts no later than versed's, because the runner accepts a subscribe before
versed hears that it did. So the lease always runs out first, by a minute less the time the
runner takes to stop a process.

What a lease's length trades:

- **A longer lease** lets a task outlast a longer versed outage or partition. Its subject is
  held that much longer when the runner is really gone.
- **A shorter lease** frees a subject sooner. The task dies in a shorter outage, and versed
  asks its runner more often.

A lease has a floor of 1m. Below it the wait's one-minute margin no longer covers the time a
runner takes to stop a process and a few missed resubscribes. A spawn that says less is
refused at install, as a malformed `timeout` is.

The lease travels on the launch and is written on the task's record, so a versed that
restarts while the task runs keeps the same clocks for it.

## Walked through

A hooked rule `ask` spawns `ask@demo.pr.1` on a remote runner.

**The runner restarts (row 4).**
1. The stream closes with no terminal frame. Nothing in the engine reacts; the task stays live
   and the subject stays held.
2. The mirror resumes. The restarted runner does not know the task and answers 404.
3. The mirror writes `failed`, `orphaned: true`. The engine sees the record, retires the task,
   runs `onFailed` and releases the claim.

**versed and the runner are partitioned (rows 5, 9, 10).**
1. versed's subscribes stop being accepted. The task stays live in versed.
2. At 4m, the task's lease, the runner has accepted no subscribe for it. It stops the task's
   process and ends the task `failed`, `orphaned: true`.
3. At 5m, the wait, the mirror writes `failed`, `orphaned: true`. The engine runs `onFailed`
   and releases the claim. The task is not running.
4. If the partition heals before 5m, the mirror's resume replays the runner's own `failed`
   frame instead, to the same end.

The same spawn with `lease: 30m` rides out a 20-minute partition untouched: the runner keeps
the task, versed keeps the subject held, and the mirror resumes from its cursor when the
partition heals.

**versed dies after the mirror wrote `done` and before the hook ran (row 6).**
1. At start, versed finds the claim `ask@demo.pr.1` with no task resumed for it.
2. The record says `done`. versed runs `onDone` and releases the claim.

**docd is down when the task ends (row 11).**
1. The mirror hears `done` and its write fails. It keeps trying. The task stays live and the
   subject stays held, because nothing acts until the record says so.
2. docd returns. The write lands, the engine runs `onDone` and releases the claim.

**versed dies between a sequence's second task ending and its third element (row 12).**
A rule `review` runs `seq: [lint, test, report]`. The subject says `step: 1`, and the record of
`test`'s task says `done`.
1. At start, versed finds the run claim `review@demo.pr.1` with no task resumed for it.
2. The element in flight is the one at `step`, `test`. Its record says `done`, so the run
   advances as it would have: `step: 2`, and `report` runs.

## Beyond the model

**An uninstall ends a package's work.** Uninstalling a charter package locks it, cancels its
tasks so that their hooks run, then removes its rules, its records and its engine. A change to a
package being uninstalled is refused with `429`, to be asked again. A pruning install uninstalls
the packages its directory leaves out first.

**"Owed" has one exception.** A claim whose charter package is gone has no rule, so no hook. Its
task is cancelled, and the claim is removed once the task's record has ended.

**"Watched" holds on a runner that keeps the lease.** It does not cover a runner that froze and
then continued, a runner that ignores the lease or a cancel, a runner whose answers to versed's
subscribes are lost on the way back, or a command that detached into a session of its own. A subprocess task whose lease runs out is stopped by ending its process, so
its teardown steps do not run. "Where 'Watched' does not hold" has the table.

**Deploy order.** versed first, runners after, in one tag. A runner that keeps the lease under a
versed that subscribes once ends every task at four minutes, and a versed that takes only a
runner's marked answers (`Verse-Runner`) takes an older runner's subscribes as unanswered, and
gives its tasks up a lease and a minute on. The runners follow within that.

## What changes

### The engine (`internal/trigger`)

- **The record ends a task; the stream does not.** `reapOnDone` is removed. `reapLoop`, which
  already watches the task records in both baseline and scoped engines, is the one path that
  retires a task and runs its hook. `taskEvent`, which rebuilt a record the mirror had not
  written yet, goes with it.
- **A spawn that never started is settled in the firing.** A launch refused before it has a
  record (the id stands as another party's) has nothing for `reapLoop` to see. The engine
  recognises a handle for work that never started, runs `onFailed` and releases the claim
  there, and does not track it.
- **A claim with no task is settled, at start.** One function takes a claim and a task id,
  reads the task's record, and:
  - writes `failed`, `orphaned: true` on a record that stands with no terminal status;
  - navigates the chain to its action, as `AdoptChain` does;
  - runs what the status calls for through `applyHook`, as a live task's end does, which
    releases the claim.
  Where no record stands at all, none is written: the hook is handed `failed`,
  `orphaned: true` and the reason directly. A chain that no longer resolves releases the claim
  with nothing run, as `AdoptChain` treats it.
- **A claim's release always lands.** The delete is retried until the store takes it, as the
  mirror's terminal write is. A release that is dropped is a subject held until a restart.
- **`onOrphaned` is removed from the action.**

A hook that cannot be resolved, a reference with nothing behind it, is a hook failure, recorded
on the subject and the task as now. A hook's write the store does not take is not recorded: it
is logged as a warning and its effect is lost, since recording it needs the store that did not
take the write. The claim is released either way.

### Sequences

A sequence already writes down where it is: `verse.charter.<rule>.step` on the subject counts
the elements that have completed, and the run's claim stands at the rule's chain (`seq.go`).
`applyHook` already carries a sequence when one of its elements ends: `done` advances the run
(`resumeSeq`), any other end stops it and runs the element's off-ramp hook (`stopSeq`). It
reads nothing from the task's handle, so it serves a task the engine never tracked. Settling
a sequence is therefore settling its element in flight, the one at `step`, whose task id
follows from its chain and the subject as every spawn's does.

At start, spawn claims are settled first, then each run claim that still has no live task:

| The element at `step` | What the settle does |
|---|---|
| Its task's record says `done` | the run advances, and the next element runs |
| Its task's record says anything else terminal | the run stops, `ended: <status>`, and the element's off-ramp hook runs |
| It spawns, and its record is not terminal or is not there | as `failed`, `orphaned: true`: the run stops and its `onFailed` runs |
| It spawns nothing | the run stops, `ended: error`, with the reason `verse restarted before this element ran` |
| `step` equals `steps`: every element completed | the sequence's own `onDone` is owed; see below |

Three changes to `seq.go` make that safe.

- **An element's end advances the run only while `step` still names that element.** Settling
  can run an element's end a second time, where versed died after the run had advanced and
  before the element's own claim was released. Advancing again would run the next element
  beside its own live task, and where that element holds a claim, would release the run's
  claim under it. So a second end of an element whose step has passed releases that element's
  claim and does nothing else.
- **A sequence's claim is released after its `onDone` has run, not before.** `finishSeq`
  releases first today, so a versed that dies between the two loses the hook with nothing left
  to say it was owed. Released after, the claim found at start with `step` equal to `steps`
  is that debt, and it is paid by ending the last element again.
- **An element that spawns nothing is never run twice.** It reads the facts of the task before
  it, or of the firing, and a restart has neither to hand. So a run caught there stops with
  the reason on the subject, where it is seen, and the rule runs it again when its shape
  changes, as for any stopped run.

A sequence whose last element spawns nothing, caught after that element and before its
`onDone`, loses the `onDone`: there is no task whose end can be run again. The run is recorded
`ended: error` with that reason.

### The mirror (`pkg/mirror`)

- **It resubscribes four times a lease.** A stream older than the task's resubscribe clock is
  closed and resumed from its cursor. Resuming is lossless, so at the default this costs one
  request a minute per live task.
- **Resubscribing does not wait on the store.** The resubscribe clock runs apart from the
  mirror's record writes. One write can block for 30s (`writeBudget`), a burst of frames is a
  run of writes, and the mirror writes them one after another, so a slow docd would otherwise
  cost a task its lease.
- **Its wait is a length of time, not a count of attempts.** It runs from the last subscribe
  the runner accepted. An accepted subscribe is known from the client, which reports it as a
  frame of its own before any of the runner's.
- **At the end of the wait it writes `failed`, `orphaned: true`,** as any terminal is written:
  record, run record, the task's open questions settled.
- **A terminal write always lands.** One the store does not answer is retried until it does.
  One the store refuses, as it does where the worker's grant on the task was removed while it
  ran, is written as the system. Every other write of a live record may still be dropped.

### versed (`internal/versed`)

- **`reconcileClaims` settles instead of deleting.** Every claim with no task resumed for it
  goes to the engine's settle: spawn claims first, each with its own task's id, then the run
  claims that still have no live task, each with its element in flight.
- **A record that names a runner url is always re-attached.** The mirror's wait decides what
  becomes of it. Only a record whose runtime was versed's own process is failed at start, with
  `orphaned: true`.
- **Tasks are resumed first at start.** Re-attaching is the first thing the start does for
  baseline, ahead of the charter and the suspensions, since every task's lease has been
  running since versed went down. A resumed task under a suspension is paused when the
  suspensions load, as a task tracked later is.
- **A scope is reconciled when its engine opens, not at start.** Scopes cannot be listed, so
  versed cannot go looking for them. When a scope's engine opens, versed resumes the scope's
  tasks and settles its claims, as it does baseline's at start. See "Scoped tasks".

### Scoped tasks

A scope's tasks get less than baseline's across a versed restart, and only there. While versed
runs, a scope's engine is open and everything above holds for it.

After a restart, until the scope's engine next opens:

- **Its tasks are unwatched.** Nothing resubscribes, so each runner ends them at their lease.
  "Watched" still holds, since the runner keeps it.
- **Its records say `running`** about tasks the runner has ended.
- **Its hooks have not run and its subjects are held.**

When the engine opens, the resume hears each runner's own `failed`, `orphaned: true` end (or a
404), the records are put right, the hooks run and the claims are released. A scope nobody
opens again stays as the three points above describe.

So a baseline task survives a versed restart shorter than its lease, and a scoped task
survives one only if its scope's engine also opens within its lease.

### The runner client (`pkg/runner/client`)

- **Only the runner fails a task, and on both answers.** The event stream and the task's
  snapshot are two requests. The runner has no such task when it answers 404 to both; that is
  `failed`, `orphaned: true`. A 404 on the stream alone is two answers that disagree, asked a
  moment apart, and concludes nothing. Any other 4xx or 5xx, or no answer, is an outage. In
  both of those the stream closes with no frame, and the mirror asks again until its wait
  runs out. A snapshot that shows the task ended is that end, whatever the stream answered.
- **A subscribe that gets no answer fails.** The stream's transport times out waiting for
  response headers, so a request into a dead network counts as not accepted.

### The runner (`pkg/runner`, `pkg/runner/runnerd`)

This is a change to the runner's contract, and the design does not hold without it.
"Watched" cannot be kept by versed alone: only the runner can stop a task versed cannot reach.

- **A launch carries its task's lease.** A length of time, beside `timeout`. A launch with
  none is given 4m by the runner.
- **The runner ends a task nobody has asked about for its lease.** Each handler notes when it
  last accepted a subscribe for the task. Past the lease, it stops the task and ends it
  `failed` with `orphaned: true` and an `err` naming the lease, such as `no reader for 4m`.
- **A subscribe is answered at once, whatever state the task is in.** The subprocess handler
  holds a subscribe with no response until the task's process is hosted, which a first clone
  can delay past any lease (`forwardOp`). versed would count those as not accepted and fail a
  task that is still starting. The stream opens at once and its frames follow when there are
  some.
- **A subscribe that arrives past the lease does not renew it.** The task is ended first, and
  the subscribe is answered with that end. The same holds for a launch of its id.
- **`orphaned` is a field on the terminal frame and the snapshot,** so the record can say who
  ended the task.
- **Two copies of a task never run.** Both handlers already hold this: a launch of a live id
  is answered by the task that has it (`subprocess.go`, `launch`; `inprocess.go`, `Launch`).
  With the lease, a relaunch after an orphaned end finds the id finished and runs it again, so
  a new firing does not inherit an old firing's work. The comment in `fire.go` (`startHooked`)
  that says a runner replaces a live entry describes neither handler, and is corrected.

### The record

| Who ended the task | `status` | `orphaned` | `err` |
|---|---|---|---|
| The task, or its runner for the task's own reasons | as reported | absent | as reported |
| The runner, lease run out | `failed` | `true` | `no reader for <lease>` |
| The runner has no such task (404) | `failed` | `true` | `the runner has no such task` |
| The mirror, wait run out | `failed` | `true` | `the runner did not answer for <wait>` |
| versed at start, the runtime was its own process | `failed` | `true` | `its runtime ended with verse` |
| The settle, claim with no terminal record | `failed` | `true` | `no account of how the task ended` |

`orphaned` is one of the facts a hook always has, with `status`, `result`, `err` and `data`
(`taskFacts`): `false` where the record does not say it. An absent path refuses a firing, so a
hook that reads `.[orphaned]` must find it on every end.

`orphaned` stays a status for questions and asks.

## Checked against the problem

| # | What happens | Hook | Subject | Task |
|---|---|---|---|---|
| 4 | The runner restarts | `onFailed`, orphaned | freed after it | ended with the runner |
| 5 | The runner is gone for good | `onFailed` at the wait | freed after it | gone |
| 6 | versed dies before the hook | the record's hook, at start | freed after it | ended |
| 7 | A scope's task, versed restarts | when the scope's engine opens | freed after it | ended by the runner at its lease, unless the scope opens first |
| 8 | A 5xx from in front of the runner | none; the mirror resumes | held | running, watched |
| 9 | A partition longer than the wait | `onFailed` at the wait | freed after it | ended by the runner at its lease |
| 10 | A half-open connection | none; resubscribed within lease / 4 | held | running, watched |
| 11 | docd down at the end | runs when the write lands | held until then | ended |
| 12 | versed dies mid-sequence | the run advances or stops from its `step`, at start | freed when the run concludes | ended |

Row 12 advances only where the element in flight has an ended task. See "Sequences" for the
cases that stop.

## What it costs

- **A versed outage longer than a task's lease fails that task.** Today a task runs on while
  versed is down for any length of time. Under this design it runs on for its lease: four
  minutes by default, longer where its spawn says so. This is the price of "Watched". A versed
  start takes one to two minutes to reach the resume today. With the resume first, a task at
  the default lease survives a restart whose downtime and resume together stay under four
  minutes.
- **Hooks are at least once.** versed dying between a hook's writes and the claim's release
  runs the hook again. That holds for a spawn's hooks, an element's off-ramp hooks and a
  sequence's `onDone`. A hook must tolerate a second run, and `docs/reference/action.md` says
  so. A sequence's elements are still at most once.
- **A sequence caught between elements by a restart stops.** Where the element in flight has
  no ended task to go by, the run ends `failed` or `error` and does not continue.
- **A hook's write that fails as docd goes down is lost,** not retried. It is logged as a
  warning and recorded nowhere, since the record would need the store that did not take the
  write. The claim is released once docd returns, and the subject stands where the rule left it.
- **Four requests a lease per live task,** from versed to its runner: one a minute at the
  default.
- **A hook runs one store round trip later than it can today,** since it waits for the record.
- **Tests that end a task on the stream alone need a record.** 16 engine test files, 83 test
  functions at most, drive a spawner with no mirror. Those among them that end a task on the
  stream end it by writing the record instead. I have not counted how many do.

## Where "Watched" does not hold

"Watched" is exact on a runner that keeps the lease and whose machine keeps running. Outside
that, the task can outlive versed's `failed`.

| The runner | The task runs on until | A new firing inherits old work |
|---|---|---|
| Froze, then continued | the rest of its lease | yes, if the rule fires again within it |
| Ignores the lease | its own end or `timeout` | yes, if the rule fires again |
| Answers subscribes whose answers never reach versed | one lease after versed's last subscribe | yes, if the rule fires again within it |
| Accepted a launch from a versed that died before the task's first record was written | one lease after the launch | yes: the next versed settles the claim as orphaned, and a firing within the lease is answered with the running task |

In every row versed's books are the same: the record is `failed`, `orphaned: true`, the hook
has run and the subject is free. What differs is the task's process, and until it stops it
can still act on anything it reaches without versed.

The design does nothing more about any of them. The lease is the runner's contract:
`docs/runners.md` states it, both handlers are tested against it, and a runner that does not
keep it is fixed by keeping it.

## Checked

By reading `main`; none of it was run.

| What the design relies on | Holds | Where |
|---|---|---|
| The task-record watch survives a docd reconnect | yes | `pkg/entity/pump.go`: a dropped or refused watch is established again with backoff, and delivers the gap. A subscriber's channel closes only when it leaves, the store closes, or logd calls the path invalid (`uncurable`). |
| A slow engine does not miss a terminal | yes | `pump.go`: a subscriber that falls behind gets the net of what it missed, and a terminal status is still standing in that net. |
| A spawn claim's name is its task's id | yes | `claimRun` and `working` both use `spawnID(owner, chain, subject)`. |
| A sequence's position is durable | yes | `seq.go`: `step`, `steps` and `shape` are published under `verse.charter.<rule>` on the subject, and the run claim stands at the rule's chain. |
| The record watch runs in baseline and scoped engines | yes | `watchTaskRecords`, called from `Start` and `ObserveScoped`. |
| `applyHook` serves a task the engine never tracked | yes | `flows.go`: it reads the task's id, subject, chain and action and the record's payload, and nothing from the handle. `navigate` resolves an element's chain (`chain.go`). |
| `applyHook` is safe to run twice for a sequence element | no | `resumeSeq` advances whatever `step` says, and `advanceSeq` releases the run's claim where the next element is held. Fixed by the first change under "Sequences". |
| A sequence's `onDone` is owed by a claim | no | `finishSeq` releases the claim before the hook. Fixed by the second change under "Sequences". |
| One store write is bounded | yes, at 30s | `pkg/entity/logdstore.go`, `writeBudget`. |
| The mirror's record writes carry no precondition | no | A write two or more steps deep carries the descend guard, asked at its parent (`descendsThrough`): for a task record that is `verse.task.instance`, whose size is the number of task records. A record's first write as a principal adds an absence guard there too. They are narrowed to the parent, not asked at the root. |
| The runner holds a subscribe for an unhosted task | yes, and it must not | `subprocess.go`, `forwardOp`: it waits on `p.ready` before any response. |
| Scopes can be listed at start | no | Decided: a scope is reconciled when its engine opens. |

Not checked:

- **What a mirror write costs on staging now.** The guard at `verse.task.instance` cost about
  80ms at 3,000 children when it was last measured (go-tony v0.0.217). Whether go-tony now
  evaluates only what the pattern names, and what the same write costs beside a compaction,
  are not measured. Resubscribing apart from the writes makes the lease independent of the
  answer.
- **What moving the resume first costs a versed start.**

## Tests this needs

- Each row of "Checked against the problem", on the engine with a mirror.
- On each runner handler: a task with no subscribe for its lease ends `failed`, `orphaned`; a
  subscribe inside the lease keeps it; a launch of a live id starts nothing.
- On each runner handler: a subscribe or a launch that arrives past the lease finds the task
  ended and does not renew it.
- On the subprocess handler: a subscribe for a task still cloning is answered at once, and
  keeps its lease.
- A claim found at start with a `done` record runs `onDone`; with no terminal record,
  `onFailed`.
- A run claim found at start: each row of the table under "Sequences".
- An element's end run twice advances the run once.
- A sequence's `onDone` runs where versed died after its last task ended and before the hook.
- A terminal write the store refuses lands as the system's.
- The lease runs out before the wait, with both clocks injected, at the default and at a
  lease the spawn set.
- A spawn with a lease under the floor is refused at install.

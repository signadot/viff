# Suspending charters and subjects

Status: proposal, nothing here runs. Design exploration for git-issue `xr69yacwh12kr4qamsn0`,
"control over subjects and charters". The ask:

- pause and resume a charter for all subjects;
- pause and resume a subject for all related charters;
- pause and resume a specific subject on a specific charter;
- the state is not per-principal: things are running or paused, and a person unaware of somebody
  else's pause resuming it is expected.

This note argues for one design, rules out the others by name, and says what adopting it costs the
rest of verse. It rests on a reading of the engine at `ead39d3d`, on how eleven comparable systems
model the same thing ([prior art](#prior-art)), and on three adversarial passes against the code
([how this was checked](#how-this-was-checked)): two broke the drafts before this one, which each
exempted work already running, and the third read this one and found the mechanisms it had left
implicit. It covers the work about a subject in every form, the way a standing resolver does. It
takes verse's scope model as given ([what a scope owns](../reference/connect.md#what-a-scope-owns)):
a suspension is a control write, and belongs to the scope it was made in
([below](#a-suspension-belongs-to-the-scope-it-was-made-in)).

## The design in one paragraph

A **suspension** is one record naming a charter (or every charter), a rule of it (or all), and a
subject (or every subject); the issue's three cases are the three shapes of it. It covers **the work
about a subject** the way a standing resolver does — every form of it, by one test — and a suspended
subject is suspended in all respects: a rule starts no firing about it, a task about it holds at its
next boundary, a question about it is withdrawn from in front of people, and a hook or `seq` step
about it records what happened and starts nothing. When the suspension lifts, each of those is
released in the order that keeps them consistent — tasks resume, parked launches start, then the
covered rules are re-armed, the path an install and a restart already take — so a rule acts on the
world as it stands when the suspension lifts, and nothing that happened meanwhile is replayed. A suspension belongs to the scope it was
made in, baseline being the empty scope, and covers only the work that scope created. The
suspensions in force are one in-memory table per scope, written only by the route that also writes
the store, and loaded before that scope's tasks are re-attached or its charter restored.

```tony
# verse.trigger.suspension."verse-dev.drift:*@git.verse.*"
charter: verse-dev.drift         # an installed charter's address, trailing * allowed; "*" for every charter
rule: "*"                         # a rule's name in it; "*" for all of them
subject: git.verse.*              # an address, trailing * allowed; "*" for every subject
scope: ""                         # the scope it was made in, as a rule record's; "" is baseline, packages included
held:
  user:walter@example.com:
    why: "reviewing the drift charter's range logic before it runs again"
    since: "2026-09-14T03:10:00Z"
    occurrence: 41872              # the store revision it was written at, as a run record's
```

```sh
verse suspend verse-dev.drift --why "…"                     # a charter, about everything
verse suspend '*' github.signadot.verse.pr.'"42"' --why "…"  # a subject, for every charter
verse suspend verse-dev.drift:claim git.verse.head --why "…"
verse suspend                                                # what is suspended, by whom, why, since when, what it has held
verse resume  verse-dev.drift                                # the same words lift it
```

## What it rests on

### One record for the relation

The issue's three cases are one relation: *work is suspended when some record covers its charter and
its subject*. Charter-about-everything is `subject: "*"`, subject-for-every-charter is `charter:
"*"`, and the pair names both. Sensu's silences are named exactly this way (`*:check`, `entity:x:*`,
`entity:x:check`), and a Datadog downtime is a monitor selector crossed with a scope; both reached
the record after the flag.

A flag in two places — `paused` on a charter's record, something on the subject — fails here for the
reason it failed elsewhere. Flux and Argo CD document an object's owner overwriting a pause somebody
put on it; in verse a subject is a world entity its source rewrites at every resync, and a rule's
record is re-put by every install.

The record is keyed by what it covers, so suspending the same thing twice is one record — but it
keeps **who holds it and why**, per holder, because Airflow's docs have to tell an operator to write
down which DAGs were already paused before pausing them all: one boolean cannot remember two
reasons. The issue's semantics are kept: a `resume` by anyone entitled to make it lifts the
suspension whoever held it. One asymmetry is kept too: a resume made by the system removes only the
system's own holder, so an automatic suspension lifting itself never lifts a person's.

Each holder also records the store revision its suspension was written at — `occurrence`, the run
record's spelling for "the revision this is about" — beside the wall-clock `since`. The write
returns it, so it costs nothing, and it is the anchor the store side needs: the listing compares
it with the head to say how much has moved under a cover, `verse data --since` reads from it to
show what a suspended rule did not see, and it is the number a task record's `occurrence` and
status's `storeRev` are compared against. Nothing is replayed from it; resume stays a current-state
replay ([below](#resume-is-ordered)).

**Suspensions only add up.** Work is suspended if any record covers it. No record exempts a subject
from a wider suspension, and resuming `verse-dev.drift git.verse.head` while `verse-dev.drift`
stands lifts that record and says the pair is still covered, and by what. Argo CD's allow and deny
sync windows, with precedence and a rule for running work, needed a nine-case table to document.

### A suspended subject is suspended in all respects

Verse already has one thing that covers *the work about a subject* uniformly: a standing resolver.
Its `condition.subject` is asked of a trigger gate's question (`resolver.Owner`,
`resolver/resolver.go:404`) and of the questions a task raises (`mirror.Meta.Owner`, "which standing
binding covers this subject", `mirror/mirror.go:150`), in a `seq` or not, by one test,
`Condition.Covers`. Nothing asks what kind of work it is. A suspension covers the same work by the
same kind of test, and that uniformity is what keeps the model small: there is no class of work that
is "already running" and therefore exempt, and so no classification to argue, document, or get wrong
when a new kind of work is added.

Every form of work already carries its subject — a firing's selected member, a task's `flow`, a
question's `subject`, a hook's `flowTask.subject` (`trigger/engine.go:36`) — and its charter: the
engine it runs in, and the chain it was spawned at. So one predicate, *is (scope, charter, rule,
subject) suspended now?*, is asked of each form where it would next make progress, and each form
already has a way to wait:

| work about the subject | while suspended | when the suspension lifts |
|---|---|---|
| a rule about to fire | starts no firing | the rule is re-armed |
| a firing waiting at its gate | its question is withdrawn, ballots kept; the firing waits for the lift | it poses the same question again, if the rule still selects the subject |
| a running task | holds at its next boundary, by the pause `flow pause` uses, marked as the suspension's | resumes, if only the suspension held it |
| a task's own question | withdrawn by the task's pause, as today | asked again by the task, as today |
| a completion hook, or a `seq` step | its writes land; its spawn is parked before any selection is posed | the parked launch starts, before the rules are re-armed |
| a deadline on a covered question | its clock stops | re-armed with the time it had left |

**A rule about to fire** is asked at the three places a firing can begin, and at the two places
every spawn and every charter write converge. First at the top of `evaluate`, **before
`firstSight`** (`edge.go:56`): a rule skipped there does not advance its watermark, so the seed
replay a lift opens at the head is admitted whether or not the lift wrote a commit. Placed after the
watermark, as a draft had it, the skip would burn the revision, and a re-arm whose seed lands at
that same head — a scope's rules armed before its table loaded, a rule installed during a restore —
would be refused and never evaluate its backlog. Then in `fireSubjects` before the cycle throttle
(`engine.go:749`), because `throttled` counts the trip and marks the chain reported
(`trigger/causal.go:264`); and on entry to `fireOne`, because a paced firing waits out the throttle
first. The two backstops are `spawnHooked` (`engine.go:1222`), which every spawn passes through — a
provider selection resumed at restart reaches it from `ResumeSelections` with no firing and no hook
above it (`trigger/select.go:282`) — and `patchEntity` (`engine.go:1762`), which every
charter-authored write does. Never in subject selection: selection is what `untilMoot` watches to
decide a question has gone moot (`engine.go:932`), and a suspension there would *moot* work rather
than hold it.

**A question** is withdrawn rather than left standing, for the reason the task runtime already gives
when a task is paused mid-question: "a held task must not leave one in front of somebody", and "a
pause is not an answer" (`task/machine.go:363-389`). The task machine withdraws the question and
asks the same one again when the hold lifts. A firing waiting at a trigger gate has no such loop
today: its goroutine blocks in `Registry.Resolve`, which leaves the docket only when the waiter's
context is cancelled, and then returns the cancellation to the firing, which ends
(`task/registry.go:109`, `engine.go:910`). So the gate goroutine gains one. Each pose gets a context
of its own, derived from the mootness context; a suspension cancels it with itself as the cause, so
the record goes `orphaned` with a `why` naming the suspension, and its ballots and its deadline
stamp stay — `clearPriorAnswer` clears those only for an *answered* record
(`mirror/question.go:743`). The goroutine keeps its flight claim and waits for the lift; then, if
the rule still selects the subject (`stillSelects`), it poses the same deterministic id again,
joining what was cast, and `askedAgain` says so. Mootness still ends a suspended firing, since the
per-pose context sits under it. Nobody can answer a question about suspended work, so no answer is
ever given and then held; and `settleByDeadline` ignores an orphaned question
(`versed/server.go:2709`), so nothing settles while it is withdrawn.

**A withdrawn question's clock stops.** A deadline bounds how long a question stands in front of
people, and a withdrawn one stands in front of nobody; Kubernetes stops a paused Deployment's
progress deadline for the same reason. Nothing new is recorded for it: `DropQuestion` already stamps
`orphanedAt` (`mirror/question.go:578`) and the deadline stands as an absolute `deadline.at`, so the
time the question had left is one subtraction, and the re-pose re-arms it at the lift plus that
remainder. A deadline that had already lapsed when the suspension landed settles on the re-pose, as
it would have.

**A running task** is held at its next boundary by the pause every runtime already implements —
local, remote and brokered alike (`task.Handle.Pause`) — with one distinction the runtime lacks:
whose pause it is. `pauseGate` is one bool (`task/pause.go:15`), `PauseFlow` and `ResumeFlow` are
unconditional (`engine.go:1601`), and the mirror projects one `paused`. Without a mark, `flow
pause`, then a suspension, then `flow resume` runs a covered task, and a lift resumes a task an
operator paused. So the engine records who holds a task (`heldBy` on `flowTask`): a lift resumes
only what the suspension paused, and `flow resume` refuses a task the table covers, naming the
suspension. A task re-attached after a restart is held when `track` first sees it, because the table
covers it; tasks are found through `flowEngines` for the scope (`versed/owns.go:34`). A task spawned
by hand is in no engine's live map, which the scope-owns work noted and left; holding it needs a
handle attached through its runner, as `reconcileTasks` attaches a survivor's.

**A hook lands; its launch is parked.** A hook is where a charter records what a task did, and
recording is not progress: the hook's patches and derives land at once, and what they wake is
already covered — the rule that reads the latch is refused before its watermark moves, and the
re-arm on lift replays the world as it then stands. Earlier drafts held the hook itself, because a
resumed rule would otherwise re-spawn the task the hook had not yet recorded; letting it record
removes that hazard rather than engineering around it, and with it the parked-hook list, the mark on
the task record and the restart sweep that re-parked from it.

What a hook must not do is **start** something. Its spawn is parked at the entry of `launchSpawn`
(`trigger/trigger.go:926`) — after the writes, and before `selectProvider`, which for `select: ask`
blocks on the resolver (`select.go:85`). That placement is forced: every completion path — `reap`,
`reapLoop`, `reapOnDone` (`engine.go:1633`, `1659`, `1318`) — retires the task and calls
`applyHook` (`engine.go:1353`) on the engine's one reap goroutine, `seq` steps are reached only
from it (`resumeSeq`, `stopSeq`, `engine.go:1384`), and a selection posed from there under a
suspension would be withdrawn and wait for the lift with every completion in the engine behind it,
the defect a selection inside a hook already has. A hook poses no gate of its own — gates are the
rule's, posed in `fireOne` before its action (`engine.go:811`) — so the selection is the only
question a hook can ask, and parking before it means a hook asks nothing while suspended. What is
parked is `launchSpawn`'s argument list — the action, the chain, the subject, the root payload and
the occurrence — in a per-engine list, and the lift re-enters `launchSpawn` with each, which is the
move `ResumeSelections` already makes after a restart and the reason that function stands alone.
The `spawnHooked` backstop stays for the one caller that reaches it with no firing and no hook
above it, a resumed selection.

A parked launch does not survive a restart. A hook's spawn due during a suspension that spans one
is lost — which is what a hook due during a restart does today, and the gap this note no longer
claims to close.

### Resume is ordered

Releasing everything at once would let a rule see its subject in the state a hook left it in, with
the task that hook was about to start not yet live, and start one of its own — and not only a
re-armed rule. A covered rule's own watch keeps delivering throughout, so the moment the table stops
refusing its firings, any commit under what it reads re-evaluates it; `working` is false because
`retire` already dropped the finished task from the live map (`engine.go:1327`), and `spawnHooked`
finds no live id until the parked launch has started. So the table entry is the **last** thing to
go, and a lift proceeds:

1. **the record is deleted** in the store, so the lift is a commit and the head moves;
2. **the entry is marked draining**: firings are still refused, and everything held is released —
   **tasks** the suspension paused resume from their boundary; **parked launches** — a hook's or a
   `seq` step's spawn — start, in order, until the engine's list is empty; **withdrawn questions**
   are posed again by the firings waiting on them, and their deadlines re-armed with the time they
   had left;
3. **the entry is removed** from the table;
4. **the covered rules are re-armed**: `armTriggerSubtree` opens each rule's watches again — the
   predecessor is cancelled by `registerWatchers` (`engine.go:494`), and in-flight firings run on
   `bgctx` and are untouched — and the opening state is replayed through the rule as an ordinary
   commit, "THE SEED IS REPLAYED" (`entity/logdstore.go:1339`), the path `LoadCharter` restores a
   rule by at every restart. A firing still waiting at its gate holds its flight claim, so the
   replay's `fireOne` about that subject is refused at `claimFlight` (`edge.go:224`), and the two
   cannot collide.

The table's readers never take its writer's lock across a lift; a wide re-arm would otherwise stall
every firing decision in the scope behind it.

The replay is admitted because the check sits before `firstSight`
([above](#a-suspended-subject-is-suspended-in-all-respects)): a skipped delivery never advanced the
rule's watermark, and the seed carries the store head, which the lift's own commit has just moved.
The re-arm needs no record of what was skipped, which is where both earlier drafts lost firings.

What a resumed rule sees is what an install sees:

- **a level rule** still matching acts once; one whose condition came and went meanwhile does
  nothing;
- **an `on` edge** that rose during the suspension is not seen, as an install over a verse where the
  predicate already holds sees none: `on` does not act on a backlog, and a suspension is one. A
  firing an edge had already started is waiting at its gate, and loses nothing;
- **a derive** naming a field another rule owns rewrites it, as at install, which `advise` warns
  about.

A re-arm replays whole rules, not only what was held, and that is its price: lifting `charter: "*"`
about one subject replays every rule in every engine of the scope, a restart's worth of evaluation.

The prior art converges on current state over replay. Temporal's scheduler skips paused time
outright (`service/worker/scheduler/workflow.go`: "use end as last action time so that we don't
reprocess time spent paused"). Airflow 3 turned catchup off by default "to reduce confusion".
Kubernetes documents a CronJob starting a surprise run on unsuspend. Drools and CLIPS cancel an
activation whose condition went false, so a group regaining focus fires on current truth. Kafka
replays because it is a log whose every record must be processed, which a charter is not.

### A suspension belongs to the scope it was made in

[What a scope owns](../reference/connect.md#what-a-scope-owns), landed for git-issue
`h7wwzxtnh12ksc4yh5n0` (`docs/working/scope-owns-plan.md`), settles it, and a suspension takes the
model whole. A scope's **world half** is copy-on-write; its **program half** — rules, tasks, gates,
flows, the things with a live half that cannot be copied — it reads by reference, and a control
write may name a scope only for work that scope created. Named for inherited work it is refused,
409, saying which scope holds it; baseline acting on a scope's work is refused the same way. Perms
take the world's rule instead, because a grant has no live half.

A suspension holds live things, so it is a control write on the program half:

- **it covers only work its scope created.** Made with `--scope me`, it covers the rules scope `me`
  installed, the tasks and questions they spawned, and the tasks spawned in that scope. Made
  without, it covers baseline's and every package's, since packages are baseline's. A baseline
  suspension does not reach into a scope, and a scope's does not reach baseline; naming another
  scope's charter is refused 409 before `control` is asked, which is the order every control route
  now keeps, for every control route;
- **its coverage is decided the way ownership already is** (`versed/owns.go`), with no logic of its
  own: a rule by the engine that `Knows` it, a task or question by which store holds its record
  (`scopeOf`), a flow by the engines of the scope (`flowEngines`);
- **its record is stored in its scope and stamped with it**, as a rule record is, and restored by
  the stamp the way `ownRecord` restores a scope's rules (`trigger/charter.go:126`);
- **its table is its scope's**: baseline's loads first at startup; a scope's loads when the scope's
  engine opens, under the same `sync.Once` as `restoreScopeCharter` and before it arms a rule,
  beside the scope's own perms mirror (the attribution's `children`, `entity/attribution.go:95`),
  and goes with the scope in `deleteScope`;
- **its authority is read from its scope's mirror**: a scope's suspension against the scope's
  layered mirror, baseline's grants and the scope's own; baseline's against baseline's.

### One table per scope, one writer

`docs/deployment-and-steering.md` holds that an intervention must never queue behind observation.
Tonight's ship is the evidence: for minutes after the wipe the store answered past its callers'
deadlines under the sources' first pass, and the drift charter's claim missed the head moving in
that window. So a suspension does not take effect by being observed.

There is **one table per scope**, as there is now one perms mirror per scope, and **only the route
writes it** — under one lock, together with the store write, so the table and the store change in
the same order. There is no watch to race the route, so no revision ordering and no tombstones are
needed. An automatic suspension is made by engine code calling the same function — the cycle
throttle is the one place that can today — never by a rule writing the record: a `derive` aimed at
`verse.trigger.suspension` is refused at install, since no table would see it, and `verse data
--put` there is already refused (`reservedWrite`).

**Baseline's table loads in a restore phase of its own, first.** A scope's loads inside
`scopeEngine`'s `sync.Once` before `restoreScopeCharter` arms anything, and a load that fails is
retried the way an arming is (`armTriggerSubtree`'s backoff), with the engine unregistered until it
succeeds — the Once as it stands logs a failure and carries on (`versed/server.go:950`), which with
"not loaded answers suspended" would freeze the scope until a restart. The tasks phase, which
re-attaches surviving tasks, and the charter and package phases wait on it (`awaiting`,
`versed/startup.go:128`), and the sources already wait on those (`versed/sources.go:841`), because
"a source's first pass before the rules are armed is an edge nothing saw" (`startup.go:124`). Until
it has loaded, the table answers *suspended* for everything, which covers the one path that can
start work during a restore — a request served before the phases finish; when the load completes,
what was held meanwhile is released in the order above. A store that cannot serve the slice holds
every rule, which is the right failure: a suspension that failed open on restart would resume what
somebody stopped on purpose.

### Identity

A rule is **(scope, charter address, name)**. Names are unique only within an engine, and a scope's
engine is unconfined, so its address is empty like baseline's: without the scope, a suspension of
baseline's `claim` would suspend every developer's scoped `claim` too.

Addresses are parsed and compared by segment at the route, never as strings, so two quotings of one
subject are one subject; a string comparison would let a spelling fail open.

One collision the design inherits and has to fix first: a firing's question id and spawn id carry
the rule's name and the subject but neither charter address nor scope (`trigger/trigger_api.go:562`,
`:344`), and every engine shares one registry (`s.Reg`). Two engines' same-named rules about one
subject therefore share a question — joined waiters on one docket entry — and withdrawing it from
one scope would not take it off the docket while the other still waits. The ids gain the charter and
the scope.

### Authority is decided when it applies

A suspension covers a piece of work only while **some holder of it may suspend that work**, asked at
the moment it would make progress, against the perms mirror of the suspension's scope, held in
memory:

- **from the charter side**, a holder may suspend a charter's work if they hold `control` over the
  charter — a package's place, or a baseline rule's own record;
- **from the subject side**, a holder may suspend the work about a subject if they hold `control` at
  that subject, **unless the work is a charter's the system installed**;
- **every charter about every subject** is refused at the route.

Deciding at application time, rather than once when the record is written, is what keeps a wildcard
honest. Checked at write time, `verse suspend '*' 'gitlab.*'` passes for anybody while nobody owns
anything beneath `gitlab` — everyone holds `control` at a world node with no owner on record
(`entity/attribution.go:467`) — and when alice later installs a source that owns `gitlab.alice`,
every charter about her repositories is suspended by somebody she never consented to. Checked at
application time, bob's holder no longer covers alice's subjects the moment she owns them; the
record stands, and the listing says it covers less. The walk costs no read once the mirror is loaded
(`Holds` answers from the cache, `attribution.go:349`); before that a check is a store read that can
time out, and a check that cannot be answered refuses, so the work stays held, as a perms check
refuses.

**A holder whose authority lapses** — their owner record replaced beneath a cover — stops covering
new decisions at once, but what was already held stays held: a paused task, a parked hook and a
withdrawn question were pushed once, and nothing re-runs the release for them until somebody
resumes. The listing shows the record covering less; making the release follow the mirror is a later
step.

**System rules are marked by how they were installed, not by perms.** A rule resolves to
`system:default` whenever its record has no owner record (`attribution.go:1161`), which includes a
user's rule whose owner write failed; a system-installed package's address has no owner record at
all (`versed/place.go:38`), so it is a node where everyone holds `control`. Neither is a reliable
"the system's". The install knows, and has to write it down: a rule record gains `by`, written by
`persist` beside `spec`, `scope` and `at` (`trigger/charter.go:133`) and read back by `LoadCharter`,
which today carries no installer (`charter.go:236`); the charter transaction's rollback reinstalls
the old rule under its original `by`, where today it reinstalls as the system store
(`versed/charter.go:779`). A rule installed by the system charter is the system's, and only the
system suspends it. An operator who needs it stopped edits the file the deployment names.

The records are readable by everyone. A suspended thing is never a mystery.

### Loud, not self-expiring

There is no `until`, and `why` is required.

Alertmanager makes an end time mandatory, and its users set it to the year 9999. PagerDuty keeps a
separate "Disable Service" for the indefinite case. Kubernetes, Flux, Cluster API, Crossplane,
Airflow, Temporal and Sensu by default do not expire a pause at all. The one account of a forgotten
silence found here — two hours intended, five months actual, a disk-full outage hidden — was fixed
by visibility: a daily list, a ticket for anything long, an alert on how much was suppressed. Expiry
would also need an armed clock that lifts at a moment nobody acts, and a restore of it. It can be
added as a field later.

What is spent instead:

- `verse suspend` lists every suspension with its holders, reasons, age, how much has moved under
  it since (its `occurrence` against the head), what it is holding now — firings not started, tasks
  held, questions withdrawn, launches parked — and whether it still covers anything;
- a withdrawn question says which suspension withdrew it, and a held task's record says what holds
  it;
- `verse status` counts suspensions, and the charter census marks a rule a suspension covers whole;
- `verse flow show <subject>` says a step is suspended, and by what.

### The name

"Pause" is taken three times: a task pauses at a boundary (`task/pause.go`), the engine has a
dormant `Pause`, and the throttle's wait is `pauseFor`. "Hold" is taken twice: a rule waiting for
its stratum is *held*, and `hold` is an alias of the gate mode `park`. "Disable", the issue's
alternative, reads as uninstall. **Suspend** is free in the code and is what Kubernetes and Flux
call a durable stop.

Whether `verse flow pause` stays beside it is a surface question, [left for
later](#surface-to-resolve-later).

## Alternatives ruled out

**Exempting work already running.** The first three drafts of this note held only what would *start*
— new firings, or a gate's answer — and left tasks, hooks and `seq` steps running, as CronJob,
Temporal and Airflow do. Every draft then had to classify each path as starting or running, and each
classification was a place it broke: a provider selection posed after the latch, an `onDenied`
spawning during a suspension, a hook's selection stalling the reap loop. A subject is either
suspended or not, for all the work about it, as it is either covered by a standing resolver or not.

**Charter-authored guards — no feature.** Every charter could read a flag in a `when`. The model can
spell it; but only the charter's author can write it, a subject's owner cannot stop somebody else's
charter, and every charter has to remember. And it would reach only firings, not the tasks and
questions about a subject.

**Uninstall and reinstall.** Today's charter-wide stop. No subject dimension, and removing a package
cancels its engine's context, ending every in-flight gate wait and failing a running task's hooks
(`versed/packages.go:387`).

**An in-memory pause.** The flow commands' shape and `Engine.Pause`'s. Undone by the first restart,
whose replay re-fires every level rule; `Engine.Pause` is dormant in versed, which never runs the
loop that services it (`engine.go:623`).

**Flags on the charter and on the subject.** [Above](#one-record-for-the-relation).

**Suspension in selection.** Moots the work it covers instead of holding it.

**An implicit `when` on every rule.** Rule-wide, so it cannot name a subject; read through each
owner's view, so an owner who cannot read the record runs suspended; and it reaches no task or
question.

**Leaving a covered question standing, unanswerable.** An earlier draft refused answers to it
instead of withdrawing it. That leaves a question in front of people that they cannot act on, which
the task runtime already decided against for a paused task; withdrawing it and asking it again is
the rule that exists.

**Holding an answered firing.** An earlier draft let answers through and held the firing afterwards.
It then acted, days later, with a rule somebody had since fixed and re-applied, on the subject as it
stood at the causing commit. With the question withdrawn, no answer exists to act on.

**Re-asking exactly what was held on resume.** Needs a record of skips per subject; loses firings
when a restricted re-ask advances the watermark past a commit the rule's watch has not delivered;
loses resumes when the check and the record are not atomic.

**A watch feeding the table.** Races the route, needs revision ordering and tombstones, and a
subtree watch re-establishes itself rather than failing, so it would go stale in silence.

**Authority checked when the record is written.** A wildcard's reach grows as owners appear beneath
it.

**Backlog on resume.** Watches carry no history, a read costs distance from the last snapshot
(`docs/convergence.md`), and a week's suspension would act on a week of superseded states. Airflow's
catchup is the cautionary tale.

**Exemptions and precedence.** Two record kinds and an order between them.

**One holder per record, or one record per holder.** The first forgets whose reason it was and lets
an automatic resume lift a person's; the second makes the state per-principal, which the issue rules
out.

**Expiry.** [Above](#loud-not-self-expiring). Deferred, not ruled out.

**Per-segment globs.** `demo.*.1` has no node whose owners could authorise it.

## What it does not solve

- **A step between deciding and doing.** An answer accepted, or a firing past its checks, a moment
  before a suspension lands still acts. The window is the time between a check and the write it
  guards.
- **A task that cannot hold.** A pause lands at a boundary, and a scripted task's only boundary is
  its end (`docs/reference/tasks.md`, flow steering); a brokered worker holds only if it honours its
  inbox. A suspension holds such a task's *consequences* — its hooks — and not the command already
  running.
- **A charter re-applied while one of its firings waits.** The firing re-poses and acts with the
  rule it started under. True today of any unanswered question; a suspension makes "fix it,
  re-apply, resume" a natural sequence, so it will be met more often.
- **An `on` edge during a suspension**, for a firing it had not started.
- **A scope deleted while one of its suspensions holds work.** `deleteScope` stops a scope's engine
  but does not cancel its live tasks; a suspension removed with its scope has to release or cancel
  what it held, and follows whatever `deleteScope` becomes.
- **A layered charter suspended one level at a time.** A suspended lower rule stops deriving, and
  rules about the rows it derived act on its last ones. Suspending the package is the whole answer.
- **A parked launch across a restart** is lost: a hook's spawn due while suspended, when verse
  restarts before the lift, does not start. A hook due during a restart is lost the same way today.
- **A holder whose authority lapses** leaves what was already held, held, until somebody resumes
  ([authority](#authority-is-decided-when-it-applies)).

## What it costs to build

- **The record and route.** `verse.trigger.suspension.<id>`; put, lift and list; one seed row,
  `read: ["*"]`; writes as `system:` under the table's lock.
- **The table.** One per scope: baseline's the first restore phase, a scope's loaded before its
  charter arms, with retry; "not loaded" answers suspended; a *draining* state for a lift.
- **Firings.** One predicate with the per-holder authority check, before `firstSight`, in
  `fireSubjects`, on entry to `fireOne`, and as backstops in `spawnHooked` and `patchEntity`; the
  gate goroutine's per-pose context and its withdraw, wait, re-pose loop, with a per-engine map from
  (rule, subject) to the firing's withdraw and resume in place of `inflight`'s bool; `reArm(rule)`,
  which is `armTriggerSubtree` again.
- **Ids.** The charter address and the scope in a firing's question id and spawn id.
- **Rule records.** `by`, written at install, read at restore, kept through a rollback.
- **Tasks.** Reaching every engine's tasks, and knowing which scope holds one, is already there:
  `flowEngines` and `scopeOf` (`versed/owns.go`). What a suspension adds: `heldBy` on `flowTask`,
  and `flow resume` refusing a covered task; a pause at `track` for a re-attached task; and a
  handle for a hand-spawned task, attached through its runner.
- **Hooks and `seq` steps.** A check at the entry of `launchSpawn`, after a hook's writes and
  before a selection can be posed, that parks its arguments in a per-engine list; the lift
  re-enters `launchSpawn` with each, as `ResumeSelections` does. No mark on the task record, no
  restart sweep, and no `at` on the task record to route one by.
- **Questions and deadlines.** Nothing new in the docket: a withdrawn question is orphaned with a
  `why`, keeps its ballots and its stamp, and is re-posed under the same id, as after a restart,
  with its deadline re-armed at the lift plus the time it had left, from `orphanedAt` and
  `deadline.at`. The `clearPriorAnswer` defect ([found along the way](#found-along-the-way)) is on
  the *answered* re-pose path and does not gate this.
- **Resume.** The ordered lift above, with the entry draining rather than locked.
- **Surfaces and docs.** The CLI pair; census, status and `flow show` fields; a section of the
  trigger model beside flows, the perms table, the CLI reference, and a line in "What a scope owns"
  in `docs/reference/connect.md`.

## What adopting it does to the rest of verse

**1. Every kind of work must carry its subject and its charter, and hold where it next makes
progress.** That is the ongoing cost, and it is almost entirely one verse already pays: a standing
resolver can only cover the work about a subject if the work knows its subject, so a new kind of
work that raises a question already has to carry it. What a suspension adds is the charter beside
it, and a place to wait. The standing guard is a test that suspends a subject and asserts that no
firing, task step, hook, question settlement or derive about it happens until it lifts.

**2. There is no classification to maintain.** The drafts that exempted running work needed an
argument per path about whether it starts or runs, and a new feature needed the same argument. Here
the question for a new feature is only *where does it wait* — the same question a pause already asks
of a task.

**3. Stored names become references.** A suspension names an address, a rule, a subject and a scope,
durably. Moving a package, renaming a rule, or respelling an address grammar — verse did that twice
this week — leaves a suspension naming nothing, and it fails **open**. Every such change adds
"respell the suspensions" to its notice, as it now does for grants; the listing marking a suspension
that covers nothing keeps the failure visible.

**4. Startup gains a first phase.** Nothing is re-attached or restored until one small slice has
loaded, and a store that cannot serve it holds every rule and task. `verse status` must say so among
what is restoring.

**5. Resume costs an ordered release and a re-arm.** Lifting a wide suspension replays every rule it
covered. On a verse with many rules over busy slices that is a restart's worth of reads at a moment
an operator chose; it must never run automatically in a loop.

**6. It adds no ownership logic of its own.** Which rules, tasks and questions a suspension may
touch is the answer every control route already gets, through the same `scopeOf` and `flowEngines`,
refused as not this scope's by the same `inherited` refusal (`versed/owns.go:214`). A later change
to what a scope owns changes what a suspension covers with it, and needs no second edit.

**7. `control` gets a consistent use.** Suspensions check it on every application. Today only task
cancel, say and vector check it; the flow routes and the runtime's task pause and resume do not,
though `docs/perms.md` says `control` covers them. That is an argument to fix the others.

**8. The install origin of a rule becomes a fact the engine keeps.** Needed to tell the system's
rules apart, and wanted elsewhere: perms resolution cannot tell them apart today.

**9. It forecloses shapes, on purpose.** Per-principal suspensions, exemptions, replay on resume,
and recurring windows would each be a second record kind or a second semantics, not a field. So
would a suspension that exempts some kind of work, which is the one most likely to be asked for.

**10. It enables automatic suspensions cheaply.** A holder can be the system: a cycle-throttle trip
that suspends the rule rather than pacing it forever, a charter suspended after repeated failed
firings (Airflow's auto-pause), a subject suspended while its source reports an error. The holder
map lets these coexist with a person's; Temporal and GitHub both learned to keep a system-made
reason apart from a human's.

**11. It costs nothing when nothing is suspended**, beyond a lookup where work would make progress;
the mark on a task record is written only for a parked hook. With suspensions, each check matches
the covering records and asks the in-memory mirror for each holder.

**Verdict.** Simple enough, because it is uniform. The drafts that tried to be smaller by holding
less each had to decide, path by path, what a suspension reaches, and each decision was where an
adversarial reading of the code broke them. Covering the work about a subject the way a standing
resolver already does removes that decision entirely. What is left reuses existing machinery at
nearly every step: a task's pause, the docket's orphan-and-re-pose path a restart already takes, the
install path for resume, `Track`'s shape for the table, `awaiting` for startup, and `control` for
authority. The genuinely new mechanisms are three, and each is small: a gate goroutine that can
withdraw and re-pose, a pause that knows whose it is, and a launch parked before it can ask
anything.

## Open questions

- **Suspending the system charter from outside.** Only the system can, which means editing the file.
  An operator in an incident may want more.
- **The third pass's fixes have not been re-read.** The checks to run next: that the draining state
  refuses a firing while parked hooks land, in the interleaving where a resumed task finishes and a
  sibling subject's commit re-evaluates the rule; that the per-pose context under the mootness
  context withdraws on both and re-poses on only one; that a scope's table load inside the Once
  precedes its first `addTrigger`; and that `at` on a task record survives the mirror's first-write
  replacement across a re-run.

### Surface, to resolve later

- **`verse flow pause`.** Keep it as the forgetful, quick spelling, or retire it into `verse suspend
  '*' <subject>`.
- **Baseline's spelling as a charter.** It has no address. `charter: ""` reads badly on a command
  line, and `baseline` could collide with a package installed there.

## Found along the way

Read while working this out, and not fixed here. None was verified by a test except the first.

- **`clearPriorAnswer` removes a question's deadline `within` and `default` on re-pose**
  (`mirror/question.go:743`, after `PostQuestion` set them at 189–197), so a re-posed gate's
  deadline settles on no option, which is not proceed. Confirmed by a targeted test in the first
  adversarial pass. It bites any re-asked question today.
- **A system-installed charter's rules have no owner record**, so the seed's `owner: ["*"]` beneath
  `verse.trigger.rule` appears to reach them, and a system-installed package's address is a node
  where everyone holds `control` (`versed/place.go:38`).
- **The charter transaction's rollback reinstalls a user's rule as the system store**
  (`versed/charter.go:771`), so after a failed re-apply the old rule may act as the system.
- **A hook whose spawn needs a provider selection blocks its engine's reap loop** until somebody
  answers (`engine.go:1654`).
- The flow routes and the runtime's `/tasks/{id}/pause|resume` still check no `control`; only task
  cancel, say and vector do (`versed/server.go:4543`). The ownership check now precedes `control`
  wherever `control` is asked.
- `reconcileTasks` calls `AdoptChain` before the charter is loaded, so a surviving task's hooks may
  never be re-attached at restart.
- **`ResumeSelections` spawns at startup outside any firing or hook** (`trigger/select.go:282`);
  `spawnHooked` is where every spawn converges.
- **A firing's question id and spawn id carry neither charter address nor scope**
  (`trigger/trigger_api.go:562`, `:344`), and every engine shares one registry, so same-named rules
  in two engines about one subject share a question.
- **A subtree watch's seed replay carries the store head**, not the slice's last commit — confirmed
  by a throwaway test in the third pass — so a rule armed after unrelated writes sees its backlog at
  a revision above anything it evaluated.
- The cycle throttle's documented "aborts if its chain breaks" is wired only through the dormant
  dispatch loop; in versed a paced firing always waits its full interval.

## How this was checked

The engine was surveyed for the places work begins, waits and acts, and for what restore, scopes,
packages and perms do there. Eleven systems were read for where they keep a pause, what resume does,
what happens to running work, and whether pauses expire.

A first draft withdrew a firing whose gate was answered and resumed by re-asking whole rules at the
head. The first adversarial pass broke it on: authority checked at an unowned prefix; subject owners
reaching the system's rules; withdrawal losing ballots, `on` approvals and deadline defaults; a
throttle side effect before the check; a resume that double-fired and re-asked too widely; a load
ordering with no place to live; scope rules colliding with baseline's; and a record that forgot its
holders.

A second draft held answered firings and re-asked exactly what was skipped. The second pass broke it
on: a held firing acting with a superseded rule on a superseded subject; a restricted re-ask moving
the watermark past an undelivered commit; a lost wakeup between the check and the skip record; the
held firing racing the re-ask; a watch racing the route; a wildcard's authority growing as owners
appeared beneath it; and a system package's address being open to anyone.

A third draft held only the two decisions that start work and left everything running untouched.
Read against the intended interface, that was the wrong cut: a standing resolver covers every form
of work about a subject, and a suspension should too. All three drafts had exempted running work,
and the exemption was the common root of what the passes kept finding. This draft removes it and
keeps every answer the passes forced about authority, identity, the table and resume.

A third pass read this draft. The record, the table per scope, ownership, authority at application
and re-arm-as-resume stood. Three mechanisms the draft asserted did not exist, and two orderings
were wrong: a firing cannot withdraw its question and keep waiting without a loop the registry does
not have; a task's pause cannot be told from an operator's; a hook held by blocking would stall
every completion in its engine; clearing the table first let a live watch re-spawn a task before its
parked hook landed; and a skip placed after `firstSight` burned the revision a re-arm's seed replays
at, so a scope's rules armed before its table loaded would never evaluate their backlog. Each is
answered above, in the mechanism the draft had left implicit.

## Prior art

| system | where the pause lives | on resume | automation × target | expiry | in flight |
|---|---|---|---|---|---|
| k8s CronJob `suspend` | field on the automation | at most one run for the last missed tick | no | no | untouched |
| Cluster API | parent `spec.paused` OR object annotation | reconcile current state; unpause re-queues children | parent × child | no | skipped |
| Flux `suspend` | field + per-object annotation | reconcile current revision | partly | no | undocumented |
| Argo CD sync windows | records with matchers | next reconcile | yes | recurring only | opt-in overrun |
| Crossplane | annotation on the target | reconcile current state | no | no | skipped; blocks delete |
| Alertmanager silence | record with matchers | still-firing alerts re-notify | yes | mandatory (set to 9999) | evaluation continues |
| Datadog downtime | record: monitors × scope | expiry re-notifies; cancel does not | yes | optional | states keep moving |
| Airflow `is_paused` | field on the DAG | catchup backfill; off by default since 3.0 | no | no | running tasks finish |
| Temporal schedule | `paused` + `notes` | paused time skipped | no | no | untouched |
| Kafka consumer | in memory | replays the backlog | partition | no | n/a |
| Sensu silence | record `subscription:check` | handlers filtered | yes, all three by wildcard | optional, default never | evaluation continues |

Sources: kubernetes.io CronJob and Deployment docs and `pkg/controller/cronjob`; the cluster-api
book, `util/annotations`, `util/paused`, issue 8629; fluxcd.io kustomization and helmrelease docs,
flux2 issue 3462 and discussion 2494; argo-cd `sync_windows` and `auto_sync` docs;
docs.crossplane.io managed resources, crossplane issue 4839; prometheus.io alertmanager and security
docs, alertmanager `silence/silence.go`, issues 1057, 2485, 2896; docs.datadoghq.com downtimes and
permissions; support.pagerduty.com maintenance windows; Airflow `core-concepts/dags.rst`,
`best-practices.rst`, 3.0 release notes; docs.temporal.io/schedule and the scheduler's
`workflow.go`; GitHub REST workflows; Kafka `KafkaConsumer` javadoc and Connect administration docs;
Drools language and rule-engine references; CLIPS Basic Programming Guide 6.31; docs.sensu.io
silencing. The forgotten silence: dev.to, "Somebody silenced that alert in March and it stayed
silenced".

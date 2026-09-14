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
model the same thing ([prior art](#prior-art)), and on two adversarial passes against the code that
broke the two drafts before this one. Both breakages are in [how this was
checked](#how-this-was-checked), because they are the argument for the design's size: each draft
held more than this one does, and each thing it held was a place it broke.

## The design in one paragraph

A **suspension** is one record naming a charter (or every charter), a rule of it (or all), and a
subject (or every subject); the issue's three cases are the three shapes of it. It holds exactly two
decisions, the two that start work in verse: **a rule deciding to fire** about a subject, and **a
question deciding to settle**. While a suspension covers them, a rule starts no firing, and a gate
question that a covered firing is waiting on takes no answer — from a person, from a resolver, or
from its deadline — so that firing simply keeps waiting, as it would for a question nobody has
answered. Nothing else is held: work already acting runs on. The suspensions in force are one
in-memory table for the process, written only by the route that also writes the store, and loaded
before any charter is restored. Lifting a suspension re-arms the rules it covered — the path an
install and a restart already take — so a rule acts on the world as it stands when the suspension
lifts, and nothing that happened meanwhile is replayed.

```tony
# verse.trigger.suspension."verse-dev.drift:*@git.verse.*"
charter: verse-dev.drift         # an installed charter's address, trailing * allowed; "*" for every charter
rule: "*"                         # a rule's name in it; "*" for all of them
subject: git.verse.*              # an address, trailing * allowed; "*" for every subject
scope: ""                         # a scope's own rules; "" for baseline's and every package's
held:
  user:walter@example.com:
    why: "reviewing the drift charter's range logic before it runs again"
    since: "2026-09-14T03:10:00Z"
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

The issue's three cases are one relation: *(rule, subject) is suspended when some record covers it*.
Charter-about-everything is `subject: "*"`, subject-for-every-charter is `charter: "*"`, and the
pair names both. Sensu's silences are named exactly this way (`*:check`, `entity:x:*`,
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

**Suspensions only add up.** A pair is suspended if any record covers it. No record exempts a
subject from a wider suspension, and resuming `verse-dev.drift git.verse.head` while
`verse-dev.drift` stands lifts that record and says the pair is still covered, and by what. Argo
CD's allow and deny sync windows, with precedence and a rule for running work, needed a nine-case
table to document.

**The subject axis is what a firing is about**, not what it writes. A rule about `git.verse.head`
that derives into a suspended `verse-dev.drift.*` is not suspended by that.

### It holds the two decisions that start work, and nothing else

Everything a charter does begins in one of two ways. A rule fires about a subject; or a firing that
was waiting on people learns what they decided. Every other action — a spawn's task, its hooks, a
`seq`'s next step, an `onDenied`, a provider selection — follows from one of those two, inside work
that has already started. So a suspension holds those two and nothing else.

**A rule deciding to fire.** The question *is this (rule, subject) suspended?* is asked at the three
places a firing can begin:

1. **before a rule's snapshot read**, when a suspension covers the rule about every subject — it
   does not read, select or fire, and costs nothing;
2. **in `fireSubjects`, before the cycle throttle** (`trigger/engine.go:749`), because `throttled`
   is not a pure question: it counts the trip and marks the chain as reported
   (`trigger/causal.go:264`), and a suspended rule inside a loop would otherwise trip and consume
   the report a real trip after resume needs;
3. **on entry to `fireOne`**, because a paced firing waits out the throttle first and a suspension
   can land meanwhile.

It is never asked in subject selection, though selection already has one rule of that shape — a
vanishing entity is not a subject (`trigger/match.go:392`). Selection is what `untilMoot` watches to
decide a pending question has gone moot (`engine.go:932`), so a suspension visible there would
withdraw every question it covered the moment it landed.

**A question deciding to settle.** A gate question posed by a firing the suspension covers takes no
answer while the suspension stands:

- **where an answer enters**, `answerQuestion` (`versed/server.go:2456`), which a person's `verse
  gate` and a resolver's verdict both reach: refused, before any ballot is cast, with the suspension
  named;
- **where the clock settles one**, `settleByDeadline` and the overdue sweep (`server.go:2677`,
  `2700`): not settled. When the suspension lifts, the deadlines of the questions it covered are
  re-armed (`armDeadlines`, `server.go:2657`), and one already past settles then, visibly.

The firing waiting on that question keeps waiting, exactly as it waits for a question nobody has
answered yet. It still watches its subject, and withdraws if the subject leaves the selection. A
restart finds a pending question, orphans it and re-poses it when the rule next fires, keeping its
ballots — today's path. No approval is ever given and then not acted on, because none can be given.
Escalating a question to the next rung of the ladder changes who answers, not what is decided, and
stays allowed.

A question raised by a *task*, and a provider selection's question, belong to work already acting
and take answers as usual. Holding a selection's question would stall the reap loop that runs hooks
for the whole suspension (hooks run synchronously, `engine.go:1654`).

**Why not hold the firings instead.** Both drafts before this held firings. Withdrawing a firing
whose gate was answered during the suspension lost its ballots and, through a latent defect, its
deadline's `default` (`mirror/question.go:743`), and lost an `on` rule's approval outright, since
the edge is gone by resume (`edge.go:85`). Holding the answered firing in its goroutine instead let
it act, days later, with the rule as it was before somebody fixed and re-applied the charter
(`engine.go:781`) and on the subject as it stood at the causing commit, and a restart lost every
hold. Refusing the answer keeps the question pending, which is a state verse already makes durable,
restarts, watches for mootness, and shows on the docket.

**Why not hold what is running.** Kubernetes CronJob, Temporal schedules and Airflow draw the same
line: a suspended thing starts nothing new. A task's `onDone` is where a charter records what the
task did and advances its latch; holding it leaves a finished task whose outcome never landed, and
the rule, seeing the subject still in the state that spawned the task, spawns it again on resume.
Running work is stopped with `verse flow pause` and `cancel` — which reach only the baseline
engine's tasks today ([what it does not solve](#what-it-does-not-solve)).

### Lifting is a re-arm

Lifting a suspension re-arms each rule it covered: its watches are stopped and opened again, and the
opening state is replayed through the rule as an ordinary commit — "THE SEED IS REPLAYED"
(`entity/logdstore.go:1339`), the path `LoadCharter` restores a rule by at every restart and an
install arms a rule by. So resume has the semantics a charter already lives with:

- **a level rule** still matching acts, once; one whose condition came and went meanwhile does
  nothing;
- **an `on` edge** that rose during the suspension is not seen, as an install over a verse where the
  predicate already holds sees none — `on` does not act on a backlog, and a suspension is one. A
  firing an edge had already *started* is waiting on its question and loses nothing;
- **a derive** naming a field another rule owns rewrites it, as at install, which `advise` warns
  about;
- **a gate with no `onDenied`** that was denied before the suspension is re-posed and marked
  `askedAgain`, as at restart.

The order of a lift is what makes it lossless: the table is updated first, then the record is
deleted, then the rules are re-armed. Any evaluation after the first step fires normally; any
evaluation before it skipped a commit older than the deletion, and the re-arm's replay is at or
after the deletion, so the rule watermark (`firstSight`, `edge.go:131`) admits it. A failed delete
restores the table.

A re-arm is coarse — the whole rule is replayed, not only the subjects that were held — and that is
its price: lifting `charter: "*"` about one subject replays every rule in every engine, a restart's
worth of evaluation. Both drafts before this one tried to re-ask exactly what was held, through the
watermark or beside it, and both lost firings: a restricted re-ask moved the watermark past a commit
the rule's own watch had not yet delivered, and nothing made "the check saw it suspended" atomic
with "the lift looked for what was skipped". A re-arm keeps no record of what was skipped, so there
is nothing to lose.

The prior art converges on current state over replay. Temporal's scheduler skips paused time
outright (`service/worker/scheduler/workflow.go`: "use end as last action time so that we don't
reprocess time spent paused"). Airflow 3 turned catchup off by default "to reduce confusion".
Kubernetes documents a CronJob starting a surprise run on unsuspend. Drools and CLIPS cancel an
activation whose condition went false, so a group regaining focus fires on current truth. Kafka
replays because it is a log whose every record must be processed, which a charter is not.

### One table, one writer

`docs/deployment-and-steering.md` holds that an intervention must never queue behind observation.
Tonight's ship is the evidence: for minutes after the wipe the store answered past its callers'
deadlines under the sources' first pass, and the drift charter's claim missed the head moving in
that window. So a suspension does not take effect by being observed.

There is **one table for the process**, as `entity.Track` keeps one perms mirror for it, and **only
the route writes it** — under one lock, together with the store write, so the table and the store
change in the same order. There is no watch to race the route, so no revision ordering and no
tombstones are needed. An automatic suspension is made by calling the same function in the process,
not by a rule writing the record.

**The table loads in a restore phase of its own, before the charter.** The charter and package
phases wait on it (`awaiting`, `versed/startup.go:128`), and the sources already wait on those
(`versed/sources.go:841`), because "a source's first pass before the rules are armed is an edge
nothing saw" (`startup.go:124`). Until it has loaded, the table answers *suspended* for everything,
which covers the one path that can arm a rule during a restore — an install served before the phases
finish; when the load completes, the rules armed meanwhile are re-armed. A store that cannot serve
the slice holds every rule, which is the right failure: a suspension that failed open on restart
would resume what somebody stopped on purpose.

### Identity

A rule is **(scope, charter address, name)**. Names are unique only within an engine, and a scope's
engine is unconfined, so its address is empty like baseline's: without the scope, a suspension of
baseline's `claim` would suspend every developer's scoped `claim` too.

Addresses are parsed and compared by segment at the route, never as strings, so two quotings of one
subject are one subject; a string comparison would let a spelling fail open.

### Authority is decided when it applies

A suspension covers a firing only while **some holder of it may suspend that firing**, asked at the
moment of the decision, against the perms mirror held in memory:

- **from the charter side**, a holder may suspend a rule if they hold `control` over the charter it
  belongs to — a package's place, or a baseline rule's own record;
- **from the subject side**, a holder may suspend a firing about a subject if they hold `control` at
  that subject, **unless the rule was installed by the system**;
- **every charter about every subject** is refused at the route.

Deciding at application time, rather than once when the record is written, is what keeps a wildcard
honest. Checked at write time, `verse suspend '*' 'gitlab.*'` passes for anybody while nobody owns
anything beneath `gitlab` — everyone holds `control` at a world node with no owner on record
(`entity/attribution.go:416`) — and when alice later installs a source that owns `gitlab.alice`,
every charter about her repositories is suspended by somebody she never consented to. Checked at
application time, bob's holder no longer covers alice's subjects the moment she owns them; the
record stands, and the listing says it covers less. The walk costs no read: the mirror is in memory
once loaded, and the routes refuse until it is (`attribution.go:894`).

**System rules are marked by how they were installed, not by perms.** A rule resolves to
`system:default` whenever its record has no owner record (`attribution.go:1096`), which includes a
user's rule whose owner write failed; a system-installed package's address has no owner record at
all (`versed/place.go:38`), so it is a node where everyone holds `control`. Neither is a reliable
"the system's". The install knows: a rule installed by the system charter is the system's, and only
the system suspends it. An operator who needs it stopped edits the file the deployment names.

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

- `verse suspend` lists every suspension with its holders, reasons, age, what it has held since the
  process started (firings not started, answers refused), and whether it still covers anything;
- a refused answer says which suspension refused it; the docket marks a covered question;
- `verse status` counts suspensions, and the charter census marks a rule a suspension covers whole;
- `verse flow show <subject>` says a step is suspended, and by what.

### The name

"Pause" is taken three times: a task pauses at a boundary (`task/pause.go`), the engine has a
dormant `Pause`, and the throttle's wait is `pauseFor`. "Hold" is taken twice: a rule waiting for
its stratum is *held*, and `hold` is an alias of the gate mode `park`. "Disable", the issue's
alternative, reads as uninstall. **Suspend** is free in the code and is what Kubernetes and Flux
call this cut, and it keeps the pair honest: *suspend* a charter or a subject so verse decides
nothing new about it; *pause* a flow to hold what is running.

## Alternatives ruled out

**Charter-authored guards — no feature.** Every charter could read a flag in a `when`. The model can
spell it, which is evidence a suspension fits; but only the charter's author can write it, a
subject's owner cannot stop somebody else's charter, and every charter has to remember.

**Uninstall and reinstall.** Today's charter-wide pause. No subject dimension, and removing a
package cancels its engine's context, ending every in-flight gate wait and failing a running task's
hooks (`versed/packages.go:387`).

**An in-memory pause.** The flow commands' shape and `Engine.Pause`'s. Undone by the first restart,
whose replay re-fires every level rule; `Engine.Pause` is dormant in versed, which never runs the
loop that services it (`engine.go:623`).

**Flags on the charter and on the subject.** [Above](#one-record-for-the-relation).

**Suspension in selection.** Withdraws every pending question it covers.

**An implicit `when` on every rule.** Rule-wide, so it cannot name a subject; read through each
owner's view, so an owner who cannot read the record runs suspended; asked at the causing commit, so
it cannot touch a question answered later.

**Withdrawing a firing whose gate was answered.** Loses ballots, deadline defaults, and `on`
approvals.

**Holding a firing whose gate was answered.** Acts later with a superseded rule on a superseded
subject, blocks the edited rule's firings on its flight claim, races the resume, and is lost by a
restart.

**Re-asking exactly what was held on resume.** Needs a record of skips per subject, loses firings
when a restricted re-ask advances the watermark past an undelivered commit, and loses resumes when
the check and the record are not atomic.

**A watch feeding the table.** Races the route, needs revision ordering and tombstones, and a
subtree watch re-establishes itself rather than failing, so it would go stale silently.

**Authority checked when the record is written.** A wildcard's reach grows as owners appear beneath
it.

**Backlog on resume.** Watches carry no history, a read costs distance from the last snapshot
(`docs/convergence.md`), and a week's suspension would act on a week of superseded states. Airflow's
catchup is the cautionary tale.

**Exemptions and precedence.** Two record kinds and an order between them.

**One holder per record, or one record per holder.** The first forgets whose reason it was and lets
an automatic resume lift a person's; the second makes the state per-principal, which the issue rules
out.

**Expiry.** Deferred, not ruled out.

**Per-segment globs.** `demo.*.1` has no node whose owners could authorise it.

## What it does not solve

- **Stopping running work on a package's engine.** `verse flow pause|cancel` reach only the baseline
  engine (git-issue `h7wwzxtnh12ksc4yh5n0` is the neighbouring defect), and every staging charter is
  a package.
- **A firing between its answer and its action.** An answer accepted a moment before a suspension
  lands still acts. The window is the time between a settle and `apply`.
- **A charter re-applied while one of its firings waits on a question.** The waiting firing acts,
  when answered, with the rule it started under. That is true today of any unanswered question and
  is not made worse here, but a suspension makes "fix it, re-apply, resume" a natural sequence, so
  it will be met more often.
- **A deadline that lapses during a suspension** settles on its default the moment the suspension
  lifts. Kubernetes stops a Deployment's progress deadline while it is paused; doing the same here
  is a field on the question and an open question below.
- **An `on` edge during a suspension**, for a firing it had not started.
- **A layered charter suspended one level at a time.** A suspended lower rule stops deriving, and
  the rules above act on its last rows. Suspending the package is the whole answer.

## What it costs to build

- **The record and route.** `verse.trigger.suspension.<id>`; put, lift and list; one seed row,
  `read: ["*"]`; writes as `system:` under the table's lock.
- **The table.** Process-wide, a restore phase of its own that the charter phases await; "not
  loaded" answers suspended.
- **The engine.** One predicate on (scope, at, name, subject) with the per-holder authority check;
  three call sites where a firing begins; `reArm(rule)`, which is `stopWatchers` and
  `armTriggerSubtree` together.
- **The docket.** The predicate where an answer enters and where a deadline settles; re-arming
  deadlines on a lift.
- **Surfaces and docs.** The CLI pair, census, status and `flow show` fields, a refused answer's
  message; a section of the trigger model beside flows, the perms table, the CLI reference.

Several hundred lines with their tests, and the new states are few: a table, a phase, and a refusal
on two existing paths.

## What adopting it does to the rest of verse

**1. Every new way to begin work must be placed.** The design is sound only if every path that
starts work is either one of the two decisions it holds or follows from one inside work already
acting. The next feature that starts work on its own — a scheduled rule, a re-sweep, a clock that
fires a rule, a new kind of question that settles something — has to ask the predicate, or it is a
way for a suspended charter to act. The standing guard is a test that suspends a charter and asserts
that nothing is written via its rules and none of its gate questions settles. This is the largest
ongoing cost, and it is one question per new path.

**2. Stored names become references.** A suspension names an address, a rule, a subject and a scope,
durably. Moving a package, renaming a rule, or respelling an address grammar — verse did that twice
this week — leaves a suspension naming nothing, and it fails **open**. Every such change adds
"respell the suspensions" to its notice, as it now does for grants; the listing marking a suspension
that covers nothing is what keeps the failure visible.

**3. Startup gains a phase.** Nothing is restored until one small slice has loaded, and a store that
cannot serve it holds every rule. `verse status` must say so among what is restoring.

**4. The docket gains a state.** A question can be pending and unanswerable. Every surface that
answers questions — the CLI, the docket, a resolver service — must understand a refusal that is
neither "not yours" nor "already answered".

**5. Resume costs a re-arm.** Lifting a wide suspension replays every rule it covered. On a verse
with many rules over busy slices, that is a restart's worth of reads at a moment an operator chose;
it must never be done automatically in a loop.

**6. `control` gets a consistent use.** Suspensions would check it on every application. Today only
task cancel, say and vector check it; the flow routes and the runtime's task pause and resume do
not, though `docs/perms.md` says `control` covers them. That is an argument to fix the others.

**7. The install origin of a rule becomes a fact the engine keeps.** Needed here to tell the
system's rules apart, and wanted elsewhere: perms resolution cannot tell them apart today.

**8. It fixes two words and forecloses some shapes.** *Suspend* is no new decisions; *pause* is
running work. Per-principal suspensions, exemptions, replay on resume, and recurring windows would
each be a second record kind or a second semantics, not a field.

**9. It enables automatic suspensions cheaply.** A holder can be the system: a cycle-throttle trip
that suspends the rule rather than pacing it forever, a charter suspended after repeated failed
firings (Airflow's auto-pause), a subject suspended while its source reports an error. The holder
map lets these coexist with a person's; Temporal and GitHub both learned to keep a system-made
reason apart from a human's.

**10. It costs nothing when nothing is suspended**, beyond one lookup per firing decision and per
answer. With suspensions, each decision matches the covering records and asks the in-memory mirror
for each holder.

**Verdict.** Simple enough, on the condition that it holds only the two decisions. The two drafts
before this one each held one more thing — a started firing, a record of what was skipped, a watch —
and each of those was a place an adversarial reading of the code broke it. What remains reuses
existing machinery at every step: the pending question verse already keeps durable, the install path
for resume, `Track`'s shape for the table, `awaiting` for startup, and `control` for authority. The
rule for keeping it simple after it lands is the same one: a suspension decides whether work
*starts*, and never reaches into work that has.

## Open questions

- **Baseline's spelling as a charter.** It has no address. `charter: ""` reads badly on a command
  line, and `baseline` could collide with a package installed there.
- **Deadlines during a suspension.** Settle on lift (this design), or extend by the time suspended,
  which needs the suspension's start on the question.
- **Suspending the system charter from outside.** Only the system can, which means editing the file.
  An operator in an incident may want more.
- **`seq`.** A sequence's next step spawns new work and is left running here, as work already
  acting. It is the classification most likely to surprise.
- **Suspensions in a scope.** Stored in baseline with their `scope:`; whether a scope's owner may
  suspend baseline's rules inside their scope is undecided.
- **This draft has not had its own adversarial pass.** The checks to run first: that
  `answerQuestion` and the deadline paths are the only ways a firing's gate question settles; that
  re-arming a rule while one of its firings waits on a question does not re-pose that question over
  its ballots (the waiting firing holds the flight claim, so the replay's `fireOne` should be
  refused first); that the lift's order admits the replay under `firstSight` in every interleaving;
  and that an install served during the restore is the only way a rule arms before the table phase.

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
- `verse flow pause|resume|cancel|say` act only on the baseline engine's tasks, though `verse flow`
  lists every engine's.
- The flow routes and the runtime's `/tasks/{id}/pause|resume` check no `control`.
- `reconcileTasks` calls `AdoptChain` before the charter is loaded, so a surviving task's hooks may
  never be re-attached at restart.
- The cycle throttle's documented "aborts if its chain breaks" is wired only through the dormant
  dispatch loop; in versed a paced firing always waits its full interval.

## How this was checked

The engine was surveyed for the places a firing begins, waits and acts, and for what restore,
scopes, packages and perms do there. Eleven systems were read for where they keep a pause, what
resume does, what happens to running work, and whether pauses expire.

A first draft withdrew a firing whose gate was answered and resumed by re-asking whole rules at the
head. The first adversarial pass broke it on: authority checked at an unowned prefix; subject owners
reaching the system's rules; withdrawal losing ballots, `on` approvals and deadline defaults; a
throttle side effect before the check; a resume that double-fired and re-asked too widely; a load
ordering with no place to live; scope rules colliding with baseline's; and a record that forgot its
holders.

A second draft held answered firings and re-asked exactly what was skipped. The second pass broke it
on: a held firing acting with a superseded rule on a superseded subject; a restricted re-ask moving
the watermark past an undelivered commit; a lost wakeup between the check and the skip record; the
held firing racing the re-ask and clearing an approval that had acted; restart losing holds of
selections, `on` approvals and denials; a watch racing the route; a wildcard's authority growing as
owners appeared beneath it; and a system package's address being open to anyone.

This draft answers both lists by holding less: two decisions, refused rather than held, and resumed
by the path restart already takes.

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

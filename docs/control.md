# Control: holding work

Two acts stop work without ending it, and they differ in how long they last and in how much they
reach. **A run's hold** stops one task at its next boundary until somebody resumes it; the rule
that started it goes on firing, and may start another once this one has ended. **A suspension**
stops the work about a charter and a subject — firings, tasks, questions, what a hook would start
— and keeps it stopped, across restarts, until somebody lifts it.

Both are verbs of `verse flow`, addressed by one set of flags. A flow is a query over the work in
flight; a suspension is a query over the work from now on, naming the same coordinates a live task
carries. So the listing and the acts share their flags, and `verse flow` narrowed by some flags
shows exactly what an act with those flags reaches now. Cancelling, which does end the work, is a
verb of the same noun.

## The flags

- `--task <id>` — **that run only**. It combines with neither of the others, so the flags alone
  say whether an act is about one run or is durable.
- `--charter <address>[:<rule>]` — an installed charter's address, and one rule of it after the
  colon. Baseline's charter has no address, so its rule is `:<rule>`, and all of its rules `:`.
  Every charter is `'*'`. `-r` (`--recursive`) names the charter at the address and every charter
  installed beneath it.
- `--subject <address>` — an address with `*` open in any segment, covering what stands at it or
  beneath it: `demo.pr.1`, `demo.pr.*`, `demo.*.1`. Every subject is `'*'`. There is no payload
  predicate: a suspension whose subject was a predicate would be lifted by an ordinary write that
  took the subject out of the match.

**An act names something.** A bare verb meaning everything is how somebody typing it to see its
usage would suspend the verse, so an act that names nothing is refused: `suspend`, `resume` and
`say` take `--task`, or `--charter`, `--subject` or both; `cancel` takes `--task`. A bare verb
prints its usage and sends nothing. Of `--charter` and `--subject`, the one left out beside the
other is `'*'`: `--charter :review` is that rule about every subject, and `--subject demo.pr.2`
is that subject for every charter. `'*'` for both is refused. Only the listing, a read, answers
for everything when no flag narrows it.

```sh
verse flow                                           # every live task and every suspension
verse flow --charter verse-dev.drift                 # its live tasks, and its suspension about everything
verse flow suspend --task review@demo.pr.2           # hold one run at its next boundary
verse flow resume  --task review@demo.pr.2
verse flow say     --subject 'demo.pr.*' "hurry"     # to every live task about a PR
verse flow cancel  --task review@demo.pr.2           # end one run (destructive)

verse flow suspend --charter verse-dev.drift --why "…"                        # a charter, about everything
verse flow suspend --subject github.signadot.verse.pr.'"42"' --why "…"         # a subject, for every charter
verse flow suspend --charter verse-dev.drift:claim --subject git.verse.head --why "…"
verse flow suspend --charter verse-dev -r --why "…"                            # every package under verse-dev
verse flow resume  --charter verse-dev.drift                                   # the same flags lift it
```

`--args` prints each row of the listing as the flags that name it, one per line, quoted so that a
shell and `xargs` both read it back — so what was shown is what an act reaches. A task's row is
`--task <id>` and a suspension's `--charter … --subject …`, so a grep picks one kind:

```sh
verse flow --args | grep -v -- --task | xargs -L1 verse flow resume              # lift every suspension
verse flow --subject demo.pr.2 --args | grep -- --task | xargs -L1 verse flow cancel   # end every run about PR 2
```

The listing answers rows of two kinds. A live task is `{kind: task, task, charter, subject,
heldBy}`, its charter the rule that spawned it (`verse-dev.drift:claim`, `:review`), and absent
for a task run by hand. A suspension is `{kind: suspension, id, charter, subject, held, holding,
covers}`: who holds it, why and since when, how far the store has moved under it, what it is
holding now — tasks paused, questions withdrawn, launches parked — and whether it names anything
installed. A suspension is usually holding work that is not running yet, so it is a row of its
own rather than a field on a task.

A narrowed listing is what an act with the same flags reaches, an absent flag being `*`. Its task
rows are the live tasks a suspension in those words would hold. Its suspension row is the one a
resume in them would lift: the record the flags spell, and not a wider one that also holds the
work — `--subject demo.pr.2` lists `*:*@demo.pr."2"` and not `:review@*`, and `--subject
'demo.pr.*'` lists the suspension spelled that way, not each PR's. What holds a subject's work,
whatever spells it — including a subject with no work yet — is `verse flow show <subject>`.

## A run's hold

`verse flow suspend --task <id>` holds one run at its next boundary — "hold this, I want to look" —
and `verse flow resume --task <id>` lets it carry on from there. The runtime's
`POST /tasks/{id}/pause|resume` are the same hold. It is the operator's: the task's `heldBy` says
`operator`, on its record and in `flow show`.

It lands at a boundary. A multi-step task — the `llm` driver, whose every tool call is one — holds
mid-run; a scripted task is a single command, so its only boundary is its end, and a hold lands
when it finishes. A question the task is asking when the hold lands is withdrawn, since a held task
must not leave one in front of somebody, and asked again when it resumes. A pause is not an
answer.

It is forgetful. It holds that run and nothing else: the rule that spawned it goes on firing, and
may fire about the same subject again once the run has ended. Whose hold it is lives in the engine
tracking the run, and a restart does not restore it.

`say` redirects the runs it reaches with an instruction, landing at the same boundary, and
`cancel` ends one — at once, for a scripted task, by killing its command. Neither is a hold. A
`park` gate is a third kind of stop, and a different one: a hold the *charter* declares on an
action, answered on the docket ([the trigger model](trigger-model.md#gates--whether-it-proceeds)).

## A suspension

A suspension is one record naming a charter, a rule of it, and a subject, and it covers **the work
about that subject** in every form, by one test, the way a standing resolver does:

| work about the subject | while suspended | when the suspension lifts |
|---|---|---|
| a rule about to fire | starts no firing | the rule is armed again |
| a firing waiting at its gate | its question is withdrawn, ballots kept; the firing waits | it asks the same question again, if the rule still selects the subject |
| a live task | holds at its next boundary, `heldBy` the suspension | resumes, unless an operator's hold holds it too |
| a completion hook, or a `seq` step | its writes land; what it would start is parked | the parked launch starts |
| a deadline on a withdrawn question | its clock stops | it has the time it had left |

`--why` is required: nothing ends a suspension but somebody reading it. There is no expiry. The
same suspension held twice is one record with two holders, each with their reason; a person's
resume lifts it whoever holds it, and a resume made by the system removes only the system's own
hold. Every charter about every subject is refused.

**Suspensions only add up.** Work is held while any suspension covers it, and nothing exempts
anything from a wider one. Lifting a pair that a wider suspension also covers leaves the pair
held, and the answer says by what (`still`).

**A lift acts on the world as it stands.** The record is deleted and the suspension drains: new
firings about its work are still refused while what it held is released — tasks resume, parked
launches start in order, withdrawn questions are asked again — and then it is removed and every
rule it covered is armed again, the path an install and a restart take. Nothing that happened
meanwhile is replayed: a level rule still matching acts once, one whose condition came and went
does nothing, and an `on` edge that rose during the suspension is not seen. A wide lift is a
restart's worth of evaluation, at a moment somebody chose.

The mechanism, rule by rule, is in [the trigger model](trigger-model.md#suspending).

## Whose hold it is

A task knows whose hold it is under — `heldBy` lists `operator` and the suspensions that paused it
— so the two do not undo each other. `verse flow resume --task` of a run a suspension holds is
refused, 409, naming the suspension, since resuming it would run covered work. A lift resumes only
what nothing else holds, so an operator's hold outlives a suspension whichever came first.

## Who may

Every verb over a task asks `control` of it: the task's owner holds it, and so does the runner its
claim granted. A `say` over many tasks asks it of each before it says anything, and one refused
refuses the whole act, naming that task.

A suspension is asked twice ([perms](perms.md#suspending)). When it is written, the writer must
hold `control` over the charter it names — a package's address, or a rule's own record — or at the
subject it names, a selector being asked at its literal head (`demo` for `demo.*.1`). When it
applies, every time work would make progress, a holder covers that work while they hold `control`
over the work's charter or at its concrete subject — so a wildcard stops covering a subject the
moment somebody else owns it. A rule the system installed is the system's alone to suspend.

## Scopes

A scope owns what it creates ([connect](reference/connect.md#what-a-scope-owns)). With `--scope`,
every verb here reaches the runs that scope started and none it merely sees through the overlay;
addressed at baseline's work it is refused, 409, naming whose the work is and how to re-address.
From baseline it is the same the other way. A suspension belongs to the scope it was made in and
covers only that scope's work — baseline's and every package's without `--scope`, a scope's own
with it — and a scope may not name a package's charter, which is baseline's.

## What is not held

- **A task that cannot hold.** A scripted task keeps running to its end; a suspension holds its
  consequences — its hooks — and not the command already running.
- **A step between deciding and doing.** An answer accepted, or a firing past its checks, a moment
  before a suspension lands still acts.
- **An `on` edge during a suspension**, for a firing it had not started.
- **A parked launch across a restart.** A hook's spawn parked by a suspension is lost if verse
  restarts before the lift.
- **Names that moved.** A suspension names an address, a rule and a subject durably; a package
  moved or a rule renamed leaves one naming nothing, which the listing marks `covers: false`.
- **A lapsed holder's held work.** A holder whose authority lapses stops covering new work, but
  what was already held stays held until somebody resumes.

## Seeing it

- `verse flow` lists live tasks with `heldBy`, and suspensions with their holders, reasons, what
  they hold and whether they cover anything.
- `verse status` says what is live and what is held: `flows: {tasks, subjects}` — live tasks, and
  the distinct subjects they are about — and, when anything is suspended, `suspensions: {count,
  tasks, subjects, questions, launches}` — how many are in force, and the tasks, their subjects,
  the questions withdrawn and the launches parked that they hold, each counted once. The charter census marks a rule suspended about every subject.
- `verse flow show <subject>` puts at the top of the tree what holds the subject's work — what
  would hold an installed rule whose subject could select it, and what holds its live tasks — and
  marks `heldBy` on a live task and `suspended` on each position whose work is held. A subject
  with no work that something would hold gets a tree too, with no branches: the address itself,
  or each stored entity a pattern selects. A tree with a live task says `active: true`;
  `--active` shows only those, `--no-active` only the others, and neither shows every one.
- A withdrawn question's record says which suspension withdrew it.

Why none of this rides the change stream is in [deployment and
steering](deployment-and-steering.md).

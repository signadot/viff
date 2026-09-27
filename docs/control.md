# Control: holding work

Two acts stop work without ending it. They differ in how long they last and in how much they
reach.

| Act | What it stops | How long |
|---|---|---|
| **a hold on a run** | one task, at its next boundary | until somebody resumes it, or verse restarts |
| **a suspension** | all the work about a charter and a subject: firings, tasks, questions, and what a hook would start | until somebody lifts it. It lasts across restarts. |

Under a hold on a run, the rule that started the task goes on firing. It may start another
task when this one has ended.

Both acts are verbs of `verse flow`, and both use the same flags. A *flow* is a query over the
work in progress. A *suspension* is a query over the work from now on, and it names the same
coordinates that a live task carries. So the listing and the acts share their flags:
`verse flow` with some flags shows what an act with those flags reaches now.

Cancelling ends the work. It is a verb of `verse flow` too.

## The flags

| Flag | What it names |
|---|---|
| `-task <id>` | one run only. It combines with neither of the other two. |
| `-charter <address>[:<rule>]` | the address of an installed charter, and one rule of it after the colon |
| `-subject <address>` | an address, with `*` for any segment. It covers what stands at the address and beneath it. |

**`-task`** cannot be combined with the others, so the flags alone say whether an act is about
one run or is durable.

**`-charter`**. The charter of baseline has no address, so a rule of it is `:<rule>`, and all
its rules are `:`. Every charter is `'*'`. With `-r` (`-recursive`) the flag names the charter
at the address and every charter installed beneath it.

**`-subject`**. Examples are `demo.pr.1`, `demo.pr.*` and `demo.*.1`. Every subject is `'*'`.
A subject takes no predicate on the payload. With one, an ordinary write that took the
subject out of the match would lift the suspension.

### An act names something

An act that names nothing is refused. Without this rule, somebody who typed a bare verb to see
its usage would suspend the whole verse.

| Verb | What it takes |
|---|---|
| `suspend`, `resume`, `say` | `-task`, or `-charter`, or `-subject`, or `-charter` and `-subject` |
| `cancel` | `-task` |

A bare verb prints its usage and sends nothing.

When you give one of `-charter` and `-subject`, the other is `'*'`. So `-charter :review` is
that rule about every subject, and `-subject demo.pr.2` is that subject for every charter.
`'*'` for both is refused.

Only the listing answers for everything when no flag narrows it, because the listing is a
read.

```sh
verse flow                                           # every live task and every suspension
verse flow -charter verse-dev.drift                  # its live tasks, and its suspension about everything
verse flow suspend -task review@demo.pr.2            # hold one run at its next boundary
verse flow resume  -task review@demo.pr.2
verse flow say     -subject 'demo.pr.*' "hurry"      # to every live task about a PR
verse flow cancel  -task review@demo.pr.2            # end one run (destructive)

verse flow suspend -charter verse-dev.drift -why "…"                          # a charter, about everything
verse flow suspend -subject github.signadot.verse.pr.'"42"' -why "…"           # a subject, for every charter
verse flow suspend -charter verse-dev.drift:claim -subject git.verse.head -why "…"
verse flow suspend -charter verse-dev -r -why "…"                              # every package under verse-dev
verse flow resume  -charter verse-dev.drift                                    # the same flags lift it
```

### `-args`: the listing as flags

`-args` prints each row of the listing as the flags that name it, one row to a line. The
quoting is what a shell and `xargs` both read back. So what the listing showed is what an act
reaches.

The row of a task is `-task <id>`. The row of a suspension is `-charter … -subject …`. A
`grep` picks one kind:

```sh
verse flow -args | grep -v '^-task ' | xargs -L1 verse flow resume               # lift every suspension
verse flow -subject demo.pr.2 -args | grep '^-task ' | xargs -L1 verse flow cancel     # end every run about PR 2
```

### The rows of the listing

The listing has rows of two kinds.

| Kind | Fields |
|---|---|
| a live task | `{kind: task, task, charter, subject, heldBy}` |
| a suspension | `{kind: suspension, id, charter, subject, held, holding, covers}` |

The `charter` of a task is the rule that spawned it, such as `verse-dev.drift:claim` or
`:review`. A task run by hand has none.

A suspension says four things:

- who holds it, why, and since when;
- how far the store has moved while it stood;
- what it holds now: tasks paused, questions withdrawn, launches parked;
- whether it names anything that is installed.

A suspension usually holds work that is not running yet. For this reason it is a row of its
own, and not a field on a task.

### What a narrowed listing shows

A narrowed listing shows what an act with the same flags reaches. A flag that is absent is
`*`.

- The task rows are the live tasks that a suspension in those words would hold.
- The suspension row is the one that a resume in those words would lift. It is the record the
  flags spell. It is not a wider record that holds the same work.

For example, `-subject demo.pr.2` lists `*:*@demo.pr."2"` and not `:review@*`. And
`-subject 'demo.pr.*'` lists the suspension spelled that way, not the suspension of each PR.

To see everything that holds the work of a subject, however it is spelled, use
`verse flow show <subject>`. It answers for a subject with no work yet, too.

## A hold on a run

`verse flow suspend -task <id>` holds one run at its next boundary. Use it when you want to
look before the run goes on. `verse flow resume -task <id>` lets the run continue from there.
The routes of the runtime, `POST /tasks/{id}/pause` and `POST /tasks/{id}/resume`, are the same
hold.

The hold is the operator's. The task's `heldBy` says `operator`, on its record and in
`flow show`.

**It lands at a boundary.** A boundary is a point between two actions of a task.

| Task | Where a hold lands |
|---|---|
| a task with many steps, such as one with the `llm` driver, where every tool call is a step | in the middle of the run |
| a scripted task, which is one command | at its end, when the command finishes |

When a hold lands, a question that the task is asking is withdrawn, because a held task must
not leave a question in front of somebody. The task asks it again when it resumes. A pause is
not an answer.

**It does not last.** The hold holds that run and nothing else. The rule that spawned the run
goes on firing, and may fire about the same subject again when the run has ended. The engine
that tracks the run remembers whose hold it is, and a restart does not restore it.

**Three acts that are not a hold.**

| Act | What it does |
|---|---|
| `say` | gives the runs it reaches an instruction. It lands at the same boundary. |
| `cancel` | ends one run. For a scripted task it ends at once, by killing the command. |
| a `park` gate | a stop that the *charter* declares on an action. It is answered on the docket ([the trigger model](reference/gates.md#gates--whether-it-proceeds)). |

## A suspension

A suspension is one record. It names a charter, a rule of the charter, and a subject. It
covers **the work about that subject** in every form, by one test, as a standing resolver
does.

| Work about the subject | While suspended | When the suspension lifts |
|---|---|---|
| a rule about to fire | starts no firing | the rule is armed again |
| a firing waiting at its gate | its question is withdrawn and its ballots are kept. The firing waits. | it asks the same question again, if the rule still selects the subject |
| a live task | holds at its next boundary. Its `heldBy` names the suspension. | resumes, unless an operator's hold holds it too |
| a completion hook, or a `seq` step | its writes land. What it would start is parked. | the parked launch starts |
| a deadline on a withdrawn question | its clock stops | it has the time it had left |

`-why` is required. A suspension has no expiry, so it ends only when somebody reads it and
lifts it.

The same suspension held twice is one record with two holders, and each holder has a reason.
A resume by a person lifts it, whoever holds it. A resume by the system removes only the
system's own hold.

A suspension of every charter about every subject is refused.

**Suspensions only add up.** Work is held while any suspension covers it. Nothing exempts work
from a wider suspension. If you lift a suspension, and a wider one covers the same work, the
work stays held. The answer names the wider one, under `still`.

**A lift acts on the world as it stands.** A lift goes in this order:

1. The record is deleted, and the suspension drains. New firings about its work are still
   refused.
2. What it held is released. Tasks resume, parked launches start in order, and withdrawn
   questions are asked again.
3. The suspension is removed, and every rule it covered is armed again. An install and a
   restart arm a rule in the same way.

Nothing that happened during the suspension is replayed.

| Rule | What it does at the lift |
|---|---|
| a level rule whose condition still holds | acts once |
| a rule whose condition held and then stopped holding | does nothing |
| a rule with an `on` edge that rose during the suspension | does not see the edge |

A wide lift costs as much evaluation as a restart, at a moment that somebody chose.

[Suspending](#suspending), at the end of this page, describes the mechanism, rule by rule.

## Whose hold it is

A task knows which holds it is under. Its `heldBy` lists `operator` and the suspensions that
paused it. So the two kinds of hold do not undo each other.

- `verse flow resume -task` is refused with `409` for a run that a suspension holds. The
  refusal names the suspension. Resuming the run would run work that is covered.
- A lift resumes only what nothing else holds. So an operator's hold outlasts a suspension,
  whichever came first.

## Who may

Every verb over a task asks for `control` of the task. The owner of the task holds `control`,
and so does the runner that its claim granted.

A `say` over many tasks asks for `control` of each task before it says anything. If one is
refused, the whole act is refused, and the refusal names that task.

A suspension is asked twice ([perms](perms.md#suspending)).

**When it is written.** The writer must hold `control` in one of two places:

- over the charter it names, which is the address of a package or the record of a rule;
- at the subject it names. A selector is asked at its literal head, which is `demo` for
  `demo.*.1`.

**When it applies**, which is every time work would make progress. A holder covers that work
while the holder has `control` over the charter of the work, or at its concrete subject. So a
wildcard stops covering a subject as soon as somebody else owns the subject.

Only the system may suspend a rule that the system installed.

## Scopes

A scope owns what it creates ([connect](reference/connect.md#what-a-scope-owns)).

| You act | What a verb reaches |
|---|---|
| with `-scope` | the runs that scope started. It does not reach runs that the scope only sees through the overlay. |
| without `-scope` | the work of baseline and of every package |

A verb addressed at the work of the other side is refused with `409`. The refusal names whose
the work is, and how to address it.

A suspension belongs to the scope it was made in, and covers the work of that scope only. A
scope may not name the charter of a package, because a package belongs to baseline.

## What is not held

- **A task that cannot hold.** A scripted task runs to its end. A suspension holds what
  follows from the task, which is its hooks. It does not hold the command that is already
  running.
- **A step between deciding and doing.** An answer that was accepted a moment before a
  suspension lands still acts. So does a firing that had passed its checks.
- **An `on` edge during a suspension**, for a firing that had not started.
- **A parked launch across a restart.** A suspension parks the spawn of a hook. If verse
  restarts before the lift, the spawn is lost.
- **Names that moved.** A suspension names an address, a rule and a subject, and keeps those
  names. When a package moves or a rule is renamed, the suspension names nothing. The listing
  marks it `covers: false`.
- **Work held by a holder whose authority ended.** A holder that loses its authority stops
  covering new work. What it already held stays held until somebody resumes it.

## Seeing it

| Command | What it shows |
|---|---|
| `verse flow` | live tasks with `heldBy`, and suspensions with their holders, their reasons, what they hold, and whether they cover anything |
| `verse status` | counts of what is live and what is held, and of what has gone wrong |
| `verse charter` | a rule that is suspended about every subject, and a rule in trouble |
| `verse flow show <subject>` | what holds the work of one subject, as a tree |

**`verse status`** has three counts.

| Field | What it counts |
|---|---|
| `flows: {tasks, subjects}` | the live tasks, and the distinct subjects they are about |
| `suspensions: {count, tasks, subjects, questions, launches}` | how many suspensions are in force, and what they hold: tasks, their subjects, questions withdrawn, launches parked. Each is counted once. The field is absent when nothing is suspended. |
| `firings: {failed, unread}` | what has gone wrong at the firings of the rules ([when a rule does nothing](trigger-model.md#when-a-rule-does-nothing)) |

**`verse charter`** marks a rule in trouble with `trouble`.

**`verse flow show <subject>`** puts what holds the work of the subject at the top of the
tree. That is what would hold an installed rule whose subject could select it, and what holds
its live tasks. The tree marks `heldBy` on a live task, and `suspended` on each position whose
work is held.

A subject with no work gets a tree too, if something would hold its work. The tree has no
branches: it is the address itself, or each stored entity that a pattern selects.

A tree with a live task says `active: true`. `-active` shows only those trees, `-no-active`
shows only the others, and neither flag shows every tree.

The record of a withdrawn question says which suspension withdrew it.

[Deployment and steering](deployment-and-steering.md) says why none of this travels on the
stream of changes.

## Suspending

A suspension is one record naming a charter (or every charter), a rule of it (or all), and a
subject (or every subject), and it holds the work about that subject in every form until it is
lifted. What an operator does with one, and how it differs from holding a run, is in
[a suspension](#a-suspension), above. This section is what a suspension does to rules.

```tony
# verse.trigger.suspension."verse-dev.drift.**:*@git.*.head"
charter: verse-dev.drift      # an installed charter's address; "*" for every charter; "" for baseline's
recursive: true               # …and every charter installed beneath it
rule: "*"                     # a rule's name in it, or "*"
subject: git.*.head           # an address, * open in any segment, covering at or beneath; "*" for every subject
held:
  user:walter@example.com:
    why: "reading the drift charter's range logic before it runs again"
    since: "2026-09-14T03:10:00Z"
    occurrence: 41872         # the store revision the hold landed at
```

The record is keyed by what it covers, addresses parsed and rendered by segment, so two spellings
of one selector are one record, and suspending the same thing twice adds a holder to it.

**A rule about to fire** is asked in three places. At the top of evaluation, before the rule's
watermark moves, a rule suspended about every subject is skipped; so the revision it skipped is
still one a re-arm's replay is admitted at. Per subject, a firing about a suspended subject is
refused before the cycle throttle sees it, so a firing that was not going to happen counts no
trip; and again on entry to a firing, since a paced firing waits out the throttle first. Every
spawn and every charter-authored write pass a backstop. Nothing asks in subject selection, which
is what decides a question has gone moot: a suspension there would moot work rather than hold it,
and a rule that stops selecting its subject during a suspension ends that firing as it would have.

**A firing at its gate** has its question withdrawn — orphaned, with the suspension as its `why`,
ballots and deadline stamp kept — and waits holding its flight. When the suspension lifts it asks
the same question under the same id if the rule still selects the subject, with the time its clock
had left.

**A completion hook or a `seq` step** lands its writes, since recording what a task did is not
progress, and parks what it would start before a provider selection could be posed, so a hook asks
nothing while suspended and the engine's reap loop never waits on one.

**A lift is ordered.** The record is deleted, and the suspension drains: new firings about its work
are still refused while what it held is released — tasks resume, parked launches start in order,
withdrawn questions are asked again — and only then is it removed and every rule it covered armed
again, its watermark first moved below the head, the path an install and a restart take. So a rule
acts on the world as it stands when the suspension lifts, and nothing that happened meanwhile is
replayed: a level rule still matching acts once, one whose condition came and went does nothing,
and an `on` edge that rose during the suspension is not seen, as an install over a verse where the
predicate already holds sees none.

**Whose it is.** Each scope's suspensions are one table in memory, written only by the route that
writes their records and loaded before the scope's tasks are re-attached or its charter armed;
until it has loaded it holds everything. A rule `derive` aimed at `verse.trigger.suspension` is
refused at install, since no table would see it. What a suspension covers is decided each time it
would apply, against the holder's `control` over the work's charter or at its subject, and a rule
the system installed (owned by `system:charter`) is the system's alone to suspend
([perms](perms.md#suspending)).

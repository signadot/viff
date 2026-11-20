# Control: holding work

Two acts stop work without ending it. They differ in how long they last and in how much they
reach.

**A hold on a run** stops one task, at its next boundary. It lasts until somebody resumes the
task, or until verse restarts.

**A suspension** stops all the work about a charter and a subject: firings, tasks, questions,
and what a hook would start. It lasts until somebody lifts it, and it survives a restart.

Both acts are verbs of `verse flow`, and both take the same flags. A *flow* is a query over the
work in progress. A *suspension* is a query over the work to come, and it names the same things
that a live task carries. So `verse flow` with some flags lists what an act with the same flags
reaches.

Cancelling ends the work. It is a verb of `verse flow` too.

## The flags

| Flag | What it names |
|---|---|
| `-task <id>` | one run only. It combines with neither of the other two. |
| `-charter <address>[:<rule>]` | the address of an installed charter, and one rule of it after the colon |
| `-subject <address>` | an address, with `*` for any segment. It covers what stands at the address and beneath it. |

**`-task`** combines with no other flag. So the flags alone say whether an act is about one run,
or lasts.

**`-charter`**: the baseline charter has no address, so one of its rules is `:<rule>`, and all
its rules are `:`. Every charter is `'*'`. With `-r` (`-recursive`), the flag names the charter
at the address and every charter installed beneath it.

**`-subject`**: for example `demo.pr.1`, `demo.pr.*` or `demo.*.1`. Every subject is `'*'`. A
subject takes no predicate on the payload. With one, an ordinary write that moved the subject
out of the match would lift the suspension.

### An act names something

An act that names nothing is refused. Otherwise, somebody who typed a bare verb to see its usage
would suspend the whole verse.

| Verb | What it takes |
|---|---|
| `suspend`, `resume`, `say` | `-task`, or `-charter`, or `-subject`, or `-charter` and `-subject` |
| `cancel` | `-task` |

A bare verb prints its usage and sends nothing.

When you give one of `-charter` and `-subject`, the other is `'*'`. So `-charter :review` is
that rule about every subject, and `-subject demo.pr.2` is that subject for every charter.
`'*'` for both is refused.

Only the listing covers everything when no flag narrows it, because the listing is a read.

```sh
verse flow                                           # every live task and every suspension
verse flow -charter repos.verse.drift                # its live tasks, and its suspension about everything
verse flow suspend -task review@demo.pr.2            # hold one run at its next boundary
verse flow resume  -task review@demo.pr.2
verse flow say     -subject 'demo.pr.*' "hurry"      # to every live task about a PR
verse flow cancel  -task review@demo.pr.2            # end one run (destructive)

verse flow suspend -charter repos.verse.drift -why "…"                        # a charter, about everything
verse flow suspend -subject github.signadot.verse.pr.'"42"' -why "…"           # a subject, for every charter
verse flow suspend -charter repos.verse.drift:claim -subject git.verse.head -why "…"
verse flow suspend -charter repos.verse -r -why "…"                            # every package under repos.verse
verse flow resume  -charter repos.verse.drift                                  # the same flags lift it
```

### `-args`: the listing as flags

`-args` prints each row of the listing as the flags that name it, one row to a line, quoted so
that a shell and `xargs` read it back. So an act reaches what the listing showed.

The row of a task is `-task <id>`. The row of a suspension is `-charter … -subject …`. A `grep`
picks one kind:

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

The `charter` of a task is the rule that spawned it, such as `repos.verse.drift:claim` or
`:review`. A task run by hand has none.

A suspension says four things:

- who holds it, why, and since when;
- how far the store has moved while it stood;
- what it holds now: tasks paused, questions withdrawn, launches parked;
- whether it names anything that is installed.

A suspension usually holds work that is not running yet, so it is a row of its own, and not a
field on a task.

### What a narrowed listing shows

A narrowed listing shows what an act with the same flags reaches. A flag that is absent is
`*`.

- The task rows are the live tasks that a suspension in those words would hold.
- The suspension row is the one that a resume in those words would lift. It is the record the
  flags spell. It is not a wider record that holds the same work.

For example, `-subject demo.pr.2` lists `*:*@demo.pr."2"` and not `:review@*`. And
`-subject 'demo.pr.*'` lists the suspension spelled that way, not the suspension of each PR.

To see everything that holds the work of a subject, however it is spelled, use
`verse flow show <subject>`. It works for a subject with no work yet, too.

## A hold on a run

`verse flow suspend -task <id>` holds one run at its next boundary. Use it to look at a run
before it goes on. `verse flow resume -task <id>` lets the run continue from there.

The hold is the operator's: the task's `heldBy` says `operator`, on its record and in
`flow show`.

**It lands at a boundary.** A boundary is a point between two actions of a task.

| Task | Where a hold lands |
|---|---|
| a task with many steps, such as an agent's, where every tool call is a step | in the middle of the run |
| a script, which is one action | at its end, when the script finishes |

When a hold lands, a question the task is asking is withdrawn, because a held task must not leave
a question in front of somebody. The task asks it again when it resumes. A pause is not an
answer.

**It does not last.** The hold holds that run and nothing else. The rule that spawned the run
goes on firing, and may fire about the same subject again when the run has ended. A restart of
verse does not keep the hold.

**Three acts that are not a hold.**

| Act | What it does |
|---|---|
| `say` | gives the runs it reaches an instruction. It lands at the same boundary. |
| `cancel` | ends one run. For a script it ends at once, by killing the process. |
| an `ask` gate | a stop that the *charter* declares on an action. It is answered on the docket ([the gates of a rule](reference/gates.md#gates-whether-it-proceeds)). |

## A suspension

A suspension is one record. It names a charter, a rule of the charter, and a subject. It
covers **the work about that subject** in every form, by one test.

| Work about the subject | While suspended | When the suspension lifts |
|---|---|---|
| a rule about to fire | starts no firing | the rule is armed again |
| a firing waiting at its gate | its question is withdrawn and its ballots are kept. The firing waits. | it asks the same question again, if the rule still selects the subject |
| a live task | holds at its next boundary. Its `heldBy` names the suspension. | resumes, unless an operator's hold holds it too |
| a completion hook, or a `seq` step | its writes land. What it would start is parked. A sequence waits at a parked step, as at one whose task is running. | the parked launch starts, and a sequence goes on when that task ends |
| a deadline on a withdrawn question | its clock stops | it has the time it had left |

`-why` is required. A suspension does not expire. It ends only when somebody lifts it.

The same suspension held twice is one record with two holders, and each holder has a reason.
A resume by a person lifts it, whoever holds it. A resume by the system removes only the
system's own hold.

A suspension of every charter about every subject is refused.

**Suspensions add up.** Work is held while any suspension covers it, and nothing exempts work
from a wider suspension. If you lift a suspension and a wider one covers the same work, the work
stays held, and the answer names the wider one under `still`.

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
[control.md](platform/control.md#suspending), in the repository, says how a suspension acts on
rules, rule by rule.

## Whose hold it is

A task knows which holds it is under: its `heldBy` lists `operator` and the suspensions that
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

A suspension needs `control` over the charter or at the subject, both when it is written and
every time it would apply ([suspending](reference/perms.md#suspending)).

## Scopes

A scope owns what it creates ([scopes](reference/scopes.md#what-a-scope-owns)).

| You act | What a verb reaches |
|---|---|
| with `-scope` | the runs that scope started. It does not reach runs that the scope only sees through the overlay. |
| without `-scope` | the work of baseline and of every package |

A verb addressed at the work of the other side is refused with `409`. The refusal names whose
the work is, and how to address it.

A suspension belongs to the scope it was made in, and covers the work of that scope only. A
scope may not name the charter of a package, because a package belongs to baseline.

## What is not held

- **A task that cannot hold.** A script runs to its end. A suspension holds what follows
  from the task, which is its hooks. It does not hold the script that is already running.
- **A step between deciding and doing.** An answer that was accepted a moment before a
  suspension lands still acts. So does a firing that had passed its checks.
- **An `on` edge during a suspension**, for a firing that had not started.
- **A parked launch whose record the store lost.** A parked launch is recorded, and a
  restart finds it parked, as it finds the suspension. The store flushes the record to
  disk just behind the write, so a store that goes down in between may come back without
  it. Then the launch never starts. Where a claim was waiting on it, the restart settles
  the claim as a task that was lost: a spawn with hooks runs its `onFailed`, and a
  sequence parked at a step ends failed, with `orphaned: true`
  ([completion hooks](reference/action.md#completion-hooks-one-trigger-chaining-visible)).
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

**`verse flow show <subject>`** puts what holds the work of the subject at the top of the
tree. That is what would hold an installed rule whose subject could select it, and what holds
its live tasks. The tree marks `heldBy` on a live task, and `suspended` on each position whose
work is held.

A subject with no work gets a tree too, if something would hold its work. The tree has no
branches: it is the address itself, or each address with data that a pattern selects.

A tree with a live task says `active: true`. `-active` shows only those trees, `-no-active`
shows only the others, and neither flag shows every tree.

The record of a withdrawn question says which suspension withdrew it.

[Deployment and steering](deployment-and-steering.md) says why none of this travels on the
stream of changes.

## Suspending

See [a suspension](#a-suspension).

# residual: gates — two clocks on one record, and what an exclusive binding does not refuse

# One gate record: what is left

Status: residual of a landed change. What runs is one question record for every decision
somebody is asked — a charter gate's, one of a task's own actions, a gate a worker raises, a
provider selection's — carrying options with a threshold each and a deadline with a default,
settled by an answer path that tallies. It is described where it runs:
[trigger-model.md](../trigger-model.md#gates--whether-it-proceeds) for the charter spelling and
what a deadline, a restart and a moot question do; [reference/tasks.md](../reference/tasks.md)
for the docket and `task do gate`; [provider-selection.md](../provider-selection.md) for a
selection as a question. It landed as six
commits from `9009da45` to the one that retired the formula.

What is here is what that work named and did not build, so a known gap does not become an
unknown one.

## 2. Where the record lives

Kept in [grants-residual.md §7](grants-residual.md#7-where-the-question-stands): the
question stands at `verse.gate.question.<id>`, and the edge to the task or rule that posed it
is computed rather than spelled in the address.

## 3. Two clocks on one record

A provider's `within` and a resolver's `deadline.within` used to be two mechanisms; they are
now two instants on one record, `deadline.at` and `escalatesAt`. The earlier fires first: a
deadline settles, an escalation hands the question up. Nothing says which an author meant when
both are set, and a charter gate with a `deadline` under a binding with a `deadline` of its own
arms both without a word. The rule that is missing is whether a bound resolver's escalation
should be the deadline's default, or the deadline should suspend escalation.

## 4. What is not refused

`select: vote` at `need >= 2` under an exclusive binding is refused at install, since an
exclusive binding answers alone and a vote is a room. A charter gate with `need: 2` under an
exclusive `gating.resolver` is the same incoherence and is not refused: the check reads spawns,
not gates. And a worker raising a gate with `--options` carrying a threshold above one, on a
task whose resolver is exclusive, is refused by nothing at all — a worker's question is posed
after any install.

## 5. A worker's clock

A worker-raised deadline is armed where the record is kept, through the hook the server hands
the mirror (`mirror.Meta.Arm`), and re-armed at startup from the record. A frame mirrored by a
reflect with no server behind it — `mirror.Reflect` called directly, as tests do — arms
nothing: the deadline is on the record and no timer reads it until the docket is read.

## 6. Smaller things

- The broker's task snapshot (`task.PendingGate`) names a pending gate and its tool, not its
  options; `task get` on a task blocked on a three-option question shows the name alone. The
  docket record is the surface that has them.
- Firings that joined one question all proceed on its answer, each reading its own causing
  commit; a subject touched twice while its gate stands runs its action twice, with the second
  write winning. This predates the record and is noted here because a threshold gate stands
  longer than an ask did.
- The trigger package's tests cannot exercise the tally or the clock, which are the answer
  path's; they assert what a firing asks and does with one answer, and versed's tests carry
  the rest. A behaviour that needs both halves has to be tested there.
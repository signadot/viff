# charter: a task may edit the running charter, gated — a process that improves itself on the record

An agent working under a charter can propose a change to that charter. The change is a write
to the rule's record, so it is an action like any other: derived, inside or outside the grant,
gated, on the docket as a diff, recorded with who made it and who approved it, and live at the
next firing. The process improves itself the way anything else in verse changes: through a
reviewed write to the store, not inside a session.

## What exists

- A charter is data under `verse.trigger.rule.*` and `verse.trigger.strata.*`, installed at
  runtime and level-triggered: a change is picked up at the next firing, with no restart.
- A child's grant can only narrow (`Grant.Subset`), and a write is a derived effect a gate can
  match by verb and path (`GateMatch`). The system charter already watches policy change.
- Every write has an author, and an owner record on a place stops grants from above, so the
  charter's place can be held and opened deliberately.
- A `verse.trigger.rule` write from a task today goes through `run_bash verse data put`, which
  the deriver cannot see into: to the vector it is `exec`, not a write to the charter.

## The smallest version

1. A tool that writes verse entities (`write_entity`, or `put`) whose Derive stamps
   `Write` at the entity's path. That is the whole mechanism: a rule edit becomes an action the
   envelope and the vector can name.
2. A grant that admits `verse.trigger.rule.<package>:*` and a gate on it (`verbs: [write]` at
   that path, `ask`), so the proposal sits on the docket. The question's reason carries the
   diff; the answerer sees what changes.
3. A charter whose rule spawns a task with that grant and a prompt of the shape "this rule
   keeps missing X; propose the change". Approve, and the next firing runs the new rule.

Proof: a drive on the local verse where a rule's task rewrites the rule's own `condition`,
the docket shows the diff, `verse c rules <name>` shows the new rule after approval, and the
record names the task, the approver and the revision.

## Open

- Governance is `2dchqs8j`'s: authoring rules is the most privileged act in the model, and
  a proposal from a task reading attacker-influenced input is a program installed by us. The
  gate is the answer for the smallest version; `09r1j5gj` (authority) is the answer at scale.
- A rule editing the rule that spawned it: the cycle throttle bounds a runaway firing, and an
  owner record on the charter's place bounds who may propose. Whether a task may edit its
  own spawning rule at all, or only sibling rules, is a policy for the charter to state.
- What the diff on the docket shows: the rule's record before and after, or the proposal as
  a patch. The io's question line shows `pose`; a task's question carries `reason`.
- Strata: a proposal that moves a rule between levels, or adds a level, is the same write at
  `verse.trigger.strata.*`; whether the first version admits it.

Related: `2dchqs8j` (a charter authored per change — this is its running-charter form),
`09r1j5gj` (authority), `n85fbhrj` (channels: the thread a proposal is discussed in).
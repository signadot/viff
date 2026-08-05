# Goals

> **Being redesigned.** This describes what runs today. Read it as the current surface, not
> as where it lands — the roadmap's [dynamic charter](../roadmap/dynamic-charter.md) is where
> the next shape is being argued out.

Everything else in verse pushes the world forward: a change fires a rule, a gate decides
whether and who, a task runs, and its completion fires the next rule. Nothing in that says
where a flow is *going*, or when it is *done*. A goal adds an explicit target without giving
up the emergent model — it drives by **owning charter**, not by reconciling. The triggers
still do the doing; the goal owns them, watches for success or failure, and holds a live
status.

A goal is three things: a `doneWhen` predicate (the target), an optional `deadline` (a
fail-latch), and a bundle of owned triggers (the doing).

```sh
verse goal add ship --subject change:pr:42 \
  --done-when '!fact ci:status' --deadline 4h
```

`--done-when` takes a formula in the input format, so it is the same `!fact` / `!quorum` /
`!until` grammar a charter's gate is written in — one language, whether it is gating an
action or naming a target. (`--deadline` is an ordinary flag, not tony, so `4h` is fine
unquoted there.)

```sh
verse goals
# - deadline: "4h"
#   name: ship
#   status: blocked
#   subject: change:pr:42
#   terminal: false
#   waitingOn:
#   - fact:ci:status
```

The goal is `blocked`, and it names what it is waiting on — the unmet leaves of `doneWhen` —
recomputed live as the world changes. It has spawned nothing; it is watching. Make the target
true, however that happens: a trigger, a source, or a person.

```sh
verse entity put ci:status:build '{state: green}'
verse goals
# - name: ship
#   outcome: done
#   status: done
#   terminal: true
#   waitingOn: []
verse entity get goal:done:ship
# payload:
#   flow: change:pr:42
#   goal: ship
#   subject: change:pr:42
#   witness:
#   - ci:status:build
```

When `doneWhen` holds, the goal latches — once, and it stays latched, so retracting the fact
does not un-finish it. Two outcomes are first-class: `done` and `unachievable`. The deadline
is the fail side, exactly like a gate's deny-quorum, where "no" is a real outcome: let one
pass with the target still unmet and the goal latches `goal:failed` with
`status=unachievable`.

## A goal recognizes; it never advances

The latch is an ordinary fact — `goal:done:ship` is just an entity — so if you want the
finish to carry a subject onward, a trigger reacts to the latch, the same emergent way
everything else composes:

```tony
- name: on-ship
  condition:
    when:    { system: goal, kind: done }
    subject: change:pr:42
  action:
    patch: { stage: shipped }
```

Name the subject rather than writing `change:pr:*` and expecting the latch to narrow it. A
subject is what the charter says: a pattern here would ship every `change:pr` in the verse,
because nothing in the latch redirects it. (`goal:done` does carry the goal's subject as
`flow`, readable by a rule about that fact as `$subject.value.flow` — but it names the
subject, it does not choose it.)

## Steering, and durability

A goal's owned triggers spawn tasks tracked as flows, so a goal *is* a flow with a
destination — steered by the same verbs, keyed by the goal:

```sh
verse goal pause  ship
verse goal say    ship "hurry"
verse goal cancel ship
```

To ship the owned triggers *with* the goal instead of hand-authoring them, hand `add` a full
record — `verse goal add -f ship.tony` — carrying `triggers: [ … ]` alongside `doneWhen`. On
a terminal outcome the goal reaps its owned triggers, so a finished goal leaves no orphaned
charter.

A goal outlives a restart: `goal:instance:ship` carries the record you installed, so a start
picks up every goal still being pursued and reinstalls its charter and its watching. If the
target was reached while verse was down, the goal latches on the way back up rather than
waiting for one more change to notice. A goal you removed stays removed, and a settled one
stays settled.

*Left to do: `doneWhen` is a single fact/quorum atom — multi-condition and temporal targets
(eventually, always, sequence), an optional plan that generates the owned triggers, and a
`DeleteScope`-based reap of a goal's owned charter.*

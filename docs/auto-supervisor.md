# An automatic supervisor

The dial in a charter is per-gate: `auto` runs, `ask` asks a person, `park` stops. Between
"every change lands on its own" and "a person looks at every change" there is a third thing, and
it is the one most flows want: *an agent decides, and promotes what is not its to decide.*

This is a worked example — a charter where a supervisor agent assesses each change, clears the
ones it may clear, and puts a human gate in front of the rest. The interesting part is not that
it can approve. It is what stops it from approving everything: the supervisor's remit is written
in the charter, not in the supervisor, and a supervisor that fails to decide has thereby decided
to promote.

The pattern is a simple one. Supervision as a live conversation, rather than a rule fixed at
install, is [attached resolvers](attached-resolvers.md).

A test drives the charter below through each of its routes
([`trigger/supervise_test.go`](https://github.com/signadot/verse/blob/main/trigger/supervise_test.go)),
and the gaps are named at the end.

## Two supervisors, one word

Verse has a thing called a Supervisor, and it is not this one. Keep them apart:

| | what it decides | who writes it |
|---|---|---|
| `task.Supervisor` (+ a `Resolver`) | whether *one tool use* inside a running task proceeds | Go, per runtime — or an [attached resolver](attached-resolvers.md), which is what a spawn's `resolver` field names |
| the supervisor here | whether *a firing* proceeds — the charter-level question a gate would put to a person | a charter rule, installable over the wire |

The first is the inside of a task: `write_file` on a path, a `call_agent`, a `run_bash`. The
second is the outside: this change, landing. An automatic supervisor of the second kind needs no
new machinery, because a charter can already spawn an agent, enforce the shape of its answer,
patch that answer onto the subject as an ordinary fact, and route on it. That is the whole
example.

## The charter

The subject is a change, `change.pr.<n>`, carrying a `stage`, a `tests` fact, and — once
assessed — the supervisor's `review`. Install it:

```sh
verse c -a -f - <<'TONY'
- name: assess                 # the supervisor decides, and decides whether to promote
  condition:
    subject: {path: [change, pr, *], value: { stage: proposed }}
  action:
    patch: { stage: assessing }
    spawn:
      driver: llm
      prompt:  "Assess $subject for landing. Read the change and the tests it touches."
      frame: |
        You are a review supervisor. Decide what is yours to decide and promote what is
        not: answer `escalate` whenever a person should look at this, `reject` when it
        should not land at all, and `approve` only when you would defend landing it
        unattended. Say why in one sentence. Do not approve to be agreeable.
      answer:
        decision: !or [approve, escalate, reject]
        risk:     !or [low, medium, high]
        why:      !irtype string
      grant:  read-only
      vector: auto
      tools:  [read_file, list_dir]
    onDone:
      patch:
        stage:  assessed
        review: { decision: $data.decision, risk: $data.risk, why: $data.why, by: $chain }
    onFailed:
      patch:
        stage:  assessed
        review:
          decision: escalate
          risk:     high
          why:      "the supervisor did not decide: $err"
          by:       $chain

- name: remit                  # the ceiling: an approval outside the remit becomes a promotion
  condition:
    subject:
      path: [change, pr, *]
      value: !and
      - { stage: assessed, review: { decision: approve } }
      - !not { tests: green, review: { risk: low } }
  action:
    patch: { review: { decision: escalate, promoted: "outside the supervisor's remit" } }

- name: land-auto              # cleared, and within the remit: no gate, no question, no wait
  condition:
    subject:
      path: [change, pr, *]
      value: { stage: assessed, tests: green, review: { decision: approve, risk: low } }
  action:
    patch: { stage: landed }

- name: land-ask               # promoted: one human decision, in `verse gate`
  condition:
    subject:
      path: [change, pr, *]
      value: { stage: assessed, review: { decision: escalate } }
  gates:
  - { name: sign-off, mode: ask }
  action:
    patch: { stage: landed }

- name: close                  # rejected: closing is cheap and reversible, so it is auto
  condition:
    subject:
      path: [change, pr, *]
      value: { stage: assessed, review: { decision: reject } }
  action:
    patch: { stage: closed }
TONY
```

Five rules, and the routing is the whole design:

| the supervisor answered | `tests` | what happens | who decided |
|---|---|---|---|
| `approve`, risk `low` | `green` | `land-auto` fires — no gate is posed at all | the supervisor |
| `approve`, risk `medium`/`high` | any | `remit` rewrites the decision to `escalate`, then `land-ask` | the charter, over the supervisor |
| `approve`, risk `low` | not `green` | same — `remit`, then `land-ask` | the charter, over the supervisor |
| `escalate` | any | `land-ask` — one `sign-off` gate in `verse gate` | the supervisor, promoting |
| `reject` | any | `close` | the supervisor |
| nothing (the run failed) | any | `onFailed` writes `escalate`, then `land-ask` | nobody — the default |

Note what is *not* in the table: no route where a supervisor's approval lands a change the
charter did not also clear, and no route where a change gets stuck with nobody looking at it.
Both are properties of the rule set rather than of the agent's judgment.

## Three things keep it honest

**The supervisor cannot act, only answer.** `grant: read-only` is hard authority: an action
outside it is *refused*, not asked about. So the supervisor cannot write the field that routes its
own decision. Its single channel into the verse is the final message, and `answer` makes that
channel typed: `decision` is one of three words, or the run fails and `onFailed` takes over. A
supervisor that narrated instead of deciding does not get a vote. `vector: auto` is consistent
rather than lax — with a read-only grant there is nothing left to ask about, and asking a person to
confirm each `read_file` is how an escalation surface stops being read.

**The remit belongs to the charter.** `land-auto` requires three things, and the supervisor
authors exactly one of them. `review.risk` is its own claim, and `tests: green` is not: that
fact comes from a source reflecting CI ([sources](sources.md)), which the supervisor has no
authority to write. So from `review.decision: approve` alone nothing follows — there is a
world where the supervisor says `approve` and the change does not land, which is the property
you want and the reason `remit` exists as its own rule. `remit` is the complement of
`land-auto` written once, in one place: for any assessed change the supervisor approved, either
`land-auto` matches or `remit` does, never both and never neither. It rewrites `decision` and
leaves `why` alone, so the human at the gate reads the supervisor's reasoning *and* the note
saying the charter overrode it.

**Failure promotes.** `onFailed` writes `escalate` and `risk: high`, and carries `$err` into `why`.
A supervisor that crashed, could not produce its answer shape, called `fail` because it could not
do the work, or was never available at all does not leave a change unassessed. It sends the change
up, with the reason attached. Point this charter at an agent that cannot see the change it was
asked about and that is what you get, in its own words:

```tony
review:
  by:       assess
  decision: escalate
  risk:     high
  why:
    "the supervisor did not decide: No files are accessible in the working directory;"
    " the PR diff and test files for change.pr.9 could not be found under any path tried."
```

The same route covers the crudest failure of all: with no model credentials configured,
`verse up` builds an `llm` spawn as a task that fails at once (`llm driver unavailable:
set llm.apiKey …`), so a charter installed on a verse with no key routes *every* change to a
person. Both are worth trying on purpose. A supervisor that goes wrong should fail toward the
human, and that is a property of where `onFailed` points, not of the agent behaving well.

## What "when" means here

The promotion happens *before* the action, not after it. `land-ask`'s gate is asked while the
change is still `assessed`; the human's answer is what makes it `landed`. The alternative
shape — land it and flag the doubtful ones — makes review a report rather than a decision, and
the point of a supervisor is to decide *when a decision must be someone else's*.

There is a second timing question, and it is worth its own rule: some changes should not cost
a supervisor run at all. A release change, say, is never an agent's to clear, so the charter
can route it straight to a gate and never spawn anything, by narrowing what `assess` is about:

```tony
value: !and
- { stage: proposed }
- !not { area: release }
```

with a companion rule matching `{ stage: proposed, area: release }` whose action marks it
`assessed` with `decision: escalate` — no spawn, no tokens, straight to `land-ask`'s gate.
Same dial, moved earlier: the cheapest promotion is the one that skips the assessment.

## Drive it

The supervisor's own run needs model credentials (`verse up` reads them — see
[§1 of the tutorial](tutorial.md#1-start)). The *routing* needs none: a `review` is
an ordinary fact, so you can write one yourself and watch where it goes. Start with one the
supervisor cleared inside its remit:

```sh
verse data change pr 7 --put '{stage: assessed, tests: green, review: {decision: approve, risk: low}}'
verse data change pr 7
# payload:
#   review: { decision: approve, risk: low }
#   stage: landed            ← land-auto fired; `verse gate` is empty
```

Now the same approval, at a change CI does not support:

```sh
verse data change pr 8 --put '{stage: assessed, tests: red, review: {decision: approve, risk: low, why: "docs only"}}'
verse data change pr 8
# payload:
#   review:
#     decision: escalate
#     promoted: "outside the supervisor's remit"
#     risk: low
#     why: "docs only"       ← the supervisor's reasoning survives the override
#   stage: assessed          ← waiting on a person
verse gate
# - gate: sign-off
#   id: trigger.land-ask.sign-off~090ea377
#   mode: ASK
#   origin: land-ask
#   reason: "trigger land-ask gate sign-off on change.pr.8"
#   subject: change.pr.8
verse gate approve trigger.land-ask.sign-off~090ea377 --as walter@example.com
# approved trigger.land-ask.sign-off~090ea377
verse data change pr 8      # stage: landed
```

The id's suffix is derived from the firing's subjects rather than counted, so the same
decision re-reached is the same question rather than a second one.

With credentials, `verse data change pr 9 --put '{stage: proposed, tests: green}'` runs the real
thing. `verse flow` shows the assessment in flight, `verse data verse task instance
assess@change.pr.9` shows what the supervisor read and said, and the change ends `landed`,
`closed`, or waiting at a gate.

## Where this stops

- **Silence has no clock, for this shape.** A supervisor task that never terminates leaves its
  change at `stage: assessing` forever. `onFailed` covers failing; it does not cover hanging. The
  clock nearby does not fit: a gate's `!deadline` guards a firing that has not happened, and
  promote-on-silence wants a deadline on a spawn, which does not exist. A
  [resolver](attached-resolvers.md) that owns a gate does have one, `deadline: {within, then}`,
  where silence past it escalates to the layer above, but that is a clock on an unanswered
  question rather than on a task that stopped reporting.
- **The decision is a field, not a ballot.** `review.by` records which rule's supervisor
  wrote it, which is weaker than what the *whether* axis already gives a human: a `!quorum`
  gate keeps an attributable, retractable ballot per voter ([§5 of the
  tutorial](tutorial.md#5-gates-and-the-docket)). An agent casting a
  ballot on that same surface is what the reserved `!judgment` requirement kind is for, and that
  is a stub, so an agent cannot be one of two approvals — only the thing that decides whether the
  one approval is needed.
- **No second opinion.** From the same gap: two supervisors that must agree is expressible on
  paper, as `!quorum 2` over judgments, and not in the engine. Two `assess`-style rules writing to
  different fields, with `land-auto` requiring both, is the shape that works.
- **The inside is a different layer.** The supervisor here decides whether a change lands, by
  writing to the subject. What the *worker* agent may do while producing it is the other
  supervision layer, and binding a resolver from a charter — `gating.resolver` for a rule's own
  gates, `spawn.resolver` for its run's — is [attached resolvers](attached-resolvers.md). The two
  compose: this charter decides about the change, a resolver decides about the actions taken to
  produce it.

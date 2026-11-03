# An automatic supervisor

A [gate](glossary.md#gate-of-a-rule) of a charter has a [mode](glossary.md#mode), and the mode
is set per gate:

| Mode | What happens |
|---|---|
| `auto` | the action runs |
| `ask` | a person is asked |

Most flows want a third behaviour, between "every change lands on its own" and "a person looks
at every change": *an agent decides, and promotes what is not its to decide*. To promote a
change is to send it to a person.

This page is a worked example. In its charter, a supervisor agent assesses each change. The
supervisor clears the changes it may clear. The charter puts a person's gate in front of the
rest.

The supervisor can approve, but the charter stops it from approving everything, in two ways:

- The supervisor's remit is written in the charter, not in the supervisor.
- A supervisor that fails to decide promotes the change.

This is a simple pattern. For supervision as a live conversation, rather than a rule fixed at
install, see [resolvers](attached-resolvers.md).

A test drives the charter below through each of its routes
([`trigger/supervise_test.go`](../trigger/supervise_test.go)). The last section names the gaps.

## Two supervisors, one word

Verse has a Go type called `Supervisor`, and it is not the supervisor of this page:

| | what it decides | who writes it |
|---|---|---|
| `task.Supervisor` (+ a `Resolver`) | whether *one step* inside a running task proceeds | Go code, per runtime. It applies the task's [autonomy](reference/tasks.md#the-autonomy) within what its environment gives: a row that asks asks whom it names, and a [resolver](attached-resolvers.md) they grant may answer for them. |
| the supervisor here | whether *a firing* proceeds. This is the question at the level of the charter that a gate would put to a person. | a rule of a charter, which you can install over the wire |

An automatic supervisor of the second kind needs nothing new. A charter can already do each
step:

1. spawn an agent;
2. enforce the shape of the agent's answer;
3. patch that answer onto the subject as an ordinary fact;
4. route on the answer.

The example is those four steps and nothing more.

## The charter

The subject is a change, `change.pr.<n>`. It carries a `stage` and a `tests` fact. After the
assessment, it also carries the supervisor's `review`. Install the charter:

```sh
verse c -a -f - <<'TONY'
- name: assess                 # the supervisor decides, and decides whether to promote
  condition:
    subject: {path: "change.pr.*", value: { stage: proposed }}
  action:
    patch: { stage: assessing }
    spawn:
      agent: {name: verse}
      environment: {systems: {network: {mode: open}}}
      prompt: |
        Assess $[subject.ref] for landing. Read the change and the tests it touches.
        You are a review supervisor. Decide what is yours to decide and promote what is
        not: answer `escalate` whenever a person should look at this, `reject` when it
        should not land at all, and `approve` only when you would defend landing it
        unattended. Say why in one sentence. Do not approve to be agreeable.
      answer:
        decision: !or [approve, escalate, reject]
        risk:     !or [low, medium, high]
        why:      !irtype string
      autonomy:
      - {then: allow}
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
      path: "change.pr.*"
      value: !and
      - { stage: assessed, review: { decision: approve } }
      - !not { tests: green, review: { risk: low } }
  action:
    patch: { review: { decision: escalate, promoted: "outside the supervisor's remit" } }

- name: land-auto              # cleared, and within the remit: no gate, no question, no wait
  condition:
    subject:
      path: "change.pr.*"
      value: { stage: assessed, tests: green, review: { decision: approve, risk: low } }
  action:
    patch: { stage: landed }

- name: land-ask               # promoted: one human decision, in `verse question`
  condition:
    subject:
      path: "change.pr.*"
      value: { stage: assessed, review: { decision: escalate } }
  gates:
  - { name: sign-off, mode: ask }
  action:
    patch: { stage: landed }

- name: close                  # rejected: closing is cheap and reversible, so it is auto
  condition:
    subject:
      path: "change.pr.*"
      value: { stage: assessed, review: { decision: reject } }
  action:
    patch: { stage: closed }
TONY
```

The charter has five [rules](glossary.md#rule). The routing between them is the whole design:

| the supervisor answered | `tests` | what happens | who decided |
|---|---|---|---|
| `approve`, risk `low` | `green` | `land-auto` fires. No gate poses a question. | the supervisor |
| `approve`, risk `medium`/`high` | any | `remit` rewrites the decision to `escalate`, then `land-ask` fires | the charter, over the supervisor |
| `approve`, risk `low` | not `green` | the same: `remit`, then `land-ask` | the charter, over the supervisor |
| `escalate` | any | `land-ask` fires, with one `sign-off` gate in `verse question` | the supervisor, promoting |
| `reject` | any | `close` fires | the supervisor |
| nothing (the run failed) | any | `onFailed` writes `escalate`, then `land-ask` fires | nobody: this is the default |

Two routes are *not* in the table:

- No route lands a change on the supervisor's approval unless the charter also clears it.
- No route leaves a change stuck with nobody looking at it.

**Both are properties of the rules, not of the agent's judgment.**

## Three things keep it honest

**The supervisor cannot act. It can only answer.** Its environment gives the network and
nothing else: no repository, so it has nothing to push, and no files or commands, so it has no
tool to write with or run. So the supervisor cannot write the field that routes its own decision.

Its one way to reach the store is its final message. The `answer` field gives that message a
type: `decision` is one of three words, or the run fails and `onFailed` takes over. A
supervisor that talks instead of deciding gets no vote. Its environment gives it the network
and nothing to write with, so the one row of its autonomy, `then: allow`, lets it read without
asking anybody: there is nothing else it could do.

**The remit belongs to the charter.** `land-auto` requires three things, and the supervisor
writes only one of them:

| Field | Who writes it |
|---|---|
| `review.decision` | the supervisor |
| `review.risk` | the supervisor: it is the supervisor's own claim |
| `tests: green` | an iod that writes CI's state into the store ([iods](io.md)). The supervisor may not write it. |

So `review.decision: approve` alone does not land a change. That is the property you want, and
it is the reason `remit` is a rule of its own. `remit` is the complement of `land-auto`, written once, in one place. For
any assessed change that the supervisor approved, either `land-auto` matches or `remit` matches.
Never both match, and never neither. `remit` rewrites `decision` and leaves `why` as it is. So
the person at the gate reads the supervisor's reasoning, *and* the note that says the charter
overrode it.

**Failure promotes.** `onFailed` writes `escalate` and `risk: high`, and carries `$err` into
`why`. A supervisor can fail in several ways:

- it crashed;
- it could not produce an answer of the right shape;
- its runner could not run it.

In each case the change is not left unassessed. The rule sends the change up, with the reason. Point this charter at an agent that cannot see the change it was asked about, and you
get this, in the agent's own words:

```tony
review:
  by:       assess
  decision: escalate
  risk:     high
  why:
    "the supervisor did not decide: No files are accessible in the working directory;"
    " the PR diff and test files for change.pr.9 could not be found under any path tried."
```

One failure does not reach this route. With no model credentials, the runner offers no
`verse` agent, and verse refuses to install the charter at all: `this runner offers no agent
"verse"`. Nothing is installed, so nothing lands. **A supervisor that goes wrong must fail
toward the person.** That is a property of where `onFailed` points, not of the agent behaving
well.

## What "when" means here

The promotion happens *before* the action, not after it. `land-ask` asks its gate's question
while the change is still `assessed`. The person's answer is what makes the change `landed`.

A second question of timing needs its own rule: some changes should not cost a supervisor
run at all. A release change, for example, is never an agent's to clear. The charter can route
it straight to a gate and spawn nothing. To do this, narrow what `assess` is about:

```tony
value: !and
- { stage: proposed }
- !not { area: release }
```

Add a companion rule that matches `{ stage: proposed, area: release }`. Its action marks the
change `assessed` with `decision: escalate`. There is no spawn and no model is used: the change
goes straight to the gate of `land-ask`. The mode works the same way, only earlier. The cheapest
promotion is the one that skips the assessment.

## Drive it

The charter needs model credentials to install, because its `assess` rule names the `verse`
agent. `verse up` reads them: see [the local runner](reference/up.md#the-local-runner).
The *routing* needs nothing more. A `review` is an ordinary fact, so you can write one yourself
and watch where it goes. Start with a review that the supervisor cleared inside its remit:

```sh
verse data change pr 7 -put '{stage: assessed, tests: green, review: {decision: approve, risk: low}}'
verse data change pr 7
# payload:
#   review: { decision: approve, risk: low }
#   stage: landed            ← land-auto fired; `verse question` is empty
```

Next, write the same approval on a change that CI does not support:

```sh
verse data change pr 8 -put '{stage: assessed, tests: red, review: {decision: approve, risk: low, why: "docs only"}}'
verse data change pr 8
# payload:
#   review:
#     decision: escalate
#     promoted: "outside the supervisor's remit"
#     risk: low
#     why: "docs only"       ← the supervisor's reasoning survives the override
#   stage: assessed          ← waiting on a person
verse question
# - from: {rule: land-ask}
#   gate: sign-off
#   id: verse.trigger.land-ask.sign-off~090ea377
#   mode: ASK
#   reason: "trigger land-ask gate sign-off on change.pr.8"
#   subject: change.pr.8
verse question approve verse.trigger.land-ask.sign-off~090ea377 --as you@example.com
# approved verse.trigger.land-ask.sign-off~090ea377
verse data change pr 8      # stage: landed
```

Verse derives the suffix of the id from the subjects of the firing. It does not count. So when
the same decision is reached again, it is the same [question](glossary.md#question), and not a
second one.

With credentials, `verse data change pr 9 -put '{stage: proposed, tests: green}'` runs the real
supervisor. Three things show the result:

- `verse flow` shows the assessment while it runs.
- `verse data verse task instance assess@change.pr.9` shows what the supervisor read and said.
- The change ends `landed`, `closed`, or waiting at a gate.

## Where this stops

- **This charter sets no clock on the supervisor.** A supervisor task that never ends leaves
  its change at `stage: assessing`. `onFailed` covers failure, not a task that hangs. A spawn
  can carry the clock: `timeout: "30m"` cancels the run once it has run that long, not
  counting time spent waiting for an answer, and an `onCancelled` hook can then write
  `escalate` as `onFailed` does ([completion hooks](reference/action.md#completion-hooks-one-trigger-chaining-visible)).
- **The decision is a field, not a ballot.** `review.by` records which rule's supervisor wrote
  the decision. That is weaker than what a gate gives. A gate with `need: 2` keeps a
  [ballot](glossary.md#ballot) for each voter, and each ballot names who cast it
  ([the tutorial](tutorial/charters.md#no-is-a-real-outcome)). A principal of kind `agent` can
  cast one of those ballots, as a person can (`verse question approve <id> --as agent:<name>`). So
  an agent can be one of the two approvals on a gate with `need: 2`. The supervisor in this
  example is a task that writes a field, and it casts no ballot.
- **No second opinion, as written.** Two supervisors that must agree would be one gate with
  `need: 2` that both answer. This example does not do that. The shape it uses is two
  `assess`-style rules that write to different fields, with `land-auto` requiring both.
- **The inside is a different layer.** The supervisor here decides whether a change lands, by
  writing to the subject. What the *worker* agent may do while it produces the change is the
  other layer of supervision: the rows of the run's autonomy, which ask whom they name, and the
  [resolvers](attached-resolvers.md) those people grant. The two layers compose. This charter decides
  about the change. A resolver decides about the actions taken to produce it.

## Why it is this way

The reasons for this design are in [auto-supervisor.md](design/auto-supervisor.md), in the repository.

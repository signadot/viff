# Asking

To ask somebody something in verse, use `verse ask`. The ask goes to the people you name, with a
date if you give one. The answer is written back on your ask, and on a yes, what you said should
follow runs for you. Every verse asks: nothing is installed for it.

An ask is how anything in verse asks for an answer. You raise one with `verse ask`; a rule's
gate raises one when it stops for a person, and a task raises one with its `ask` tool. They are
the same record, on the one docket.

## The words

| | |
|---|---|
| **ask** | a record at `verse.ask.<id>`: what is asked, of whom, by when, and what follows. Yours is written by `verse ask`, and its id is derived from the name you give it |
| **asker** | whoever raised it. Your ask is your record |
| **answerers** | whom it is put to: a name or a list, in the [grammar a grant uses](perms.md#principals-and-grantees). Absent, anyone may answer |
| **due** | the date it settles at if nobody answers. Absent, it has no deadline, and waits until it is answered |
| **on silence** | what it settles on at its date if nobody answers: `no` unless you say `--on-silence yes` |
| **question** | an ask as whom it asks sees it: one waiting on you. `verse question` lists them |
| **docket** | every pending ask you can read, whoever raised it and whoever it waits on. `verse question --all` lists it |
| **then** | what follows a yes, as one key naming it: `{prompt: <text>}` is a task a model works from that prompt; `{calls: [{tool: …, input: …}, …]}` is a call list, run as written; `{answer: {ask: <id>, choice: <option>}}` answers another ask as you; `{ask: {…}}` asks another. Absent, the ask is a **vote** |
| **else** | what follows a no, answered or by silence at its date, as one key naming it, as `then` does |
| **answer** | what is written back: `yes` or `no` |
| **edit** | which asking of your ask an answer consented to: 1 as first asked, and one more each time you change what it asks |
| **labels** | what the ask says about itself for whoever watches asks: `--label team=payments,kind=deploy`. Verse routes nothing by them |

## Asking

```sh
verse ask land -q "Review verse/ask this week? If not, any objection?" \
  --to oscar@example.com --due 48h --on-silence yes --then '{prompt: "merge verse/ask to main"}'
```

`verse ask` answers with the ask's id, `land-` and a suffix. Commands take the name or the id. A
name you have used for two asks is refused with both ids, so you can pick one.

The ask goes to davido: on the docket, and in Slack where the deployment runs the Slack io. He
answers it as he answers any ask, with `verse question <id> --answer proceed` (yes) or `deny` (no).
You don't answer your own ask, unless you named yourself among whom it asks.
`--due` takes a date (`2026-10-02T17:00:00Z`) or a duration from now. If davido answers yes, the
ask gets `answer: yes`, and its `then` runs as a task for you. A no gets `answer: no` and runs
nothing. Nobody answering by the date is a no, unless you said otherwise: this ask asks for
objections, so `--on-silence yes` makes silence a yes. You say it when you ask, and it stays
when the date moves.

Leave out `--to` and anyone may answer. Leave out `--then` and the ask is a vote: the answer is
all it leaves.

A `then` of calls runs exactly what it lists, as `verse task run --driver calls` does:

```sh
verse ask tag -q "Tag v1.2?" --to oscar@example.com \
  --then '{calls: [{tool: run_bash, input: {command: "git tag v1.2 && git push origin v1.2"}}]}'
```

A prompt leaves a model to choose its actions, so that task's own gates ask you. A call list is
what you wrote, so it runs as you wrote it.

A `then` may also answer another ask, or ask one, as you: see
[Composing asks](#composing-asks).

What you give is checked before anything is written: the names, the date, and a `then` of one
key an ask runs. Refused, nothing stands.

## Composing asks

A `then` may be one more act of yours: an answer to another ask, or another ask. It is made as
you when the ask it follows is answered yes, and it lends you nothing you do not hold. The yes is
only the condition.

**Your answer waits on somebody else's.** Alice asks you to ship the hotfix. You will say yes, but
only once David has looked at the UI:

```sh
# alice
verse ask ship -q "Ship the hotfix?" --to walter@example.com
#   asked ship-3f2a…: Ship the hotfix?

# you
verse ask ui-ok -q "Is the UI ok to ship?" --to ivan@example.com --due 24h \
  --then '{answer: {ask: ship-3f2a…, choice: proceed, say: "david checked the UI"}}'
```

When David answers `proceed`, `ship` is answered `proceed` by you, with your words, as if you had
answered it yourself. `verse ask ui-ok` says what became of it:

```
answer: yes
…
then-answered:
  ask: ship-3f2a…
  at: "2026-09-30T10:12:00Z"
  status: 204
```

and Alice's `verse ask ship` reads `answer: yes`. Its record says it was answered
`by: user:walter@example.com`. If David says no,
or nobody answers by the date, nothing is answered, and `ship` still waits on you. Where `ship`
takes two proceeds, yours is your one ballot, and it waits for another.

**Asks chain.** `{ask: {…}}` takes what `verse ask` does (name, question, answerers, due,
onSilence, then, about, labels, keep) and asks it as you on a yes. A `due` of a duration counts
from that yes, and its own `then` may answer or ask in turn. David's yes asks Ann to deploy, and
her yes answers `ship`:

```sh
verse ask ui-ok -q "Is the UI ok to ship?" --to ivan@example.com --due 24h \
  --then '{ask: {name: deploy, question: "Deploy the hotfix to staging?", answerers: [ann@example.com],
            due: 4h, then: {answer: {ask: ship-3f2a…, choice: proceed}}}}'
```

`verse ask ui-ok` then reads `then-asked: {id: deploy-…}`, and `deploy` is your ask, to Ann, due
four hours after David's yes.

**Checked when you ask, and again when it is made.** When you ask `ui-ok`, verse checks that
`ship` stands, waits on you, and has a `proceed`, and that Ann can read what `deploy` is about. It
refuses the ask, and writes nothing, where any of that fails. When the yes comes, the answer or
the ask is made as yours would be then:

- The answer is to `ship` as you saw it. If Alice changes `ship` before David answers, it is
  asked again, and your answer to the old words is refused.
- If `ship` has settled meanwhile, there is nothing to answer.

What was refused, and why, is on your ask, as `then-answered` or `then-asked` with `refused`.

**A no has an `else`.** It takes what `then` takes, and runs on a no: one answered, or silence at
the date, which is a no unless you said `--on-silence yes`. It is how you escalate. Ask Ann, and
if she says no or nobody answers in four hours, ask the lead:

```sh
verse ask ship -q "Ship the hotfix?" --to ann@example.com --due 4h \
  --else '{ask: {name: ship-lead, question: "Ann said no, or did not answer: ship the hotfix?",
           answerers: [lead@example.com], due: 4h}}'
```

`verse ask ship` then reads `else-asked: {id: ship-lead-…}`. An ask withdrawn before anybody
answered runs neither. The message in Slack says both: what a yes runs, and what a no runs.

## How long it stands

An ask that has settled, answered or not, stands for a week and is then removed. That holds for
every ask on the docket, whoever raised it. `--keep 72h` says how long instead, for yours.
`verse ask` shows when it goes, as `expires`. What its `then` ran is a task, and stands as tasks
do. An ask settles once. A rule's gate that fires again after its ask settled asks a new ask,
with an id of its own, and the settled one keeps its answer until it goes. An ask that stood
orphaned and is asked again, when a restart or a lifted suspension brings the gate back to it, is
not going: it stands while it waits.

## Who reads it

An ask is read by you and by whom it asks, and by nobody else. An ask naming nobody, which
anyone may answer, is read by everyone.

An ask `--about` something is read by whoever may read that thing instead. Naming nobody, it is
answered by whoever may write that thing. You, and everybody you ask, must be able to read what
it is about, or verse refuses the ask.

## Following your asks

```sh
verse ask                # your asks: the question, who is asked, the date, the answer or pending
verse ask land           # one of them, and whom it waits on
verse ask --watch        # a line for each event, until stopped
```

`--watch` prints a line when an ask of yours is asked, is asked again, is answered or is
withdrawn. Each line starts with its revision. Pass the last one you saw as `--since` on the
next run to replay what happened in between. The store replays exactly for as long as it keeps
its log in full, an hour by default (`verse up --logd-config` says otherwise).

What others ask of you is the other side, `verse question`:

```sh
verse question            # the asks waiting on you, that you did not raise
verse question <id>       # one of them, with its options and ballots
verse question --watch    # a line when one starts waiting on you, and when it settles
```

`verse question` leaves out an ask a resolver serves for you; `--served` adds it
([Resolvers](attached-resolvers.md)). `--all` lists the whole docket. `verse question --watch`
takes `--since` as `verse ask --watch` does.

## Changing your mind

While an ask waits, you change it, or take it back:

```sh
verse ask land --due 72h                               # move its date: nothing is asked again
verse ask land --then '{prompt: "merge it and tag it"}'   # change what follows: it is asked again
verse ask land --to ani@example.com                    # ask somebody else: asked again, of them
verse ask land --withdraw                              # take it back
```

An answer is consent to what was asked, not to when. So a new date moves the clock and keeps
whatever was answered. A new `then` or new answerers change what was asked, so the ask is asked
again as its next **edit**. Answers given to the edit before don't count, and an answer that
names that edit is refused. Whom it asks changes without a moment in which somebody answers
words they weren't asked.

Withdrawing takes it off the docket and removes it. Nothing is written on anybody's behalf.

Once an ask has settled, it doesn't change. Anything more is a new ask.

In Slack, the message says who asked and what a yes runs, and a reply answers that edit. The
message is redrawn when the ask changes. A reply to the words it showed before the change is
refused, and a reply to the redrawn message is answered as it now reads.

## Whose authority the `then` runs with

Yours alone, and the `else`'s too. You raised the ask, so what follows it is yours to answer for. Ask Daniel "may I
merge this?", and his yes lets you merge what you may merge. Daniel doesn't need that right. The
task is your record, so its own gates ask you.

Nobody but you writes your ask: it is yours, and nobody owns a place above it.

## Saying something to a running task

Words you say to a running task are an ask to the task:

```sh
verse task say run-7 "use the staging bucket"     # said to run-7: say-3f2a…
verse ask say-3f2a…                               # answer: heard, once the task has taken them
```

The ask is yours, about the task, and read by whoever may read the task. It has no deadline: a
conversation turn has none. If the task is waiting on your turn, the words are your reply, and
the turn is answered. Otherwise the task takes them at its next boundary, and the ask settles as
`heard`. A task that ends before it takes them leaves the ask withdrawn, saying so. Words to a
remote task are heard when verse relays them to its runner. `verse flow say` says the words to
each task the flow reaches, as an ask to each.

Nobody else answers these asks, so they are not on the docket.

## Where the rest is

- [Gates](reference/gates.md) is how a rule raises an ask, and who may answer one.
- [A question for the person](reference/tasks.md#a-question-for-the-person) is how a task raises
  one, with a clock of its own (`within`, `default`) or none.
- [Permissions](reference/perms.md#questions) has who reads and who answers an ask.

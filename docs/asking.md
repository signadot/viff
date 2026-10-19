# Asking

`verse ask` asks somebody something. The ask goes to the people you name, with a date if you
give one. Their answer is written back on your ask. On a yes, what you said should follow runs
for you. Every verse can ask: nothing has to be installed for it.

An ask is how anything in verse waits for a person. You raise one with `verse ask`. A rule's
gate raises one when it stops for a person, and a task raises one with its `question-ask` tool.
All of them are the same record, on one docket.

## The words

| | |
|---|---|
| **ask** | a record at `verse.ask.<id>`: what is asked, of whom, by when, and what follows. `verse ask` writes yours, and its id comes from the name you give it |
| **asker** | whoever raised the ask. Your ask is your record |
| **answerers** | whom the ask is put to: a name or a list, in the [grammar a grant uses](perms.md#principals-and-grantees). Absent, anyone may answer |
| **due** | the date the ask settles at if nobody answers. Absent, it has no date and waits for an answer |
| **on silence** | what the ask settles on at its date if nobody answers: `no`, unless you say `--on-silence yes` |
| **question** | an ask as the person asked sees it: one that waits on you. `verse question` lists them |
| **docket** | every pending ask you can read, whoever raised it and whoever it waits on. `verse question --all` lists it |
| **then** | what follows a yes, as one key: `{prompt: <text>}` is a task an agent works from that prompt; `{script: <text>}` is a shell script, run as written; `{answer: {ask: <id>, choice: <option>}}` answers another ask as you; `{ask: {…}}` raises another ask. A prompt or a script takes a `policy` beside it, what the run is given, written as a spawn's ([the policy](reference/tasks.md#the-policy)); without one the run is given no tool. Absent, the ask is a **vote** |
| **else** | what follows a no, whether answered or by silence at the date. It takes one key, as `then` does |
| **answer** | what is written back: `yes` or `no` |
| **edit** | which version of your ask an answer agreed to: 1 as first asked, and one more each time you change what it asks |
| **labels** | what the ask says about itself, for whoever watches asks: `--label team=payments,kind=deploy`. Verse routes nothing by them |

## Asking

```sh
cat > then.tony <<'EOF'
prompt: merge verse/ask to main
policy:
- match: {system: commands}
  then: allow
- then: deny
EOF
verse ask land -q "Review verse/ask this week? If not, any objection?" \
  --to dana@example.com --due 48h --on-silence yes --then @then.tony
```

`then.tony` is what follows a yes: an agent given the prompt, under a policy that gives it the
shell and nothing else.

`verse ask` answers with the ask's id: `land-` and a suffix. Commands take the name or the id.
If you have used one name for two asks, verse refuses the name and prints both ids, so you can
pick one.

The ask goes to Dana. It is on the docket, and in Slack where the deployment runs the Slack iod.
Dana answers it like any ask: `verse question <id> --answer proceed` for yes, or `deny` for no.
You do not answer your own ask, unless you named yourself among the answerers.

`--due` takes a date (`2026-10-02T17:00:00Z`) or a duration from now. If Dana answers yes, the
ask gets `answer: yes`, and its `then` runs as a task for you. A no gets `answer: no` and runs
nothing. No answer by the date is a no, unless you said otherwise. This ask asks for objections,
so `--on-silence yes` makes silence a yes. You choose it when you ask, and it stays when the
date moves.

Leave out `--to`, and anyone may answer. Leave out `--then`, and the ask is a vote: the answer
is all it leaves.

A `then` that is a script runs exactly what it says, as `verse task run --script` does:

```sh
verse ask tag -q "Tag v1.2?" --to dana@example.com \
  --then '{script: "git tag v1.2 && git push origin v1.2"}'
```

A prompt lets an agent choose its own actions, so that task's own gates ask you. A script is
what you wrote, so it runs as you wrote it.

A `then` may also answer another ask, or raise one, as you ([composing asks](#composing-asks)).

Verse checks what you give before it writes anything: the names, the date, and that `then` is
one key an ask can run. If it refuses, nothing is written.

## Composing asks

A `then` may be one more act of yours: an answer to another ask, or another ask. Verse makes it
as you when the ask it follows is answered yes. It gives you no right you do not already have.
The yes is only the condition.

**Your answer waits on somebody else's.** Alice asks you to ship the hotfix. You will say yes,
but only once Dana has looked at the UI:

```sh
# alice
verse ask ship -q "Ship the hotfix?" --to you@example.com
#   asked ship-3f2a…: Ship the hotfix?

# you
verse ask ui-ok -q "Is the UI ok to ship?" --to dana@example.com --due 24h \
  --then '{answer: {ask: ship-3f2a…, choice: proceed, say: "dana checked the UI"}}'
```

When Dana answers `proceed`, verse answers `ship` with `proceed` as you, with your words, as if
you had answered it yourself. `verse ask ui-ok` says what happened:

```
answer: yes
…
then-answered:
  ask: ship-3f2a…
  at: "2026-09-30T10:12:00Z"
  status: 204
```

Alice's `verse ask ship` reads `answer: yes`, and its record says it was answered
`by: user:you@example.com`. If Dana says no, or nobody answers by the date, nothing is answered,
and `ship` still waits on you. If `ship` needs two proceeds, yours is one ballot, and it waits
for another.

**Asks chain.** `{ask: {…}}` takes what `verse ask` takes (name, question, answerers, due,
onSilence, then, about, labels, keep), and raises that ask as you on a yes. A `due` that is a
duration counts from the yes. Its own `then` may answer or ask in turn. Here Dana's yes asks Ann
to deploy, and Ann's yes answers `ship`:

```sh
verse ask ui-ok -q "Is the UI ok to ship?" --to dana@example.com --due 24h \
  --then '{ask: {name: deploy, question: "Deploy the hotfix to staging?", answerers: [ann@example.com],
            due: 4h, then: {answer: {ask: ship-3f2a…, choice: proceed}}}}'
```

`verse ask ui-ok` then reads `then-asked: {id: deploy-…}`. `deploy` is your ask, to Ann, due
four hours after Dana's yes.

**Verse checks when you ask, and again when it acts.** When you ask `ui-ok`, verse checks that
`ship` exists, waits on you, and has a `proceed` option, and that Ann can read what `deploy` is
about. If any check fails, verse refuses the ask and writes nothing. When the yes comes, verse
makes the answer or the ask as you would make it at that moment:

- The answer is to `ship` as you saw it. If Alice changes `ship` before Dana answers, `ship` is
  asked again, and your answer to the old words is refused.
- If `ship` has settled in the meantime, there is nothing to answer.

What was refused, and why, is on your ask, as `then-answered` or `then-asked` with `refused`.

**A no has an `else`.** It takes what `then` takes, and runs on a no: an answered no, or silence
at the date, which is a no unless you said `--on-silence yes`. This is how you escalate. Ask
Ann, and if she says no or nobody answers in four hours, ask the lead:

```sh
verse ask ship -q "Ship the hotfix?" --to ann@example.com --due 4h \
  --else '{ask: {name: ship-lead, question: "Ann said no, or did not answer: ship the hotfix?",
           answerers: [lead@example.com], due: 4h}}'
```

`verse ask ship` then reads `else-asked: {id: ship-lead-…}`. An ask withdrawn before anybody
answered runs neither. The message in Slack shows both: what a yes runs, and what a no runs.

## How long it stands

An ask that has settled, answered or not, stays for a week and is then removed. That holds for
every ask on the docket, whoever raised it. For your own ask, `--keep 72h` sets another time.
`verse ask` shows when it goes, as `expires`. What its `then` ran is a task, and it stays as
tasks do.

An ask settles once. If a rule's gate fires again after its ask settled, it raises a new ask
with its own id. The settled ask keeps its answer until it goes. An ask that is asked again
while it still waits, because a restart or a lifted suspension brings its gate back, is not
removed: it stays while it waits.

## Who reads it

You read your ask, and so do the people it asks. Nobody else does. An ask that names nobody,
which anyone may answer, is read by everyone.

An ask `--about` something is read by whoever may read that thing instead. If it also names
nobody, whoever may write that thing may answer it. You, and everybody you ask, must be able to
read what the ask is about, or verse refuses it.

## Following your asks

```sh
verse ask                # your asks: the question, who is asked, the date, the answer or pending
verse ask land           # one of them, and whom it waits on
verse ask --watch        # a line for each event, until stopped
```

`--watch` prints a line when an ask of yours is asked, asked again, answered or withdrawn. Each
line starts with its revision. Pass the last one you saw as `--since` on the next run, and
verse replays what happened in between. The store can replay for as long as it keeps its full
log: an hour by default (`verse up --logd-config` changes it).

What others ask of you is the other side, `verse question`:

```sh
verse question            # the asks waiting on you, that you did not raise
verse question <id>       # one of them, with its options and ballots
verse question --watch    # a line when one starts waiting on you, and when it settles
```

`verse question` leaves out an ask that a resolver answers for you; `--served` adds it
([resolvers](attached-resolvers.md)). `--all` lists the whole docket. `verse question --watch`
takes `--since` as `verse ask --watch` does.

## Changing your mind

While an ask waits, you can change it, or take it back:

```sh
verse ask land --due 72h                               # move its date: nothing is asked again
verse ask land --then @then.tony                       # change what follows: it is asked again
verse ask land --to ani@example.com                    # ask somebody else: asked again, of them
verse ask land --withdraw                              # take it back
```

An answer agrees to what was asked, not to when. So a new date moves the clock and keeps any
answer already given. A new `then` or new answerers change what is asked, so the ask is asked
again as its next **edit**. Answers to the earlier edit do not count, and an answer that names
that edit is refused. So nobody answers words they were not asked.

Withdrawing an ask takes it off the docket and removes it. Nothing is written on anybody's
behalf.

Once an ask has settled, it does not change. Anything more is a new ask.

In Slack, the message says who asked and what a yes runs, and a reply answers that edit. The
message is drawn again when the ask changes. A reply to the old words is refused. A reply to the
new message answers what it now says.

## Whose authority the `then` runs with

Yours alone, and the same for `else`. You raised the ask, so what follows it is yours to answer
for. Ask Dana "may I merge this?", and her yes lets you merge what you may merge. Dana does not
need that right. The task is your record, so its own gates ask you.

Nobody but you writes your ask. It is yours, and nobody owns a place above it.

## Saying something to a running task

Words you say to a running task are an ask to the task:

```sh
verse task say run-7 "use the staging bucket"     # said to run-7: say-3f2a…
verse ask say-3f2a…                               # answer: heard, once the task has taken them
```

The ask is yours, about the task, and whoever may read the task may read it. It has no date: a
turn in a conversation has none. If the task is waiting for your turn, the words are your
reply, and the turn is answered. Otherwise the task takes them at its next boundary, and the ask
settles as `heard`. If the task ends before it takes them, the ask is withdrawn, and says so.
Words to a task on a remote runner are heard when verse passes them to the runner.
`verse flow say` says the words to each task the flow reaches, as one ask to each.

Nobody else answers these asks, so they are not on the docket.

## Where the rest is

- [Gates](reference/gates.md): how a rule raises an ask, and who may answer one.
- [A question for the person](reference/tasks.md#a-question-for-the-person): how a task raises
  one, with a clock of its own (`within`, `default`) or none.
- [Permissions](reference/perms.md#questions): who reads and who answers an ask.

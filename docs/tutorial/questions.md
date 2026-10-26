# [Tutorial](README.md) -- Questions

A question in verse is an [ask](../glossary.md#ask). It is put to the people who may answer it.
It can have a date, and it can say what a yes runs.

There are three ways to ask. You ask with `verse ask`. A task asks when it reaches something it
may not do alone ([tasks](tasks.md)). A rule asks at its gate ([charters](charters.md)). All
three make the same record, and every pending ask is on one list, the
[docket](../glossary.md#docket).

This page needs [a verse running](README.md#start), and nothing else.

## Ask

```sh
verse ask deploy -q "Deploy the release to staging?" --to ana@example.com --due 1h \
  --then '{spawn: {script: "echo deploying", environment: {systems: {network: {mode: open}}}}}'
# asked deploy-b8a7d6bea9e3: Deploy the release to staging?
```

| Flag | What it says |
|---|---|
| `-q` | the question |
| `--to` | who may answer. Without it, anyone may. |
| `--due` | when it settles if nobody answers, as a date or a duration from now. Without it, it waits until somebody answers. |
| `--then` | what runs after a yes, on your authority: a spawn, written as a charter's is. Here it is a script, run as written, with the host's network; a `prompt` gives the job to an agent instead, and `autonomy` says what it may do unasked. Without `--then`, the ask only records the answer. |

The name, `deploy`, is yours. The id adds a suffix, and commands take either one.

## Who sees it

```sh
verse ask                 # your asks: the question, who is asked, the date, the answer or pending
verse question            # the asks waiting on you: none, because you asked Ana
verse question --all      # the docket: every pending ask you can read
```

```tony
- deadline:
    at: "2026-10-18T01:08:12Z"
    default: deny
  from:
    principal: user:you@example.com
  id: deploy-b8a7d6bea9e3
  options:
  - id: proceed
    need: 1
  - id: deny
    need: 1
  question: "Deploy the release to staging?"
  to:
    grantees:
    - user:ana@example.com
  waitingOn:
  - user:ana@example.com
  …
```

The options are the two answers, `proceed` and `deny`, and each needs one vote. The deadline is
what `--due` asked for. If nobody answers by then, the answer is no.

## Answer

Only somebody the ask is put to may answer. In a shared verse, Ana answers from her own CLI, or
from Slack, where a charter has the Slack iod carry the question to her and her reply back. On a
verse on your own machine, `--as` says who is answering, and verse takes the name as given; a
deployment authenticates you instead.
```sh
verse question deploy-b8a7d6bea9e3 --answer proceed --as bob@example.com
# answer proceed: 403 Forbidden: refused: user:bob@example.com holds nothing at verse.ask.deploy-b8a7d6bea9e3 …
verse question deploy-b8a7d6bea9e3 --answer proceed --as ana@example.com
# answered deploy-b8a7d6bea9e3
verse ask deploy          # answer: yes
```

Bob is not asked, so he may not answer. He may not even read the ask. Ana's yes ran what
`--then` said, as a task that acts for you:

```sh
verse task
```

```tony
- script: true
  id: deploy-b8a7d6bea9e3-74645a658d25
  result: deploying
  status: done
  …
```

A task is the next page. Here it is enough that a yes can start one.

## Silence, and changing your mind

If nobody answers by the date, the answer is no, unless you say otherwise. An ask for
objections says otherwise:

```sh
verse ask hotfix -q "Any objection to the hotfix?" --to dee@example.com --due 10s --on-silence yes
verse ask hotfix          # ten seconds later: answer: yes
```

While an ask waits, you can change it or take it back:

```sh
verse ask lunch -q "Lunch at noon?"
verse ask lunch --withdraw
```

A new date moves the clock and keeps the answers given. A new `--then` or a new `--to` changes
what was asked. So the ask is asked again, and the earlier answers do not count.

## A question is data

An ask is an entity at `verse.ask.<id>`, so the verbs of [data](data.md) read it:

```sh
verse data verse ask deploy-b8a7d6bea9e3 -trim '{answer, by, verdict, ballots}'
```

```tony
answer: yes
ballots:
  user:ana@example.com:
    at: "2026-10-18T00:08:18Z"
    authenticated: asserted
    choice: proceed
    edit: 1
by: user:ana@example.com
verdict: proceed
```

Each answer is a ballot. It says who chose what, and whether verse authenticated them or took
their word. `verse watch verse ask '*'` follows every ask you can read, and a rule can select an
ask as it selects any other entity.

[Asking](../asking.md) has the rest: asks that answer other asks, an `else` for a no, how long an
ask stands, and who may read it. Next, [tasks](tasks.md).

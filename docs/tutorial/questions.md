# [Tutorial](README.md) -- Questions

A question in viff is an [ask](../glossary.md#ask). It is put to the people who may answer it.
It can have a date, and it can say what a yes runs.

There are four ways to ask:

| Who asks | How |
|---|---|
| you | `viff ask`, on this page |
| a task, for itself | when it reaches something it may not do alone, and it waits for the answer ([tasks](tasks.md)) |
| a task, for you | with its `viff-ask-create` tool, when its environment gives it the [`viff` system](../systems/viff.md). The ask is yours, and the task does not wait for it |
| a rule | at its gate ([charters](charters.md)) |

All four make the same record, and every pending ask is on one list, the
[docket](../glossary.md#docket).

This page needs [a viff running](README.md#start), and nothing else.

## Ask

```sh
viff ask deploy -q "Deploy the release to staging?" --to ana@example.com --due 1h \
  --on '[{match: {verdict: proceed}, then: {spawn: {script: "echo deploying", environment: {systems: {network: {mode: open}}}}}}]'
# raised question deploy-b8a7d6bea9e3: Deploy the release to staging?
```

| Flag | What it says |
|---|---|
| `-q` | the question |
| `--to` | who may answer. Without it, anyone may. |
| `--due` | when it settles if nobody answers, as a date or a duration from now. Without it, it waits until somebody answers. |
| `--on` | what runs after the answer, on your authority: rows, each a `match` on the answer and a `then`. This one row meets a yes (`{verdict: proceed}`), and its `then` is a spawn, written as a charter's is. Here it is a script, run as written, with the host's network; a `prompt` gives the job to an agent instead, and `autonomy` says what it may do unasked. Without `--on`, the ask only records the answer. |

The name, `deploy`, is yours. The id adds a suffix, and commands take either one.

## Who sees it

```sh
viff ask                 # your asks: the question, who is asked, the date, the answer or pending
viff question            # the asks waiting on you: none, because you asked Ana
viff question --all      # the docket: every pending ask you can read
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

Only somebody the ask is put to may answer. In a shared viff, Ana answers from her own CLI, or
from Slack, where a charter has the Slack iod carry the question to her and her reply back. On a
viff on your own machine, `--as` says who is answering, and viff takes the name as given; a
deployment authenticates you instead.
```sh
viff question deploy-b8a7d6bea9e3 --answer proceed --as bob@example.com
# answer proceed: 403 Forbidden: refused: user:bob@example.com holds nothing at mirror.ask.deploy-b8a7d6bea9e3 …
viff question deploy-b8a7d6bea9e3 --answer proceed --as ana@example.com
# answered deploy-b8a7d6bea9e3
viff ask deploy          # answer: yes
```

Bob is not asked, so he may not answer. He may not even read the ask. Ana's yes met the row,
which ran its spawn as a task that acts for you:

```sh
viff task
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

If nobody answers by the date, the ask settles on `deny`, unless you say otherwise. An ask for
objections says otherwise:

```sh
viff ask hotfix -q "Any objection to the hotfix?" --to dee@example.com --due 10s --on-silence proceed
viff ask hotfix          # ten seconds later: answer: yes
```

While an ask waits, you can change it or take it back. Ask about the production deploy, give it
more time, and then withdraw it:

```sh
viff ask deploy-prod -q "Deploy the release to production?" --to ana@example.com --due 1h
viff ask deploy-prod --due 4h         # moved deploy-prod-… to the date four hours from now
viff ask deploy-prod --withdraw
```

A new date moves the clock and keeps the answers given. A new `--on` or a new `--to` changes
what was asked. So the ask is asked again, and the earlier answers do not count.

## A question is data

An ask is data at `mirror.ask.<id>`, so the verbs of [data](data.md) read it:

```sh
viff data mirror ask deploy-b8a7d6bea9e3 -trim '{answer, by, verdict, ballots}'
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

Each answer is a ballot. It says who chose what, and whether viff authenticated them or took
their word. `viff watch mirror ask '*'` follows every ask you can read, and a rule can select an
ask as it selects any other data.

[Asking](../asking.md) has the rest: asks that answer other asks, a row for a no or for silence, how long an
ask stands, and who may read it. Next, [tasks](tasks.md).

# question

The `question` system is the questions that a task puts to people. The agent or script hands the
turn over and waits on the docket for an answer. A task that asks as the person it runs for, and
goes on, uses the [verse system](verse.md) instead.

It is not the question a runner asks before a step, when an autonomy row of the task says
`ask`. That question comes from the autonomy, not from a call of the task.

## What a charter sees

Every question is a record at `verse.ask.<id>`, whoever asked it: a person with `verse ask`, a
rule's gate, or a task with this tool. A rule reacts to one as it reacts to any data. A
charter installed at the system level is what carries the docket to people in Slack
([asking](../asking.md)).

## What a charter writes

A rule asks in two ways: with a gate before it acts, and with an `ask` action, whose yes runs
what it names ([asking](../asking.md)). Neither is a verb of this system. This system is how a
task asks.

## What a task can do

A task has `question-ask` when its environment gives `question`, as it has any tool. Asking a
person is the step `question.ask`, and the autonomy row that matches it decides it: `allow` puts the question at
once, `deny` refuses it, and `ask` asks before the question is put. Codex's and Claude's own
questions for a person are the same step, and Claude is offered its question tool only where
the environment gives `question`.

```tony
spawn:
  agent: {name: verse}
  environment: {systems: {network: {mode: open}, question: {}}}
  prompt: "Decide whether $[subject.ref] can be rolled back, and ask before you say so."
  autonomy:
  - match: {system: question}
    then: allow
  - then: deny
```

| Tool | What it does | Step |
|---|---|---|
| `question-ask` | puts a question to the person the task runs for, or to the answerers it names, and waits; the answer is the call's result. With `options`, the answer is one of them. With `within`, silence settles on `default`. | `question.ask` |

The question is the task's, and it is on the docket beside every other question. `verse
question` lists it, and the answer given there is what the tool returns. In a conversation, the
agent asks after every answer, to give the person the turn. A script asks with `verse-call-tool
question-ask`, and reads the answer on its standard output.

## Where the credential lives

Nowhere. A question is verse's own record. The runner poses it, and the answer comes back
through verse. No system outside verse is reached, so no credential is needed.

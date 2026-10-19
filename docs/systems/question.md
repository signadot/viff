# question

The `question` system is the questions that a task puts to people. The doer hands the turn over
and waits on the docket for an answer.

It is not the question a runner asks before a step, when a row of the task's policy says
`ask`. That question comes from the policy, not from a call of the doer.

## What a charter sees

Every question is a record at `verse.ask.<id>`, whoever asked it: a person with `verse ask`, a
rule's gate, or a task with this tool. A rule reacts to one as it reacts to any entity. A
charter installed at the system level is what carries the docket to people in Slack
([asking](../asking.md)).

## What a charter writes

A rule asks in two ways: with a gate before it acts, and with an `ask` action, whose yes runs
what it names ([asking](../asking.md)). Neither is a verb of this system. This system is how a
task asks.

## What a task can do

A doer has `question-ask` when its policy names `question`, as it has any tool. Asking a person
is the step `question.ask`, and the row that matches it decides it: `allow` puts the question at
once, `deny` refuses it, and `ask` asks before the question is put. Codex's own question for a
person is the same step.

```tony
spawn:
  doer: {agent: verse, with: {network: {mode: open}}}
  prompt: "Decide whether $subject can be rolled back, and ask before you say so."
  policy:
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

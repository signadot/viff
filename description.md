# question.raise: a task raises an ask with then and else, as verse ask does

PR #51's original ask (faxkagtmh12krs61vxn0): a task poses a question with a `then:` or `else:` through the tool, as a person does with `verse ask`. A stored `then` belongs where the asker does not wait for the answer in the store, so this is a raise, not the conversational `question-ask`, which waits and is withdrawn with the task.

- A verb `raise` of the `question` system, tool `question-raise`: the ask's words, options, answerers (`to`), clock (`due`, `onSilence`), labels, `then`, `else`. It does not wait: the call answers the ask's id, or verse's refusal in verse's words. Autonomy rows decide it like any verb, so a spawn can ask its author before a big raise.
- The runner carries it to `POST /asks` with its own token and `Verse-Task` (the relay, previous issue). The ask is the task owner's: `from: {principal: <owner>, task: <id>}`, and its `then` runs as the owner when it settles, as a person's does.
- Containment: where the request names a task, a `then`/`else` that spawns lies within the task's environment and its autonomy is no looser (systems.Environment.Within, task.Autonomy.Within), checked at the raise. A task this verse no longer holds the launch of (after a restart) has a spawning `then` refused.
- Offered only where the runner has a verse to reach and a token of its own; the embedded runner is ENG-1297.

Recorded on "Verse as a system, v0" (Notion 40408fc654be8049879edeafe483abcc).
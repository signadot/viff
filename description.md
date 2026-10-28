# An autonomy row's ask takes no then or else

A task waits on its row's question, so what follows a yes is the step the task takes; a stored `then` belongs only where the asker does not wait for the answer in the store (a person at the CLI; later, a caller of the ask tool). The row's `then`/`else` is the form PR #51 landed after "via the tool" was dropped from its ask (faxkagtmh12krs61vxn0); the gate's half went in 0f2dbf501.

Remove:
- `then`/`else` on a row's `ask:` (task.Ask), refused at parse as an unknown field.
- the record's copy of them on a task's question (mirror followsFor/asPosed, PostFrameQuestion), and the hosted question's copy in versed.
- the spawn-time and autonomy-change checks of them (checkAutonomyThens, checkFollow, taskBound if nothing else uses it).

Keep `systems.Environment.Within` and `task.Autonomy.Within`: the ask tool's `then` will need them.

Recorded on the "Verse as a system, v0" Notion page (40408fc654be8049879edeafe483abcc), section "A task's question, with a continuation".
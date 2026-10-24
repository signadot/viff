# verse mcp: a conversation with a task, as `task run --attach` has one

`verse task run --attach` is a conversation: the launch carries `converse`, so when the agent goes quiet the person is handed the turn (a `gate: turn` question with `reply` and `end`), and the run ends when they end it. An agent on `verse mcp` cannot have one. `verse_task_run` never sets `converse`, so a task it runs ends when the model stops, and the plan for `verse mcp` (tdk97ayqh12krfwnv1n0) listed `task run --attach` as not a tool.

The reading half is there: `verse_task_events` streams the transcript from a cursor, and `verse_question_answer` answers any question, a turn included (`option: reply` with `say`, or `end`). What is missing is the launch, and a hint on the frame.

- `verse_task_run` takes `converse`: the run is a conversation, as `--attach` makes one. A script has no turn, so `converse` on a script is refused rather than ignored, since the parameter was given.
- `verse_task_events` says, after a pending frame, how to answer it: the tool, the question's id and its options, as `task attach` prints the command for a reader who cannot answer there.
- docs/reference/mcp.md says how a conversation goes: run with `converse`, read, answer the turn, end it; tasks.md's conversation section names the tools.
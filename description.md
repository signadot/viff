# docket: a task's question says the tool and the verb, never the action — approvers answer blind

A question a task's gate raises reaches the docket as `tool: shell`, `reason: "verb exec"`, the gate's name and the derived effects. Nothing on the record says WHAT the action is: not the command a `run_bash` or `shell` would run, not the paths a `write_file` or a patch would touch, not the destination of a `push`. `verse question`, `verse question <id>`, the Slack ask and `attach`'s own prompt all show the same thing, so whoever answers answers blind. `attach` shows the command only after approval, on the exec frame.

Seen answering six gates of a codex task from the docket on 2026-10-09 (pvk89kvah12kry4rs1n0), and it is not the codex driver's: the `llm` driver's `run_bash` and `write_file` questions have the same shape, and so does a worker's.

## The fix

One bounded, one-line rendering of the action, carried everywhere the question is:

- `task.ToolUse.Describe()`: the command for a shell; the path for a file; the destination for a push; the question for an ask; otherwise the scalar inputs, compactly. Truncated, so a record stays a record.
- `task.Pending.Action` and `task.Frame.Action` (on `pending` and `decision` frames), so a local task's question and a remote runner's both carry it, and `task.PendingGate.Action` for `verse task get`.
- The question record gets `action:` from either path (`mirror.PostQuestion`, `mirror.PostFrameQuestion`), so `verse question` shows it without a CLI change.
- `attach`'s prompt names it: `⏸ gate "shell" (shell: go test ./...) — approve? [y/n]`.

Independent of the codex branch; against main, so the next ship carries it.
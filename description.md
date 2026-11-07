# slack: a task's question shows its action and its step's fields, not only its pose

A task's question asked in Slack shows what the question's record says of it: its `pose` and, under it, `about` (`pkg/io/slack/out.go`, `body` and `posed.line`). The record says more. Since q3k89kva (`bd5b7271`) it carries `action`, the action in one line (`task.ToolUse.Describe()`), and since the systems work it carries `step` and `fields`, the step the question is about with each field in its form's type (`api.AskRecord`). `verse question <id>` prints the record whole, so an answerer at the CLI sees the step; one in Slack sees neither, and answers knowing the tool and the gate and not what the call does.

It matters most where the runner performs the act and the person is asked about its arguments:

- **`mcp.call`** (1v5art3x): a `create_sandbox` asked about shows the tool, and not the sandbox's `spec`. `Describe()` leaves nested values out by design, so `action` alone does not carry it; `fields.arguments` does.
- **`secret.exec`** (pa35grmz): the `argv`, `env` and `dir` of the command run with the secret.
- **`slack.post`** (1z5art3x): the text a post would say.

An answer is bound to the step it was given about (275art3x), so the binding holds either way; this is what the person can see when they give it.

## The fix

- **The line:** under the pose, the question's `action` where it has one, as `attach` shows it.
- **The step:** beneath, the step's fields from `fields`, each as its form reads (a `doc` field as structure), bounded so the message stays a message, and saying where it was cut and that `verse question <id>` has it whole.
- **No field that is a secret's value.** No step carries one today (`secret.exec` carries a secret's name, and its env names variables); a test holds that the rendering shows only what the record holds.

## Tests

- A task's question with an `action` and `fields` renders both under the pose; one with neither renders as today.
- A `doc` field with nested values (an `mcp.call`'s arguments) is shown as structure, and one past the bound is cut and says so.
- A message redrawn when the question settles keeps the lines.

Related: q3k89kva (closed, the record's side), 1v5art3x, pa35grmz, 275art3x.
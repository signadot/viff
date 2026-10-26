# asks: a rule's and a task's question run their `then` and `else` before the answer returns, as whoever asked

A question's `then` and `else` ran only for an ask a principal raised. `followAsk` read `from.principal` and skipped them silently when it was empty, so a rule's gate question and a task's question never ran one, and neither had a way to carry one: `trigger.Gate` had `onDenied` and no `then` or `else`, and an autonomy row's `ask` had `within`, `default`, `need`, `answerers` and `labels`.

The behaviour to make uniform, whoever asked: what follows a question runs as whoever asked, before the answer goes back to them — the `then` on a yes, the `else` on a no (a refusal answered, or the clock settling on one). For a rule, that is `via:` the rule (from `from.rule` and `from.at`), and the firing proceeds once it has run. For a task, that is whom the task acts for, or the task itself where it acts for nobody (from `from.task`), and the task's turn is delivered once it has run. "Run" for a spawn means launched, not finished: the asker gets the answer once the spawn is under way, never when it reaches a terminal state. An onward ask keeps refusing a `then`, since the carry is what follows it.

What changes:
- `trigger.Gate` gains `then` and `else`, in the shape an ask's have (`{spawn}`, `{answer}` or `{ask}`), checked at install as the rule; the engine puts them on the gate question. `else` is about the question where `onDenied` is about the subject.
- an autonomy row's `ask` gains `then` and `else`, same shape, checked at the spawn and when the autonomy is replaced, as whom the task acts for; `hostedQuestion` (and a relayed frame, through `asPosed`) puts them on the task's question.
- `mirror.PostQuestion` writes them on the record.
- versed runs the consequence in `settle`, before delivery, for every kind, through the existing machinery (`thenAnswers`, `thenAsks`, `runThen`) as the principal `from` names; the marks on the record (`then-task`, `then-answered`, `then-asked`, and the `else-` ones) keep it to once, so a delivery retried finds it done. The person's path moves there too, so there is one place, and `resumeAsks` replays an answered question's consequence whoever asked.
- a `then` run as a `via:` principal acts for nobody: its task's questions go to the docket, not to `$principal`.
- a task hands what follows its answer no more than it has: a row's `then` or `else` that spawns is held within the task's own environment (`systems.Environment.Within`) and its autonomy no looser (`task.Autonomy.Within`), at the spawn and when the autonomy is replaced. The launch's environment is kept beside the hosted task for that; one hosted again after a restart has none this verse knows, and a replaced autonomy whose consequence spawns is refused there, saying to run the task again.

Builds on PR #50 (verse/ask-from-to), which gives `from`.

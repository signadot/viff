# asks: a rule's and a task's question run their `then` before the answer returns, as whoever asked

A question's `then` runs only for an ask a principal raised. `followAsk` reads `from.principal` and skips the `then` silently when it is empty, so a rule's gate question and a task's question never run one, and neither has a way to carry one: `trigger.Gate` has `onDenied` and no `then`, and an autonomy row's `ask` has `within`, `default`, `need`, `answerers` and `labels`, no `then`.

The behaviour to make uniform, whoever asked: a question's `then` runs as whoever asked, before the answer goes back to them. For a rule, that is `via:` the rule (from `from.rule` and `from.at`), and the firing proceeds once the `then` has run. For a task, that is the task's own principal (from `from.task`), and the task's turn is delivered once the `then` has run. "Run" for a spawn means launched, not finished: the asker gets the answer once the spawn is under way, never when it reaches a terminal state. An onward ask keeps refusing a `then`, since the carry is what follows it.

What changes:
- `trigger.Gate` gains `then`, in the shape an ask's `then` has (`{spawn}`, `{answer}` or `{ask}`), checked at install as the rule; the engine puts it on the gate question.
- an autonomy row's `ask` gains `then`, same shape, checked at spawn as the task; `hostedQuestion` puts it on the task's question.
- `mirror.PostQuestion` writes `then` on the record.
- versed runs the consequence in `settle`, before delivery, for every kind, through the existing `then` machinery (`thenAnswers`, `thenAsks`, `runThen`) as the principal `from` names; the marks on the record (`then-task`, `then-answered`, `then-asked`) keep it to once, so a delivery retried finds it done. The person's path moves there too, so there is one place.
- a `then` run as a `via:` principal acts for nobody: its task's questions go to the docket, not to `$principal`.

Not in this change: an `else` on a rule's or a task's question (`onDenied` stays what a rule's no does); `then` on an onward ask.

Builds on PR #50 (verse/ask-from-to), which gives `from`.
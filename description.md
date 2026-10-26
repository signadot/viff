# an ask says who raised it and whom it is put to, as from and to; a task's record says actsFor

Linear: ENG-1300 (ENG-1300). The design is attached there: "Design: who raised a question, and whom it is put to (from/to/by)", reviewed by Scott.

## What

Every question record at `verse.ask.<id>` says its parties in three keys:

- `from`: who raised it, exactly one of `{rule, at}`, `{task}` or `{principal: <verbatim>}`;
- `to`: whom it's put to, `{grantees: […]}` or `{task}`, and may be absent;
- `by`: who settled it (unchanged).

They replace `origin`, `asker`, `answerers`, `saidTo` and the top-level `task`. `serves` marks an onward ask. The request's `answerers` becomes `to` (`api.Ask`, `api.AskChange`), read strictly so the old key is refused. A gate's own `answerers:` configuration keeps its name.

A task's record (`verse.task.instance.<id>`) says whom it acts for as `actsFor`, not `asker`.

`from.principal` and `actsFor` carry authority: their readers are access code, and move unchanged in behaviour except where commit 2 says.

## Commits

1. `mirror, versed, api, io, cli, deploy!:` an ask says who raised it and whom it is put to, as `from` and `to`: the record, its readers in versed, mirror, Slack and the CLI, the request's `to`, and `gate-ask.tony`, in one commit, since the charter's and Slack's tests read the records the code writes.
2. `versed!:` an ask is changed by whoever asked it: `patchAsk` checks `from.principal`, not the owner (a resolver is refused changing its own onward ask today, and its installer is let in).
3. `mirror, launch, trigger, versed!:` a task's record says whom it acts for as `actsFor`.
4. `docs:` the pages that show these keys, including the `then: {ask: … answerers: […]}` examples and wording in docs/asking.md and docs/tutorial/questions.md, which now describe a body refused with 400.

## Deploy

Repopulate, not migrate, after a post in #verse-dev that anyone may veto: pending asks are cleared, and no task running at the deploy survives it. verse, the iods and the charter deploy together.

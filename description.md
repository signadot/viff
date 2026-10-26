# an ask says who raised it and whom it is put to, as from and to; a task's record says actsFor

Linear: ENG-1300 (ENG-1300). The design is attached there: "Design: who raised a question, and whom it is put to (from/to/by)", reviewed by Scott.

## What

Every question record at `verse.ask.<id>` says its parties in three keys:

- `from`: who raised it, exactly one of `{rule, at}`, `{task}` or `{principal: <verbatim>}`;
- `to`: whom it's put to, `{grantees: […]}` or `{task}`, and may be absent;
- `by`: who settled it (unchanged).

They replace `origin`, `asker`, `answerers`, `saidTo` and the top-level `task`. `serves` marks an onward ask. The request's `answerers` becomes `to` (`api.Ask`, `api.ServedAsk`, `api.AskChange`). A gate's own `answerers:` configuration keeps its name.

A task's record (`verse.task.instance.<id>`) says whom it acts for as `actsFor`, not `asker`.

`from.principal` and `actsFor` carry authority: their readers are access code, and move unchanged in behaviour except where commit 2 says.

## Commits

1. `mirror, trigger, versed, api!:` an ask says who raised it and whom it is put to, as `from` and `to`.
2. `versed!:` an ask is changed by whoever asked it: `patchAsk` checks `from.principal`, not the owner (a resolver is refused changing its own onward ask today, and its installer is let in).
3. `io, cli!:` the iods and the CLI read `from` and `to`.
4. `deploy!:` `gate-ask.tony` has a DM rule per kind of `from`, and two for a principal's.
5. `mirror, launch, trigger, versed!:` a task's record says whom it acts for as `actsFor`.
6. `docs:` the pages that show these keys.

## Deploy

Repopulate, not migrate, after a post in #verse-dev that anyone may veto: pending asks are cleared, and no task running at the deploy survives it. verse, the iods and the charter deploy together.
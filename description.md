# residual: docs corrections — what was found and not corrected

1a9dc067 on `verse/build-on` corrected about sixty statements in the docs. These were found by the same sweep and left as they are, because each needs more than a check against the source: a decision, a run that could not be made locally, or a rewrite of a passage.

## Narrative that disagrees with the code

- `docs/task-control.md`: a `decision` is said to be emitted on the POST, and the broker emits it at the worker's poll (`broker/broker.go`, `inbox`). `cancelled` is said to land on the worker's confirmation, and is emitted at once. A claimed task is said to resume after a restart, and a `-broker` broker is rebuilt empty.
- `docs/task-control.md` and `docs/external-tasks.md` speak of a durable journal; the task's log is in memory (`task/loop.go`), and `docs/reference/tasks.md` says an index.
- `docs/external-tasks.md` names `unixExec`, `remoteDriver` and a terminal `code`. None is in the tree.

## Two pages disagree, not settled here

- An io's claim: `docs/io.md` and `docs/reference/io.md` show a claim carrying a `path`; `io.Own` has the one field `At`.
- `docs/reference/io.md` refuses an overlap "beside anything else in its system"; `docs/io.md` says "in the same scope".
- `docs/reference/iod.md` has the owner write four grants by hand where the install writes them, omits `-admin-addr`, and says Linear has no `-out`.
- `docs/reference/tasks.md` gives `reownedWhy` two values where `docs/reference/resolvers.md` gives three.
- `docs/reference/tasks.md` and `docs/seams.md` say `-as` is required on `task do`; it falls back to the configured user, and now to the local user.
- `docs/perms-guide.md` says nothing uses the `agent` principal yet, and shows a record `at: spaces.ana.notes` where `docs/perms.md` says the record lands at the topmost node made.
- `deploy/README.md`'s knob table has no row for the GitHub io.

## Output samples

The docs show one address in two spellings in printed output: `at: demo.pr.1` and `subject: 'demo.vote."1"'`. The samples were not all re-run.

## Source comments

About forty comments in `versed/`, `io/` and `cmd/verse/` still write addresses with colons (`task:instance`, `git:*`, `demo:pr:1`). The two strings a user sees were corrected; the comments were not.

## Found on the way

- `verse runner add pool:code <url>` installs a runner at `verse.runners.pool:code`, a name with a colon in it. Nothing refuses it.
- `docs/auto-supervisor.md` said an agent cannot be one of two approvals on a gate. Run against a throwaway verse, `verse gate approve <id> -as agent:reviewer` was recorded as one of two. The page was corrected to say so. Whether an agent counting toward a threshold is intended is not a question the docs can answer.
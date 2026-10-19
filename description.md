# review of #24, the lows: a create is never forced, the default decision has a name, Repo.Auth goes, deployed runners get a git ceiling, docs drift

Daniel's Low findings on PR #24 (comment 6272626247) that are fixed in one pass:

- Low 1: the deployed runners (deploy/verse-runner, deploy/codexrunner) set no ceiling, so containment did nothing on staging. Both now carry a `git` ceiling: `github.com/signadot/**` allowed, the rest denied.
- Low 4: a first create with `force: true` was pushed forced. A branch not at the forge is created, never forced.
- Low 5: a decision no row made was named `policy[-1]`, which `Policy.Named` could not resolve. It is `policy[default]`, which resolves to DefaultPolicy's row, and which no row can be named (a name has no brackets).
- Low 8: `workspace.Repo.Auth` was never set, so the "own credential" paths in `Mirrors.Ensure` were unreachable. Removed.
- Low 9: README.md and docs/reference/tasks.md said "autonomy" and "how much the task decides alone"; docs/task-control.md and docs/external-tasks.md described enforcement outside the process as attestation of reported effects. They now say what holds: the runner performs the verbs that cross a credential, decides each by the task's policy and its own ceiling at the act, and holds a replaced policy to the launch's verbs and the ceilings.

Low 3 (`created` per channel) is closed by 3mf9wqb3: `own` is read off the tip's trailers. Lows 2, 6 and 7 and Medium 4 are their own issues.
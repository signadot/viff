# repo-charters: the branch a repository's charters follow is configurable

The repo-charters reconciler (system/repocharters) follows `main`. The branch is written into the template: it reads `git.<repo>.ref.main`, it asks about pull requests whose `base: main`, and its issue texts say "main". So a repository whose default branch is something else can't have its charters reconciled. example/app, on `master`, is the one that stands in the way now.

**Change:** a `branch` on each `repoCharters` entry (`RepoCharterConfig.Branch`, `Params.Branch`), with `main` as the default. The reconciler reads that ref and asks about pull requests onto it, and its words name it. The record and rule names stay `main`, `state.main` and so on, because they name the branch's role (what is proposed onto) and not its name. `verse/live` stays as it is: it's the reconciler's own branch, not the repository's.

The rendering for the default doesn't change, and the reference example is still the same rendering. A test renders the reconciler for `master` and checks that it reads that ref and asks about pull requests onto it.
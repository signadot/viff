# runner: issue tools — git-issue, github-issue and linear-issue — as named tools a program or agent calls

Three more tools for the verse runner on main, each a name a spawn can ask for and a runner config can withhold, with nothing added to the spawn spec's structure:

- `git-issue` files an issue in a workspace repository's git-issue tracker. The runner creates it in its mirror of the repository (issuelib.GitStore on the bare mirror, plumbing only) and pushes the issue ref to the remote with its own token. Offered where the workspace has a repository at all.
- `github-issue` opens an issue at the GitHub repository a workspace repository is on, through GitHub's REST API with the runner's token, as the pull-request tools do. Offered where some repository is at github.com.
- `linear-issue` creates a Linear issue in a team named by key, through Linear's GraphQL API with a credential the runner holds (`LINEAR_API_KEY`, another variable with `-linear-key-env`). Offered when the runner has the credential. Needs no repository.

Each is performed on the runner's side of the delivery channel (new ops), so no credential reaches a task. Each call derives a `create` on an `issue` resource (`task.KindIssue`) named by its tracker — the repository's remote for git-issue, `github:owner/name`, `linear:TEAM` — so a charter gates filing in one tracker apart from another, and apart from pushing.

The names join `knownTools` and the offer (`delivery.Offered`, `offeredFor`), so `tools:` on a spawn, an environment's floor and the runner's `tools` all apply to them unchanged, and a spawn naming one the launch cannot offer is refused at launch as today.

Tests: the server ops against a bare origin (git-issue ref lands and lists), a fake GitHub API and a fake Linear API; end to end, a program filing a git issue under an asking gate, approved; a Linear filing with a fake API over the runner; `linear-issue` absent from the list without a key and a spawn naming it refused. Docs: runnerd.md (the env table, the tools row), the examples README's tools paragraphs.

The point beside the tools themselves: three trackers added with no change to the spawn spec's shape, which is the claim made for naming tools over an effects vocabulary.
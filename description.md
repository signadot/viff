# A step's repo lowercases only the host; a spawn whose owner/name case differs from the forge's is refused

A step's `repo` field is `runner.Repo.Canonical()`: `host/owner/name`, lowercased whole (verse/cut, round 4). That assumes the forge reads owner and name case-insensitively, as GitHub and GitLab do. On a case-sensitive self-hosted forge, `host/Team/App` and `host/team/app` are two repositories but one policy identity.

Decided (set-aside decision #5 of 2026-10-18): lowercase only the host.

## The hole that lowercasing only the host opens

On GitHub, `Acme/Secret` and `acme/secret` are the same repository. If the owner/name keeps the case the spawn wrote, consider a ceiling or policy like this:

```tony
- {match: {system: git, repo: github.com/acme/secret}, then: deny}
- {match: {system: git, repo: "github.com/acme/**"}, then: allow}
```

A spawn of `acme/Secret` gets past the deny. Its step reads `github.com/acme/Secret`, so the deny row does not match and the allow row does. `TestARowNamesARepoWhateverItsCase` (runner/repo_test.go) pins the current protection.

## Proposed shape

1. `Canonical()` lowercases the host only. The `repo` form (systems/document.go `FormRepo`) requires a lowercase host segment, and owner/name may be in any case.
2. A spawn whose owner/name case differs from the forge's canonical spelling is refused. The runner reads the canonical name when it sets up the checkout. For GitHub and GHES that is `GET /repos/{owner}/{name}` → `full_name`, through `systems.github.api` and the runner's token. Where it can be checked earlier (install, prompt), verse refuses there.
3. Open: what a forge with no such lookup does. The choices are no check, which leaves the hole there, or refusing a `repo` row on that forge unless it is spelled exactly.

Until (2) lands, (1) alone reintroduces the bypass, so the two land together.

Where: runner/repo.go (`Canonical`, `Host`), systems/document.go (`FormRepo` check), the runner's checkout setup (runner/runnerd/workspace, Repos), docs/reference/tasks.md ("A `repo` is `host/owner/name` in lower case").
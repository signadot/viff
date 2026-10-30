# Charters outside verse still write `runner:`, refused since #59

Follow-up from the #59 review (point 6). #59 refuses `runner:` on a spawn as an unknown field. Charters outside this repo still write it, and fail at install:
- verse-charters `running/measure/defects.tony`;
- verse-workshop:
  - `examples/charters/stale-review/stale-review.tony`;
  - `examples/release-notes/draft.tony`, `draft-codex.tony` and `review.tony`;
  - `examples/tasks/github.tony`, `linear.tony`, `slack.tony`, `slack-claude.tony` and `slack-codex.tony`.

Where a file names a runner to pick its agent (`verse.runners.codex`, `verse.runners.claude`), the spawn names the agent instead.

The workshop is pinned to a staging tag, so it needs a release, with its README's tag, alongside the ship of #59.
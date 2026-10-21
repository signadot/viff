# deploy: the runners offer linear-issue and slack

The verse runner has `linear-issue-*` and `slack-*` tools on main (#24, 40934c34), offered where the runner's config names the system's credential. No staging runner does, so every runner on staging describes git, git-issue, github, github-issue, question and the inner systems, and a task's Linear or Slack call has no tool.

Name them in `deploy/verse-runner`, `deploy/codex-runner` and `deploy/claude-runner` (the last on PR #31, which this rides):

- `linear-issue`, `credential: LINEAR_API_KEY`, ceiling: create in ENG, comment on and close `ENG-*`, deny the rest. ENG is the team the Linear iod reflects.
- `slack`, `credential: SLACK_BOT_TOKEN`, ceiling: #verse-dev and #verse-dev-slackio-testing, the channels the bot is in, deny the rest.
- The keys from the Secrets the iods already read (`verse-linear`, `verse-slack`), `optional: true` as `GITHUB_TOKEN` is: an unset one is logged at start and its calls are refused.

Not in scope: the Linear iod's out half (`-out`, `linear.ENG.out`).

Rollout: `make ship-runners`.

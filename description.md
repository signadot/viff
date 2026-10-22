# deploy: staging reconciles example/infra and example/app's charters

For the workshop (2026-10-22, 18:00), repo charters should be doable on hackspace and signadot as well as cli and libconnect.

- hackspace (default branch `main`, App already installed): a repoCharters entry at `repos.hackspace`, a git mirror, github reflection, and out places in repopulate.sh.
- signadot (default branch `master`, already mirrored and reflected): a repoCharters entry at `repos.signadot` with `branch: master`, and its out places.

Neither repository has `.verse/charters`, so each reconciler asks once "Remove every charter at repos.<repo>?". For signadot that would remove the comment-drift and pr charters repopulate.sh installs at `repos.signadot`; answer **no** right after the ship. Moving that pair into example/app's `.verse/charters` is the follow-up.

Rollout: `make ship` (iods + versed), `deploy/repopulate.sh verse:9797 --force`, then answer the two empty-tree questions.
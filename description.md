# deploy: the codex runner is `codex-runner`, as every other object is named

The codex runner's directory, manifests, Kubernetes objects, tier label, make targets and install line are spelled `codexrunner`, the one deploy name with no hyphen beside `verse-runner`, `linear-io`, `github-io`. Rename it `codex-runner` throughout deploy/, the Makefile and deploy/README.md; the build options follow the camelCase of `runnerImage` and `linearIO` (`codexRunner`, `codexRunnerImage`). The image tag `verse-runner:codex-<sha>` stays.

Rollout on staging: a selected apply (`-l tier=codex-runner`) does not remove the old `codexrunner` Deployment, Service and ConfigMap, and the runner installed as `codex` points at `http://codexrunner:9802`; both are hand steps after the ship.
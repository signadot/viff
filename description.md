# deploy: the codex runner's key by reference (an ExternalSecret), as every other credential

`verse-codex` is the one credential this deploy still makes by hand (`kubectl create secret generic verse-codex --from-literal=OPENAI_API_KEY=…`). Every other one is an ExternalSecret from an `aws-secretsmanager` entry a `*SecretKey` knob names, deleted where the knob is empty; the claude runner's followed that on verse/claude-runner (0t4bdajwh12krvwatxn0). This gives codex the same: `deploy/secrets/codex.yaml`, `codexSecretKey`, the staging profile naming `staging/verse/codex`, the README rows.

Rollout on staging, by hand, once: create the entry `staging/verse/codex` as `{"OPENAI_API_KEY": "<the key verse-codex holds>"}`; delete the by-hand `verse-codex` Secret so the ExternalSecret (creationPolicy Owner) can own the name; apply the untiered objects (`make deploy PROFILE=staging SELECT='!tier'`). The running codex-runner pod keeps its environment across the gap; the next one reads the synced Secret.

Branch `verse/codex-secret` in `.worktrees/codex-secret`, off main.
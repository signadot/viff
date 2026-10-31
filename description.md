# staging: the verse-codex ExternalSecret has no entry to sync from (staging/verse/codex)

## State (2026-10-31)

The staging profile names `codexSecretKey: "staging/verse/codex"` (deploy/profiles/staging.tony). Since #29 shipped, the `verse-codex` ExternalSecret has been in `SecretSyncedError`. No entry by that name exists in AWS Secrets Manager (us-west-2).

The codex runner still starts. It reads `OPENAI_API_KEY` from the `verse-codex` Secret that was made by hand 21 days ago (`optional: true` in codex-runner.yaml), so nothing is broken today. But the key isn't managed like every other credential, and the ExternalSecret fails on every refresh.

## Waiting on

The OpenAI platform account the key will be metered to is still being set up. Once it exists:

1. Create the entry as deploy/README.md's "Creating the entries" says: `{"OPENAI_API_KEY": "…"}` at `staging/verse/codex`, us-west-2.
2. Check that `kubectl -n verse get externalsecret verse-codex` shows `SecretSynced`, and that the Secret now carries the new key. If the ExternalSecret's creation policy refuses to take over a Secret made by hand, delete that Secret first and let it sync.
3. Restart codex-runner so it reads the key, and run one codex spawn.
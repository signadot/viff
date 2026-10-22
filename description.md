# deploy: staging reconciles the charters of verse, signadot/cli and example/libconnect

#32 emptied staging's `repoCharters` while the charter syntax changed. #33 has landed, so verse's entry goes back. Two more repositories join it, signadot/cli and example/libconnect, both on `main`.

**Change:**
- `deploy/profiles/staging.tony`: add cli and libconnect to `gitMirror` (as `git.cli` and `git.libconnect`) and to `githubRepos`.
- `deploy/repopulate.sh`: register the git and github iods with the out places for all three repositories (`git.<repo>.out`, `github.signadot.<repo>.out`).
- `deploy/auth/staging.tony`: `repoCharters` entries for verse (`repos.verse`), cli (`repos.cli`) and libconnect (`repos.libconnect`), each answerable by `user:*`.

**Before the iods ship:** the signadot-verse App has to be installed on signadot/cli and example/libconnect. Both iods read every repository at start and exit on one they can't read, which would also take down reflection of verse and signadot.

**Expected after the ship:** neither repository has `.verse/charters` yet, so each reflects the empty tree and asks once: "Remove every charter at repos.<repo>?" A yes creates `verse/live` there. A `verse/live` ruleset is optional hardening and a repository setting.
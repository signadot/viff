# deploy: staging reconciles signadot/hotrod's charters too

This follows h3cgay94. signadot/hotrod, on `main`, joins verse, cli and libconnect.

- Add it to `gitMirror` (as `git.hotrod`) and to `githubRepos`.
- Add `git.hotrod.out` and `github.signadot.hotrod.out` to the iods' out places in repopulate.sh.
- Add a `repoCharters` entry at `repos.hotrod`, answerable by `user:*`.

The same prerequisite applies before the iods ship: the signadot-verse App has to be installed on signadot/hotrod.
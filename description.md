# repo-charters: a repository dropped from repoCharters is not turned off

Removing a repository's entry from `repoCharters` and restarting versed changes nothing: start installs only the configured reconcilers, and `restorePackages` restores the stored `repo-charters.<repo>` package from the store. No person may suspend or remove it (a system rule is the system's to hold), and the charters it had the git iod install at the entry's `at` keep running. So turning the experiment off needs a wipe.

An experimental feature turns off by config alone. A repository no longer named in `repoCharters` is removed at start: its reconciler package, what the reconciler keeps under `repo-charters.<repo>`, and the charters installed at the entry's `at` on its say.

Then staging drops `repo: verse` from `repoCharters` (deploy/auth/staging.tony), so verse's own charter syntax can change without the deployed verse checking the new syntax against the old.

Also: deploy/README.md says `verse flow suspend -charter repo-charters.verse -r` pauses the reconciler; a person cannot suspend a system rule (trigger/suspend.go mayHold), so the line is wrong.
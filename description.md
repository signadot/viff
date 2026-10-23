# versed: at startup a system charter whose text is unchanged is installed again, replaying every rule

`installSystemCharters` applies every config-named system charter "over what stood, every time", after `restorePackages` has already brought each of them back from the store. An install replays every rule over everything it selects, so on staging each repo-charters package's rules ran over their slice about three times per startup: once in the restore, twice more in this phase (each package's install-time `seen` warning prints twice in it).

Staging, 544d8b26, 2026-10-23: pod start 08:41:12, restored packages in 2m53s, installed the system charters in 8m1s, Ready 08:54:35. The config had not changed.

Fix: a system charter whose install would change nothing against what stands is skipped. "Nothing" has to cover everything the install writes: rules and strata (versed/chartercheck.go already compares these against the running engine), the package's reads, its schedules and outbox declarations, and the system's claim on its records. Also find out why one install of a package installs its rules twice.
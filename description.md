# staging: the github iod's out loops pass every 5s, and their pass reports keep docd busy

On staging (2026-10-23) docd spent 254s of 10 minutes (42%) on 352 commits to `verse.iods.github.out`, the 264-byte pass record versed writes on every `POST /iods/github/pass`. The github iod runs one out loop per repository (six since 7e92489a), each passing at `-out-every`'s default 5s, so ~72 reports a minute, each 0.5–0.9s in apply on docd. The same rate keeps docd snapshotting and compacting without a break (tony-format m56xswgsh12ks9ravdn0). Every verse command waits behind it: `verse c` 43–49s, `verse status` 10s, a read 1.5–3s.

Three follow-ups (Slack #verse-dev, 2026-10-23):
1. deploy: the github iod's out loops pass every minute (`-out-every 1m`). A comment's hint still kicks a pass at once; an input written under a github outbox waits up to a minute.
2. versed: the pass record is written only when what it says changes.
3. versed: `verse c` reads the schedules once for the tree, not once per package.
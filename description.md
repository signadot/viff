# versed: restoring packages reads the whole charter and strata once per package

`restorePackages` calls `LoadStrata` and `LoadCharter` on each package's engine, and each of those lists every record under `verse.trigger` (all strata, all rules) and keeps the ones the engine owns (`ownRecord`). With N packages that is N full reads of each slice, one after another, at the moment the store is busiest. Staging restores seven packages.

The same shape as the schedules in `verse c` (mz6fwh8gh12ksrgavdn0, 42514735).

Fix: `restorePackages` lists the strata and the charter once and hands every engine the records; each engine still takes only its own. `LoadStrata` / `LoadCharter` stay as the read plus that load, for the baseline engine and scopes.
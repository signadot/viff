# Review of the launch state and the verse system: deployed ceilings, retire's serial, the record's vars, the docs example

Review findings on 53c74a85c and 22d488c09 (verse/verse-system-base), fixed together:

1. **The deployed runners give `verse` no ceiling.** deploy/verse-runner, codex-runner and claude-runner configs give `question: {ceiling: {}}` only, and a ceiling written nowhere is closed, so once shipped a launch naming `verse` and every `verse-ask-create` would be refused. Each gives `verse: {ceiling: {}}`.
2. **`retire` reads the serial when the task ends**, from the map, not the run's that ended: a run of the same id hosted in between loses its entry an hour later. The serial is taken when the run is reflected and handed to its end.
3. **A restart reflects before it hosts**, so a re-attached task that already ended retires nothing and its entry stands. Hosted first, as every launch site does.
4. **The record carries the environment's `vars`**, data a spawn hands its script, readable by whoever reads the task. The record carries its `systems` alone, which is all the bound reads.
5. **docs/systems/verse.md's example fails**: the spawn names no runner, so verse's own runtime refuses `verse`, and the `then`'s push has no `git`.
6. **The retention test races** on the package-level `hostedRetention`. It is the Server's (`HostedRetention`), a test setting its own.
7. **taskBound's comment** still says a restart loses the launch.
8. **`due` is parsed untrimmed** as a duration, so ` 48h` is sent raw and refused.
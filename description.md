# verse up: no local runner is left running that no verse will stop

Follow-up from the #59 review (point 9).

- **A changed `--addr` or issuer on a kept data dir:** the old runner refuses the new verse and is "left running" (cmd/verse/localrunner.go). The config is rewritten with the new port, so no later run finds the old runner again.
- **`stop()` gives up after 15 s** and removes the registration anyway, for example against an older runnerd with no `/stop`. The runner keeps running, unregistered.
- **Ctrl-C or SIGTERM while `startLocalRunner` is still probing:** the shutdown goroutine reads `lr == nil` and skips `stop()`. The child is spawned in its own process group, so it survives the verse.
- **`respawn` doesn't re-check ctx after its backoff sleep,** so a cancel in that window starts a child after `stop()` has captured the old one.
- **One failed 3 s probe** of an adopted runner, during the 30 s check, starts a second runner and moves the registration. The first runner, and its tasks, are orphaned.
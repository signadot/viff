# trigger: an install that left no owner record waits ten seconds for a pass that cannot arm it

`Engine.InstallTriggerAs` ends by kicking a pass and waiting for it to answer for the rule (`passed`, `internal/trigger/trigger_api.go`). A rule placed ahead of its owner record is held with the cause "waiting for its owner record", which counts as one a pass clears, since the install writes the record after the rule and the pass after it arms the rule.

Where the record is never written, no pass clears it and nothing ends the wait but its bound, `followWait`, ten seconds. Two ways to get there:

- the installer holds less than `owner` at the place (`read-write` beneath `verse.trigger.rule`, by a line somebody added);
- the store took the rule and did not record whose it is.

In both, versed then refuses the install with 409 "would act as nobody" and takes the rule back out (`actsAsInstaller`, `internal/versed/place.go`). The answer is right and arrives ten seconds late.

## Observed

A goroutine dump of `TestAnInstallerHoldingLessThanOwnerInstallsNothing` mid-run has the request in `trigger.(*Engine).passed` under `InstallTriggerAs`. The two tests that reach it, that one and `TestAnInstallTheStoreDidNotRecordIsTakenBackOut`, took 10.4s and 10.3s each: 21.8s of the versed suite for three tests in `actsas_test.go`.

## Fix

By the time `passed` is called the install's write has returned, so an owner record that is not there was not written. `passed` reads whose the rule is and, where it is still nobody's, does not wait.

Found while checking 4h5pm5kvh12krkbjsdn0 (the versed suite's runtime).
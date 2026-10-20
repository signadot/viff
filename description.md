# verserunner: script-doer tests flake under the full suite (TestADeniedPushSendsNothingAndTheWorkIsHeld, TestAScriptPostsToSlackUnderAnAskingPolicy)

Seen 2026-10-21 on verse/claude-runner (fcfff61e, whose diff does not reach these tests). In four runs of `go test -count=1 ./runner/runnerd/verserunner/`, two failed, each time on a different test, and each failure took about 3.24s:

- `TestADeniedPushSendsNothingAndTheWorkIsHeld`, in a full-suite run (`go test $(go list ./... | grep -v /scratchpad)`).
- `TestAScriptPostsToSlackUnderAnAskingPolicy`, with the package run alone. The script's result was empty (`slacktools_test.go:109: the script read:` followed by nothing), and `slacktools_test.go:128: Slack saw []`.

Each test passes alone (`-count=8` and `-count=5`), and the package passed once at HEAD 0866bc3c. Both tests run a script doer that uses verse-call-tool (`withCallTool`) and wait on an ask or a push. The shared ~3.24s suggests a fixed wait inside the script or call-tool path that expires under load, rather than either test's own logic.

Not investigated beyond this.
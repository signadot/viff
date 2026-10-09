# versed: two tests read deploy/charters/verse/repo/verse files that 3f2f5fd5 removed, so main's suite is red

On origin/main e96c714d, `go test ./versed` fails twice before any change:

    --- FAIL: TestEveryDeployedCharterInstalls (0.00s)
        deployedcharters_test.go:50: lstat ../deploy/charters/verse/repo/verse/comment-drift: no such file or directory
    --- FAIL: TestIssueNextProposesOnTheTick (0.17s)
        issuenext_test.go:46: open ../deploy/charters/verse/repo/verse/issue-next.tony: no such file or directory

`deploy/charters/verse/repo/verse/` holds only `comment-drift-states.md` since 3f2f5fd5 (scott/repo charters, #2). The two tests still name the directory the charters left.

Found while checking the removal branches (h6hx7ddc, hahx7ddc), which inherit the red; neither touches these tests. To settle: either the tests follow the charters to where they went, or the charters are back under the path the tests read, or the tests go with the charters. `make check` does not run the suite, which is how a red main stays red.
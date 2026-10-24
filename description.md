# cmd/verse: TestWaitFollowsAgainWhenItsStreamEnds is flaky under a full run -- an ask's POST is reset by the test server

Seen 2026-10-24 on branch verse/github-pr-read (whose changes do not touch cmd/verse's wait or asks), in `go test ./systems/... ./task/... ./runner/... ./cmd/...`:

```
--- FAIL: TestWaitFollowsAgainWhenItsStreamEnds (1.21s)
    mcpwait_test.go:182: ask [second -q Second?]: ask: Post "http://127.0.0.1:63571/asks?format=tony": read tcp 127.0.0.1:63573->127.0.0.1:63571: read: connection reset by peer
```

Run alone it passes (`-count=5`, all green). The second ask's POST lands on a connection the test's verse closed or reset: the stream ending, which the test provokes, appears to take the HTTP server's connection with it, and the ask that follows reuses it. Under a full run's load the window is wide enough to hit.

Not investigated further; filed so the next red run of this test is recognised rather than re-diagnosed.
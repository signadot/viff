# cmd/verse: TestWaitFollowsAgainWhenItsStreamEnds flakes on the CLI's reused connection

The test fails in about 1 run in 20 (2 of 40 measured), at the ask after `ts.CloseClientConnections()`:

    ask [second -q Second?]: ask: Post "http://127.0.0.1:…/asks?format=tony": EOF
    (or: read: connection reset by peer)

The wait itself does not fail. Closing every connection to the verse also closes the server's end of the CLI's idle keep-alive connection, which sits in the process-wide `http.DefaultTransport` pool. The next `ask` can reuse it before the client has read the close, and Go retries a request on a reused dead connection only when it is idempotent, which a POST is not. A real CLI is a process per command, with a fresh pool, so this is the test's in-process CLI only.

Fix: close the shared transport's idle connections after closing the server's, so the next ask dials afresh. The wait's stream is in use, not idle, so it still ends, which is what the test is about.
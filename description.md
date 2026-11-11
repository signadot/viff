# mcp: a session a plain 401/403/400 failed is never opened again

From Daniel's review of PR #7 (pullrequestreview-5864267561).

The SDK fails a whole connection on a non-2xx reply that carries no JSON-RPC body (any but 429, 500, 502, 503, 504), and `with()` in `pkg/runner/runnerd/mcpclient/client.go` opens a session again only on `mcp.ErrSessionMissing`. So after one such reply every later call of that task to that server fails with `connection closed: ... client is closing`, without being sent, and `delivery/mcp.go` reports each as "whether the server performed it is not known", which is false: nothing was sent.

Reproduced: a middleware answering one POST with a plain 401 (also 403, 400). The call that got it fails with `sending "tools/call": Unauthorized`; the next three fail closed; the server performed 1 call of 5.

A likely trigger is a key rotation: a call lands while the old key is revoked and the new file is not yet written, a gateway answers a plain 401, and the task cannot reach the server again, though the credential is read at each request so that a rotation holds for the next call.

Fix: recover from the state, not the cause. A call that fails with `mcp.ErrConnectionClosed`, and for which no request reached the round tripper (the call's `mark`), opens a new session and is sent on it, once. Nothing was sent, so "a call is never sent twice" holds; a call in flight when the connection failed had a request sent, and is not sent again.

A test: a plain 401 on one call, the next call answers, and the server saw each call once.
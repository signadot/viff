# github app auth: minting an installation token holds the lock, under the asking request's context

`installation.authorization` (io/github/app.go) holds `i.mu` across `mint()`, the POST to `/app/installations/<id>/access_tokens`. It also uses the context of whichever request happened to find the token due.

- **Waiting.** While a token is minted, every other request waits on the lock: reflection GETs, GraphQL calls, and out-half writes for each repository. That is one round trip normally, and up to the client's 60s timeout when GitHub is slow.
- **Cancellation.** If the asking request's context is cancelled, for example a reflection that verse gave up on, the mint fails, and the next request starts it again.

A possible shape: mint under a context of the installation's own, shared singleflight-style, so waiters take the one result. Filed from the review of verse/github-out (2026-10-02); not fixed.
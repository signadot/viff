# verse up: adopt a runner and a store only once they prove themselves; one verse per data dir

Follow-up from the #59 review (point 8).

- **The bearer goes out before identity is known.** At adoption, `LocalRunnerAt` → `ProbeOver` presents a verse token to whatever listens on the recorded port (cmd/verse/localrunner.go:86–93; `RegisterLocalRunner`'s handshake). A 404 server there received it.
  - The token is cached per audience for 10 minutes, so the runner started next accepts the same one.
  - `local-runner.tony` keeps the port across a clean `down` (mode 0644). On a shared host, whoever binds that port while you're down gets a token for your runner.
  - The fix is to ask the bare describe first, and to present the bearer only to what answers with the expected subject.
- **The docd pair is adopted on a bare TCP dial** (cmd/verse/docd.go:74–86).
  - After a crash, anything on that port is taken as this data dir's store.
  - If another verse's docd holds it, this verse silently runs on that store.
  - If an unrelated process holds it, there are 3 minutes of retries, then a `verse up` that keeps failing until `docd.addr` is deleted, and nothing says to delete it.
- **Two `verse up` on one data dir share a store and one `local` registration.**
  - With different `--addr`, the second starts its own runner and repoints `verse.runners.local`, so the first verse's spawns go to a runner that refuses its issuer. The second's `down` then removes the registration, and the first never re-registers.
  - With the same `--addr`, the second attaches to the live store and runs `StartServing` before `Listen` fails. A lock on the data dir would cover both cases.
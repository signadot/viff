# The full suite and smoke cost 6–7 minutes a commit; the time-to-benefit ratio should come down

Every commit is checked with gofmt, `make check`, `go test ./...` and `make smoke`. In practice that is 6–7 minutes of wall clock per commit, and the tree has to stay still while it runs. During 2026-10-01/02 it ran a dozen times, mostly on changes confined to one or two packages. Most of those minutes bought no information the changed packages' tests had not already given.

## Where the time goes

From the last full run (rebased verse/github-auth, 2026-10-01), `go test ./...` per package:

| package | seconds |
|---|---|
| versed | 126.5 |
| trigger | 45.2 |
| entity | 44.1 |
| cmd/verse | 26.5 |
| io | 15.3 |
| docs/reference/examples/coderunner | 13.1 |
| io/slack | 12.6 |
| everything else | under 7 each |

- The packages sum to about 297s. `go test` runs them in parallel, so the wall clock is about versed's 126s plus compile time, and smoke adds about a minute more.
- **versed alone sets the floor.** It has 395 tests, none of them `t.Parallel()`, and most start their own logd+docd pair (`entitytest.Store` → `Addr` → `docdOver(LogdAddr)`).
- versed, trigger and entity have 161 `time.Sleep` calls between their tests.

## Possible directions (nothing decided)

1. **Parallel versed tests.** Each test already has its own store, so most could call `t.Parallel()`. The ones sharing a process-wide thing (a port, an env var, a global) would be found and left serial.
2. **A shared logd per package**, with each test in its own scope or root, instead of a logd+docd pair per test. The cost is isolation, which would have to be shown to hold.
3. **Sleeps replaced by waits on the condition** (an `until` helper with a short poll, as some tests already have), so a test takes as long as the thing it waits for and not a fixed guess.
4. **Fewer tests: removed, or merged into more general ones.**
   - Many tests were added one per fix, each standing up its own server and store to check one case. Several in one file often exercise the same contract from slightly different sides.
   - **Removed:** a test whose contract is already covered by another, or that pins a detail no longer load-bearing (for example one that only restates an implementation choice since refactored away).
   - **Merged:** cases of one contract become rows of a table-driven test over one server or store, each row a subtest. That is one setup, not N, and the contract is read in one place.
   - What must survive is each contract and each regression a test was written for. A merge keeps the case as a row, and a removal names which test still covers it. The mutation checks that showed a test catches something are a guide: a test no mutation distinguishes from another is a candidate.
5. **A targeted check for a commit**, kept separate from the full run before a merge or push:
   - the changed packages and their importers (`go list -deps -test` in reverse), plus `make check`;
   - the full suite and smoke before a merge to main or a push.
6. **Measure first:** `go test -json ./... | ` a per-test duration report, so the slowest tests, and the setup most repeated, are named rather than guessed.

The aim is the ratio: a check that takes seconds for the common change, and the full minutes only where the change can reach that far.

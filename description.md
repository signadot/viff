# verse up: the local runnerd is the version verse is, and dev builds find theirs

Follow-up from the #59 review (point 10), about `resolveRunnerd` (cmd/verse/localrunner.go).

- **No version check beside verse.** A `runnerd` beside verse is used without one, for example `~/go/bin/runnerd` left from an earlier `go install ./cmd/...`. An older runnerd may lack `/stop`, which leaves a runner no verse stops.
- **A mismatched runnerd on PATH** only draws a warning and is used. The matching version is never installed.
- **Dev builds look like releases.** `go build` reports a pseudo-version, not `(devel)`, so a dev build runs `go install …/cmd/runnerd@<pseudo-version>`. That fails for unpushed commits and is refused for `+dirty` versions.
- **A half-finished install sticks.** If runnerd installs and verse-call-tool doesn't (network drop, ctrl-c), the next run finds the cached runnerd and never retries verse-call-tool.
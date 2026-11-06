# check-json fails on main: delivery/issues.go imports encoding/json

`make check-json` fails on `main` at 210fc9d2:

```
encoding/json imported outside the allowlist:
  pkg/runner/runnerd/delivery/issues.go
```

## Where it came from

bf5437e7 ("github-issue: refuse a pull request's number on comment and close") reads
`/issues/{n}` before a comment or a close, and asks whether the answer carries `pull_request`.
It decodes that field as a `json.RawMessage`, which is the import. The decoding itself is
`githubCall`'s, in `delivery/github.go`, which is on the allowlist.

## The fix

The field is only asked whether it is there. Decode it as `any`: absent or `null` is nil, and
anything else is not. `issues.go` then needs no import, and the allowlist in
`scripts/check-json.sh` stays as it is: the JSON still stops at `github.go`.

## Tests

`TestCommentAndCloseAtTheRemoteTrackers` and the test bf5437e7 added beside it
(`delivery/issues_test.go`) already cover a pull request's number, an issue's and one that is
nothing. They pass unchanged.
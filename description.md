# cmd/verse, trigger, versed, api: five user-facing messages say what the code does not

Found by the docs pass (ybved3g6h12ksffvw9n0), checked against the code, not changed there:

- cmd/verse/client.go:646, `verse question`'s synopsis: "approve|deny are the two default options". The options are `proceed` and `deny` (`--answer`, client.go:836).
- trigger/trigger.go:432, a deadline that does not read: "(quote the duration: within: \"24h\")". go-tony reads an unquoted `24h` as the string `"24h"`, and verse's own charters write `every: 3h` unquoted, so the hint sends a reader after the wrong cause.
- versed/packages.go:282, a `reads:` with no address: "installed with `verse c -a --at <address>`". `--at` is not a flag of `verse charter`; the address is the charter's `at:` or the steps.
- api/status.go:92: a rule's trouble is "on the rule in `verse charter`, under `trouble`". `verse charter` prints it as a stderr note, not under the rule (charterfile/charterfile.go:379, 410-421).
- versed/server.go:2326, a write into verse's own part: the verbs listed include `source add`. The verb is `iod add`.
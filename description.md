# verse-charter-get answers [] for an if pattern that does not apply; addrSegs is duplicated

Review of 344d0a9e6.

1. The stock `verse-charter-get` filters with its own `filtered` (runner/runnerd/stock/versecharter.go), which takes a `tony.Match` error as no match. `if: "{at: !glob [1]}"` makes Match answer `cannot glob non-string`: `verse charter --if` and `verse_charter` say so, and the task is answered `[]`, which reads as "nothing here matches". The fix shares the filter: `api.Filter` (Keep, Trim; Item, Apply), with a match that does not apply a misuse of `if` (api.FieldError). The CLI's filters and the stock tool both apply it, and the stock tool answers `not read: if: …`. A stock test covers if and trim on a charter.

2. `addrSegs` stands in both charterfile and cmd/verse/charterfile.go; the CLI uses charterfile's, exported as `AddrSegs`.
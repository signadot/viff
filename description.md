# the git iod's obsolete sweep never settles anything: the docket strips posedAt

The git iod's obsolete sweep (`io/gitobsolete.go:84`) reads `posedAt` from `GET /asks?waiting=me` to decide whether a question is older than its cutoff, and skips the question when `posedAt` doesn't parse. The docket strips `posedAt` (and `status`) from every row (`versed/server.go:2710`, "structural: every pending question has them"), so against a real versed the sweep skips every question and never settles one as obsolete. That's live on staging.

Its test passes only because its fake verse returns `posedAt`, which the real docket doesn't.

**Fix:** the docket stops stripping. A docket row is the stored question with `servedBy` and `where` added and nothing removed. That fixes the sweep without touching it, makes the iod test's fake verse truthful as it stands, and is what the typed docket view planned under danxdqbwha9xs8jyv9n0 needs anyway: a view that embeds the record can't remove a key. A test holds the docket to keeping `posedAt` and `status`.

Found by the inventory of the ask record for the plan issue danxdqbwha9xs8jyv9n0.
# runnerd: Carry cuts verse's answer at 4 MiB and hands the task the cut

`Guard.Carry` (runner/runnerd/verseauth.go) reads the answer with `io.ReadAll(io.LimitReader(resp.Body, 4<<20))` and drops the error. An answer over the cap (a `verse-data-get` of a wide container, a long ask listing) comes back as a 200 with a document cut mid-way. The tool's parse then fails with "the verse's answer does not read", blaming verse, and a document cut at the wrong byte can read as a shorter one with no error at all.

Read one byte past the cap. An answer over it is an error naming the cap, not a document.
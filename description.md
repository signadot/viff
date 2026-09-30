# residual: charter-print — a whole-verse round trip is refused

Carried out of a separate (since-closed) issue when verse/charter-print landed on main (6f22afe1).

`verse c -d out && verse c -a -d out` is refused on any real verse. Baseline holds the system's charter, and installing it again as a user writes `verse.schedule.minutely`, which the system owns. Per package the round trip holds.

Candidate, not built: installing a definition identical to what stands is a no-op that needs no ownership. That would also let a repo sync skip unchanged files.
# charter ttl: a package charter stands for as long as its install says, then goes as `verse charter -r` takes it

Scott: the charter install work is coupled with a TTL for charters, not per rule; optional for every installer, a task's install included ("task-installed charter keeps spawning as the owner after the task is gone" is the intended use case).

The model:

- **`ttl:` is a key of a fielded charter**, beside `at:`, `reads:` and `sends:`: a duration (`72h`). Like `reads:` and `sends:` it is the package's declaration, kept on the declaration record (`verse:trigger:package:<address>`), and like them it is replaced whole by each fielded install: one with no `ttl:` stands until it is removed. The shorthand (bare rules at a path) declares nothing and leaves the expiry as it is.
- **The install writes `expires`**, its instant plus the ttl, on the same record. Installing again renews it. An install that is refused puts the record back as it was, expiry included.
- **At `expires` the package goes** as `verse charter -r <address>` takes it down: its rules, levels, schedules, outboxes and declaration, and nothing it wrote. Only the package at the address: a charter under it is a package of its own, with its own ttl or none.
- **Baseline has no ttl**, as it has no `reads:`: it is not removed whole. A scope's charter has none either: it goes with its scope.
- **Read back**, `verse charter` writes `ttl:` in the file, which installs back as written, and says the expiry instant as a note, as it says a held rule: state, not the author's text.
- **The timer is versed's**, armed from a watch on the declaration records, as the asks' expiries are (askttl.go), so a restart arms what stands.

Then the task's charter-install and charter-remove tools, in their own issue.
# runners: a runner is a principal verse verifies, decided at registration

A runner is whoever it says it is. Versed reaches it by url and sends no bearer; versed writes a task's reports as the runner's principal from what it mirrors off the stream; a `runner:` named in a header is admitted under every mode. Only people and iods are verified. This has cost nothing while a runner performed nothing on verse; it starts costing with the first verb a runner performs on verse (ENG-1299, 3gf9wqb3h12kr2ymtnn0), which is gated only if the performer is a principal verse's permissions can name.

The design (Notion, "Verified runners: who a runner is, decided at registration"; agreed with Daniel 2026-10-06, his reply to the review notes §3):

- The decision is made once, at registration: `verse runner add <name> <url> -subject <workload token subject>`. The registration records `subject`. Without it the runner is asserted, as today.
- One table at the door: a workload token (the issuer under `auth.iod`) whose subject a registration names is `runner:<identity>` under every mode; `whoami` says `by: runner`. A bare header naming a verified runner's principal is refused.
- The runner's door: `verse: {url, issuer, subject, audience, ca}` in its configuration; it refuses every request whose bearer does not verify to that subject. Versed presents its own workload token on every call, audience `runner`.
- The describe is the handshake: the runner answers `Verse-Runner-Token` to a caller it verified; versed checks it against the registration before recording the description. A verified registration whose runner does not verify takes no spawn.
- Deploy: a ServiceAccount and projected token per runner and for versed; `repopulate.sh` registers with `-subject`.

Out: a runner with no issuer (a minted token); renaming `auth.iod`.
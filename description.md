# proposal: perms — one principal for a person's accounts in external systems, kept in sync

**A rough proposal; nothing is built.**

Verse acts on answers and requests that arrive through external systems, such as Slack and GitHub. Each io has to turn "this external account" into "this verse principal". Today that is done ad hoc:
- the Slack io looks a person up by email (`users.lookupByEmail`, `userEmail`);
- the GitHub review work (jdvbwvja) would need "GitHub login → principal". GitHub often hides email addresses, so email lookup does not carry over.

Proposed: perms reflect external systems. A person is one principal, and their accounts elsewhere (GitHub login, Slack id, …) are recorded as theirs, verified rather than claimed. The accounts would be kept in sync by something like OAuth or OIDC, not by a map that a person or an io maintains. ios would then ask perms who an account is, rather than each keeping a lookup of its own.

## Open questions

- Where the linked accounts live: at `verse.for.<person>`, or a record of their own.
- How a link is verified and kept current: OAuth or OIDC per provider, or an adopter's SSO (amwvs9t2) as the source of truth.
- What an io does with an account nobody has linked. The proposed answer for now: its answers do not count.
- How this relates to grants that name people, and to taking a place named for somebody (b737wbpb).

Related: amwvs9t2 (plugging into an adopter's SSO), b737wbpb (a grant cannot map a name to a principal), v27eah17 (identity in the MCP user face), jdvbwvja (the GitHub review environment needs it).
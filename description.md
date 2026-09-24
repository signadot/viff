# environments: a named, durable directory surface (expose), its write half, and a verse-side environment record

From `docs/roadmap/expose.md`, removed with the roadmap; the note whole is
`git show fb75880d:docs/roadmap/expose.md`.

## The gap

`connect` fuses three things into one ephemeral session: federating a directory, a nonce scope
that dies with it, and a validator per file. Nothing lets a directory surface be NAMED, outlive
the session, and be programmed by charters that were not told a nonce.

## The proposal

**`verse expose <path> --name <name>`** federates a tree into a scope under a legible name
(whose, where, role), installs nothing and spawns nothing. Reactivity is a charter matching the
exposure's places, living as long as the exposure; `connect` becomes expose + a validator charter.
The exposure and its scope are two records coupled by convention — a scope may be a nonce, an
exposure name must be filterable.

**The write half.** A read federates by following a reference; a write mutates an external
system, so it is an outbound effect. Write-through: the content controller grows a patch path
for its slice, confined under the exposure's root, so read-your-writes holds and bytes never
enter the store. Closed alternatives: a staging overlay (warehouses pending bytes) and writes as
proposed diffs (breaks read-your-writes; fine as a review layer later).

**A task sees more than one surface.** `fsExec` today is federated-or-local, never both. A
task's context should be a table of named surfaces (exposures, and its own root) with one
current: bare paths resolve against it, `<name>:<path>` reaches another, no fallback between
them. `cd` moves the cursor within a declared set.

**An environment record.** `launch.Spec.Environment` now reaches the runner, which resolves it
against its own config (`km5mzagq`). This proposes a verse-side named record too — surfaces,
variables, a capability floor — with `spawn { environment: <name> }` binding a task to it.
Layering: possible (environment, by construction) ⊇ granted (grant, by refusal) ⊇ auto (vector,
by asking). Agent Sandbox's template / claim / instance split fits: expose registers a
definition, a spawn claims an instance, and a derived grant belongs to the claim.

## Open

Whether the verse-side record resolves into the runner-side one or stays a second vocabulary;
naming and uniqueness of exposures; who reclaims a stale one (the scopes issue); write authority
and conflict policy; `cd` as operator control, agent tool, or both; floor inheritance across
`call_agent` (else delegation escalates); variables vs secrets (`tzaw7prf`, `dxe2txpg`);
qualified-path syntax.

Prior art the note surveyed: Agent Sandbox (`kubernetes-sigs/agent-sandbox`), Agent Substrate,
MCP roots (advisory, where a surface is load-bearing).
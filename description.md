# authority: nothing produces a grant — derive the envelope from the request at the call boundary

From `docs/roadmap/authority.md`, removed with the roadmap; the note whole is
`git show fb75880d:docs/roadmap/authority.md`.

## What is built

The OVERRIDE half (`task/authority.go`): a spawn may declare `vector:` (`auto|ask|park`) and
`grant:` (`read-only|no-delegate|none|all`), both resolved by name on the engine side, an unknown
name fails the spawn, and `Grant.Permits` is checked per action ahead of the vector. `task run
--vector` exists.

## The gap

Nothing PRODUCES a grant. A root task that declares none starts on a wildcard
(`Grant{Allow: []GateMatch{{}}}`), so the soft per-action gate does the hard axis's job:

    verse task run "go test ./..."
    #   ⏸ gate "shell" (run_bash) — approve?

The caller authorized that command by typing it; the gate re-asks it.

## The proposal: derive the envelope at the call boundary

Authority enters with the request; a gate asks only about what its answerer has not already
authorized. At spawn — once, over text the caller wrote, before the agent has said anything —
turn the request into a `Grant`, install it, and let the loop gate only what exceeds it. An
envelope fixed at spawn can be narrowed by delegation (`Grant.Subset`) or widened only by a new
call-boundary crossing, which is a human act.

- **Stage 1, register, no model.** A request that is a command the caller could have run is a
  command: authorize it, gate nothing for realizing it. Doubt → instruction.
- **Stage 2, a derived envelope.** An instruction's envelope as `GateMatch` predicates ("exec
  `go test ./...`, read and write under root"); the loop gates escalation only. Fail closed: no
  confident envelope → a narrow one, and the gate asks, which is today's behaviour.

Inside the envelope an action resolves `auto`; outside it is refused, not asked.

## Constraints

- **A bound prompt is attacker-influenced.** A trigger's prompt is a template whose `$vars`
  expand from a payload, so derivation runs over the authored template and its author, never
  bound values. Until then trigger spawns take a declared grant only (what shipped).
- **Over-authorization is silent**, so the installed envelope must be on the task's record —
  `e3b4qmac`.
- A derived grant belongs to the spawn, not to an environment (see the environment issue), or
  environments stop being shareable.

## Open

- Does a `say` widen the envelope, or is widening a distinct, attributed act?
- Out-of-envelope with nobody attached: fail fast rather than park?
- Escalation needs predicate subsumption; `Grant.Subset` is identity-or-wildcard.
- Is the envelope the task's or the (requester, request) pair's? The requester is now a
  principal the store checks.
- `Task.scope` (`task/loop.go`) still means authority; it wants `Grant`'s name.

Prior art the note surveyed: the confused deputy; on-behalf-of vs autonomous delegation (this is
neither — the envelope derives from what was asked, not what the user may do); AuthZEN's PEP/PDP
split and its approval and MCP tool-authorization profiles; Google's Agent Executor at `ace03604`
(manual suspend, no parent link, no narrowing).
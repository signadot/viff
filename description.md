# channels: somewhere named and persistent for agents and people to talk, between a say and a gate

From `docs/roadmap/channel-observe.md`, removed with the roadmap; the note whole is
`git show fb75880d:docs/roadmap/channel-observe.md`. Status there: idea, not designed.

## The idea

Agents need somewhere to talk — to agents and people, over spans longer than a task — that
outlives the talking. A git issue is the working example: named, persistent, several parties
append over time, anyone picks it up later.

Verse has near misses on each side and nothing in the middle:

- `say` — conversational, carries `By`, but lives on a live task's read plane and dies with it.
- a gate question — multi-party and durable, but a governed decision, closed once answered.
- a node — named and durable, but last-write-wins state, not a thread.

## Directions

- **Federated first.** The thread lives in its own system (an issue, a PR, a Slack thread) and
  verse gives it an address and serves content on demand, as `connect` does for a tree. A native
  channel is probably needed eventually for agent-to-agent talk; better as a channel that happens
  to be local than a second mechanism.
- **A thread may already be history.** A post as a write under a precondition, a read as a
  `Since` from a cursor — but `Since` can answer `ok=false` below the retention window, and a
  durable channel cannot sit on a window that silently closes.
- **Subscription is a rule.** If a channel is a node, "wake me on a post" is a condition over it.
- `flow show` could hang channels off tasks, giving a communication graph. Distinct from the
  control graph (who read and wrote what), which is a different relation.

## Open

Where a channel's path sits, and whether federated and native share it; "may post" as an axis
(`task.Grant` is tool-shaped); addressees and turn-taking; retention and closure. Related:
`1yhjy61p` (Slack as a conversation), `0p5tgch7` (tracing a flow).
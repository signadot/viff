# A client can watch the charter tree

A client that shows charters, such as PR #4's `verse tui`, can only poll today. It re-reads the whole `/charter?depth=-1` tree every 2s, once per open screen. Charters change rarely, so nearly all of those reads return what the client already has. On a shared verse that is load on logd for nothing.

Tasks and questions don't have this problem. A client can watch `verse.task.instance.*` and `verse.ask.*` and re-read what changed. Charters can't be done the same way, for two reasons:

1. **Their records can't be watched by an ordinary user.** A package's rules, levels and read declarations live under `verse.trigger.*`, and its schedules under `verse.schedule`. A user without a system grant is refused a read at `verse.trigger`, so a `/watch` there is refused too. `/charter` answers that user anyway, because the server reads on their behalf.
2. **Part of the tree is not records.** `charterTree` (`internal/versed/charter.go`) marks the rules it lists with engine state:
   - `markHeldRules` (from `eng.Pendings()`): a rule is pending and what it waits for.
   - `markSuspendedRules`.
   - `markTroubledRules`.
   
   A rule can change between these states without any store write, so even a watch with full access would miss those changes.

## Proposal

The server provides a stream for the charter tree. A client gets a frame whenever what `/charter` would answer at an address changes. The frame says which address changed, and the client re-reads `/charter` there. The tree stays the server's, so nothing the client shows is computed anywhere else.

Changes come from two sources:
- writes under `verse.trigger` and `verse.schedule`;
- the engine's own transitions: held to armed, suspended, troubled.

Authority is the same as reading `/charter`. The stream needs no grant on `verse.trigger`.

Open: is this a route of its own (`GET /charter/watch?at=…`), or does `/watch` learn a charter address space?

## Done when

- A client watching at an address gets a frame when a charter there is installed, replaced or removed, when a schedule under it changes, and when a rule under it goes pending → armed, is suspended, or becomes troubled.
- A user who can read `/charter` but not `verse.trigger` can watch.
- A test covers the case with no store write: the engine moves a rule from pending to armed, and the watcher gets a frame.
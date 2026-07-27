# Roadmap

Design notes for work that is **not built**. Nothing in this directory describes code you
can run; each note records a gap, a direction, and the reasoning that has survived so far.
For what verse does today, go back up to [the docs index](../README.md).

A note lands here when its subject does not exist in the tree — not when it is merely
incomplete. A doc about something that runs, with a corner missing, stays in `docs/` and
says which corner.

## The two threads

Most of what follows belongs to one of two strands, and the strands cross.

### Authority and the sandbox

Where a process's permission comes from, and what a process can see when it runs.

- [Authority](authority.md) — a process's hard envelope (`process.Grant`) is fully wired
  and never produced: every process starts on a wildcard, so the soft per-action gate is
  doing the hard axis's job. The proposal derives the envelope from the request, at the
  call boundary, where the request still exists as text.
- [expose](expose.md) — `connect` fuses a directory surface, a scope lifetime, and a
  validator charter into one ephemeral session. `expose` names the surface and lets it
  outlive the session, which turns out to be the missing referent for
  `launch.Spec.Environment` and leads to a sandbox record: surfaces, variables, and a
  capability floor.

These two are the same seam from opposite ends. `launch.Spec` already declares three axes —
`Policy` (the soft dial), `Grant` (the hard envelope), `Environment` (the floor) — and only
the first has any consumer. Read the layering as *possible ⊇ granted ⊇ auto*: the floor is
enforced by construction, the grant by refusal, the vector by asking.

### Federation: the write half, and the lifetime

Reading through the verse is built. Writing through it, and the lifecycle of the isolation
it happens in, are not.

- [Write-back](write-back.md) — `read_file` federates cleanly because a read is a pure
  follow-the-reference. A write mutates an external system, so it is an outbound effect
  rather than a read run backwards. The recommendation is a read-write content controller
  that writes through to the owning tree, gated.
- [Scope lifecycle](scope-lifecycle.md) — any write carrying `--scope` creates an overlay,
  but only `verse connect` ever deletes one, and only its own. Creation is universal and
  implicit; teardown is specific and manual. The direction is to record a scope as an
  entity on first scoped write, and put a `verse scope` surface over it.

`expose` sits across this thread too: a named exposure is what write-back was missing —
a definite target with an authority model.

## Further out

- [Named persistent channels](channel-observe.md) — an idea rather than a design. Agents
  need somewhere to talk that outlives any one process. A `say` is conversational but dies
  with its process; a motion is durable but is a governed decision, not a conversation; an
  entity is named and durable but is state, not a thread. The gap is the middle of those
  three.

## Rough order

Dependencies, not dates.

1. **Scope lifecycle** stands alone and unblocks the rest by making isolation a thing you
   can name, list, and reclaim.
2. **expose** needs a durable scope to hold its files, and produces the named surface the
   other two want.
3. **Write-back** needs a definite target and an authority model — which is an exposure.
4. **Authority** can start any time on the CLI side (the call boundary is already there),
   but its `Environment` half wants the sandbox record `expose` arrives at.
5. **Channels** are undesigned, and the note argues its named-durable-handle machinery is
   probably `expose`'s — worth building in an order that finds out.

## What is not here

Two categories deliberately stay in `docs/`:

- Notes about work that landed, kept for the reasoning — [external
  processes](../external-processes.md) chose the seam that
  [process control](../process-control.md) implements.
- Notes about built things with a stated gap — [process control](../process-control.md)
  still owes an inbox long-poll, and [sources](../sources.md) has a git Source that no
  command yet exposes. A gap inside something that runs is recorded where it runs.

# Roadmap

Design notes for work that is not built. Nothing in this directory describes code you
can run; each note records a gap, a direction, and the reasoning that has survived so far.
For what verse does today, go back up to [the docs index](../README.md).

This directory is meant to be read front to back, as one argument. That is what
distinguishes it from [working/](../working/README.md), which holds undone design too: a
roadmap note has been reconciled with the others — it agrees with them, cross-references
instead of repeating, and has a place in the ordering under [the two
threads](#the-two-threads). A note arrives here by being synthesized into that whole,
which usually means editing an existing note rather than adding a file.

Not existing yet is necessary but not sufficient. An idea, a half-design, or an argument
still finding its shape belongs in `working/` until someone does the reconciling. A doc
about something that runs, with a corner missing, stays in `docs/` and says which corner.

## The two threads

Most of what follows belongs to one of two strands, and the strands cross.

### Authority and the sandbox

Where a task's permission comes from, and what a task can see when it runs.

- [Authority](authority.md) — a task's hard envelope (`task.Grant`) is fully wired
  and never produced: every task starts on a wildcard, so the soft per-action gate is
  doing the hard axis's job. The proposal derives the envelope from the request, at the
  call boundary, where the request still exists as text.
- [expose](expose.md) — `connect` fuses a directory surface, a scope lifetime, and a
  validator charter into one ephemeral session. `expose` names the surface and lets it
  outlive the session, which turns out to be the missing referent for
  `launch.Spec.Environment` and leads to a sandbox record: surfaces, variables, and a
  capability floor.

These two are the same seam from opposite ends. `launch.Spec` already declares three axes —
`Vector` (the soft dial), `Grant` (the hard envelope), `Environment` (the floor) — and only
the first has any consumer. Read the layering as *possible ⊇ granted ⊇ auto*: the floor is
enforced by construction, the grant by refusal, the vector by asking.

### Federation: the write half, and the lifetime

Reading through the verse is built. Writing through it, and the lifecycle of the isolation
it happens in, are not.

- [The write half of expose](expose.md#the-write-half) — `read_file` federates cleanly
  because a read is a pure follow-the-reference. A write mutates an external system, so it
  is an outbound effect rather than a read run backwards, and it needs a definite target,
  which is what a named exposure supplies. Write-through to the owning tree, gated.
- [Scope lifecycle](scope-lifecycle.md) — any write carrying `--scope` creates an overlay,
  but only `verse connect` ever deletes one, and only its own. Creation is universal and
  implicit; teardown is specific and manual. The direction is to record a scope as an
  entity on first scoped write, and put a `verse scope` surface over it.

## Further out

Two ideas rather than designs.

- [Named persistent channels](channel-observe.md) — agents need somewhere to talk that
  outlives any one task. A `say` is conversational but dies with its task; a motion is
  durable but is a governed decision, not a conversation; an entity is named and durable but
  is state, not a thread. The gap is the middle of those three.
- [A charter authored per change](dynamic-charter.md) — a prompt and a diff spawn an agent
  that writes the rules for *that* change, which the engine installs and runs. The substrate
  is mostly there — a charter is data, a goal already owns and reaps a bundle of triggers,
  a scope isolates them. What is not there is any account of governing an agent that writes
  the rules the system will then run on itself.

## Rough order

Dependencies, not dates.

1. Scope lifecycle stands alone and unblocks the rest by making isolation a thing you
   can name, list, and reclaim.
2. expose needs a durable scope to hold its files, and produces the named surface
   everything else wants — including its own write half, which needs a definite target.
3. Authority can start any time on the CLI side (the call boundary is already there),
   but its `Environment` half wants the sandbox record `expose` arrives at.
4. Channels are undesigned, and the note argues its named-durable-handle machinery is
   probably `expose`'s — worth building in an order that finds out.
5. A per-change charter is last, and not for want of substrate: it is the one idea here
   that needs authority settled first, since it has an agent writing rules rather than
   taking actions.

## What is not here

Two categories deliberately stay in `docs/`:

- Notes about work that landed, kept for the reasoning — [external
  tasks](../external-tasks.md) chose the seam that
  [task control](../task-control.md) implements.
- Notes about built things with a stated gap — [task control](../task-control.md)
  still owes an inbox long-poll, and [sources](../sources.md) cannot yet be reached by a
  source verse must not dial. A gap inside something that runs is recorded where it runs.

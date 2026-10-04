# verse documentation

Verse is a small runtime for governing SDLC work across systems it does not own, on a dial of
agentic autonomy. You install a *charter* of trigger rules; the runtime reacts to changes in a
shared entity store and does the work with governed agent tasks, and every reaction passes a
gate.

This site is for somebody who uses verse: writes charters, runs tasks, and answers gates.
Running a verse, extending it with runners and ios, and how it is built are documented in the
repository: see [the platform docs](platform.md).

## Start here

Read these in order.

1. [Overview](overview.md) — what verse is, and the model.
2. [Tutorial](tutorial.md) — entities, tasks, and answering the questions a gate asks. Each
   section seeds itself with one command. It runs on your own machine and needs no credentials.
3. [Control](control.md) — how to stop work without ending it. Read it before you let verse
   act on anything that matters.
4. [Write a charter](build.md) — the smallest rule that works, with every step.

[The glossary](glossary.md) defines each term once. Use it whenever a page uses a word you do
not know.

## Use verse

- [Commands](reference/README.md) — every command, and a file per topic.
- [Tasks](reference/tasks.md) — running a task by hand, and answering its gates.
- [Asking](asking.md) — `verse ask`: a question to somebody, with a date, and what follows a
  yes run on your authority.
- [Permissions, walked](perms-guide.md) — your own entities, a task and its gates, a charter
  and what it owns, a gate with nobody behind it, a gate with a resolver.
- [Connect and scopes](reference/connect.md) — reflecting your working tree into a scope.

## Write a charter

For a charter author: the rules, what they read, and what they may write. Start with
[Write a charter](build.md#write-a-charter), which writes one rule, installs it and fires it.
The pages below say what each part means.

- [Introduction](charter-intro.md) — the shapes a rule takes (patch, latch, ask, seq, when,
  on, derive), each installed and fired by one command.
- [The trigger model](trigger-model.md) — the model of a rule. The fields of a rule are in
  the five pages below.
- [The condition of a rule](reference/condition.md) — `subject`, `on` and `when`, and what a
  match can say.
- [The action of a rule](reference/action.md) — `patch`, `derive`, `spawn`, `calls` and `seq`,
  and what an action reads.
- [The gates of a rule](reference/gates.md) — what a gate asks, who answers it, and how it
  settles.
- [The charter](reference/charter.md) — packages, what a package reads and writes, and how a
  charter is stored.
- [Schedules](reference/schedules.md) — schedules and their ticks, which make time a cause.
- [Entities](reference/entities.md) — addresses, reads, writes and watches.
- [Strata](strata.md) — a declared level ordering, and `derive`.
- [Convergence](convergence.md) — what ends a firing, and what is checked at install.
- [Provider selection](provider-selection.md) — how a doer is chosen for a spawn.
- [Permissions](perms.md) — one owner per node, and grants for everybody else. The detail is
  [reference/perms.md](reference/perms.md).
- [An automatic supervisor](auto-supervisor.md) — a worked charter where an agent decides,
  and promotes what is not its to decide.
- [Representation](reference/representation.md) — tony, JSON and YAML: what verse reads and
  what it writes.

## Elsewhere

- [tony](https://signadot.github.io/tony-format/) — the document language a charter is
  written in, and [docd](https://signadot.github.io/tony-format/docd/), the store behind a
  durable verse.
- [go-tony](https://pkg.go.dev/github.com/signadot/tony-format/go-tony) on pkg.go.dev —
  including [`ir/kpath`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath)
  (the addressing a task chain uses),
  [`mergeop`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop) (the patch
  and match operators), and
  [`system/libctl`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/system/libctl)
  (the controller `Handler`).

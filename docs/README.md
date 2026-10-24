# Verse

Verse is a shared space where a team's software delivery work runs: agents, scripts and people
in one verse, and the team decides how much of the work runs without asking.

Verse watches the systems the work lives in: a git repository, GitHub, Slack, Linear, an issue
tracker. When something there changes, verse does what your rules say. It starts an agent on a
checkout, opens a pull request, posts a message, or labels an issue. Before a step that matters,
it asks the people the rule names, and nothing happens until they answer.

Verse is not a programming language, and it is not an agent. It is a small runtime. It
coordinates the agents and tools you already have, under rules you write, and it keeps a record
of every decision. The rules are the same for one agent or for many.

One verse is shared by a team or an organization. Everything in it is done under a user's
identity: a write, a task, an answer, each says who. A question goes to the people it names,
and they answer from wherever they are: the CLI, Slack, a pull request. A task one person
started is visible to everyone who may read it, and anyone with the permission steers it. Verse
is not a personal automation, and no one person is its orchestrator.

## [Basic concepts](concepts.md)

Four terms, the loop they run in, and the model behind them. Read this first.

## [Tutorial](tutorial/README.md)

The four parts of verse, a page each: data, questions, tasks, and the charters that put them
together. It runs on a verse on your own machine, standing in for the shared one.

## [Verse as groundskeeper](groundskeeper.md)

Verse looks after a repository on its own, from charters kept in the repository.

## [Systems](systems/README.md)

The systems verse reads and acts on, a page each: what a charter sees, what a task can do, and
where the credential lives.

## [Control](control.md)

How to stop work without ending it.

## [Glossary](glossary.md)

Each term, defined once. Use it when a page uses a word you do not know.

## [Comparison](comparison.md)

Verse next to GitHub Actions, Temporal and an agent framework, for a reader who knows one of
them.

## [Reference](reference/README.md)

Every command, and a page for each part of a charter.

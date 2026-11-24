# Tutorial

Viff has four parts: data, questions, tasks and charters. This tutorial has one page for each,
and a second page of charters. The pages go from the simplest part to the one that uses the
other three.

Each part is data to the next. A question is data, and so is a task. A charter is rules over
data, and the rules ask questions and start tasks. Once you know the four, you can write
charters for your own work.

A real deployment adds more data, agents and runners. [Iods](../io.md) connect viff to a
repository, a forge or a chat workspace. An iod writes what the system holds into the store, and
carries what a rule writes back out to the system: a comment, a tag, a message.
[Runners](../runners.md) do the work of tasks. An operator installs iods and runners for you.
The charters are yours.

Every command in the tutorial runs on a viff on your own machine. It stands in for the shared viff
a team runs: there, the people an ask names are your colleagues, they answer from their own machines
or from Slack, and what you start they can see and steer. Here you are everyone, and `--as` lets you
answer as them. Each page sets up the data it reads. The few runs that go to an agent need a model
key, and each page says which. The [basic concepts](../concepts.md) cover the same ground in
summary.

## Start

You need Go 1.27.1 or later, and this repository checked out.

```sh
go build -o viff ./cmd/viff
export PATH="$PWD:$PATH"          # so that `viff` is this build, in every terminal you use
export VIFF_ADDR=localhost:9797  # where clients reach it (this is the default)
viff up                          # engine + task runtime + store, in one process
```

Leave `viff up` running. Every other subcommand is a client. Open a second terminal at the
root of the checkout, with the same `PATH` and `VIFF_ADDR`.

On a viff on your own machine, the CLI acts as `user:<login>@localhost`, and that user owns
what you write. [The permissions walk](../perms-guide.md#before-the-first-step-who-you-are) shows
how to name yourself.

`up` chooses a store, a model and a root directory for agents, and the defaults are fine for the
tutorial. An agent works in its own temporary directory, so a file it writes is not in your
current directory. [reference/up.md](../reference/up.md) has the flags.

Then start with [data](data.md).

## [Data](data.md)

[Data](../glossary.md#data): what viff knows about the world, each document at an address.
You read it, write it and watch it change.

## [Questions](questions.md)

[Asks](../glossary.md#ask): a question put to the people who may answer it, with what a yes runs
and what silence means.

## [Tasks](tasks.md)

Work done in the world by an agent or a script. You watch it and steer it, and it asks before
what it may not do alone.

## [Charters](charters.md)

[Rules](../glossary.md#rule): when the data looks like this, ask this, and do that.

## [Charters, continued](charters-2.md)

The rest of what a rule can say: the backlog, the latch, steps in order, a cause rather than a
condition, a charter in a file, why a rule did nothing, and stopping a rule.

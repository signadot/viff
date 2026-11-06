# delivery: a table of ops, a request that is its step, and a deadline per op

**Status:** in work, in the worktree `.worktrees/delivery-channel` (branch `verse/delivery-channel`).

This is the groundwork that 1f5art3x asks for first: items 1, 2 and 5 of its section "Performed
systems: what to shore up first". It adds no system and changes no act. It changes how the
runner's side of the delivery channel (`pkg/runner/runnerd/delivery`) is put together, so that
`mcp`, `pkg-nix` and `secret` can be added without each one remembering the rules.

## What is wrong today

1. **The check is each handler's to remember.** `answer` (`server.go`) switches on the op and
   calls a handler. Each handler builds its step and calls `refused` itself, in 12 places. A new
   handler that forgets is not gated on the runner's side.
2. **The request is a struct with a field for each system** (`protocol.go`: `Channel`, `Emoji`,
   `Team`, `Issue`, …). Each handler puts the step's fields together by hand from them. The
   task's side builds the same step by hand too. Nothing holds the two to the same step. A new
   system adds fields to the struct.
3. **One deadline, one act at a time.** `answer` gives every op `workspace.ForgeTimeout`, and
   `Serve` answers one request before it reads the next. An MCP call, an install and a command
   do not fit that.

## What it becomes

1. **A table of ops.** An op is one value: its name, how it reads its request into the steps it
   asks for, its deadline, and its act. `answer` looks the op up, has it name its steps, checks
   every step with `refused`, and only then calls the act. No act runs unchecked, by
   construction. An act of several steps needs nothing more.
2. **A request that is its step.** A request is an op, the decision's id, the step's fields by
   name, and a body. The fields are read by the system's document (`pkg/systems`), which already
   lists each verb's fields. An op may still normalise a field before the check (a channel's
   name, a repository's canonical name).
3. **A deadline per op, and requests that do not wait on each other.** Each op names its
   deadline. A request carries an id, a reply carries the same id, and the server answers each
   request as it is done.

## The plan

1. Read every op and write down, for each: its steps, the fields of each, what it normalises,
   and what it refuses before the check. This is the table's content, and the test's.
2. The table and the new `answer`, with the handlers moved under it one system at a time. The
   wire stays as it is in this step, so every existing test still says the same thing.
3. A conformance test over the table: no op acts under an empty environment, and every op in
   the table is a verb some document describes (or is named as an exception, as `fetch` and
   the branch's state are).
4. The general request on the wire. The task's side (`client.go`, and whoever builds a
   `Request`) moves with it.
5. A deadline per op.
6. Request ids, and a server that answers out of order. The client matches replies by id.

Each step is a commit that builds and passes on its own.

## What does not change

- What any act does, and what any step is named.
- `refused`: the environment, then the autonomy and the relayed answer, then the ceiling.
- The frame: a length and a tony document, at most 1 MiB.

## Not in this issue

- Binding an answer to a digest of the body (item 3 of the list in 1f5art3x).
- Registering a performed system as one value in `stock/config.go` (item 4).
- A system's kind in its document (item 6).
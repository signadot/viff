# delivery: a table of ops, a request that is its step, and a deadline per op

**Status:** written, in the worktree `.worktrees/delivery-channel` (branch `verse/delivery-channel`).
Not committed. The runner's tests pass, and the delivery package passes under the race detector.

This is the groundwork that 1f5art3x asks for first: items 1, 2 and 5 of its section "Performed
systems: what to shore up first". It adds no system and no act. It changes how the runner's side
of the delivery channel (`pkg/runner/runnerd/delivery`) is put together, so that `mcp`,
`pkg-nix` and `secret` can be added without each one remembering the rules.

## What was wrong

1. **The check was each handler's to remember.** `answer` (`server.go`) switched on the op and
   called a handler. Each handler built its step and called `refused` itself, in 12 places. A
   new handler that forgot was not gated on the runner's side.
2. **The request was a struct with a field for each system** (`protocol.go`: `Channel`, `Emoji`,
   `Team`, `Issue`, …). A new system added fields to it.
3. **One deadline, one act at a time.** `answer` gave every op `workspace.ForgeTimeout`, and
   `Serve` answered one request before it read the next. An MCP call, an install and a command
   do not fit that.

## What it is now

1. **A table of ops** (`ops.go`). An op is one value: the steps it may ask about, the names it
   reads of a request, its deadline, and `ask`. An op performs nothing itself. `ask` reads the
   request into the steps it asks about and the act that follows them. `Server.perform` checks
   every step with `refused`, and only then calls the act. An op that answers OK with no act is
   answered as a bug, unless the table says why it asks about nothing (`unasked`). One op says
   so: `state`.
2. **A request is an op, what it is asked with by name, and a body** (`Request.Fields`,
   `Request.Body`). An op says which names it reads (`takes`). A request that names another is
   refused, and nothing is read of it. A new system adds ops to the table and nothing to the
   protocol.
3. **A step carries only fields its document describes.** `perform` refuses one that does not,
   as a bug. So the runner's step is held to the document that the task's side is held to
   (`systems.Check`).
4. **A deadline per op.** Each op names its own. Every op of today has `workspace.ForgeTimeout`,
   as before.
5. **Requests are named, and answered as each is done.** A request carries an id and its answer
   carries the same. The client (`client.go`) matches answers by id, so several may ask at once.
   Requests are performed in turn, in the order the task asked, as before. An op the table marks
   `apart` waits for none of them and holds none. No op of today is apart. When the task's end
   is gone, a request in turn is finished (as `Settle` needs), and one apart is given up. The
   runner holds at most 32 of a task's requests at once.

## What it found

- **The Slack tool sent `limit` with every verb.** The runner dropped it for a post, a reply
  and a reaction. The tool now sends each verb what its op reads
  (`pkg/runner/runnerd/stock/slacktools.go`).

## Tests

- `TestNoOpActsUnderAnEmptyEnvironment`: every op of the table, on a server that holds every
  credential under open ceilings, is refused as outside an empty environment. A call that
  reaches a service fails the test. An op added to the table with no request in the test's
  list fails it.
- `TestEveryOpAsksAboutADocumentedStep`: each op's steps are verbs a document describes, each
  has a deadline, and only `state` asks about nothing.
- `TestAnActFollowsEveryStepItAskedAbout`: `perform` on its own, with ops that break each rule.
- `TestARequestNamesOnlyWhatItsOpReads`: a name an op does not read is refused; a document of
  several lines crosses in a field.
- `TestAnOpApartDoesNotWaitForThoseInTurn`: the order in turn, an op apart answered meanwhile,
  and what happens to each when the task's end goes.
- `TestAFrameTooLargeLeavesTheChannelInStep` reads the answer to a frame the client did not
  send on a channel with no client, since the client now reads its own answers.

## What does not change

- What any act does, what any step is named, and what any refusal says.
- `refused`: the environment, then the autonomy and the relayed answer, then the ceiling.
- The frame: a length and a tony document, at most 1 MiB.
- The order of what an op does before its check. A push still asks the forge for the branch's
  state first, since two of its step's fields are facts only the runner has.

## What it does not do

- **Both sides still build the step, each by hand.** The task's tool derives its step's fields,
  and the runner's op derives them again from the request. Both are now held to the document's
  field names. Their values are not held to each other. The verse system's verbs already share
  one function for this (`StepOf`); the others do not.
- **An act of several steps that a row asks about.** `perform` checks each step. A relayed
  answer is taken once, by its decision's id (`Channel.answered`). So the second step of such
  an act finds no answer. No op of today asks about more than one step. `secret.exec` will.
- **No op is refused before it reads anything.** An op whose step the environment does not
  give at all still does its reading first (a push asks the forge). `perform` could refuse it
  on the op's `steps` alone.

## Not in this issue

- Binding an answer to a digest of the body (item 3 of the list in 1f5art3x).
- Registering a performed system as one value in `stock/config.go` (item 4; it is in 1q5art3x).
- A system's kind in its document (item 6).

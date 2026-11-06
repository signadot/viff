# delivery: a table of ops, a request that is its step, and a deadline per op

**Status:** written, reviewed three times, and committed as 81250f61 on the branch
`verse/delivery-channel` (worktree `.worktrees/delivery-channel`), one commit over main at
55ba496d. Not merged to main and not pushed. The delivery package passes under the race
detector, and the runner and `cmd` suites pass.

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
   reads of a request, whether it reads a body, its deadline, and `ask`. An op performs nothing
   itself. `ask` reads the request into the steps it asks about and the act that follows them.
   `Server.perform` checks every step with `refused`, and only then calls the act. An op that
   answers OK with no act is answered as a bug, unless the table says why it asks about nothing
   (`unasked`). One op says so: `state`.
2. **A request is an op, what it is asked with by name, and a body** (`Request.Fields`,
   `Request.Body`). An op says which names it reads and how it reads each: as text (`takes`),
   as true or false (`flags`), as a number (`numbers`); and whether it reads a body. A request
   that names another, or carries a body to an op that reads none, is refused, and nothing is
   read of it. A flag that is neither `true` nor `false` is refused too, and a number that is
   not one (`misread`). An op that reads a name another way than its entry says, a flag as
   text, is answered as a bug (`misreading`): what was checked is then not what was read. Read
   so before its steps, the answer says nothing was performed. Read so in its act, the answer
   says whether it was performed is not known, since the act may have posted or pushed first.
   A name beside `fields`, at the top of the request, is refused as one within it is: the
   request is decoded strictly, and the refusal is sent under the request's id. A new system
   adds ops to the table and nothing to the protocol.
3. **A step carries only fields its document describes.** `perform` refuses one that does not,
   as a bug. So the runner's step is held to the document that the task's side is held to
   (`systems.Check`).
4. **A deadline per op.** Each op names its own. Every op of today has `workspace.ForgeTimeout`,
   as before.
5. **Requests are named, and performed in turn.** A request carries an id and its answer
   carries the same. The client (`client.go`) matches answers by id, so several may ask at once.
   Requests are performed in the order the task asked, as before. Their answers may arrive in
   another order: a turn passes once its request is performed, before its answer is sent (see
   7). An op the table marks `apart` waits for none of them and holds none. It is handed the
   task without its checkout and without the branches it made and pushed (`Task.apart`), so it
   cannot read what the ops in turn write. No op of today is apart.
6. **The runner holds at most 32 of a task's requests at once, and refuses the next.** The one
   refused hears that nothing was done and to ask again. The runner does not leave it unread:
   a loop that waited for room would not see the task's end go. A request's place is given back
   as its answer is about to be written, holding the write, so a task that asks again on
   hearing an answer finds room. One more than 32 may so be writing its answer.
7. **When the task's end goes, what was begun in turn is finished, and nothing else is.** The
   request being performed is finished, as `Settle` needs. A request with none ahead of it is
   begun as it is read, so a task that asks for a push and exits has its push. That holds for a
   request asked on hearing an answer too, since the turn has passed by the time the answer is
   sent. One still waiting its turn is not begun, and one apart is given up: the runner does
   not act for a task that is gone.
8. **An answer that does not read ends the channel** (the client). It could be the answer to
   any request in flight. Everyone waiting hears that what they asked may have been done, so a
   write is not made twice on a retry. However the client's reading ends, it closes its end, so
   the runner sees the task's side go.

## What it found

- **The Slack tool sent `limit` with every verb.** The runner dropped it for a post, a reply
  and a reaction.
- **A close carried a body.** The issue tools sent `body` with a close, at GitHub and at Linear,
  and the Slack tool sent the text with a read and a reaction. The runner dropped each. A model
  that closed an issue with a comment in `body` was told it had closed it.
- **An update of a pull request carried `draft`,** which an update does not change.

Each tool now sends what its op reads (`pkg/runner/runnerd/stock`). And a tool given something
it does not take refuses the call, rather than drop it and go on (`untaken`): a close given a
body, a reaction or a read given text, a post given a thread, an update given `draft: true`, a
comment on a pull request given a review comment's id (it was meant as a reply in the thread).
Such a call derives no fields, so no row is asked about it. What is given as nothing (empty,
false, zero) is let by. This is in the Slack tools, the GitHub and Linear issue verbs, the pull
request tools and `github-comment-pr`. The other tools are as they were.

## Tests

- `TestNoOpActsUnderAnEmptyEnvironment`: every op of the table, on a server that holds every
  credential under open ceilings, is refused as outside an empty environment. A call that
  reaches a service fails the test. An op added to the table with no request in the test's
  list fails it.
- `TestEveryOpAsksAboutADocumentedStep`: each op's steps are verbs a document describes, each
  has a deadline, only `state` asks about nothing, and no op reads a name more than one way.
- `TestAnActFollowsEveryStepItAskedAbout`: `perform` on its own, with ops that break each rule.
- `TestARequestNamesOnlyWhatItsOpReads`: a name an op does not read, a body it does not read, a
  flag and a number that are not one, each refused; a document of several lines crosses in a
  field.
- `TestANameBesideFieldsIsRefused`: a request written the old way, refused under its id.
- `TestAnOpReadsANameAsItsEntrySays`: an op that reads text as a flag, or a flag or a number as
  text, before its steps or in its act, answered as a bug, and what each answer says about
  whether anything was performed.
- `TestAnOpApartDoesNotWaitForThoseInTurn`: the order in turn, and an op apart answered
  meanwhile. `TestAnOpApartIsHandedNoCheckout`: what an op apart is handed.
- `TestARequestPastWhatTheRunnerHoldsIsRefused`: the thirty-third is refused under its id, the
  thirty-two are answered, and the next is then taken.
- `TestARequestAskedOnHearingAnAnswerFindsRoom`: 32 askers, each asking again on hearing its
  answer, in turn and apart, and none refused for want of room. Before the place was given
  back ahead of the write, about 3 in 64,000 were.
- `TestWhenTheTasksEndGoesOnlyWhatWasBegunIsFinished`,
  `TestARequestWithNoneAheadIsFinishedThoughTheEndGoes` and
  `TestARequestAskedOnHearingAnAnswerIsFinishedThoughTheEndGoes`: item 7. The last one guards
  a window that was narrow (a few runs in thousands), so it would catch the old code only
  some of the time.
- `TestAnAnswerThatDoesNotReadEndsTheChannel` and `TestTheClientClosesItsEndWhenReadingEnds`:
  item 8.
- `TestAFrameTooLargeLeavesTheChannelInStep` reads the answer to a frame the client did not
  send on a channel with no client, since the client now reads its own answers. It failed
  about 14 runs in 400 before: the test wrote a frame beside the client on one end. It writes
  it whole first now, and passes 400 runs under the race detector.
- `TestAToolRefusesWhatItDoesNotTake` (`stock`): each tool above, given what it does not take.

The tests that need other ops give the server a table of their own (`Server.table`). `answer`,
which most tests call, is a test's helper now: `Serve` is the one way in.

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
- **An answer that names no request is dropped by the client.** The runner gives one for a
  frame too large, where it read no id. The client refuses such a request before it sends it,
  so only another client meets this.
- **A tool's refusal of what it does not take is per tool.** The tools named above call
  `untaken`. The place that calls every tool does not, so the rest still drop what they are
  given and do not take. Two more of the kind are known and left: `slack-read` given a `limit`
  that is no number reads the default, and `create-pr` given a `draft` that is neither true
  nor false opens it ready. One check against a tool's `Params`, where every tool is called,
  would cover all of them, and touches every tool of the system.
- **The refusal past 32 is an error to the tool.** `relay` gives the task's loop an error for
  it, and the verse executor and a script fail the task on a tool's error. A script asks one
  at a time, so it does not reach 32. The verse executor was not traced.

## Not in this issue

- The Slack tools' request names no decision: 1z5art3x.
- Binding an answer to a digest of the body (item 3 of the list in 1f5art3x).
- Registering a performed system as one value in `stock/config.go` (item 4; it is in 1q5art3x).
- A system's kind in its document (item 6).

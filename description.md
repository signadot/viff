# ask: a task hands the turn to a person as a question (plan)

Status: built on `verse/ask`, including the runtime-raised turn. Re-cut
around `ask` after a second review; the say pipe it replaces, and why, is at the end.

An `ask` tool lets a task hand the turn to a person. The model calls it with what it wants to say
or know; the task blocks on a `gate.question` record until somebody answers; the answer's text is
the tool result the model reads next. A conversation is a run of asks, and it ends when the model
stops without asking. Nothing new waits in the loop: an `ask` is an action blocked at a question,
which is the wait the loop already has.

## Why this shape

- **The wait already exists.** `Registry.Resolve` (`task/registry.go`) posts a question to the
  docket, blocks until it is answered, and withdraws it when the context ends. A worker on another
  runner raises the same record with `verse task do gate` (`926f6c2a`). `ask` is that path called
  from a harness tool.
- **Everything that answers a gate answers an ask.** The docket, `verse gate`, the attach prompt
  (`promptGate`), Slack, a resolver, a developer's own Claude session through verse's tools
  (`js1e345a`): each consumes the record `ask` produces. A task waiting on a person is visible in
  all of them, and answerable from any.
- **Restart, cancel and deadline come with the record.** A cancel withdraws the question
  (`OnDrop` → `mirror.DropQuestion`); a restart orphans it cleanly
  (`TestARestartTakesADeadTasksQuestionWithIt`); `GateQuestion.Deadline` bounds how long it
  stands, per question, with the docket showing the clock.
- **The turn is on the record.** Each ask settles as a question with who answered and what they
  said. That is the thread `n85fbhrj` (channels) wants, produced by the mechanism that exists.
- **It runs everywhere the harness does.** The coderunner's llm driver is `harness.Build` with the
  runner's registry; `ask` there reaches versed through the runner's question relay like any gate.
  For a Claude Code session the same turn is `AskUserQuestion` seen through hooks; see `js1e345a`.

## Order of work

Five steps. Each leaves `make check` and the suite green. Step 0 is a bug fix on its own.

### 0. `task run -attach` reads from the start

`task run -attach` subscribes live (no `since`: `subscribeAt`, `versed/server.go`; taskd `events`),
and a live `Subscribe` replays nothing (`task/event.go`), so frames sent before the stream opens
are lost: a run printed from cursor 2. The task was just spawned by this command, so ask for
`since=0`. Also what makes a `pending` frame raised before the attach connects visible.

Proves: a fast task's first frame is printed by `task run -attach`.

### 1. The `ask` tool in the harness

- A `Tool` in `task/harness` (`build.go`'s `Tool`, beside the runtime's own): `ask` with `question`
  (required) and `options` (optional: named outcomes, as a worker's `--options`). `Do` builds a
  `GateQuestion{ID: <task>~ask<n>, Origin: <task>, Gate: "ask", Reason: question, Options}` — the
  id carries `~` after the task id, which is how an answer finds its owner — with `reply` as the
  one option when the model named none, and calls `cfg.Resolver.Resolve(ctx, q, tu)`. The result
  is `<by>: <text>` for `reply`, or the chosen option's id; the tool result is what the model
  reads, so the answer needs no folding of its own.
- A cancel ends the wait through `ctx` and the question leaves the docket; that is the executor
  honouring its context, which the `Executor` contract requires.
- **The vector must not ask about asking.** `Vector.Evaluate` applies its default to an action no
  gate matched (`task/vector.go`), so under `vector: ask` the call would be gated before it asks.
  `ask` derives to its own verb, and the supervisor treats an action whose only effect is that
  verb as `auto`: a question is the vector's instrument, not an action it governs.
- The `pending` frame reaches the read plane and the docket with no new wiring: taskd registers
  `reg.OnPost` on every hosted task (`task/taskd/daemon.go:221`), which emits `EvPending` and posts
  the record, so attach prompts on it and a re-attach replays it.
- Offered to children as the runtime's tools are, so a sub-agent's ask carries its own `Origin`.

Proves: an ask blocks the task and lists on the docket; an answer's text is the tool result; a
cancel withdraws it; under `vector: ask` no second question is posed; a deadline settles it.

### 2. Answer text on the wire

Already there: `api.Resolve` carries `say` for `POST /tasks/{id}/resolve` as for `/gate/resolve`,
and the answer path folds it into `Resolution.Steer`. Only the CLI's `resolveTaskGate` lacked it,
which step 3 adds. No commit of its own.

### 3. The attach prompt for a question with a `reply` option

- `promptGate` (`cmd/verse/task.go`) prompts free text for a question whose options are `reply`
  (plus any named ones): the model's `question` is printed, then `> `. Named options keep the
  numbered menu, with `reply` as "answer in words".
- **Line discipline.** One stdin reader. Lines typed while the task is working are queued and
  printed as `(queued for the task's next question)`; each line extends one pending message, and
  when the prompt is up they go as a single answer, joined by newlines, so one turn is one message.
  A yes/no or menu prompt takes only a line typed after it is shown, requires an explicit answer
  and asks again otherwise; queued lines stay queued for the next `reply`. Piped stdin works the
  same: each `reply` prompt consumes what has accumulated, and EOF with nothing queued leaves the
  question standing on the docket for somebody else.
- On a re-attach the replayed `pending` frame prompts again if the question is still open (the
  attach loop already knows a settled question from the `decision` frame that follows it).
- An identity is needed to answer, as now; without one the prompt is not offered and the
  `verse gate …` line is printed instead.

Proves: piped-stdin tests against a local verse (an answer reaches the model as its tool result;
two lines typed during work arrive as one reply; a line typed before a yes/no gate is not its
answer; EOF leaves the question pending).

### 4. The model is told

When `ask` is offered, `defaultFrame` says: the person who started this task can be asked with
`ask`; when the task is a conversation, answer and then `ask` for the next message; stopping without
asking ends the task and the last message is its result. Without this the model answers once as
though that were the task, which is what the "let's chat" run did. A spawn's own `frame` can say
otherwise, as now.

Proves: a "let's chat" run on a local verse asks after its first reply and ends after
`yield done` or a stop with no ask.

### 5. Docs

`docs/reference/tasks.md` (`ask` beside `yield`; a conversation as a run of asks; answering from
the attach prompt, `verse gate`, Slack) and the `task` entries in `docs/reference/README.md`.
Also: a conversation's questions are its record, and the task's result is the model's last
message.

## Not in this plan

- ~~**A turn the runtime raises for the person.**~~ Built, on `verse/ask` (793a8671 … cd3018bf):
  `task run -attach` is a conversation unless `-no-converse` — the runtime poses the turn as a
  `gate: turn` question (`reply`/`end`) whenever the driver goes quiet, Ctrl-D ends it, and a
  bare "let's chat" runs as a conversation without the model asking.
- **Claude Code sessions.** For the `claude` driver the person's turn is the session's own REPL,
  and the model's question is `AskUserQuestion`; how those reach the docket and Slack is on
  `js1e345a`, with two probes noted there.
- **Transport.** Everything here is HTTP: SSE for frames, POST for answers. A pty to a session is
  different (bytes, both directions, keystroke latency). That is `js1e345a`'s "attachable" session, not this plan.
- **Restarts.** A local task dies with versed and is marked `orphaned`, its question with it.
  The model's context is in memory only and the transcript truncates tool results (`evTrunc`), so
  resuming a conversation is its own design.

## What replaced the say pipe

The first cut held a task open between turns with a lease an attached client renewed: a new rung in
`quiesce`, a second exception to property 5 (`task/contract.go`), leases in taskd, three routes, a
spawn field, a new frame kind, and a contract change on `-attach`; local-only, with Slack and other
runners deferred. It re-implemented a wait the loop already has. A question is that wait: durable,
on the docket, answerable from every surface, withdrawn on cancel, orphaned on restart, bounded by
its own deadline, and already relayed to every runner. What the pipe alone offered was the person
holding the turn when the model stops without asking — the floor `yield` already accepts, and
covered if needed by the runtime-raised turn above. What `ask` costs is a record per turn, which
is the design working as intended. Step 0 and the line discipline survive from the first cut.

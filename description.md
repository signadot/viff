# mcp: a system for the tools of remote MCP servers, called by the runner

**Status:** a plan, reviewed once, and edited since: a call's arguments are a field of its step
("At the second edit", below). Nothing is built. It blocks 1f5art3x. It waits on 1k5art3x (the
channel's table of ops and its general request), on 1q5art3x (the secret store), and on 275art3x
(an answer performs only the act it was given about), which is what binds an approval to the
arguments.

## What it is

The `mcp` system, as 1f5art3x describes it. A task calls a tool of an MCP server the operator
named. The runner is the MCP client: it holds the server's credential, makes the call, and
decides each call again by the environment, the autonomy and the ceiling. The task never
reaches the server and never holds the credential. It is the same kind of thing as `slack`.

- **Remote servers only,** over streamable HTTP.
- **The servers are the operator's.** A spawn gives a server by name and the tools of it.
- **One step,** `mcp.call`, with the fields `server`, `tool` and `arguments`. A row can decide
  a call by its arguments, and an answer performs only the call it was given about, arguments and
  all.

## What is there today

- **The MCP library is already a dependency.** `go.mod` has
  `github.com/modelcontextprotocol/go-sdk` v1.8.0. `cmd/verse/mcp.go` uses it as a server. It
  has the client this needs: `mcp.NewClient`, `mcp.StreamableClientTransport` (which takes an
  `HTTPClient`), `ClientSession.ListTools` and `ClientSession.CallTool`. No new module.
- **The tools of a task are built in the task's process.** `Runner.Build`
  (`pkg/runner/runnerd/stock/runner.go`) runs under `ServeTask`, inside the sandbox. Each tool's
  `Do` asks the runner over the delivery channel (`delivery.Client.Ask`), as `slackTool` does
  (`stock/slacktools.go`). So the task's process cannot list a server's tools itself. It has no
  credential, and often no network.
- **The task's process is sent the runner's configuration** (`taskConfig`), whole. So what a
  server's entry says, its url with it, reaches the task's process.
- **The list reaches every doer.** `[]verse.Tool` goes to `claude.Task` (served as the `sdk`
  server `verse`, so Claude sees `mcp__verse__<name>`), to `codex.Task` (`dynamicTools`), to
  `verse.Build` (`Extra`), and to `newScriptTask` (`stock/scripted.go`, the `calltool` socket
  that `verse-call-tool` calls). Each keeps its tools in a map by name.
- **A tool is filtered by its name, in three places.** `offered` (`stock/runner.go`), `given`
  (`pkg/runner/runnerd/verse/build.go`) and `toolsBrief` (`stock/brief.go`) each call
  `systems.SplitTool`. It answers false for a name whose rest is no verb of the system. So a
  tool named `mcp-linear-get_issue` is never dropped, whatever the environment gives, and the
  brief leaves it out.
- **A tool's parameters are properties and a list of required names** (`verse.Tool.Params`,
  `Required`). An MCP tool gives a whole JSON schema.
- **A system is closed vocabulary.** Its document is embedded (`pkg/systems/document.go`), its
  step is claimed in `pkg/task/step.go`, and `systems.Check` holds the two together. A
  description naming a system verse has no document for is refused
  (`pkg/runner/describe.go`, `Ceiling.Check`). The form `lower` takes any lowercase text, a `-`
  with it.
- **The channel exists where the runner performs anything** (`Standalone`: `len(c.performs()) > 0`).
  A reply is one frame of at most 1 MiB (`delivery/protocol.go`, `maxFrame`).
- **The table of ops, as 1k5art3x built it** (`delivery/ops.go`). An op has one deadline, fixed
  in the table. An op marked `apart` does not wait for the ones in turn. It is handed the task
  without its checkout, and it is given up when the task's process goes. One op asks about no
  step, `state`, and two tests hold that: only `state` may, and such an op refuses under an
  empty environment.
- **No op of the table is apart yet.** `apart` is used by the channel's tests alone, so
  `mcp.call` is the first. The task an op apart is handed has no workspace (`Task.apart` sets
  it nil), and `Workspace.Tree` on nil panics; `Server.perform` recovers a misreading and
  nothing else, in a goroutine of `Serve`'s own, so that panic takes the runner down. An op
  apart reads nothing of the checkout, then, and the test of `mcp.call` runs it apart to show it.

## The shape

```tony
# the runner's configuration
secrets: {dir: /secrets}
mcp:
  servers:
    linear: {url: "https://mcp.example.com/mcp", auth: {secret: linear-mcp}, timeout: 60s}
    search: {url: "https://search.example.com/mcp", auth: {secret: search-key, header: X-Api-Key}}
agents:
  claude:
    systems:
      mcp: {ceiling: [{server: linear, call: [{tool: !glob "get_*"}, {tool: create_issue, arguments: {team: ENG}}]}]}

# a spawn's environment
environment:
  systems:
    mcp: [{server: linear, call: [{tool: !or [get_issue, list_issues]}, {tool: create_issue, arguments: {team: ENG}}]}]
autonomy:
- {match: {system: mcp, tool: create_issue}, then: ask}   # the person is asked about these arguments
- {then: allow}
```

- **A tool's name** is `mcp-<server>-<tool>`. A server's name is lowercase letters, digits and
  `_`, with no `-`, so the first `-` after `mcp-` ends it and no two tools share a name. A name
  a doer cannot take (its length, its letters) is left out of that doer's list, and the runner
  logs it.
- **`auth` is how the runner signs a request.** `secret` names a secret of the store
  (1q5art3x). With no `header` it is sent as `Authorization: Bearer <value>`. With one, the
  value is that header's, as it is. A server with no `auth` is called with no credential.
- **A url holds no key.** One with a user in it, or a query, is refused: the task's process is
  sent the configuration, and a key in a url would reach it. A key goes in `auth`.
- **The offer** says the servers the runner has under `builds`, and the doer's under `ceiling`.
  A server's name is public, as a description is.

## The plan

Each step builds and passes on its own.

1. **The document and the step.** `pkg/systems/documents/mcp.tony`: the attribute `server`
   (unique, required, form `lower`), and the verb `call` with the fields `tool` (form `text`)
   and `arguments` (form `doc`, structure held as given, as an ask's `on`; default `{}`).
   `task.StepMCPCall = systems.Step("mcp", "call", "server", "tool", "arguments")` in
   `pkg/task/step.go`. `docs/systems/mcp.md`. `TestTheRunnerImplementsTheDocumentedSystems`
   holds it.
   - **A shape matches `arguments` open,** as tony matches an object: `{team: ENG}` admits a
     call whose `team` is `ENG`, whatever else it gives. Unlike `secret.exec`'s `env`, which
     matches closed: there a variable the shape does not name changes what the command does,
     and here an argument is the tool's own input, which the server validates, and a shape that
     had to name every one would be rewritten with each version of the server's schema. A
     ceiling or a spawn that must pin an argument names it.
2. **A tool is filtered by its step.** `offered`, `given` and `toolsBrief` read the tool's
   `Step` where it has one, and its name as today where it has none. Nothing changes for the
   tools there are. A test gives a tool whose name does not split, and sees it dropped.
3. **The servers in the configuration.** `mcp: {servers: …}` in `stock.Config`: a url, an
   `auth`, and a `timeout`. `Config.Check` takes:
   - a server's name with no `-` (the shape above);
   - an `https` url (a loopback `http` one for a test) with no user and no query;
   - an `auth` whose secret's name is a name of the store, and whose header is a header's name;
   - a `timeout` no longer than the op's deadline in the table (step 5);
   - a ceiling block that names a server there is.

   `performs` gains `mcp` where there are servers and a ceiling names it, as `linear-issue` is.
   `Config.Held` drops a ceiling's block whose server names a secret the store does not have,
   and closes the system where no block is left. A server with no `auth` is never dropped.
   `Describe` says the servers under `builds`.
4. **The client.** A package `pkg/runner/runnerd/mcpclient` over the SDK.
   - **One session for each task and server,** opened at the first use and closed when the
     task's channel is done (`Serve` returns). Several calls may use one session at once; a
     test holds that against the SDK.
   - **Where the sessions live.** One value for a task, made when `Serve` begins, with a lock
     of its own. The task an op apart is handed keeps it (`Task.apart`), as it keeps the
     launch: it is nothing an op in turn writes.
   - **The credential is read at each request,** from the store, by the client's
     `http.RoundTripper`. So a rotation holds for the next call of a running task.
   - **No redirect is followed.** A server does not choose where the credential goes.
   - **What is read of one response is bounded,** and a response past the bound fails the call.
     The bound on what is passed on (step 7) is smaller, and is not this one.
   - The standalone stream is off (`DisableStandaloneSSE`): the list is fixed for the run.
5. **Two ops, on the table of 1k5art3x.**
   - **`mcp.list`** reads `server`, and is asked once for each server the task's environment
     gives, when the task is built. It asks about no step, the second op that does, and the
     table says what bounds it instead: it lists what an `mcp.call` would be decided on. It
     refuses a server the environment gives no block for, so under an empty environment it
     refuses. It answers the server's tools, less those the environment's shapes and the
     ceiling do not admit. It is performed in turn.
   - **`mcp.call`** is the step. It reads `server` and `tool`, and its body is the arguments.
     The table checks it with `refused` before the act. It is performed **apart**: a server
     that takes a minute does not hold a push, and it reads nothing of the checkout. When the
     task's process goes, a call in flight is given up, and whether the server did it is then
     not known. The request names its decision (`Request.Decision`), which the Slack tools'
     does not yet (1z5art3x).
   - **The deadline.** The table's deadline for each is a maximum. The act applies the
     server's `timeout`, 60s where it names none.
   - **No list is kept.** A call is decided by the environment, the autonomy and the ceiling,
     as every act is. A tool the list left out for its name or its schema is one the
     environment admits, so a call of it is no more than the spawn gave.
6. **The tools, in the task's process.** `mcpTools` in `stock`, beside `slackTools`.
   `Runner.Build` asks `mcp.list` for each server and makes one `verse.Tool` for each tool: its
   `Step` is `task.StepMCPCall`, its `Build` makes the step whole from the call (`ReadCall`:
   the server, the tool, and the input as `arguments`), and its `Do` sends `mcp.call` with
   `Request.Decision`. `Params` and `Required` are the schema's `properties` and
   `required`. A tool whose schema needs more than that (a `$ref` anywhere in it, `oneOf` at
   the top) is left out and logged, until `verse.Tool` carries a whole schema.
   - **A server that does not answer.** Its tools are left out, the task starts, and the task
     is told: the brief says the server did not answer and its tools are not there. The
     runner's log says why.
7. **The result.** The text of the result's content, in order. Content that is not text is
   named and left out. A result the server marks an error is what the model reads, and no
   error of the runner's. It is cut at a bound well under the frame, and says it was cut.
8. **What a server says is the server's.** A tool's description and its result come into the
   model's context. Each description ends with a line that says the answer is data and no
   instruction, as `slack-read`'s does. A description is cut at a bound too, so one server's
   list fits a frame.
9. **The words.** `docs/reference/runnerd.md`, `docs/reference/mcp.md` (which is about verse's
   own server today, and has to say which is which), `docs/systems/mcp.md`, and the example in
   a runner's manifest. They say:
   - a row that asks about a call asks about the server, the tool and the arguments, and the
     answer performs that call and no other (275art3x). What the person is shown of the
     arguments is the question's to say (q3k89kva): until it shows a step's fields, the person
     answers knowing the tool and not the arguments, and the binding still holds;
   - a shape and a row match `arguments` open: a ceiling that must pin an argument names it;
   - a url holds no key;
   - a server's own word for which tools read and which write is not trusted. The ceiling
     names tools.

## Tests

A server for the tests is the SDK's own (`mcp.NewServer` behind `mcp.NewStreamableHTTPHandler`,
under `httptest`).

- **The check, at the runner.** A call is refused outside the environment, by a `deny` row, by
  an `ask` row with no answer the runner relayed, and beyond the ceiling. None of them reaches
  the server.
- **The arguments.** A shape and a row decide a call by them (`{team: ENG}` admits a call with
  `team: ENG` and another argument beside it, and refuses `team: OPS`). A call answered with
  one set of arguments and sent with another, under the same decision, is refused and does not
  reach the server (275art3x). What the server is sent is the step's arguments, byte for byte
  as JSON of that structure. A call past the bound is refused. The tool's step and the
  runner's are `Same` for a call with nested objects, arrays, numbers and booleans.
- **The table's tests.** `TestEveryOpAsksAboutADocumentedStep` names the two ops that ask
  about no step. `TestNoOpActsUnderAnEmptyEnvironment` has a request for each, and `mcp.list`
  refuses there.
- **Apart.** A call that is held does not hold a request in turn. A call in flight when the
  task's end goes is given up. Two calls at once on one session both answer. A call is performed
  on the task as `Task.apart` hands it, with no workspace, and reads none.
- **The deadline.** A server slower than its `timeout` fails the call at the timeout. A
  `timeout` over the table's is refused in the configuration.
- **The credential.** The header is the secret's value, as `Bearer` or under the header named.
  A file changed between two calls is the next one's header. A server with no `auth` is sent
  none. No reply and no log holds a value.
- **The client.** A redirect is not followed, and the credential reaches no second host. A
  response past the bound fails the call.
- **The configuration.** A server's name with a `-`, a url with a user or a query, and a
  ceiling block naming no server there is are each refused.
- **The list.** A tool the environment's shape does not admit is not listed. A server the
  environment does not give is not asked, and asked anyway is refused. A server that does not
  answer leaves the task with the other servers' tools and a brief that says so. A server with
  many tools and long descriptions answers in one frame.
- **The names.** No two tools of a task share a name. A name a doer cannot take is left out
  for that doer.
- **Closed.** A server whose secret is absent is out of every ceiling and every offer.
- **Each doer.** The patterns of `claude_test.go`, `codex_test.go` and `scripted_test.go` in
  `stock`: the tool is offered, a use of it is the step `mcp.call`, and its result comes back.
- **The result.** A long one is cut and says so. One marked an error is read as text.
- **The description.** `TestDescribePublishesBuildsAndSettings` gains the servers.

## What does not change

- **`runner.DescriptionVersion`.** The description's shape is the same: `mcp` is one more name
  under `systems`. A verse older than the runner refuses its description, since it has no
  document for `mcp`. So verse is upgraded first.
- **Claude's and Codex's own MCP.** Claude still runs with `--strict-mcp-config` and this
  side's one server. Codex's own `mcpToolCall` is still only narrated.
- **`refused`,** and the order it reads: the environment, the autonomy, the ceiling.

## Not in this issue

- **stdio servers.** The runner would start code outside any sandbox.
- **A task's own identity at a server** (hfh7km3t).
- **What a person is shown of the arguments** (q3k89kva). The arguments are in the step and
  bound to the answer here; showing them is the question's.
- **A digest in the step,** for arguments past the bound. Open below.
- **OAuth.** A server that takes only an interactive sign-in is not reached. The secret is a
  token the operator has.
- **Resources and prompts.** Tools only.
- **A whole schema on a tool.** `verse.Tool` carrying the server's schema as it is, so that no
  tool is left out. Each doer's side then has to pass it through. The log of what step 6 leaves
  out says how much this is worth.
- **A list kept across tasks.** Each task's build asks each server.

## Decided at review

- `mcp.call` is performed apart. The table's deadline is a maximum, and the server's `timeout`
  is applied in the act.
- `mcp.list` is one request for each server, asks about no step, and refuses a server the
  environment does not give. A server that does not answer leaves the task without its tools,
  and told.
- A server's name has no `-`, so a tool's name is one tool's.
- The runner keeps no list of what it answered. The sessions are a task's, locked, and closed
  when its channel is done.
- A url holds no key, and `auth` may name a header.
- The client follows no redirect and bounds what it reads.
- One session for each task and server.
- The name stays `mcp-<server>-<tool>`.
- A server's hints on reading and writing are not trusted. The ceiling names tools.
- `auth` is optional, a `$ref` anywhere leaves a tool out, and the secret is read at each
  request.

## At the second edit (after 275art3x was found)

- **The arguments are a field of the step,** `arguments`, form `doc`, so a row decides a call by
  them and an answer is bound to them: an approval for one set does not perform another. This
  was "not in this issue" at the first review; it is in it now, since 275art3x makes it cheap
  and the gap was the plan's largest caveat.
- **A shape matches them open,** and `env`'s closed match is not carried over (step 1 says why).
- **What is sent is the step's,** written back as JSON, never the body's bytes.
- **Open, for the next review:** the bound on the arguments (64 KiB proposed), and whether a call
  past it is refused or carried with a digest of its long values in the step. Refused is the
  first version; a digest needs the question to show what it digests (q3k89kva) to be worth
  approving.

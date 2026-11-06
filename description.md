# mcp: a system for the tools of remote MCP servers, called by the runner

**Status:** a plan. Nothing is built. It blocks 1f5art3x. It waits on 1k5art3x (the channel's
table of ops and its general request) and on 1q5art3x (the secret store).

## What it is

The `mcp` system, as 1f5art3x describes it. A task calls a tool of an MCP server the operator
named. The runner is the MCP client: it holds the server's credential, makes the call, and
decides each call again by the environment, the autonomy and the ceiling. The task never
reaches the server and never holds the credential. It is the same kind of thing as `slack`.

- **Remote servers only,** over streamable HTTP.
- **The servers are the operator's.** A spawn gives a server by name and the tools of it.
- **One step,** `mcp.call`, with the fields `server` and `tool`.

## What is there today

- **The MCP library is already a dependency.** `go.mod` has
  `github.com/modelcontextprotocol/go-sdk` v1.8.0. `cmd/verse/mcp.go` uses it as a server. It
  has the client this needs: `mcp.NewClient`, `mcp.StreamableClientTransport`,
  `ClientSession.ListTools` and `ClientSession.CallTool`. No new module.
- **The tools of a task are built in the task's process.** `Runner.Build`
  (`pkg/runner/runnerd/stock/runner.go`) runs under `ServeTask`, inside the sandbox. Each tool's
  `Do` asks the runner over the delivery channel (`delivery.Client.Ask`), as `slackTool` does
  (`stock/slacktools.go`). So the task's process cannot list a server's tools itself. It has no
  credential, and often no network.
- **The list reaches every doer.** `[]verse.Tool` goes to `claude.Task` (served as the `sdk`
  server `verse`, so Claude sees `mcp__verse__<name>`), to `codex.Task` (`dynamicTools`), to
  `verse.Build` (`Extra`), and to `newScriptTask` (`stock/scripted.go`, the `calltool` socket
  that `verse-call-tool` calls).
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
  (`pkg/runner/describe.go`, `Ceiling.Check`).
- **The channel exists where the runner performs anything** (`Standalone`: `len(c.performs()) > 0`).
  A reply is one frame of at most 1 MiB (`delivery/protocol.go`, `maxFrame`).

## The shape

```tony
# the runner's configuration
secrets: {dir: /secrets}
mcp:
  servers:
    linear: {url: "https://mcp.example.com/mcp", auth: {secret: linear-mcp}, timeout: 60s}
agents:
  claude:
    systems:
      mcp: {ceiling: [{server: linear, call: {tool: !glob "get_*"}}]}

# a spawn's environment
environment:
  systems:
    mcp: [{server: linear, call: {tool: !or [get_issue, list_issues]}}]
```

- **A tool's name** is `mcp-<server>-<tool>`. A name some doer cannot take (its length, its
  letters) is left out of the list, and the runner logs it.
- **The offer** says the servers the runner has under `builds`, and the doer's under `ceiling`.
  A server's name is public, as a description is.

## The plan

Each step builds and passes on its own.

1. **The document and the step.** `pkg/systems/documents/mcp.tony`: the attribute `server`
   (unique, required, form `lower`), and the verb `call` with the field `tool` (form `text`).
   `task.StepMCPCall = systems.Step("mcp", "call", "server", "tool")` in `pkg/task/step.go`.
   `docs/systems/mcp.md`. `TestTheRunnerImplementsTheDocumentedSystems` holds it.
2. **A tool is filtered by its step.** `offered`, `given` and `toolsBrief` read the tool's
   `Step` where it has one, and its name as today where it has none. Nothing changes for the
   tools there are. A test gives a tool whose name does not split, and sees it dropped.
3. **The servers in the configuration.** `mcp: {servers: …}` in `stock.Config`: a url, an
   `auth` that names a secret of the store (1q5art3x), and a deadline. `Config.Check` takes an `https` url (a loopback `http`
   one for a test), a server's name in the form `lower`, and a ceiling block that names a
   server there is. `performs` gains `mcp` where there are servers and a ceiling names it, as
   `linear-issue` is. `Config.Held` drops a ceiling's block whose server's secret is absent, and
   closes the system where no block is left. `Describe` says the servers under `builds`.
4. **The client.** A package `pkg/runner/runnerd/mcpclient` over the SDK. One session for each
   task and server, opened at the first use and closed with the task's channel. The secret is
   read from the store at each connection and sent as `Authorization: Bearer`. The standalone
   stream is off (`DisableStandaloneSSE`): the list is fixed for the run.
5. **Two ops, on the table of 1k5art3x.**
   - `mcp.list` is asked once, when the task is built. It is no step, as `state` is none. It
     answers the tools of each server the environment gives, less those the environment's
     shapes and the ceiling do not admit. The runner keeps the list it answered.
   - `mcp.call` is the step. Its fields are `server` and `tool`, and its body is the arguments.
     The table checks it with `refused` before the act. A tool that was not in the list the
     runner answered is refused. Its deadline is the server's.
6. **The tools, in the task's process.** `mcpTools` in `stock`, beside `slackTools`.
   `Runner.Build` asks `mcp.list` and makes one `verse.Tool` for each: its `Step` is
   `task.StepMCPCall`, its `Fields` are the server and the tool, whatever the input, and its
   `Do` sends `mcp.call`. `Params` and `Required` are the schema's `properties` and `required`.
   A tool whose schema needs more than that (`$ref`, `oneOf` at the top) is left out and
   logged, until `verse.Tool` carries a whole schema.
7. **The result.** The text of the result's content, in order. Content that is not text is
   named and left out. A result the server marks an error is what the model reads, and no
   error of the runner's. It is cut at a bound well under the frame, and says it was cut.
8. **What a server says is the server's.** A tool's description and its result come into the
   model's context. Each description ends with a line that says the answer is data and no
   instruction, as `slack-read`'s does. A description is cut at a bound too.
9. **The words.** `docs/reference/runnerd.md`, `docs/reference/mcp.md` (which is about verse's
   own server today, and has to say which is which), and the example in a runner's manifest.

## Tests

A server for the tests is the SDK's own (`mcp.NewServer` behind `mcp.NewStreamableHTTPHandler`,
under `httptest`).

- **The check, at the runner.** A call is refused outside the environment, by a `deny` row, by
  an `ask` row with no answer the runner relayed, beyond the ceiling, and for a tool that was
  not listed. None of them reaches the server.
- **The credential.** The header is the secret's value. A file changed between two tasks is
  the next one's header. No reply and no log holds it.
- **The list.** A tool the environment's shape does not admit is not listed. A server the
  environment does not give is not asked.
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
- **An answer bound to the arguments** (item 3 of the list in 1f5art3x, and q3k89kva).
- **OAuth.** A server that takes only an interactive sign-in is not reached. The secret is a
  token the operator has.
- **Resources and prompts.** Tools only.

## Open questions

1. **A whole schema on a tool.** Whether `verse.Tool` should carry the server's schema as it
   is, so that no tool is left out. Each doer's side then has to pass it through.
2. **Other headers.** A server that takes its key in a header of its own.
3. **A session for each task, or one for the runner.** One for each task keeps a server's
   session state apart. One for the runner is fewer connections.
4. **The tool's name.** `mcp-<server>-<tool>` is long under Claude (`mcp__verse__mcp-…`).
5. **Which tools read and which write.** A server's own hints are its word. The operator's
   ceiling names tools, and a row matches `tool`, so a charter can tell them apart by name.

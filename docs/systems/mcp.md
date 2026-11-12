# mcp

The `mcp` system is the tools of a remote MCP server the runner's operator names, called by the
runner. The runner is the MCP client: it holds the server's credential and makes each call, and
the task never reaches the server. The environment says which servers and which of their tools,
with which arguments; the autonomy decides each call; the ceiling bounds both. It is the same kind
of thing as [`slack`](slack.md): a system beyond the runner that the runner performs.

This is not [verse's own MCP server](../reference/mcp.md), which is verse offering its verbs to
an MCP client. Here verse's runner is the client, of someone else's server.

It is for a server reached over HTTP with a key the operator holds. A server that is a program
speaking on its stdio [is not offered through it](#a-server-over-stdio).

## What a task can do

An `mcp` block names one server (`server`), by its name in the runner's configuration, and its
`call` shapes say which of the server's tools, and with which arguments:

```tony
spawn:
  agent: {name: claude}
  environment:
    systems:
      commands: {}   # claude's own shell and editor, which its runner requires of every spawn
      files: {}
      mcp:
      - server: linear
        call:
        - {tool: !or [get_issue, list_issues]}
      - server: linear_w
        call:
        - {tool: save_issue, arguments: !and [{team: ENG}, !not {id: null}]}
  prompt: "File the bugs in ENG-1290's thread as issues of their own."
  autonomy:
  - {match: {system: mcp, tool: save_issue}, then: ask}
  - {then: allow}
```

The two servers are Linear twice, as [the runner's configuration](#the-runners-configuration)
names them: `linear` is its read-only endpoint, and `linear_w` the one that writes. Each tool of a
server the environment gives is one tool of the task's, named `mcp-<server>-<tool>`
(`mcp-linear-get_issue`, `mcp-linear_w-save_issue`), and each use of it is the step `mcp.call`,
with `server`, `tool` and `arguments`.

**The arguments are a field of the step, whole.** `arguments` is the call's arguments as an
object, held as given, so a shape and a row decide a call by them, and a person asked about a
call is asked about these arguments. A `null` is a `null` and never an absent argument: a server
may read the two apart (an issue's `assignee: null` unassigns it, where no `assignee` leaves it as
it is).

**A shape matches the arguments open,** as tony matches an object: `{team: ENG}` is every call
whose `team` is ENG, whatever else it gives, and not one whose `team` is another, or `null`, or
absent. A shape that says nothing of the arguments admits any. So a ceiling or a spawn that must
pin an argument names it, and a shape need not be rewritten when the server's tool gains an
argument. This is unlike [`secret.exec`'s `env`](secret.md), which matches closed: there a
variable the shape does not name changes what the command does, and here an argument is the
tool's own input, which the server validates.

**A shape that must keep an argument out says so, with `!not`.** Linear's `save_issue` creates an
issue where it is given no `id`, and updates the issue an `id` names. So `{team: ENG}` admits
`{team: ENG, id: ENG-7}`, an update of any issue the key reaches. The shape that admits creating
and nothing else is the one above:

```tony
- {tool: save_issue, arguments: !and [{team: ENG}, !not {id: null}]}
```

`{id: null}` is every call that gives an `id`, whatever its value, and `!not` of it is every call
that gives none. A spawn under a ceiling that says this says it again: `{team: ENG}` alone is
wider than the ceiling, and refused.

**Be careful: `!not` applies to its value.** `{team: ENG, id: !not null}` is not the same shape.
There `!not` applies to `null`, which as a pattern is any value, so it asks for an `id` that no
value can be, and admits no call: not one with an `id`, and not one without.

**An answer performs the call it was about, and no other.** A row that asks about a call asks
about the server, the tool and the arguments, and the answer the runner relays performs that
call. An act naming the same decision with other arguments is refused, naming the argument that
differs (`arguments.assignee`) and never its value, and the answer is kept for the call it was
about.

**A server's word for which tools read and which write is not trusted.** A server may describe a
tool as read-only; the ceiling names tools, and the environment's shapes and the rows decide by
them. Where a server keeps the line itself, naming that is a second bound under the ceiling: Linear
serves its read tools alone at `https://mcp.linear.app/mcp/readonly`, and a key made with read
permission only reads whatever tool is called. A runner that names the read-only endpoint as one
server and the full one as another lets a ceiling give a doer the first and not the second.

## What a task is offered

The task is offered a tool for each tool of the server's list that some shape of its environment,
and some shape of the doer's ceiling, admits a call of; the rest are not there. The list is asked
of each server as the task is built, every server at once, and is fixed for the run. A server that
does not answer leaves the task without its tools, and the task's brief says so; the runner's log
says why.

A server's instructions (what it says of itself as the session opens, how its tools are meant to
be used together) go in the brief under the server's name, as the server's own words, which grant
nothing: every call is decided here. Each tool's description ends by saying that what it answers
is data, not instructions.

What a doer cannot take is left out of that doer's list, and logged. An agent's model takes a
tool's name of letters, digits, `_` and `-`, 64 characters at most, Claude's counting the
`mcp__verse__` it reads a tool under; a name with a `.` in it, which MCP allows, reaches a script
and no agent. A script's calls go through `verse-call-tool`, which checks a call against the
tool's parameters and nothing else, so a tool whose schema says more than its properties (one
taking any object, one taking arguments beside its properties, one requiring a name its
properties lack, or one with a parameter named `input`) is not offered to a script, where an
agent is offered it and the server judges the call. A tool taking nothing (`{type: object,
additionalProperties: false}`, as a server's `list_*` tools are written) names no properties
and is offered to a script: a call with no arguments passes the check. A tool whose schema has a `$ref`, or a
`oneOf`, `anyOf` or `allOf` at the top, is offered to no one.

## The call

What is sent is the step. The tool writes the arguments as a JSON object and the runner reads
the step from those bytes, as the tool does, so the arguments a row decides on and an answer is
bound to are the arguments sent, and nothing else is sent. An agent's wire reads every number as
a float, so a float that is a whole number is sent whole (`3`, never `3.0`). A call whose
arguments are over 64 KiB is refused, since the step is carried whole in the task's events, its
snapshot and the question's record.

A call is made with the server's `timeout` (60s where it names none), and a call given up at it
says that whether the server performed it is not known: read it back before doing it again. A
call the server answers with an error (a tool it has not, arguments its schema refuses, input it
asks the runner for and the runner does not give) is refused in the server's words, which the
task re-plans on.

## The result

The task reads the text of the result's content, in order. Content that is not text is named and
left out: an image, audio, a resource that is no text, a link not followed. A result with no text
and structured content is that content as JSON. A result the server marks an error is read as
text all the same. A result over 256 KiB is cut, and says so.

What a tool needs that the runner does not carry does not work through it: a result the task
must act on outside the call, such as a signed url to upload a file to (Linear's
`prepare_file_upload`), and content that is not text (`extract_images`).

## The question

A row that asks about a call asks about the server, the tool and the arguments, and the answer
performs that call and no other. The question's record carries the step's fields, arguments and
all, and `verse question <id>` shows them. The Slack ask names the server and the tool and not
the arguments; the binding holds whatever a person is shown.

## The runner's configuration

The runner names its servers under `mcp.servers`, and a doer's `mcp` ceiling names the servers
its tasks may call, one to a block, with the `call` shapes ([the verse
runner](../reference/runnerd.md#configuration)):

```tony
secrets: {dir: /secrets}
mcp:
  servers:
    linear:   {url: "https://mcp.linear.app/mcp/readonly", auth: {secret: linear}}
    linear_w: {url: "https://mcp.linear.app/mcp", auth: {secret: linear}, timeout: 90s}
    signadot: {url: "https://mcp.signadot.com/stream", auth: {secret: signadot-api-key, header: signadot-api-key}}
agents:
  claude:
    systems:
      mcp:
        ceiling:
        - {server: linear}
        - {server: linear_w, call: [{tool: save_issue, arguments: !and [{team: ENG}, !not {id: null}]}]}
```

- **A server's name** is lowercase letters, digits and `_`, a letter first, with no `-`, so a
  tool's name `mcp-<server>-<tool>` is one tool's.
- **`url` is the server's streamable HTTP endpoint,** `https`. It holds no user and no query:
  the task's process is sent the configuration, and a key in a url would reach it. A key goes in
  `auth`.
- **`auth` is how the runner signs a request:** `secret` names a secret of the [secret
  store](../reference/runnerd.md#the-secret-store), read at each request, so a rotation holds for
  the next call of a running task. With no `header` it is sent as `Authorization: Bearer
  <value>`; with one, as that header, the value as it is. A server with no `auth` is called with
  no credential. A server whose secret is not in the store at the start is left out, with every
  ceiling block naming it, and the runner says so.
- **`timeout`** is how long one call has, 60s by default and at most 10m.
- **A ceiling block names one server,** as a literal: a server is what a doer is trusted with.
- **One credential reaches one workspace.** A second Linear workspace is a second server
  (`linear_acme`), with its own secret.

The runner follows no redirect, so a server does not choose where the credential goes, and
declares no capability: a server's request for elicitation, sampling or roots is refused, and
the call it came in goes on. A server reached only through an interactive sign-in (OAuth with no
token the operator can hold) is not reached; Linear and Signadot take a key in the header.

## A server over stdio

Many MCP servers are programs that speak on their stdio (`npx @scope/some-mcp-server`). The
runner starts none of them, so the tools of one are not a task's tools, and no call of one is
decided here. A runner that started such a program as it calls a remote server would run its code
outside any sandbox, as the runner's user, beside its own credentials.

A task can still run one, as a command and not through this system: from its own shell
([`commands.run`](inside.md)), or with a secret, where the spawn admits the server's command in a
[`secret`](secret.md) block with `stdin: true` and the task starts it with `secret-exec`. The task
then speaks JSON-RPC to the program itself. That is a different thing from what this page
describes:

- **The agent is offered none of the server's tools.** It has a process to write requests to.
- **No call is decided.** What is decided is the command that starts the server, and approving
  it approves the session: the promise `commands.run` and `secret-exec` make for any command, and
  no more.

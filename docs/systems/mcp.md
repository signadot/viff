# mcp

The `mcp` system is the tools of a remote MCP server the runner's operator names, called by the
runner. The runner is the MCP client: it holds the server's credential and makes each call, and
the task never reaches the server. The environment says which servers and which of their tools,
with which arguments; the autonomy decides each call; the ceiling bounds both. It is the same kind
of thing as [`slack`](slack.md): a system beyond the runner that the runner performs.

This is not [verse's own MCP server](../reference/mcp.md), which is verse offering its verbs to
an MCP client. Here verse's runner is the client, of someone else's server.

## What a task can do

An `mcp` block names one server (`server`), by its name in the runner's configuration, and its
`call` shapes say which of the server's tools, and with which arguments:

```tony
spawn:
  agent: {name: claude}
  environment:
    systems:
      mcp:
      - server: linear
        call:
        - {tool: !or [get_issue, list_issues]}
        - {tool: create_issue, arguments: {team: ENG}}
  prompt: "File the bugs in ENG-1290's thread as issues of their own, and link them from it."
  autonomy:
  - {match: {system: mcp, tool: create_issue}, then: ask}
  - {then: allow}
```

Each tool of a server the environment gives is one tool of the task's, named
`mcp-<server>-<tool>` (`mcp-linear-get_issue`), and each use of it is the step `mcp.call`, with
`server`, `tool` and `arguments`.

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

**An answer performs the call it was about, and no other.** A row that asks about a call asks
about the server, the tool and the arguments, and the answer the runner relays performs that
call. An act naming the same decision with other arguments is refused, naming the argument that
differs (`arguments.assignee`) and never its value, and the answer is kept for the call it was
about.

**A server's word for which tools read and which write is not trusted.** A server may describe a
tool as read-only; the ceiling names tools, and the environment's shapes and the rows decide by
them.

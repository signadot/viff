# MCP

`viff mcp` serves viff to an agent's host, such as Claude Code, over
[MCP](https://modelcontextprotocol.io). The host starts it and talks to it on stdin and stdout.
Each tool does what the `viff` command it is named after does, and answers what that command
prints.

This is viff as an MCP server. Viff's runner is also an MCP client, of remote servers its
operator names, whose tools a task calls through the runner: that is the [`mcp`
system](../systems/mcp.md), and this page is not about it.

## Adding it to a host

The host runs `viff mcp` as a command. For Claude Code:

```sh
claude mcp add viff -- viff mcp
claude mcp add viff-local -e VIFF_CONFIG=$HOME/.config/viff-local.tony -- viff mcp
claude mcp add viff-dev -- viff --addr localhost:9797 mcp
```

The server reaches whichever viff `viff` reaches with the same config and flags.

## Who it acts as

The server acts as whoever `viff` acts as where it was started: the config's `user:`, the
login, or `VIFF_USER`. Who that is is fixed when it starts. The login itself is read from
your tokens file at each request, so a login renewed or made again elsewhere is the one it
sends, and its own renewals keep what else the file holds. No tool takes an identity, and
there is no `--as`, so nothing an agent reads can make it act as somebody else. With nobody
configured, a tool that writes, and `viff_wait`, is refused.

## Reading only

`viff mcp -read-only` serves the tools that read and nothing else: `viff_data`,
`viff_question`, `viff_ask_get`, `viff_ask_list`, `viff_wait`, `viff_task`, `viff_task_events`, `viff_flow`,
`viff_flow_show`, `viff_charter`, `viff_charter_check`, `viff_status` and `viff_schema`, and
the prompts. An agent that reads what other people wrote may meet instructions in it. A server
that offers no write leaves it nothing to follow them with, whatever the host's own permissions
allow.

```sh
claude mcp add viff-read -- viff mcp -read-only
```

## The tools

| Tool | The command | What it does |
|---|---|---|
| `viff_data` | `viff data` | reads the data at an address: `at`, `if`, `if_at`, `trim`, `paths`, `depth` |
| `viff_data_put` | `viff data <address> --put` | writes a value at an address, on `create`, `update` or `expect`, with `grants` for a node it creates |
| `viff_data_rm` | `viff data <address> --rm` | removes the data at an address, or one field of it |
| `viff_question` | `viff question` | lists the questions waiting on you, or shows one by `id` |
| `viff_question_answer` | `viff question <id> --answer` | answers a question with an `option` |
| `viff_ask_get` | `viff ask <name\|id>` | shows one of your asks, by name or id |
| `viff_ask_list` | `viff ask` | lists your asks |
| `viff_ask_create` | `viff ask [<name>] -q` | asks someone: `question`, `name`, `to`, `due`, `on`, … |
| `viff_ask_change` | `viff ask <name> --due …` | moves or changes an ask |
| `viff_ask_withdraw` | `viff ask <name> --withdraw` | withdraws an ask |
| `viff_wait` | `viff ask --watch`, `viff question --watch` | waits for an ask of yours or a question for you to change |
| `viff_task` | `viff task` | lists tasks, or shows one by `id` |
| `viff_task_events` | `viff task attach` | reads a task's transcript from a cursor |
| `viff_task_run` | `viff task run -f` | runs a `spawn`: a prompt or a script, its environment and its autonomy |
| `viff_task_autonomy` | `viff task autonomy` | replaces a running task's autonomy |
| `viff_flow_suspend` | `viff flow suspend` | holds one task, or suspends a charter's work about a subject, with `why` |
| `viff_flow_resume` | `viff flow resume` | lets a held task go, or lifts the suspension the same selection made |
| `viff_flow_say` | `viff flow say` | says something to every live task a selection names, at its next boundary |
| `viff_flow_cancel` | `viff flow cancel --task` | cancels a task |
| `viff_flow` | `viff flow` | lists the live tasks and suspensions a selection names |
| `viff_flow_show` | `viff flow show` | draws a subject's work as a tree, and what suspends it |
| `viff_charter` | `viff charter` | reads the charter at a path: the file, a rule or level of it, or its `paths` |
| `viff_charter_check` | `viff charter --check` | says whether charters would install at an address, and what would change |
| `viff_charter_install` | `viff charter -a` | installs a charter, a rule or a list of them at an address, or several charters each at its own `at:`; with `prune`, what they leave out at and under the address goes; with `wait`, the answer is held that long for the rules to arm |
| `viff_charter_remove` | `viff charter -r` | removes a package, or one rule, level or schedule of one |
| `viff_status` | `viff status` | says what the viff instance is running, and what went wrong at its firings |
| `viff_schema` | `viff schema` | prints the schema, or one definition of it |

Every tool's parameters are described in its schema, which the host shows the agent.

A charter path is a kinded path, as a `paths` listing answers it: `viff-dev.drift.rules.watcher`.
Where the address ends is the server's to work out. When it cannot, because a package's address
ends in `rules`, `strata` or `schedules`, `at` says where, as `--` does on the command line.
`viff_charter_check` and `viff_charter_install` take the charters as tony text, separated by
`---` lines when there are several. Several charters each say their own `at:`, as files in a
directory do. A place and everything under it is removed from the command line, with
`viff charter -R`; no tool does it.

The flow verbs take a selection as `viff flow`'s flags give it: `task`, or `charter`
(`<address>[:<rule>]`, with `recursive` for the charters beneath it) and `subject`, the one left
out being every one. `viff_flow_cancel` takes a task alone: a charter's or a subject's work is
suspended, then its tasks cancelled one by one.

An answer is at most 64 KiB. A read that would be longer is refused, never cut, with what to
narrow: a deeper address, `if`, `trim`, or `paths`. The two tools that wait answer in parts
instead, each going on from the cursor of the last.

## Prompts

Two prompts tell the agent which reads answer a question, and what to make of them. They read
nothing themselves; the agent's calls do.

| Prompt | Arguments | What it asks |
|---|---|---|
| `explain-charter` | `address` | the charter's rules and levels, what each rule selects now, and what they have spawned |
| `why-not` | `rule` (`<address>:<rule>`), `subject` | why the rule has not acted on the subject: its condition against the subject's record, and the questions, suspensions and trouble standing on it |

## Waiting

Two tools wait, and answer a **cursor** to pass back as `since` on the next call. A call from a
cursor answers everything after it, so nothing that happens between two calls is missed, and
nothing is answered twice. Each answers as text and as structured content alike, the lines or
frames with the cursor, since a host may show the agent only one of them.

`viff_wait` answers what `viff ask --watch` and `viff question --watch` print: an ask of
yours asked, answered, asked again or gone, and a question starting to wait on you or settling.
It answers at once when there is something after `since`, otherwise at the next thing, or with
nothing at the `timeout` (300 seconds by default, at most 3600). The server opens one watch on
the first call and keeps it until it ends, and remembers the last 1024 lines. A cursor older
than that is refused, and so is one from an earlier server, after the host has started it
again; call without `since`, and read `viff_question` and `viff_ask_list`, to catch up.

`viff_task_events` answers a task's transcript, frame by frame. It says when the task has
ended, after which nothing follows. A stream that closes while the task still runs is said,
and the next call from the cursor goes on.

Only these two watch, and each is narrow: your own asks and questions, or one task. There is no
tool that watches all tasks or the data, since a watch that wide costs the viff instance more than one
agent's session is worth.

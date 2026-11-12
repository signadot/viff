# MCP

`verse mcp` serves verse to an agent's host, such as Claude Code, over
[MCP](https://modelcontextprotocol.io). The host starts it and talks to it on stdin and stdout.
Each tool does what the `verse` command it is named after does, and answers what that command
prints.

This is verse as an MCP server. Verse's runner is also an MCP client, of remote servers its
operator names, whose tools a task calls through the runner: that is the [`mcp`
system](../systems/mcp.md), and this page is not about it.

## Adding it to a host

The host runs `verse mcp` as a command. For Claude Code:

```sh
claude mcp add verse -- verse mcp
claude mcp add verse-local -e VERSE_CONFIG=$HOME/.config/verse-local.tony -- verse mcp
claude mcp add verse-dev -- verse --addr localhost:9797 mcp
```

The server reaches whichever verse `verse` reaches with the same config and flags.

## Who it acts as

The server acts as whoever `verse` acts as where it was started: the config's `user:`, the
login, or `VERSE_USER`. Who that is is fixed when it starts. The login itself is read from
your tokens file at each request, so a login renewed or made again elsewhere is the one it
sends, and its own renewals keep what else the file holds. No tool takes an identity, and
there is no `--as`, so nothing an agent reads can make it act as somebody else. With nobody
configured, a tool that writes, and `verse_wait`, is refused.

## Reading only

`verse mcp -read-only` serves the tools that read and nothing else: `verse_data`,
`verse_question`, `verse_ask_get`, `verse_ask_list`, `verse_wait`, `verse_task`, `verse_task_events`, `verse_flow`,
`verse_flow_show`, `verse_charter`, `verse_charter_check`, `verse_status` and `verse_schema`, and
the prompts. An agent that reads what other people wrote may meet instructions in it. A server
that offers no write leaves it nothing to follow them with, whatever the host's own permissions
allow.

```sh
claude mcp add verse-read -- verse mcp -read-only
```

## The tools

| Tool | The command | What it does |
|---|---|---|
| `verse_data` | `verse data` | reads the data at an address: `at`, `if`, `if_at`, `trim`, `paths`, `depth` |
| `verse_data_put` | `verse data <address> --put` | writes a value at an address, on `create`, `update` or `expect`, with `grants` for a node it creates |
| `verse_data_rm` | `verse data <address> --rm` | removes the data at an address, or one field of it |
| `verse_question` | `verse question` | lists the questions waiting on you, or shows one by `id` |
| `verse_question_answer` | `verse question <id> --answer` | answers a question with an `option` |
| `verse_ask_get` | `verse ask <name\|id>` | shows one of your asks, by name or id |
| `verse_ask_list` | `verse ask` | lists your asks |
| `verse_ask_create` | `verse ask [<name>] -q` | asks someone: `question`, `name`, `to`, `due`, `on`, … |
| `verse_ask_change` | `verse ask <name> --due …` | moves or changes an ask |
| `verse_ask_withdraw` | `verse ask <name> --withdraw` | withdraws an ask |
| `verse_wait` | `verse ask --watch`, `verse question --watch` | waits for an ask of yours or a question for you to change |
| `verse_task` | `verse task` | lists tasks, or shows one by `id` |
| `verse_task_events` | `verse task attach` | reads a task's transcript from a cursor |
| `verse_task_run` | `verse task run -f` | runs a `spawn`: a prompt or a script, its environment and its autonomy |
| `verse_task_autonomy` | `verse task autonomy` | replaces a running task's autonomy |
| `verse_flow_suspend` | `verse flow suspend` | holds one task, or suspends a charter's work about a subject, with `why` |
| `verse_flow_resume` | `verse flow resume` | lets a held task go, or lifts the suspension the same selection made |
| `verse_flow_say` | `verse flow say` | says something to every live task a selection names, at its next boundary |
| `verse_flow_cancel` | `verse flow cancel --task` | cancels a task |
| `verse_flow` | `verse flow` | lists the live tasks and suspensions a selection names |
| `verse_flow_show` | `verse flow show` | draws a subject's work as a tree, and what suspends it |
| `verse_charter` | `verse charter` | reads the charter at a path: the file, a rule or level of it, or its `paths` |
| `verse_charter_check` | `verse charter --check` | says whether charters would install at an address, and what would change |
| `verse_charter_install` | `verse charter -a` | installs a charter, a rule or a list of them at an address, or several charters each at its own `at:`; with `prune`, what they leave out at and under the address goes; with `wait`, the answer is held that long for the rules to arm |
| `verse_charter_remove` | `verse charter -r` | removes a package, or one rule, level or schedule of one |
| `verse_status` | `verse status` | says what the verse is running, and what went wrong at its firings |
| `verse_schema` | `verse schema` | prints the schema, or one definition of it |

Every tool's parameters are described in its schema, which the host shows the agent.

A charter path is a kinded path, as a `paths` listing answers it: `verse-dev.drift.rules.watcher`.
Where the address ends is the server's to work out. When it cannot, because a package's address
ends in `rules`, `strata` or `schedules`, `at` says where, as `--` does on the command line.
`verse_charter_check` and `verse_charter_install` take the charters as tony text, separated by
`---` lines when there are several. Several charters each say their own `at:`, as files in a
directory do. A place and everything under it is removed from the command line, with
`verse charter -R`; no tool does it.

The flow verbs take a selection as `verse flow`'s flags give it: `task`, or `charter`
(`<address>[:<rule>]`, with `recursive` for the charters beneath it) and `subject`, the one left
out being every one. `verse_flow_cancel` takes a task alone: a charter's or a subject's work is
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

`verse_wait` answers what `verse ask --watch` and `verse question --watch` print: an ask of
yours asked, answered, asked again or gone, and a question starting to wait on you or settling.
It answers at once when there is something after `since`, otherwise at the next thing, or with
nothing at the `timeout` (300 seconds by default, at most 3600). The server opens one watch on
the first call and keeps it until it ends, and remembers the last 1024 lines. A cursor older
than that is refused, and so is one from an earlier server, after the host has started it
again; call without `since`, and read `verse_question` and `verse_ask_list`, to catch up.

`verse_task_events` answers a task's transcript, frame by frame. It says when the task has
ended, after which nothing follows. A stream that closes while the task still runs is said,
and the next call from the cursor goes on.

Only these two watch, and each is narrow: your own asks and questions, or one task. There is no
tool that watches all tasks or the data, since a watch that wide costs the verse more than one
agent's session is worth.

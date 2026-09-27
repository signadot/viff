# proposal: the surface of the user face of verse's MCP server, interactive part

Status: a proposal, for discussion. It is under the umbrella 55crejnf. It is one of two
issues for the interactive part of the user face. This one is about the surface: the
tools. The other is about identity and authority.

Related issues: 55crejnf (the three faces), 9ce3bva0 (the rendezvous), w22rnvsj (the
ask), js1e345a (`verse/claude-gates`).

## Terms

The terms **door**, **face** and **rendezvous** are defined in the umbrella issue
55crejnf. Two more are used here:

- The **person** is the one whose agent uses this server.
- The **host** is the program that runs the agent and starts the server. It decides
  which tools the agent can use, and it can ask the person before a tool runs.

## The proposal

We propose a command, `verse mcp`, that serves the interactive part of the user face.
The host of a person's agent starts it. It talks over standard input and output, and
it acts as the person. Who the person is, and what the server may do for them, is the
subject of the other issue.

Through it, the agent of a person can do what the person does interactively with
verse: read, raise a rendezvous, answer one, speak to a running task, pause, resume or
cancel one, and receive answers.

It does not manage charters. That is the second issue under the umbrella.

## An example

A person works in their own agent and says: "Ask Ana to review verse/ask by Friday,
then merge it."

1. The agent reads verse to find what it needs.
2. The agent calls the tool that raises a rendezvous. The host shows the person the
   question, the answerer, the deadline and the consequence, and asks for a yes.
3. The person says yes. The ask is written as a record of the person.
4. The agent waits. When Ana answers, the agent is told.
5. After a yes from Ana, the consequence runs with the authority of the person.

This is the same sequence as in Slack. The difference is who confirms: in Slack, verse
asks the person in the thread. Here, the host asks the person before the tool runs.

## The tools

| Tool | What it does | Writes | The command today |
|---|---|---|---|
| read one record | answers the record at an address | no | `verse data <address>` |
| list | answers the names below an address | no | `verse data <address> --paths` |
| my questions | answers the questions that wait for the person | no | `verse gate` |
| raise | raises a rendezvous: a question, the answerers, a deadline, a consequence | yes | `verse data ask <name> --put`, until `verse ask` exists |
| answer | answers one question that waits for the person | yes | `verse gate <id> --answer <option>` |
| say | sends words to a running task of the person | yes | `verse task say <id> <text>` |
| change my ask | moves the deadline of an ask of the person, or withdraws it | yes | a write to the record |
| pause, resume | holds a running task of the person at its next boundary, or lets it continue | yes | `verse flow suspend`, `verse flow resume` |
| cancel | ends a running task of the person | yes | `verse task cancel <id>` |
| wait | waits until an answer arrives, or a new question waits for the person | no | `verse watch` |

The names of the tools are not decided here. Each tool that writes is a separate tool,
so a host can refuse it by name.

Pause, resume and cancel are not a rendezvous: one party does them alone. The server
offers them because a person does them interactively. They need the permission
`control` on the task.

## Rules

1. **The tools add no operation.** Each tool calls the operation that the command line
   calls. So the ask command of `verse/ask-next` comes first.
2. **A read follows the decision made earlier.** What is not there and what the person
   cannot read get one answer. A read that failed is an error.
3. **An answer from a tool is never cut in the middle of a record.** If a record is too
   long, the tool says so and answers a part that is complete.
4. **The agent is told the answer.** The tool `wait` takes a cursor from the answer
   before, so nothing between two calls is missed.
5. **The tool `wait` has no time limit.** It waits until there is something to say, or
   until the agent stops it. The server sets no limit of its own.

## Hosts that limit a tool call

A host that stops a tool call after a fixed time makes every tool that waits
impossible. This is a defect of the host, and it is reported to the host's makers. It
is not a reason to put a limit in the tool.

Until a host is corrected, the person runs `verse watch` in the background, and the
host wakes the agent when it prints a line.

## Why the host is the confirmation

In the Slack conversation, a model reads and proposes, and the person confirms in the
thread. Here the host already does this: it shows the tool call and asks the person.
So the server does not add a second confirmation. It must make the tool call easy to
read: the question, the answerer, the deadline and the consequence are separate
parameters, not one text.

## What this proposal needs from the work

- The ask command in `verse/ask-next`.
- A default user for a verse on the same machine. This exists on `verse/build-on`.
- `verse watch` must be able to show answers to the asks of the person, and new
  questions for the person.

## Open questions

1. **The session of a developer in the design js1e345a.** That design gives "what is
   waiting on me" and "answer this" to a developer through the server of a task. This
   proposal moves them here. The design must agree.
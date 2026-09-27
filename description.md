# proposal: three faces for an MCP server of verse — user, task and chat

Status: a proposal, for discussion. This is an umbrella issue. Each face gets its own
issue, listed at the end.

Related issues: 9ce3bva0 (the rendezvous), w22rnvsj (the ask), js1e345a
(`verse/claude-gates`).

## Terms

- An **MCP server** offers tools to an agent. The program that runs the agent, the host,
  starts the server and decides which tools the agent can use.
- A **door** is a way for a person to enter verse. At a door, what the person says or
  does becomes a record in verse. The record is written as that person.
- A **face** is the role of an MCP server: who is on the other side, and whose
  authority the server uses. This document proposes three faces.
- A **rendezvous** is the building block that issue 9ce3bva0 proposes: one party raises
  it, the other party answers, it has a deadline, and it has a consequence.

## The proposal

We propose that an MCP server of verse has exactly one face. There are three faces.

| Face | Who is on the other side | The server acts as | Who says who that is | How long it lives |
|---|---|---|---|---|
| **User** | the agent of one person | that person | the configuration of that person | one session of the person |
| **Task** | a task that verse started, on a runner | the task | verse, when it starts the task | the task |
| **Chat** | a chat bot in a shared place, for example Slack | the person who wrote each message | the chat platform | as long as the bot runs |

The user face has two parts:

- **Interactive**: what a person does now. Read verse, raise a rendezvous, answer one,
  and receive answers.
- **Charters**: managing automation that stays installed. Install a charter, change
  it, remove it.

The two parts are separate sets of tools. A host can allow the first part and refuse
the second part.

## Why we need the distinction

Several designs already name an MCP server. They do not mean the same thing.

- **The ask design** (w22rnvsj) names "the Claude (MCP) door": a person asks through
  their agent. This is the user face.
- **The design for a governed Claude session** (js1e345a) starts one MCP server for each
  task. The server carries tools, steering messages and permission prompts between
  the session and verse. This is the task face. The same design also uses that server
  for a developer's own session: "what is waiting on me" and "answer this". That is
  the user face, in the same server.
- **The verse tools** (`verse_get`, `verse_list`, `verse_docket`, `verse_act`) exist
  today for tasks that run inside the verse process. They are task face tools, but
  three of them are simple reads that a person's agent also needs.
- **The Slack conversation** is built as a connector and a charter. It is
  not an MCP server today. A chat bot that runs outside verse would need one.

The faces differ in authority. A tool that is safe in one face is not safe in another:

- In the task face, verse decides who the task acts as. The agent cannot choose.
- In the user face, the agent acts with everything the person is allowed to do. The
  protection is the host, which asks the person before a tool runs.
- In the chat face, the person changes with each message. A tool that remembers "the
  user" from the last message acts as the wrong person.

## What the faces share

- **The rendezvous.** Every face offers it. A person raises one through their agent. A
  task raises one to its person. A chat bot raises one for the person who wrote.
- **One set of operations.** The command line and every face use the same operations
  and write the same records. A face adds no operation of its own.
- **Waiting for an answer.** A rendezvous is answered later. A host does not pass MCP
  subscriptions to its agent. So each face needs a tool that waits for the answer, or
  a command that prints answers as they arrive.

## What this proposal serves

- Charter purpose 2: "To govern that work whoever does it … or the agents, tools and
  people already in use." A person who already works with their own agents enters
  verse through the user face, without a change to verse itself.
- Charter purpose 5: "To act so as to be something others can freely build on and
  use." A face is a small, stated surface that others can build on.

## Open questions

1. **A task on a remote runner.** The code says that a tool which calls verse from a
   remote runner "would be a second write door with its own idea of who is writing"
   (`versed/versetools.go`). The task face must answer this: verse gives the task its
   identity at the start, and the server accepts no other.
2. **Identity in the user face.** Today a caller says who it is, and verse does not
   check it. The user face lets an agent make that statement. We must decide if this is
   acceptable on a machine that one person uses, and what changes for a shared verse.
3. **A tool that waits for a long time.** Hosts stop a tool call after some time. The
   design js1e345a lists this as not yet tested. If a long wait does not work, the
   user face needs the command that prints answers.
4. **Is the chat face an MCP server?** Today the Slack conversation runs inside verse.
   The chat face is needed only if a chat bot runs outside verse.

## Issues under this umbrella

Four ideas, five issues. The interactive part of the user face has two issues.

1. User face, interactive:
   - the surface: a5mvads7
   - identity and authority: v27eah17
2. User face, charters: kqa3sawf
3. Task face. Not filed.
4. Chat face. Not filed.

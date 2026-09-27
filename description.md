# proposal: the user face of verse's MCP server, charters part

Status: a proposal, for discussion. It is under the umbrella 55crejnf.

Related issues: 55crejnf (the three faces), a5mvads7 (the surface of the interactive
part), v27eah17 (identity and authority).

## Terms

The terms **door**, **face** and **rendezvous** are defined in the umbrella issue
55crejnf. The terms **person** and **host** are defined in issue a5mvads7. One more is
used here:

- A **charter** is a set of rules that stays installed. A rule reacts to a change in
  verse. Charters are the automation of verse.

## The proposal

We propose a second set of tools in `verse mcp`, for charters. Most of them read. The
agent of a person uses them to answer questions about the automation:

- What is installed, and where?
- What does this rule do?
- Why did this happen, or why did it not happen?

Two tools write: install and remove. They are separate tools, so a host can allow the
reading tools and refuse the writing tools.

This set is separate from the interactive set of issue a5mvads7. A host can start the
server with one set, or with both.

## The tools that read

| Tool | What it answers | The command today |
|---|---|---|
| read a charter | the charter at an address: all of it, or to a given depth | `verse charter <address> [--depth n]` |
| read one rule | one rule of a charter, or a part of the rule | `verse charter <address> rules <name>` |
| list | one line for each rule and each level below an address | `verse charter <address> --paths` |
| read the flow of a subject | what ran for a subject, what is running, what comes next, and what holds it | `verse flow show <subject>` |
| list running work | the tasks that run now, and what is suspended | `verse flow` |

## The tools that write

| Tool | What it does | The command today |
|---|---|---|
| install | installs a charter at an address | `verse charter <address> --add --file <file>` |
| remove | removes one package, or one rule of a package | `verse charter <address> --rm` |

The server does not offer the removal of a place with everything below it
(`--rm-recursive`). A person does that from the command line.

## Rules

1. **The rules of issue a5mvads7 apply:** the tools add no operation, a read follows the
   decision made earlier, and an answer is never cut in the middle of a record.
2. **Identity and authority are as in issue v27eah17.** The server acts as the person.
   A charter that the agent installs is a charter of the person.
3. **An install answers what verse answers.** If verse accepts the charter with a
   warning, or holds a rule, the tool says so.

## Why reading comes first

A charter stays installed and acts later, when nobody watches. So a wrong charter costs
more than a wrong question. Reading has no such cost, and it is what a person needs
most often: to understand what the automation did.

## Open questions

1. **Should the first version write at all?** A first version with only the reading
   tools is possible.
2. **Is proposing a rule a rendezvous?** Scenario 17 of issue w22rnvsj is "Whenever a PR
   touches perms/, ask a reviewer." The agent could raise it as a rendezvous to the owner of
   the place, and the consequence installs the rule. Then install is not a tool of its
   own.
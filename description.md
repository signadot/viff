# proposal: identity and authority in the user face of verse's MCP server

Status: a proposal, for discussion. It is under the umbrella 55crejnf. It is one of two
issues for the interactive part of the user face. This one is about identity and
authority. The other is about the surface: the tools.

Related issues: 55crejnf (the three faces), 9ce3bva0 (the rendezvous).

## Terms

The terms **door**, **face** and **rendezvous** are defined in the umbrella issue
55crejnf. Three more are used here:

- The **person** is the one whose agent uses this server.
- The **host** is the program that runs the agent and starts the server.
- An **identity** is the name that verse writes on a record to say who made it, for
  example `user:ana@example.com`.

## The proposal

1. **The server acts as the person.** Every read and every write uses the identity of
   the person.
2. **The person gives the identity, not the agent.** The server takes the identity from
   the configuration of the person, in the same way as the command line. No tool has a
   parameter that names an identity.
3. **The server has the authority of the person, and no more.** It can do what the
   person is allowed to do. When the agent raises a rendezvous, the consequence uses
   the authority of the person, as issue 9ce3bva0 proposes.
4. **The host protects the person.** The host decides which tools the agent can use,
   and it can ask the person before a tool runs. Each tool that writes is a separate
   tool, so the host can refuse it by name.

## What is true today

- A caller says who it is, and verse does not check it. The command line sends the
  identity from `--as`, or from `agent:` or `user:` in the configuration.
- If nobody is configured, and verse is on the same machine, the command line acts as
  `user:<login>@localhost`. This is on the branch `verse/build-on`.
- The Slack connector says who wrote each message, and verse accepts that.

So this server does not make identity weaker than it is. But it puts the statement "I
am this person" in a place where an agent is active.

## Why the agent must not choose the identity

An agent reads text that other people wrote: issues, comments, web pages. Such text can
contain instructions. If a tool had a parameter for the identity, such text could make
the agent act as another person. With no parameter, the worst case is that the agent
acts as its own person, and the host can ask that person first.

## Open questions

1. **A shared verse.** On a machine that one person uses, a stated identity is
   acceptable. For a verse that several people share, the server must prove the
   identity. What is the proof: a token, or the login of the chat platform, or another
   thing?
2. **Does a record say that an agent made it?** A rule writes as `via:<its address>`,
   so a reader sees that a rule acted for its installer. Should a write through this
   server also say that an agent acted for the person?
3. **`agent:` in the configuration.** The command line can send an agent identity.
   Does this server ever use it, or is it always the person?
4. **A host that allows everything.** Proposal 4 depends on a host that asks. If the
   person sets the host to allow every tool, there is no confirmation. Should the
   server refuse to write in that case, or is that the choice of the person?
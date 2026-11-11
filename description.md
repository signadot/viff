# mcp: a stdio server's tools, offered and gated as a remote server's are

Most MCP servers people run are programs speaking on their stdio. The `mcp` system calls remote servers only. A task can run a stdio server as a command (its own shell, or `secret-exec`), but that gives the agent a process to write JSON-RPC to: none of the server's tools are offered to it, and no call is decided. Raised by Daniel's question on PR #7 (pullrequestreview-5864267561).

This issue is the design. Nothing here is decided.

## What is wanted

A stdio server's tools are a task's tools (`mcp-<server>-<tool>`), each use is the step `mcp.call` with `server`, `tool` and `arguments`, and the environment, the autonomy and the ceiling decide it, exactly as for a remote server.

## A reading that adds little

Today a server is a name in the runner's configuration, and everything above it goes by that name: a ceiling block names one server as a literal ("a server is what a doer is trusted with"), the offer follows the ceiling, the environment names the server and its `call` shapes, and the step carries the name. Only `mcp.servers.<name>` knows how the server is reached (`url`, `auth`, `timeout`).

So a stdio server could be a server whose configuration says how to start it where a remote one says where it is:

```tony
mcp:
  servers:
    linear:    {url: "https://mcp.linear.app/mcp/readonly", auth: {secret: linear}}
    github_ro: {command: [github-mcp-server, stdio, --read-only], secret: github-token, as: GITHUB_TOKEN,
                network: {mode: allow, tcp-client: {host: api.github.com}}}
    github:    {command: [github-mcp-server, stdio], secret: github-token, as: GITHUB_TOKEN,
                network: {mode: allow, tcp-client: {host: api.github.com}}}
agents:
  claude:
    systems:
      mcp:
        ceiling:
        - {server: github_ro}
        - {server: github, call: [{tool: create_issue, arguments: {owner: acme}}]}
```

Under this reading the two questions answer themselves, which is the argument for it.

**How is a server the runner installs named in the offer?** By its name, as any server is. The offer, the ceiling, the environment and the rows do not change and do not learn that the server is a program. Where the program comes from is the configuration's: on the image's `PATH`, or a package of the `pkg` system (#8) the server's entry names, installed runner side as a task's are.

**Are the program's flags gated, `--read-only` say?** Not per call and not per spawn: they are the operator's, fixed under the name. `github_ro` and `github` are two servers, and a ceiling gives a doer the first and not the second. That is the pattern `mcp.md` already teaches for Linear's read-only endpoint, and for a second workspace (`linear_acme`).

## Where that reading runs out

Walk one case: a server whose flag is the task's own, `--repo acme/lib` or a filesystem server's root.

- With flags fixed under the name, the operator writes one server per repo. Fine for three, not for a hundred, and a new repo is a runner restart.
- With flags the spawn chooses, the server's entry holds a pattern, the environment's `mcp` block gives the arguments, and the ceiling bounds them with a shape, as `secret.exec`'s `argv` is bounded. They would be decided where the environment is, at install (verse refuses a spawn whose environment is beyond the ceiling), since the server starts once as the task is built and there is no step for a row to decide. Whether a session's start should be a step a row can ask about (`mcp.start`?) is then a real question: approving a `--read-write` start is approving every call after it only as far as the `call` shapes allow, so it may add nothing.

A suggestion, not a decision: begin with flags fixed under the name, and take up spawn-chosen arguments when a server shows up whose flag is the task's own.

## The bridge

"Bridge" need not mean loopback HTTP. The SDK has an `IOTransport` (a reader and a writer) beside the streamable one, and `mcpclient.Sessions.connect` is the one place a transport is chosen; `Call`, `List` and the session's reopening sit above it. A stdio server could be a second transport there, over the stdio of a process the runner starts confined. Compared with a stdio-to-HTTP program on loopback:

| | transport in the runner | loopback HTTP bridge |
|---|---|---|
| new programs | none | a bridge per server, or one multiplexing |
| who can reach the server | the runner's client only | anything on the host that can reach the port |
| lifetime | the task's session: started at first use, killed with the task | a service the operator runs, shared by tasks |
| isolation between tasks | a process per task | one process, every task's calls |

The last two rows cut both ways: a shared service starts once, and a per-task process costs a start per task and gives each task a server that remembers only that task.

## What the process runs in

`secret.exec` already confines a command with one secret, a network, a program held to the runner's own paths and the install root, and its stdio on sockets. The differences to settle:

- **The workspace.** A command run with a secret runs over the task's workspace. A server the runner starts for a task need not see it, and should not by default: it is the operator's program, not the task's. A filesystem server wants it, which is the spawn-chosen-arguments question again.
- **The network.** `secret.exec` runs under the task's network. A remote server's calls leave from the runner and no task network bounds them. A stdio server's network is neither: the server's entry would name it, as above.
- **When it dies.** A process that exits answers the next call `mcp.ErrConnectionClosed` with nothing sent, which is the case the session's reopening already handles (ar7brzgnh12ksx6syhn0): the server is started again and the call sent once. Whether a server that keeps state across calls should be restarted silently is open.
- **Its stderr.** Somewhere the operator reads, with the secret's value redacted as `secret.exec` redacts it.

## Open

1. Is "a stdio server is a server whose entry says `command`" the model, or should the offer distinguish the two kinds?
2. Flags fixed under the name only, or spawn-chosen arguments from the start?
3. A transport in the runner, or a loopback bridge?
4. Does the process see the workspace, ever?
5. How a server's entry names a `pkg` package to install, once #8 is in.
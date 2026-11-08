# secret-exec: input on the I/O socket

The run's I/O socket carries the command's stdout and stderr today, and its stdin is empty: `secret-exec` deferred input when it was built ([pa35grmz]). Accepting input on the socket is the missing piece for:

- **Commands that read stdin**: a migration that asks for confirmation, `gpg --passphrase-fd 0`-style flows, any filter run over data the task produces after the command starts.
- **The task-as-client stdio MCP path**: a spawn admits a stdio MCP server's command in a `secret` block, the task starts it with `secret-exec` and speaks JSON-RPC over the run's I/O socket. Approving the command approves the session — honest, and the same promise `secret-exec` makes for any command. (The runner-as-client path, where the operator names a `command` kind of server under `mcp:` and each call is gated, is the stronger shape and is tracked with the mcp system; this issue only unblocks the socket.)

What exists: the run streams stdout and stderr back over two sockets, redacted, mirrored to `.verse/exec/<id>.out`/`.err`; the command's stdin is wired to nothing.

Open questions:

1. **One socket or a third?** Today's two are read-only streams. Input could be a write side on the existing control/stdout socket, or a third socket opened with the run.
2. **Redaction on the way in?** Output is redacted; input is the task's own words going to a process that already holds the secret, so there is nothing to redact — but worth saying explicitly in the words.
3. **Shapes**: does a row get to say "no stdin" (a command admitted for its argv alone, refused an interactive channel)? A `stdin: false` knob on the exec shape would keep today's behaviour the default.

Where it goes: on `verse/secret-exec`, the branch that owns the exec op and its channel (`pkg/runner/runnerd/delivery`) — the stack the team reads is pkg on mcp on secret-exec, and this is secret-exec's feature. After the pkg stack ([2k5art3xh12ksjcyy1n0]) ships, so the stack is not rebased under the staging run; `verse/mcp` and `verse/pkg` rebase onto the new tip then.

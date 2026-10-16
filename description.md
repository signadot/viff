# verse-call-tool: a task's tools from the shell — ask, push, fetch, the pull-request tools, and whatever is added next

A program running in a task's workspace — a coding agent the environment names under `agents:`, or a script — has no way to call the runner's tools: `ask`, `push`, `fetch`, `create-pr`, `update-pr`. Only the verse agent and codex have them, because each is wired to the loop in Go. A shell-called binary closes that for every program at once.

**The binary.** `verse-call-tool` runs in the workspace and calls one tool of the task that owns the workspace:

    verse-call-tool                          # the tools on offer here, with their arguments
    verse-call-tool push -branch verse/x     # one call; flags are the tool's parameters
    verse-call-tool ask -question "ship it?" -options yes -options no -within 30m
    verse-call-tool <tool> -input '<tony or json document>'   # the whole input as a document

The result is stdout. Exit 0 did it, 3 the call was refused (an answer the caller re-plans on, text on stdout), 1 it failed (stderr), 2 usage.

**The channel.** `runner/runnerd/calltool`: HTTP over a unix socket, tony bodies through codec, two routes — `GET /tools` lists the tools as the model sees them (name, description, params, required), `POST /call` runs one. The task process serves it; the socket's path reaches the program as `VERSE_TOOLS`. The server end takes a `Call` function, so the host decides what performing a call means: for a program driver, putting the call in front of the loop as an ordinary action, gated by the autonomy and narrated, as the codex agent does for codex's calls. The binary knows nothing of tools by name: the list grows on the server and the flags follow.

**Steps.**
1. The package and the binary, tested on their own (this issue's first commit).
2. The binary on the runnerd image; the task process serves the socket and installs the binary in each task's workspace; the command driver proposes each call as an action.
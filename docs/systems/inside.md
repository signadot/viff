# files, commands and agent

Three systems are not outside viff. They are what an agent does inside its run: it writes files,
runs commands and calls sub-agents. A spawn gives them in its environment as it gives `git`, the
runner intercepts each act for the task's autonomy to decide, and a runner offers them as it
offers `git`.

| System | Verb | Params | What the runner does |
|---|---|---|---|
| `files` | `write` | `path` | stops a write the agent asks for before it happens ([advisory](#a-row-on-files-or-commands-is-advisory)) |
| `commands` | `run` | `command` | stops a command before it runs, and matches the command as one string ([advisory](#a-row-on-files-or-commands-is-advisory)) |
| `agent` | `call-sub-agent` | `agent` | stops a call to a sub-agent, a child run inside the runner |

## What a charter sees

Nothing. No iod reads a task's workspace or its shell. What a task did is on its own record:
each step is an event that names the verb and its params ([tasks](../reference/tasks.md)).

## What a charter writes

An environment and rows. The environment gives the systems, the autonomy says what happens to
each act within them:

```tony
environment:
  systems:
    files: {}
    commands: {}
autonomy:
- {match: {system: files, verb: write, path: !regexp "^db/"}, then: ask}
- {match: {system: commands, verb: run, command: !regexp "^make ship"}, then: ask}
- {then: allow}
```

A block that names no verb, `files: {}`, gives every verb of its system. "No shell" is leaving
`commands` out of the environment: the tool is then not offered at all, rather than refused at
each call. Here `agent` is left out, so the task calls no sub-agent.

## The network

What a run may reach is the `network` system, given in the environment as any system is. Its
`mode` is fixed when the run starts, and under `allow` its `tcp-client` shapes name what it may
connect to:

```tony
environment:
  systems:
    network:
      mode: allow
      tcp-client: [{host: api.github.com}, {host: pypi.org, port: 443}]
```

| `mode` | What the run reaches |
|---|---|
| `none` | nothing. This is the network when `network` is left out. |
| `open` | the host's network, which the runner does not see into |
| `allow` | only what a `tcp-client` shape matches, through a proxy the runner serves the task: `host`, with `port` or without it (then any port); `*.example.com` is every host under `example.com`, and an address is written in its short form (`2001:db8::1`) |

Under `allow`, each connection is the step `network.tcp-client`, with `host` (lower case, no
trailing dot) and `port`, decided as a step is: outside the shapes it is refused, saying what
they are; within them a row on the network allows or denies it. A connection can't wait for an
answer, so a row on the network never asks, and the default row doesn't decide a connection: the
shapes are what the task reaches, and a host they give is reached unless a row on the network
denies it (`{match: {system: network, host: api.github.com}, then: deny}`), whatever the default
row says. The runner's network ceiling holds after the rows. A replaced autonomy decides the
next connection. What the proxy refused is on the task's record, under `refused`, by destination
and how often.

An agent always reaches its model: under `none` or `allow` the runner's proxy lets the model's
host through, outside any step.

A runner says which modes its sandbox builds, under the network's `builds`. One that runs its
tasks unconfined, such as `viff up`'s or one on macOS, builds only `open`, so a spawn there says
`network: {mode: open}` and is refused without it ([what it offers](../reference/runnerd.md#what-it-offers)).

## A row on files or commands is advisory

A row on `files` or `commands` decides the act as the agent names it. It does not decide every
way to the same result:

- A write through the shell, such as `echo x > db/x.sql`, is a `commands.run` step. It is not a
  `files.write` step, so a row on `files` with `path: !regexp "^db/"` does not see it.
- A `command` pattern matches the whole command string. `!regexp "^git push"` matches
  `git push origin x`, but not `cd x && git push`; `!regexp "git push"` matches both.

So treat these rows as advice the runner checks, not as a wall. What must hold, write where the
runner does it: a push is the runner's own act (`git.push`), and the network is built into the
run. Enforcing `files` and `commands` in the kernel is planned in issue bst6zbs0.

## What a task can do

What the one doing the work does:

- The viff agent writes files, runs commands and calls sub-agents with its own tools, and
  reaches the network from its shell.
- Codex does the same through its own sandbox, and Claude Code through its permission prompts.
  The runner stops each at the same places.
- A script is one action, not a series of steps, so nothing inside it is stopped. Only its
  network is confined.

Viff refuses to install a spawn whose environment gives a system the runner's offer does not
hold, or one beyond the offer's ceiling. Codex and Claude can't run without `commands.run` and
`files.write`, which their offers say (`requires`), so a Codex or Claude spawn whose environment
leaves them out is refused, naming `commands: {}` or `commands: {run: {}}` to add.

## Where the credential lives

Nowhere: none of these uses a credential. The runner stops each call for the autonomy to decide,
on the systems its offer holds for the agent.
[What it offers](../reference/runnerd.md#what-it-offers) has the details.

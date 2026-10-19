# files, commands and agent

Three systems are not outside verse. They are what a doer does inside its run: it writes files,
runs commands and calls sub-agents. The runner intercepts each of these for the task's policy to
decide. A charter writes rows about them as it writes rows about a push, and a runner offers
them as it offers `git`.

| System | Verb | Params | What the runner does |
|---|---|---|---|
| `files` | `write` | `path` | stops a write the doer asks for before it happens ([advisory](#a-row-on-files-or-commands-is-advisory)) |
| `commands` | `run` | `command` | stops a command before it runs, and matches the command as one string ([advisory](#a-row-on-files-or-commands-is-advisory)) |
| `agent` | `call-sub-agent` | `agent` | stops a call to a sub-agent, a child run inside the runner |

## What a charter sees

Nothing. No iod reads a task's workspace or its shell. What a task did is on its own record:
each step is an event that names the verb and its params ([tasks](../reference/tasks.md)).

## What a charter writes

Rows. The policy says what happens to each act:

```tony
policy:
- {match: {system: files, verb: write, path: "db/**"}, then: ask}
- {match: {system: commands, verb: run, command: "make ship*"}, then: ask}
- {match: {system: agent}, then: withhold}
- {then: allow}
```

"No shell" is `{match: {system: commands}, then: withhold}`. The tool is then not offered at
all, rather than refused at each call.

## The network is the launch's

What a run may reach is not a system and has no rows. It is a fact of the launch, under the
doer's `with`, as its repositories are:

```tony
doer:
  agent: verse
  with:
    network: {mode: allow, allow: [api.github.com, "pypi.org:443"]}
```

| `mode` | What the run reaches |
|---|---|
| `none` | nothing. This is the network when `network` is left out. |
| `open` | the host's network |
| `allow` | only the hosts in `allow`, each `host` or `host:port`; a host with no port is its web ports, 80 and 443, `*.example.com` is every host under `example.com`, and an address is written in its short form (`2001:db8::1`, `[2001:db8::1]:8443`) |

The runner builds the network once, when the run starts. Nothing asks about a connection, and a
change to the policy does not change it. An agent always reaches its model: under `none` or
`allow` the runner allows the model's host beside the ones the launch names.

A runner offers the networks its sandbox can build. One that runs its tasks unconfined, such as
`verse up`'s or one on macOS, builds only `open`, so a launch there says `network: {mode: open}`
and is refused without it ([what it offers](../reference/runnerd.md#what-it-offers)).

## A row on files or commands is advisory

A row on `files` or `commands` decides the act as the doer names it. It does not decide every
way to the same result:

- A write through the shell, such as `echo x > db/x.sql`, is a `commands.run` step. It is not a
  `files.write` step, so a row on `files` with `path: "db/**"` does not see it.
- A `command` pattern matches the whole command string. `git push*` matches
  `git push origin x`, but not `cd x && git push`.

So treat these rows as advice the runner checks, not as a wall. What must hold, write where the
runner does it: a push is the runner's own act (`git.push`), and the network is built into the
run. Enforcing `files` and `commands` in the kernel is planned in issue bst6zbs0.

## What a task can do

What its doer does:

- The verse agent writes files, runs commands and calls sub-agents with its own tools, and
  reaches the network from its shell.
- Codex does the same through its own sandbox. The runner stops it at the same places.
- A script is one action, not a series of steps, so nothing inside it is stopped. Only its
  network is confined.

Verse refuses to install a row on a system that the doer's offer does not intercept.

## Where the credential lives

Nowhere: none of these uses a credential. The runner stops each call for the policy to decide,
on the systems its offer names for the doer.
[What it offers](../reference/runnerd.md#what-it-offers) has the details.

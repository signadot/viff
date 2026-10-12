# runnerd

`runnerd` is the binary that holds the [runners](../glossary.md#runner) that verse ships. A
runner is a program that does the work a spawn describes. Verse reaches it over a URL.

| Subject | Page |
|---|---|
| what a runner is, and how verse uses one | [../runners.md](../runners.md) |
| the installed runner, its commands and routes | [runners.md](runners.md) |
| the contract a runner serves | [../task-control.md](../task-control.md) |

`runnerd` is a binary of its own, for the reason `iod` is ([iod.md](iod.md)). It is in the same
image as `verse`. Each subcommand runs one runner:

```
runnerd verse -config <file>   verse's own agent, and scripts
```

The same runners also run embedded. `verse up` hosts the verse runner in its own process and
reaches it by Go calls, with no `runnerd` process. Both use package `runner/runnerd`, which holds
a runner's tasks and serves them. A central deployment is meant to run no work in verse itself,
with each runner a `runnerd` of its own; today an ask's `then:` still runs in verse.

## The verse runner

The verse runner runs verse's own agent, which reaches its model through Portkey, and scripts.
It serves the task contract, and `GET /describe`, which says what it offers.

```sh
PORTKEY_API_KEY=… runnerd verse -config verse-runner.tony
verse runner add verse-runner http://localhost:9802
verse task run --runner verse-runner …
```

On today's runner API, a launch names a driver. `llm` runs the agent, and `scripted` runs the
prompt as a script. A launch that names no driver runs the agent when the runner has a model
key, and a script otherwise.

A script runs by its own `#!` line, which can name any program in the runner's image that reads
a file: `#!/bin/sh`, `#!/usr/bin/env python3`, `#!/usr/bin/env node`. The line is read as
the kernel reads it: the program, and at most one argument. A script with no `#!` line runs with
`bash`.

Each task runs in a directory of its own, which is removed when the task ends. A task's shell
sees only the variables that `task.DefaultShellEnv` names, so it never sees the runner's
secrets.

### Configuration

The configuration is a file. Its format comes from its name (`.tony`, `.json`, `.yaml`). A key
that the runner doesn't know is refused when the runner starts.

```tony
listen: ":9802"
models:
  portkey:
    virtualKey: verse-default
  model: gpt-6.1-sol
annotations:
  verse.portkey/virtual-key: string
```

| Key | What it holds | Default |
|---|---|---|
| `listen` | the address the runner serves verse on | `:9802` |
| `models.portkey.virtualKey` | the Portkey virtual key, when a spawn doesn't pick one. It is a routing label, not a secret. | none |
| `models.model` | the agent's model, when a spawn doesn't pick one | the agent's own |
| `annotations` | the annotations a spawn may set, with the type of their value. The verse runner knows one, `verse.portkey/virtual-key`: with it, the virtual key a spawn picked is used. | none |

Secrets are never in the file. The runner reads them from its environment, where a deployment
puts a Kubernetes Secret.

| Variable | What it holds |
|---|---|
| `PORTKEY_API_KEY` | the Portkey account key. Without it, the runner offers scripts only. |
| `PORTKEY_BASE_URL` | the Portkey endpoint. Default: Portkey's own. |

### What it offers

`GET /describe` answers what the runner offers, from its configuration:

```tony
offers:
- agent: verse
  with: {model: any}
  intercepts: [files.write, commands.run]
  annotations: {verse.portkey/virtual-key: string}
- script: true
```

A script offer takes no `with`: a script names what runs it in its own `#!` line.

The agent is offered only when `PORTKEY_API_KEY` is set. Verse doesn't read `/describe` yet.

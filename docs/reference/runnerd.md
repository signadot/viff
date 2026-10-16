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
runnerd verse -config <file> [-sandbox bwrap|none]   verse's own agent, scripts, and the programs
                                                      its configuration names, each task in a
                                                      workspace of git checkouts when it declares
                                                      environments
```

The same runners also run embedded. `verse up` hosts the verse runner in its own process and
reaches it by Go calls, with no `runnerd` process. Both use package `runner/runnerd`, which holds
a runner's tasks and serves them. A central deployment is meant to run no work in verse itself,
with each runner a `runnerd` of its own; today an ask's `then:` still runs in verse.

## The sandbox

`runnerd` runs each task in a process of its own, inside a sandbox. Everything the task runs is
inside it: the agent's loop and its shell, or a script.

| `-sandbox` | What a task's process gets |
|---|---|
| `bwrap` (the default) | its own pid, ipc and uts namespaces, its own `/proc`, `/dev` and `/tmp`, and no capabilities. The system (`/usr`, `/bin`, `/lib`, `/etc`, …) is read-only, and the task's directory is the only path it may write. It doesn't see the runner's other tasks. The network is the runner's. |
| `none` | nothing: it runs as the runner's user, in the runner's filesystem. Logged when the runner starts. |

With `bwrap`, the runner refuses to start when bubblewrap isn't installed or can't run. bwrap
needs unprivileged user namespaces:

- **Ubuntu 24.04 and later** restrict them with AppArmor
  (`kernel.apparmor_restrict_unprivileged_userns=1`), and bwrap fails with *setting up uid map:
  Permission denied*. Allow bwrap with an AppArmor profile, or run with `-sandbox none`.
- **Kubernetes:** the pod needs the three settings the coderunner's pod has: `hostUsers: false`,
  `procMount: Unmasked` and `seccompProfile: Unconfined`. The coderunner section of
  `deploy/README.md` says why each one is needed.
- **macOS** has no bwrap: run with `-sandbox none`.

The task's process has none of the runner's environment. Its stdin carries the runner's
configuration and the secrets the runner read from its environment, and its environment has only
what `task.DefaultShellEnv` names. `HOME` is the task's directory, and `TMPDIR` is `/tmp`. A
process that exits before its task ends fails the task, with the process's exit status.

### What keeps the secrets from the task

The task's process holds the runner's secrets (the Portkey key) in its memory, and the task's
shell, or its script, runs inside the same sandbox: as the same uid, since bwrap maps the caller
to itself, and in the same pid namespace, so it can see that process.

| What | Covered by |
|---|---|
| the environment, and what a subprocess inherits | the secrets come on stdin, never in an environment |
| `/proc/<pid>/mem`, `/proc/<pid>/environ`, and ptrace | the task's process makes itself non-dumpable (`PR_SET_DUMPABLE`) before it serves, so a same-uid process without `CAP_SYS_PTRACE` is refused them. Without it, only the node's `kernel.yama.ptrace_scope` would decide, and its default, 0, allows reading another process's memory. bwrap also drops every capability (`--cap-drop ALL`), which it would otherwise leave to a runner run as root. With `-sandbox none`, a task of a runner run as root keeps its capabilities: its memory is still refused, its `environ`, which holds no secret, is not. |
| a separate uid for the task | **not done**: the task runs as the runner's uid. A uid boundary would not depend on dumpability. |

On a platform without procfs (macOS, with `-sandbox none`), nothing makes the process
non-dumpable.

`verse up` doesn't sandbox: its tasks run in verse's own process, as the user who ran it.

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

Each task runs in a directory of its own, in a process of its own ([the sandbox](#the-sandbox)).
The directory is removed when the task ends. A task's shell sees only the variables that
`task.DefaultShellEnv` names, and can't read the runner's secrets from the task's process
([what keeps the secrets from the task](#what-keeps-the-secrets-from-the-task)).

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
| `models.baseUrl` | the model endpoint, OpenAI-compatible | `PORTKEY_BASE_URL`, else Portkey's own |
| `tools` | the tools this runner offers at all, a subset of what it has: a runner whose image has no git has no use for `push`. An environment's `tools` is within it; a spawn naming a tool outside it is refused at launch. | everything the runner has |
| `environments` | what a task runs with: each a name, the driver, the tools a spawn gets when it names none, the binaries its work expects (`commands`), the environment allowlist (`env`), the egress allowlist (`egress`). A spawn names one with `environment:`, and brings its own repositories (`repos:`), which the runner checks. See [the coderunner example](examples/README.md#a-workspace-per-task) for the whole shape. | none: each task gets a fresh directory |
| `default` | the environment a spawn that names none gets | none: a spawn must name one |
| `agents` | programs a spawn may name as its `driver:`, each an `argv` with `$prompt` in it, how to `resume` a session, and whether its `output` is `text` or `json` | none |

Secrets are never in the file. The runner reads them from its environment, where a deployment
puts a Kubernetes Secret.

| Variable | What it holds |
|---|---|
| `PORTKEY_API_KEY` | the Portkey account key. Without it, the runner offers scripts only. |
| `PORTKEY_BASE_URL` | the Portkey endpoint. Default: Portkey's own. |
| `PORTKEY_VIRTUAL_KEY` | the virtual key a spawn that picks none runs under, when `models.portkey.virtualKey` names none. |
| `VERSE_GIT_TOKEN` | the git token the environments' mirrors are fetched with, and every push is made with. Another variable with `-git-token-env`. |

With environments, the runner takes `-data <dir>` for its mirrors and workspaces (default: a temp
directory), `-keep` to keep a task's workspace when it ends, and serves `GET /runner`: what it is,
the sandbox it runs tasks in and the networks it builds, its environments, and the work it holds
that did not leave. `-listen` overrides the configuration's `listen`, and `-admin-addr` is the
admin and pprof face (default: the listen port plus one).

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

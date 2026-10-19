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
runnerd verse -config <file> [-sandbox bwrap|none]   verse's own agent, and scripts, each task
                                                      in a process of its own and, when the
                                                      launch names repositories, in a workspace
                                                      of git checkouts
```

The same runner also runs embedded. `verse up` hosts the verse runner in its own process and
reaches it by Go calls, with no `runnerd` process. Both use package `runner/runnerd`, which holds
a runner's tasks and serves them. A central deployment is meant to run no work in verse itself,
with each runner a `runnerd` of its own; today an ask's `then:` still runs in verse.

## The sandbox

`runnerd` runs each task in a process of its own, inside a sandbox. Everything the task runs is
inside it: the agent's loop and its shell, or a script.

| `-sandbox` | What a task's process gets |
|---|---|
| `bwrap` (the default) | its own pid, ipc and uts namespaces, its own `/proc`, `/dev` and `/tmp`, and no capabilities. The system (`/usr`, `/bin`, `/lib`, `/etc`, …) is read-only, and the task's directory is the only path it may write. It doesn't see the runner's other tasks. The network is the launch's `with.network`: the runner's under `open`; nothing under `none`, or when the launch says nothing; or, under `allow`, the hosts it names, through a proxy the runner serves the task. |
| `none` | nothing: it runs as the runner's user, in the runner's filesystem, on the runner's network, so a launch here says `network: {mode: open}`. Logged when the runner starts. |

With `bwrap`, the runner refuses to start when bubblewrap isn't installed or can't run. bwrap
needs unprivileged user namespaces:

- **Ubuntu 24.04 and later** restrict them with AppArmor
  (`kernel.apparmor_restrict_unprivileged_userns=1`), and bwrap fails with *setting up uid map:
  Permission denied*. Allow bwrap with an AppArmor profile, or run with `-sandbox none`.
- **Kubernetes:** the pod needs three settings, in this order of dependency: `hostUsers: false`,
  `procMount: Unmasked` and `seccompProfile: Unconfined`. The runtime's default seccomp profile
  masks the namespace flags out of `clone`, which bwrap reports as *the kernel does not allow
  non-privileged user namespaces*; with the filter lifted, the masked paths under `/proc` keep an
  unprivileged user namespace from mounting a fresh procfs, which bwrap reports as *Can't mount
  proc on /newroot/proc*. Each message names the wrong layer.
- **macOS** has no bwrap: run with `-sandbox none`.

The task's process has none of the runner's environment. Its stdin carries the runner's
configuration and the secrets the runner read from its environment, and its environment has only
what `task.DefaultShellEnv` names, or the configuration's `shellEnv`. `HOME` is the task's
directory, and `TMPDIR` is `/tmp`. A process that exits before its task ends fails the task,
with the process's exit status.

### What keeps the secrets from the task

The task's process holds the runner's secrets (the Portkey key, its git tokens) in its memory,
and the task's shell, or its script, runs inside the same sandbox: as the same uid, since bwrap
maps the caller to itself, and in the same pid namespace, so it can see that process.

| What | Covered by |
|---|---|
| the environment, and what a subprocess inherits | the secrets come on stdin, never in an environment |
| `/proc/<pid>/mem`, `/proc/<pid>/environ`, and ptrace | the task's process makes itself non-dumpable (`PR_SET_DUMPABLE`) before it serves, so a same-uid process without `CAP_SYS_PTRACE` is refused them. Without it, only the node's `kernel.yama.ptrace_scope` would decide, and its default, 0, allows reading another process's memory. bwrap also drops every capability (`--cap-drop ALL`), which it would otherwise leave to a runner run as root. With `-sandbox none`, a task of a runner run as root keeps its capabilities: its memory is still refused, its `environ`, which holds no secret, is not. |
| a separate uid for the task | **not done**: the task runs as the runner's uid. A uid boundary would not depend on dumpability. |

On a platform without procfs (macOS, with `-sandbox none`), nothing makes the process
non-dumpable.

`verse up` doesn't sandbox: its tasks run in verse's own process, as the user who ran it.

## The verse runner

The verse runner offers verse's own agent, `verse`, which reaches its model through Portkey;
OpenAI's Codex, `codex`, where its configuration says how to run it; and scripts. It serves the
task contract, and `GET /describe`, which says what it offers.

```sh
PORTKEY_API_KEY=… runnerd verse -config verse-runner.tony
verse runner add verse-runner http://localhost:9802
verse task run --runner verse-runner …
```

A launch's `doer` picks which: `{agent: verse}` runs the agent with the prompt, and
`{script: "…"}` runs the script. The agent is offered only when the runner has a Portkey key;
a launch naming it on a runner without one is refused.

A script runs by its own `#!` line, which can name any program in the runner's image that reads
a file: `#!/bin/sh`, `#!/usr/bin/env python3`, `#!/usr/bin/env node`. The line is read as
the kernel reads it: the program, and at most one argument. A script with no `#!` line runs with
`bash`. A script calls the task's tools from its shell with `verse-call-tool`, which the runner
finds beside itself and copies into the task's workspace.

Each task runs in a directory of its own, in a process of its own ([the sandbox](#the-sandbox)).
A launch that names repositories (`with.git`) gets them checked out there, each from a bare
mirror the runner keeps and shares between its tasks, and what may leave each is the launch's
policy's to say, within the ceiling the runner's configuration puts over it: the runner pushes,
and opens pull requests, from its own mirror with its own token, and refuses what either
refuses. The directory is removed when the task ends. A task's
shell sees only the variables that `task.DefaultShellEnv` names, and can't read the runner's
secrets from the task's process
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
repos:
  mirrors: /var/lib/verse-runner/mirrors
systems:
  git:
    ceiling:
    - {match: {verb: push, ref: refs/heads/release/**}, then: deny}
  linear-issue:
    credential: LINEAR_API_KEY
  slack:
    credential: SLACK_BOT_TOKEN
    ceiling:
    - {match: {channel: deploys}, then: allow}
    - {then: deny}
```

| Key | What it holds | Default |
|---|---|---|
| `listen` | the address the runner serves verse on | `:9802` |
| `models.portkey.virtualKey` | the Portkey virtual key, when a launch doesn't pick one. It is a routing label, not a secret. | none |
| `models.model` | the agent's model, when a launch doesn't pick one | the agent's own |
| `annotations` | the annotations a launch may set, with the type of their value. The verse runner knows one, `verse.portkey/virtual-key`: with it, the virtual key a launch picked is used. | none |
| `shellEnv` | the variables of the runner's environment a task's shell is given, by name | `task.DefaultShellEnv`: enough for a shell to find its tools, and no secret |
| `repos.mirrors` | the directory the runner keeps one bare mirror per repository in, shared by its tasks. Without `repos`, the runner takes no launch that names repositories. | none |
| `repos.host` | the forge the runner's repositories are on, as a launch's `repo` names it (`host/owner/name`, or `owner/name` for github.com) | `github.com` |
| `repos.forge` | where the host's `owner/name` is fetched from. A directory holding `owner/name` repositories serves a runner with no network. | `https://<host>` |
| `codex` | how the runner runs [the codex agent](#codex-gated-inside-the-run): `bin`, `model`, `sandbox`, `apiKeyEnv` (the NAME of the variable holding the key) and `config` overrides. Without it, `doer: {agent: codex}` is not offered. | none |
| `systems.<name>` | the operator's say over one system the runner intercepts for its tasks: `git` and `git-issue` come with `repos`, `github` and `github-issue` where the forge is GitHub, and `linear-issue` and `slack` where their credential is named; a section for one of those the runner does not perform is refused. `files`, `commands` and `agent`, which the runner stops inside a run, take a ceiling and nothing else: `commands: {ceiling: [{then: deny}]}` is a runner whose tasks have no shell. | none |
| `systems.<name>.credential` | the NAME of the variable holding the credential the runner acts with. For `git`, its forge token; for `linear-issue`, the `Authorization` header verbatim, a personal key bare or an OAuth token as `Bearer …`; for `slack`, the bot token (`xoxb-…`). For `linear-issue` and `slack`, it is what makes the system's tools offered at all. | `GITHUB_TOKEN` for `git`; none otherwise |
| `systems.github.api` | where the forge's GitHub API is. A GitHub Enterprise Server is this one line: `https://<host>/api/v3`, with `repos.host` naming the host. | `https://api.github.com` |
| `systems.<name>.ceiling` | the ceiling over what any task may do with the system: rows of `allow` and `deny`, written as [a task's policy](tasks.md#the-policy) is, the system implied, and ending with what the rest get. A ceiling never asks: asking is a task's policy's. | allow everything |

Secrets are never in the file. The runner reads them from its environment, where a deployment
puts a Kubernetes Secret.

| Variable | What it holds |
|---|---|
| `PORTKEY_API_KEY` | the Portkey account key. Without it, the runner offers scripts only. |
| `PORTKEY_BASE_URL` | the Portkey endpoint. Default: Portkey's own. |
| `PORTKEY_VIRTUAL_KEY` | the Portkey virtual key, when neither a launch nor `models.portkey.virtualKey` names one |
| `GITHUB_TOKEN`, or the variable `systems.git.credential` names | the runner's token for its forge: what it fetches and pushes with. It stays in the runner; a task's process never has it. |
| the variable `codex.apiKeyEnv` names | the OpenAI API key the codex agent runs under, handed to the codex process as a login file |
| the variable `systems.linear-issue.credential` names | the Linear credential. It stays in the runner; a task's process never has it. |
| the variable `systems.slack.credential` names | the Slack bot token, with the scopes `chat:write`, `channels:read`, `groups:read`, `channels:history`, `groups:history` and `reactions:write` ([slack](../systems/slack.md#where-the-credential-lives)). It stays in the runner; a task's process never has it. |

A variable that is not set is logged when the runner starts, and the runner starts without it:
no agent, or a repository fetched without a token.

The flags: `-config <file>` (default `verse-runner.tony`), `-sandbox bwrap|none`, and
`-admin-addr` for the admin and pprof face (`VERSE_RUNNER_ADMIN_ADDR`; default: the listen
port plus one; `off` disables it). `-task` is how the runner starts each task's process, with the
configuration on stdin, and is not for a person to run.

### What it offers

`GET /describe` answers what the runner offers, from its configuration and its secrets:

```tony
offers:
- agent: verse
  with: {model: any, network: [open, none, allow]}
  annotations: {verse.portkey/virtual-key: string}
  systems:
    git:
      ceiling:
      - {match: {system: git, verb: push, ref: refs/heads/release/**}, then: deny}
      - {match: {system: git, repo: github.com/**}, then: allow}
      - {then: deny}
    git-issue: {ceiling: [{then: allow}]}
    github: {ceiling: [{then: allow}]}
    github-issue: {ceiling: [{then: allow}]}
    linear-issue: {ceiling: [{then: allow}]}
    slack: {ceiling: [{match: {system: slack, channel: deploys}, then: allow}, {then: deny}]}
    files: {ceiling: [{then: allow}]}
    commands: {ceiling: [{then: allow}]}
    agent: {ceiling: [{then: allow}]}
  converse: true
- script: true
  with: {network: [open, none, allow]}
  systems:
    git:
      ceiling:
      - {match: {system: git, verb: push, ref: refs/heads/release/**}, then: deny}
      - {match: {system: git, repo: github.com/**}, then: allow}
      - {then: deny}
    git-issue: {ceiling: [{then: allow}]}
    github: {ceiling: [{then: allow}]}
    github-issue: {ceiling: [{then: allow}]}
    linear-issue: {ceiling: [{then: allow}]}
    slack: {ceiling: [{match: {system: slack, channel: deploys}, then: allow}, {then: deny}]}
```

`systems` is each system the offer intercepts for its tasks, and it is the whole of what an
offer says about the world. A system offered is a system intercepted: the runner performs its
verbs or stops them for the task's policy to decide, and a policy row on a system the offer does
not intercept is refused at install, since nothing there could honour it. The runner performs
`git`, `git-issue`, `github`, `github-issue`, `linear-issue` and `slack` for every doer, each
where it has what it takes (a repository's tracker with `repos`, the forge's where the forge is
GitHub, Linear's and Slack's with a credential). It stops `files`, `commands` and `agent` inside
a run for the verse agent, `files` and `commands` for codex, and none for a script, which is one
action and no step. For `files` and `commands` that is the call as the doer names it
([advisory](../systems/inside.md#a-row-on-files-or-commands-is-advisory)).

`with.network` is the networks the runner's sandbox builds, and a launch's `with.network` is
checked against it, absent being `none`. Under `bwrap` it is all three: `none` takes the network
away, `allow` reaches the launch's hosts through a proxy and nothing else, and `open` is the
host's. A runner that runs its tasks unconfined builds only `open`, so it refuses a launch that
leaves the network out, rather than give the task a network its author did not ask for
([the network](../systems/inside.md#the-network-is-the-launchs)).

Each system carries the ceiling its configuration puts over it, completed with `allow` for the
rest; the `git` ceiling is completed within the runner's forge, so a repository elsewhere is
refused as a fact. What is held at install, and from the task's creation on, is which verbs the
task has: the ones its rows name, each one the runner offers. Within that, what a row says of a
call is a dial, turned at the act. So a row that allows or asks about calls the ceiling refuses
every one of is refused at install, naming both lines, since it could never do anything, and a
repository the `git` ceiling refuses every call on is refused as a fact; a row wider than the
ceiling installs, and the ceiling decides each of its calls as they are made. A policy slid onto a
running task is held to both ceilings: the spawn's, so it names no verb the launch's rows did
not, and the runner's, as at install. The runner reads its ceiling again at each act, whatever
the task's policy said, so a ceiling tightened after a launch holds from then on. A script offer
takes no `model`: a script names what runs it in its own `#!` line. With a `codex:` section the
runner also offers `agent: codex`, with `model` and `effort`, intercepting `files.write` and
`commands.run`.

Verse reads it when the runner is installed and whenever `verse runner check` asks, keeps it
on the runner's registration as `offers`, and refuses a spawn naming the runner that its
offers would refuse, where the spawn is written. The runner checks each launch again as it
arrives. An offer that does not say which networks it builds is refused: verse does not
install the runner, and a check that finds one keeps what the runner said before.

### Codex, gated inside the run

`doer: {agent: codex}` runs OpenAI's Codex through its client protocol, `codex app-server`, and
not as a program whose output the runner reads when it exits. Codex asks its client before it
runs a command or applies a patch, and asks it the questions the model has for a person. The
runner puts each of those in front of the loop as an ordinary action, so the policy decides
it, the docket lists it, and `verse question` and `attach` answer it, exactly as
for the `verse` agent.

| Codex asks | Verse sees | The step |
|---|---|---|
| to run a command | `shell` | `commands.run` on the command |
| to apply a patch | `apply_patch` | `files.write` on each path, relative to the workspace (a move writes both) |
| a question for the person | `ask` | none: it is put to them, on the docket |
| for more permissions | `request_permissions` | `files.write` on each path it would write |

A request that is several steps is decided once, by the first of them a row of the task's
policy names: a patch touching `a.go` and `db/b.sql` is asked about by a row on `db/**`. Where
no row names any, the first decides, by the rows on its verb. A policy for codex names
`commands` and `files`, its shell and its edits, or the launch is refused: codex cannot run
without them, and a row may deny them. An approved command runs inside codex's own sandbox, and the
action's exec frame carries what it printed and how it exited. A refused one is declined, and
the model is told why, so it plans around the refusal rather than retrying it. The thread runs
under codex's `untrusted` approval policy, so codex asks about every command it does not know
to be read-only and every patch. Under rows that allow `commands` and `files`, verse
answers yes at once, and the run is unattended but narrated and on the record, action by action.

```tony
codex:
  bin: codex                   # the program; `codex app-server` is what runs
  model: gpt-5-codex           # when a spawn picks none
  sandbox: workspace-write     # read-only | workspace-write | danger-full-access
  apiKeyEnv: OPENAI_API_KEY    # the NAME of a variable; this file carries no secret
  config: {model_reasoning_effort: "medium"}   # extra `-c key=value` overrides
```

A spawn's `with.model` is the thread's model, over the section's, and its `with.effort` is the
reasoning effort of its turns. Codex's API, `api.openai.com`, is reachable whatever the task's
network, as the verse agent's gateway is.

The key is read by the runner and written, for the codex process alone, as the login file
`codex login --with-api-key` would write, in a `CODEX_HOME` made for the task and removed with
it. Codex does not read a key from its environment, and a variable is what every command it
runs would inherit, so the key is in no environment at all. Without a key, codex uses its own
login under the `HOME` the task's shell is given.

Two things are reported and not gated, and the agent's source says so where it decides them.
Codex runs the commands its own policy trusts as read-only without asking, and it searches
the web without asking. Both appear on the read plane when they complete, so an observer sees
them, and no gate saw them first.

**The runner's tools are codex's tools.** The tools a launch's repositories give it (`git-push`,
`git-fetch`, `github-create-pr`, `github-update-pr`, and their trackers' verbs) are declared to codex when its thread starts, and a call codex makes of
one is an action of the loop: the tool's own step, decided by the policy, performed
by the runner, its result the call's output. A refused call is refused to codex as its result,
with the reason. So codex delivers the way the `verse` agent does: it commits with the trailers
and calls `git-push`.

`verse task say` reaches the running turn. The task's result is the agent's last message. A
cancel interrupts the turn and ends the codex process.

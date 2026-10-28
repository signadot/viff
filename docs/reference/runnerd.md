# runnerd

`runnerd` is the binary that holds the [runners](../glossary.md#runner) that verse ships. A
runner is a program that does the work a spawn describes. Verse reaches it over a URL.

| Subject | Page |
|---|---|
| what a runner is, and how verse uses one | [../runners.md](../runners.md) |
| the installed runner, its commands and routes | [runners.md](runners.md) |
| the contract a runner serves | [../task-control.md](../task-control.md) |

`runnerd` is a binary of its own, for the reason `iod` is ([iod.md](iod.md)). It is in the same
image as `verse`. It runs one runner, offering what its configuration names: the agents under
`agents:` and, with a `script:` section, scripts, each task in a process of its own and, when the
launch names repositories, in a workspace of git checkouts:

```
runnerd -config <file> -token-file <file> [-sandbox bwrap|none]
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
| `bwrap` (the default) | its own pid, ipc and uts namespaces, its own `/proc`, `/dev` and `/tmp`, and no capabilities. The system (`/usr`, `/bin`, `/lib`, `/etc`, …) is read-only, and the task's directory is the only path it may write. It doesn't see the runner's other tasks. The network is the launch environment's `network`: the runner's under `open`; nothing under `none`, or when the launch says nothing; or, under `allow`, a proxy the runner serves the task, which decides each connection by the environment's `tcp-client` shapes, the autonomy and the ceiling. |
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
what its doer's `shellEnv` names (`task.DefaultShellEnv` where the section names none), so no
task's process holds a variable another doer's shell is given. `HOME` is the task's
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
| the codex and claude agents' model keys | **not covered**: each agent reads its key where the commands it runs can too, codex from a login file and Claude from its environment, so the key is the task's ([Codex](#codex-gated-inside-the-run), [Claude](#claude-gated-inside-the-run)). |

On a platform without procfs (macOS, with `-sandbox none`), nothing makes the process
non-dumpable.

`verse up` doesn't sandbox: its tasks run in verse's own process, as the user who ran it.

## The verse runner

The verse runner offers what its configuration names, and nothing else: verse's own agent,
`verse`, which reaches its model through Portkey; OpenAI's Codex, `codex`; Anthropic's Claude
Code, `claude`; each where `agents:` names it; and scripts, where the configuration has a
`script:` section. It serves the task contract, and `GET /describe`, which says what it offers.

```sh
openssl rand -hex 32 > runner.token
PORTKEY_API_KEY=… runnerd -config runner.tony -token-file runner.token
verse runner add my-runner http://localhost:9802 -token-file runner.token
verse task run --runner my-runner …
```

A launch's work picks which: a `prompt` runs an agent with it (`agent: {name: verse}`, or the
runner's default agent), and a `script` runs the script.

A script runs by its own `#!` line, which can name any program in the runner's image that reads
a file: `#!/bin/sh`, `#!/usr/bin/env python3`, `#!/usr/bin/env node`. The line is read as
the kernel reads it: the program, and at most one argument. A script with no `#!` line runs with
`bash`. A script calls the task's tools from its shell with `verse-call-tool`, which the runner
finds beside itself and copies into the task's workspace.

Each task runs in a directory of its own, in a process of its own ([the sandbox](#the-sandbox)).
A launch whose environment gives repositories (its `git` blocks) gets them checked out there,
each from a bare mirror the runner keeps and shares between its tasks, and what may leave each is
its block's push shapes, within the ceiling the runner's configuration puts over it, decided by
the launch's autonomy: the runner pushes,
and opens pull requests, from its own mirror with its own token, and refuses what either
refuses. The directory is removed when the task ends. A task's
shell sees only the variables its doer's `shellEnv` names, and can't read the runner's
secrets from the task's process
([what keeps the secrets from the task](#what-keeps-the-secrets-from-the-task)).

### Configuration

The configuration is a file. Its format comes from its name (`.tony`, `.json`, `.yaml`). A key
that the runner doesn't know is refused when the runner starts, at any depth.

The process's own things are at the root; each doer has a section of its own, under which sits
everything that touches its tasks' shells and reach: an `agents:` map keyed by agent name, and a
`script:` section for scripts. Presence is the switch: an agent the map does not name is not
offered, no `script:` offers no scripts, and a configuration with neither is refused.

```tony
listen: ":9802"
verse: {url: http://verse:9797}            # the one verse this runner serves; required
repos:
  mirrors: /var/lib/verse-runner/mirrors
agents:
  claude:
    bin: /usr/local/bin/claude             # `claude` on PATH when absent
    shellEnv: [PATH, HOME, USER, LANG]     # what claude's shells see of the process
    settings:
      model: {values: [sonnet, opus], default: sonnet}
      effort: {default: high}
    systems:                               # claude's ceilings
      git:
        ceiling:
        - repo: "github.com/acme/**"
          fetch: {}
          push: [{ref: "refs/heads/verse/**"}, {ref: "refs/heads/release/*", force: "false"}]
      git-issue: {ceiling: {repo: "github.com/acme/**"}}
      linear-issue: {ceiling: {create: {team: ENG}, comment: {}, close: {}}}   # closed without LINEAR_API_KEY
      network: {ceiling: {mode: [none, allow], tcp-client: [{host: "*.github.com"}, {host: pypi.org}]}}
      files: {ceiling: {}}
      commands: {ceiling: {}}
script:                                    # scripts are offered only with this section
  shellEnv: [PATH, HOME, USER, LANG, MYSQL_HOST, MYSQL_USER, MYSQL_PWD]   # no agent sees these
  systems:                                 # the scripts' ceilings
    git:
      ceiling:
      - {repo: github.com/acme/svc, fetch: {}, push: {ref: refs/heads/main}}
    slack: {ceiling: {post: {channel: deploys}}}   # closed without SLACK_BOT_TOKEN
```

| Key | What it holds | Default |
|---|---|---|
| `listen` | the address the runner serves verse on | `:9802` |
| `verse` | the one verse this runner serves, by the tokens versed signs for it ([runners at the door](perms.md#runners-at-the-door)): `url`, where the runner reaches versed, announces itself and reads versed's keys (`/.well-known/jwks.json` under it); `issuer`, the name versed's tokens are issued under (`auth.issuer.url` on the verse side), where a verse behind a proxy is called something else than `url` (default `url`); `audience`, the `aud` this runner takes versed's tokens for, its own name (default: the subject of its own token, `VERSE_TOKEN_FILE`, which is what its registration names; the default runner, which has no registration, says `runner`); `ca`, a PEM bundle for a verse served under a certificate no public root signed. A request whose bearer is not a live token of that issuer for that audience is refused `401` before its body is read, and `503` while versed's keys cannot be read. A bare `GET /describe` is answered to anyone, since the description is public; a bearer presented with it is checked all the same, and the describe answers this runner's own token to the verse it verified, and to nobody else. The runner announces itself at start, `POST /runners/hello` with its token in a loop until verse has described it, so verse describes it then and there and nobody runs `verse runner check`. The runner's readiness (`GET /ready`) is its server and nothing else; whether verse heard it, and whether the describe verified it, is the registration's to say. A deployment whose one configuration serves every cluster names the verse in the environment instead (`VERSE_URL`, `VERSE_ISSUER`, `VERSE_AUDIENCE`, `VERSE_CA`, below); naming it in both is refused. | none, and the runner does not start without it, here or in the environment |
| `repos.mirrors` | the directory the runner keeps one bare mirror per repository in, shared by its tasks. Without `repos`, the runner takes no launch that names repositories. | none |
| `repos.host` | the forge the runner's repositories are on, as a launch's `repo` names it (`host/owner/name`, or `owner/name` for github.com) | `github.com` |
| `repos.forge` | where the host's `owner/name` is fetched from. A directory holding `owner/name` repositories serves a runner with no network. | `https://<host>` |
| `repos.api` | where the forge's GitHub API is. A GitHub Enterprise Server is this one line: `https://<host>/api/v3`, with `repos.host` naming the host. | `https://api.github.com` where the forge is github.com |
| `agents.<name>` | one agent the process runs: `verse`, `codex` or `claude`; another name is refused. Its value holds the program's own keys, which are the agent's: codex `bin`, `sandbox` and `config` ([Codex](#codex-gated-inside-the-run)); claude `bin` ([Claude](#claude-gated-inside-the-run)); verse none. A key the agent's program does not take is refused, naming the ones it does; a `bin` that is not there is refused when the runner starts. Beside them, the four keys every agent takes, below. | none |
| `agents.<name>.settings.<setting>` | the operator's say over one setting the agent takes: `values`, what a launch may set, as patterns (`model: {values: ["gpt-6*"]}`), which is what the offer publishes for the setting; and `default`, what the runner uses where a launch sets none, which then goes on the task's record. A setting not named takes any value the agent takes, and the agent's own default. Where the agent lists its own values (Claude's efforts), the ones named are among them; a default is within both. A setting the agent does not take, or an empty `values`, is refused. | any value, the agent's own default |
| `agents.verse.annotations.<name>` | an annotation a launch may set, with the `type` of its value and the `default` used where a launch sets none. The verse agent knows one, `verse.portkey/virtual-key`, a Portkey virtual key: a routing label, not a secret. Only the verse agent takes annotations. | none |
| `agents.<name>.shellEnv`, `script.shellEnv` | the variables of the runner's environment the doer's task shell is given, by name: the process's values of those names and nothing else. They are the whole environment of the doer's task process too, and a launch's `with.env` may not name one of its own doer's. No variable the runner reads a credential from may be named (`GITHUB_TOKEN`, `LINEAR_API_KEY`, `SLACK_BOT_TOKEN`, `PORTKEY_API_KEY`, `PORTKEY_BASE_URL`, `PORTKEY_VIRTUAL_KEY`, `OPENAI_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`), and an empty list is refused: leave it out for the default. A declaration, not a wall: exactly as strong as the sandbox, and with `-sandbox none` no stronger than the task's good manners. | `task.DefaultShellEnv`: enough for a shell to find its tools, and no secret |
| `agents.<name>.systems.<system>`, `script.systems.<system>` | the doer's ceiling over one system, `{ceiling: …}`. The runner performs `git` and `git-issue` with `repos`, `github` and `github-issue` where the forge is GitHub, and `linear-issue` and `slack` where a section's ceiling names them and their credential is set: where it is not, the runner starts with the system closed to every doer, says so, and no offer holds it, so a spawn that needs it is refused where it is written; a section for a system the runner does not perform is refused. Every doer's offer holds each system the runner performs, closed where the doer's section names none. `files`, `commands`, `agent` and `question`, which the runner stops inside a run, are a doer's where the runner stops that doer on them: the verse agent all four, codex and claude `files`, `commands` and `question`, a script `question` alone; another is refused. `network` is every doer's. | closed: nothing of the system. For `network`, no bound beyond what the sandbox builds |
| `….ceiling` | the ceiling over what the doer's tasks may do with the system: blocks written as a spawn's environment writes the system, a block or a list of them, each attribute a pattern or a list of patterns (`repo: "github.com/acme/**"`, `mode: [none, allow]`), each verb its shapes. A ceiling lists what it allows: a spawn's environment must lie within it, every block within one of its blocks and every shape within one of its shapes, and there is no "all but". A block naming no verb (`files: {ceiling: {}}`) allows every verb of its system, any call; a section with no ceiling at all is refused, since it would close the system as leaving it out does. An attribute a block names bounds every environment block it holds: one that leaves it unsaid stands for its default (network's `mode`, none), or, with none (git's `from`), is beyond the block. A `git` ceiling names its repositories, on the runner's forge. A `network` ceiling naming a mode the runner's sandbox can't build is refused when the runner starts. A runner whose ceilings can't give what an agent requires (codex's and claude's `commands.run` and `files.write`) does not start. | |

#### Layouts

The configuration allows any placement of doers on processes, and verse needs no change between
them: one pod with an agent and scripts; an agent-only pod and a scripts-only pod; one pod per
agent; one with every agent and scripts, as a laptop's. Mixing doers on a pod is fine by
default: credentials stay in the runner process, ceilings and `shellEnv` are each doer's, and
workspaces each task's. The reasons to give a doer a pod of its own are an image (a CLI the
others should not carry), a secret that should not even be in the others' process, and load.
The deployment under `deploy/` runs the verse agent and scripts on one runner and codex and
claude each on its own.

#### The variables it reads

Secrets are never in the file. The runner reads them from its environment, where a deployment
puts a Kubernetes Secret, under fixed names: no configuration names a variable.

| Variable | What it holds | Absent |
|---|---|---|
| `VERSE_TOKEN_FILE` | the file holding this runner's own token: its workload token (on Kubernetes, the pod's projected token, audience `verse`), or, off Kubernetes, a static token of 32 bytes at least and without exactly two `.` (a JWT's shape, which `verse runner add -token-file` refuses), whose hash its registration names (`verse runner add -token-file`) and which is its audience too, answered on `GET /describe` to the verse the `verse` block verified, and to nobody else. It is what a registration naming this runner's `subject` is checked against. A file named and holding nothing is refused when the runner starts. | the runner does not start (or `-token-file`) |
| `VERSE_URL`, `VERSE_ISSUER`, `VERSE_AUDIENCE`, `VERSE_CA` | the `verse` block, for a configuration that has none: where the runner reaches its verse and reads its keys, the name its tokens are issued under, the audience the runner takes them for, and a CA bundle for a verse under a certificate no public root signed (the last three may be empty). Where the configuration has the block too, the runner refuses to start: the verse is named once. | the configuration's `verse` block, and without it the runner does not start |
| `PORTKEY_API_KEY` | the Portkey account key, for `agents.verse` | logged; the verse agent is closed, and a spawn prompting it is refused where it is written. A runner left offering nothing does not start |
| `PORTKEY_BASE_URL` | the Portkey endpoint | Portkey's own |
| `PORTKEY_VIRTUAL_KEY` | the Portkey virtual key, where neither a launch nor the annotation's `default` names one | Portkey's routing for the account key |
| `GITHUB_TOKEN` | the runner's token for its forge: what it fetches and pushes with, and acts at GitHub with. It stays in the runner; a task's process never has it. | logged; a forge on this filesystem needs none, and a fetch or push over the network is refused |
| `LINEAR_API_KEY` | the Linear credential, the `Authorization` header verbatim: a personal key bare, an OAuth token as `Bearer …`. It stays in the runner. | logged; `linear-issue` is closed to every doer, and a spawn naming it is refused where it is written |
| `SLACK_BOT_TOKEN` | the Slack bot token, with the scopes `chat:write`, `channels:read`, `groups:read`, `channels:history`, `groups:history` and `reactions:write` ([slack](../systems/slack.md#where-the-credential-lives)). It stays in the runner. | logged; `slack` is closed to every doer, and a spawn naming it is refused where it is written |
| `OPENAI_API_KEY` | the OpenAI API key the codex agent runs under, handed to the codex process as a login file | logged; codex's own login under the task's `HOME` |
| `CLAUDE_CODE_OAUTH_TOKEN`, else `ANTHROPIC_API_KEY` | the credential the claude agent runs under, put in the claude process's environment under the name it was read from: a subscription's token (`claude setup-token`), which is a person's, or an API key, which is what a shared runner holds. Where both are set, the token is used, and that is logged at start. | logged; Claude's own login under the task's `HOME` |

The flags: `-config <file>` (default `runner.tony`), `-sandbox bwrap|none`,
`-token-file <file>` (default `$VERSE_TOKEN_FILE`), `-verse-url`, `-verse-issuer`,
`-verse-audience` and `-verse-ca` (defaults `$VERSE_URL`, `$VERSE_ISSUER`, `$VERSE_AUDIENCE`,
`$VERSE_CA`: the `verse` block, where the configuration has none), and
`-admin-addr` for the admin and pprof face (`VERSE_RUNNER_ADMIN_ADDR`; default: the listen
port plus one; `off` disables it). `-task` is how the runner starts each task's process, with the
configuration on stdin, and is not for a person to run. `runnerd` takes no arguments beyond its
flags.

### What it offers

`GET /describe` answers what the runner offers, from its configuration. For a runner running the
verse agent and scripts under the same ceilings:

```tony
version: 2
offers:
- agent:
    name: verse
    settings:
      model: {values: ["gpt-6*"], default: true}
    annotations: {verse.portkey/virtual-key: string}
  converse: true
  systems:
    network:
      builds: {mode: [none, open, allow]}
      ceiling: {mode: [none, allow], tcp-client: [{host: "*.github.com"}, {host: pypi.org}]}
    git:
      ceiling:
        repo: "github.com/acme/**"
        fetch: {}
        push: [{ref: "refs/heads/verse/**"}, {force: "false", ref: "refs/heads/release/*"}]
    git-issue: {ceiling: {repo: "github.com/acme/**", close: {}, comment: {}, create: {}, pull: {}, push: {}}}
    github: {}
    github-issue: {}
    linear-issue: {ceiling: {close: {}, comment: {}, create: {team: ENG}}}
    slack: {ceiling: {post: {channel: deploys}, read: {channel: deploys}}}
    files: {ceiling: {}}
    commands: {ceiling: {}}
    agent: {ceiling: {}}
    question: {}
- script: true
  systems:
    network:
      builds: {mode: [none, open, allow]}
      ceiling: {mode: [none, allow], tcp-client: [{host: "*.github.com"}, {host: pypi.org}]}
    git:
      ceiling:
        repo: "github.com/acme/**"
        fetch: {}
        push: [{ref: "refs/heads/verse/**"}, {force: "false", ref: "refs/heads/release/*"}]
    git-issue: {ceiling: {repo: "github.com/acme/**", close: {}, comment: {}, create: {}, pull: {}, push: {}}}
    github: {}
    github-issue: {}
    linear-issue: {ceiling: {close: {}, comment: {}, create: {team: ENG}}}
    slack: {ceiling: {post: {channel: deploys}, read: {channel: deploys}}}
    question: {}
```

A system held with no ceiling (`github: {}` here) is closed: a spawn naming it is refused. Each
offer's ceilings are its doer's section's, so a runner whose `script:` names a ceiling its agents'
sections do not offers it to scripts alone. A description offers each doer once: one offer per
agent name and one script offer, since a launch names its doer by the agent's name, or by nothing
for a script, and two offers of one doer could not be told apart; verse refuses a description
that offers one twice.

`version` is the shape of the description. A description in another version is not read: verse
asks the runner again.

`requires` is the steps an offer's doer can't run without, as `<system>.<verb>`: a Codex or Claude
offer says `requires: [commands.run, files.write]`. A spawn whose environment gives none of one is
refused where it is written, naming both ways to add it (`commands: {}` or
`commands: {run: {}}`); the environment may still narrow the verb's shapes. A runner whose
ceiling can't give a step its offer requires does not start. Absent, nothing is required.

`agent` is the agent offered: its `name`, the `settings` a launch's agent may set, each with its
`values` (patterns, the operator's narrowing under `agents:` already applied; absent, any value
of its `form`, `string` unless it says `number` or `bool`) and whether the agent has a `default`
of its own where a launch names none, and the `annotations` verse or the runner may write for it.
A launch writes its settings flat beside the name, `agent: {name: codex, model: gpt-5-codex,
effort: high}`, and a setting the offer does not list is refused. `model` is the setting verse
reads: the task's record says which model ran, its default filled in. The first agent offered, in
the order verse, codex, claude, is the runner's default, a prompt's that names none.

`systems` is each system the offer holds for its tasks, and it is the whole of what an offer says
about the world. For each, `builds` is what the runner can build of it, by its nature, and
`ceiling` what its operator allows, apart so that a refusal says which: "this runner can't build
it (it builds: open)" or "beyond this runner's ceiling". `builds` is never configured; today it is
the network's modes, from the sandbox (under `bwrap` all three, unconfined only `open`), and
absent it is the whole of the system. A launch's environment lies within both, at install and at
the launch, and a run that names no network has the network `none`, which the runner has to build
too: an unconfined runner refuses it, rather than give the task a network its author did not ask
for ([the network](../systems/inside.md#the-network)).

The runner performs `git`, `git-issue`, `github`, `github-issue`, `linear-issue` and `slack` for
every doer, each where it has what it takes (a repository's tracker with `repos`, the forge's
where the forge is GitHub, Linear's and Slack's where a section's ceiling names them), each under
the doer's own ceiling. It stops `files`, `commands`
and `agent` inside a run for the verse agent, `files` and `commands` for codex and claude, and none
for a script, which is one action and no step. For `files` and `commands` that is the call as the doer
names it ([advisory](../systems/inside.md#a-row-on-files-or-commands-is-advisory)). The runner
reads its ceilings again at each act, so a ceiling tightened after a launch holds from then on. A
script offer takes no settings: a script names what runs it in its own `#!` line. With
`agents.codex` the runner offers `agent: codex`, with `model` and `effort`, intercepting
`files.write` and `commands.run`; with `agents.claude`, `agent: claude` the same way.

Verse reads it when the runner is installed and whenever `verse runner check` asks, keeps it on
the runner's registration as `described`, and refuses a spawn naming the runner that its offers
would refuse, where the spawn is written. The runner checks each launch again as it arrives. A
description that does not check (another version, no network with what it builds, a setting
named `name` or `annotations`) is refused: verse does not install the runner, and a check that
finds one keeps what the runner said before.

### Codex, gated inside the run

`agent: {name: codex}` runs OpenAI's Codex through its client protocol, `codex app-server`, and
not as a program whose output the runner reads when it exits. Codex asks its client before it
runs a command or applies a patch, and asks it the questions the model has for a person. The
runner puts each of those in front of the loop as an ordinary action, so the environment and
the autonomy decide it, the docket lists it, and `verse question` and `attach` answer it, exactly as
for the `verse` agent.

| Codex asks | Verse sees | The step |
|---|---|---|
| to run a command | `shell` | `commands.run` on the command |
| to apply a patch | `apply_patch` | `files.write` on each path, relative to the workspace (a move writes both) |
| a question for the person | `ask` | `question.ask`: decided by the autonomy, then put to them, on the docket |
| for more permissions | `request_permissions` | `files.write` on each path it would write |

A request that is several steps goes ahead only where each of them would on its own: a patch
touching `a.go` and `db/b.sql` is refused by a row, the environment or a ceiling that refuses
either, wherever the row stands, and asked about by a row on `db/**`. Each row that asks about
any of its steps asks once, and every answer must be yes. A codex launch's environment gives
`commands` and `files`, its shell and its edits, or the launch is refused where it is written
(the offer `requires` them): codex cannot run without them, and a row may deny them. An
approved command runs inside codex's own sandbox, and the
action's exec frame carries what it printed and how it exited. A refused one is declined, and
the model is told why, so it plans around the refusal rather than retrying it. The thread runs
under codex's `untrusted` approval policy, so codex asks about every command it does not know
to be read-only and every patch. Under rows that allow `commands` and `files`, verse
answers yes at once, and the run is unattended but narrated and on the record, action by action.

```tony
agents:
  codex:
    bin: codex                   # the program; `codex app-server` is what runs
    sandbox: workspace-write     # read-only | workspace-write | danger-full-access
    config: {model_reasoning_effort: "medium"}   # extra `-c key=value` overrides
    settings: {model: {default: gpt-5-codex}}    # when a spawn picks none
    systems: {commands: {ceiling: {}}, files: {ceiling: {}}}
```

A spawn's agent's `model` setting is the thread's model, over the configured default, and its
`effort` the reasoning effort of its turns (`agent: {name: codex, model: gpt-5-codex, effort:
high}`); `agents.codex.settings` narrows either. The key is `OPENAI_API_KEY`. Codex's API, `api.openai.com`, is reachable whatever the task's
network, as the verse agent's gateway is.

The key is read by the runner and written as the login file `codex login --with-api-key`
would write, in a `CODEX_HOME` made for the task and removed with it. Codex does not read a key
from its environment, so the key is in no environment, but the file is in the sandbox as the
task's user, and a command codex runs can read it. Nothing in the runner keeps the key in once
a command has read it: it can leave over the run's network, and, whatever the network, in what
the task hands back (the model's messages and result, a question on the docket, the work it
pushes). It is a credential the task holds. Without a key, codex uses its own login under the
`HOME` the task's shell is given.

Two things are reported and not gated, and the agent's source says so where it decides them.
Codex runs the commands its own policy trusts as read-only without asking, and it searches
the web without asking. Both appear on the read plane when they complete, so an observer sees
them, and no gate saw them first.

**The runner's tools are codex's tools.** The tools a launch's repositories give it (`git-push`,
`git-fetch`, `github-create-pr`, `github-update-pr`, and their trackers' verbs) are declared to codex when its thread starts, and a call codex makes of
one is an action of the loop: the tool's own step, decided by the autonomy, performed
by the runner, its result the call's output. A refused call is refused to codex as its result,
with the reason. So codex delivers the way the `verse` agent does: it commits with the trailers
and calls `git-push`.

`verse task say` reaches the running turn. The task's result is the agent's last message. A
cancel interrupts the turn and ends the codex process.

### Claude, gated inside the run

`agent: {name: claude}` runs Anthropic's Claude Code headless, over the stream protocol it
speaks on its stdio, and not as a program whose output the runner reads when it exits. Told
that the runner answers its permission prompts, Claude asks before every tool call its
permission mode does not already allow: under `manual`, each command, each edit, and each
question it has for a person. The runner puts each of those in front of the loop as an
ordinary action, so the environment and the autonomy decide it, the docket lists it, and `verse question` and
`attach` answer it, exactly as for the `verse` agent.

| Claude asks | Verse sees | The step |
|---|---|---|
| to run a command (Bash) | `bash` | `commands.run` on the command |
| to edit or write a file (Edit, Write) | `edit`, `write` | `files.write` on the path, relative to the workspace |
| a question for the person (AskUserQuestion), where the environment gives `question.ask` | `ask` | `question.ask`: decided by the autonomy, then put to them, on the docket |

An edit through a symlink is decided on the path Claude names and on the file of the workspace
it lands on, and goes ahead only where both would, as a patch of codex's does: a row, the
environment or a ceiling on `db/**` decides a write through `docs/link → ../db`, wherever the row stands. The
workspace is the task's filesystem: where a write can land at all is the sandbox's to bound. A
file a command writes (`echo x > db/x.sql`, a build, a generator) is Claude's Bash and so
`commands.run` alone; no `files` row sees it
([advisory](../systems/inside.md#a-row-on-files-or-commands-is-advisory)). A command in
the background is refused, since its output would reach nobody; Claude is told to run it in
the foreground.

A claude launch's environment gives `commands` and `files`, its shell and its edits, or the
launch is refused where it is written (the offer `requires` them): Claude cannot run without
them, and a row may deny them. An allowed call runs, and the
action's exec frame carries what it printed. A refused one is denied with the reason, which the
model reads as the call's result, so it plans around the refusal rather than retrying it.
Under rows that allow `commands` and `files`, verse answers yes at once, and the run is
unattended but narrated and on the record, action by action.

```tony
agents:
  claude:
    bin: claude                               # the program; `claude -p` is what runs
    settings: {model: {default: sonnet}}      # when a spawn picks none; an alias or a model id
    systems: {commands: {ceiling: {}}, files: {ceiling: {}}}
```

A spawn's agent's `model` setting is the session's model, over the configured default, and its
`effort` the session's effort, one of `low`, `medium`, `high`, `xhigh` or `max` (`agent: {name:
claude, model: opus, effort: high}`); `agents.claude.settings` narrows either. The credential is
`CLAUDE_CODE_OAUTH_TOKEN` where it is set, else `ANTHROPIC_API_KEY`. Claude's API, `api.anthropic.com`,
is reachable whatever the task's network, as the verse agent's gateway is.

The runner owns the session. Claude is started with none of a workspace's or a user's settings
(`--setting-sources ""`), so a `.claude/settings.json` in a repository cannot allow a tool or
add a hook; with one setting of the runner's, an `ask` rule on every tool that is a step, so
the commands Claude's own list calls read-only (`date`, `ls`, …) are asked about like any
other and a `commands` row sees every one; with no MCP server but the runner's
(`--strict-mcp-config`); and with the tools named above and its reads (Read, Glob, Grep) and
nothing else (`--tools`), so sub-agents, the web and the rest are not in the session at all. A
tool the runner has no step for is refused.

The credential is read by the runner and put in the claude process's environment under the
name it was read from, which is the name Claude reads it under. That environment is what every
command Claude runs inherits, so a command can read the credential. As with codex's key,
nothing in the runner keeps it in once a command has read it: it can leave over the run's
network, and, whatever the network, in what the task hands back (Claude's messages and result,
a question on the docket, the work it pushes). It is a credential the task holds. With a
credential, Claude's own state lives in a `CLAUDE_CONFIG_DIR` made for the task and removed with
it. Without one, Claude uses its own login, and keeps its state, under the `HOME` the task's
shell is given, which every such task on the runner shares.

One thing is reported and not gated, and the agent's source says so where it decides it:
Claude's reads in the workspace run without asking, and appear on the read plane when they
complete, so an observer sees them, and no gate saw them first. Claude asks before a read
outside the workspace, and is refused: the runner offers no reads there.

**The runner's tools are Claude's tools.** The tools a launch's repositories give it
(`git-push`, `git-fetch`, `github-create-pr`, `github-update-pr`, and their trackers' verbs)
are served to Claude as an MCP server over the same pipe, and a call Claude makes of one is an
action of the loop: the tool's own step, decided by the autonomy, performed by the runner, its
result the call's output. A refused call is refused to Claude as its result, with the reason.
So Claude delivers the way the `verse` agent does: it commits with the trailers and calls
`git-push`.

`verse task say` reaches the running session, which takes the words up at its next step; words
that reach it as it ends its turn start a turn of their own, and the run goes on to that turn's
end. The task's result is the agent's last message. A cancel interrupts the session and ends
the claude process.

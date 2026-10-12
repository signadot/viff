# Deploying the coderunner and driving Claude Code with it

This page sets up the coderunner from start to finish. You set it up on your own Linux
workstation, or you point it at staging. Every step uses your own Claude credential. There is
no shared credential.

## What you are actually standing up

You set up three separate things.

| Thing | What it is |
|---|---|
| **The coderunner** | a place where work runs |
| **An environment** | a named workspace in the config file of the runner |
| **The claude driver** | an entry under `agents:` in that same config file. It is not code. |

**The coderunner** is a [runner](../../../glossary.md#runner). It is one HTTP process. It makes
a workspace for each task and starts a child process for each task, and bubblewrap confines
that process. The coderunner is not an [io](../../../glossary.md#io), and it is not the engine.
Verse reaches it by URL and knows nothing else about it.

**An [environment](../../../glossary.md#environment)** says which repositories are checked out
and from which base. It says which tools and binaries exist, which hosts the task can reach,
and where the task may push work. A spawn names an environment by a string. Verse carries the
name and does not resolve it.

**The claude driver** is an argv, a way to resume a session, and a way to read what the program
printed. [`command.go`](command.go) states that nothing in the runner knows what a coding agent
is. A shell script in the same entry works in the same way.

> **The trade you are choosing.** With `driver: llm`, every file read, every write and every
> shell command is an action. The [vector](../../../glossary.md#vector) sees each action, gates
> it and narrates it. With `driver: claude`, configured as below, verse gates *the invocation*
> and nothing inside it. [`taskproc.go`](taskproc.go) says so where it builds the default
> vector:
>
> > One action per turn, and that action hands the work to another program. ASK is the honest
> > default for it — verse cannot see inside a subprocess, so the invocation is the only thing
> > there is to decide about, which is the whole difference from the `llm` driver and the reason
> > both exist.
>
> This limit belongs to this runner and the argv below. It does not belong to foreign agents
> in general. Claude Code stops to decide before each tool runs. It decides with its permission
> rules, with a `PreToolUse` hook, and, in a headless run, with `--permission-prompt-tool`,
> which hands the decision to an MCP tool. That decision point is where verse could ask a
> vector, and gate inside the run. This runner starts a subprocess and reads what it printed.
> Its argv passes `--permission-mode bypassPermissions`, which turns that decision point off.
> So here the invocation is the only thing to decide about, and nothing asks the vector from
> inside the run.
>
> Choose `claude` when you want the quality of the agent itself, and containment is enough
> supervision for you. [task-control.md](../../../task-control.md) calls the two guarantees
> *enforced* and *attested* for this reason. They are not one thing.

The page has two paths:

| | |
|---|---|
| **Path A — Linux workstation** | The runner, verse and the CLI all run locally, with your own `claude` login. This is the fastest path to a first run. It is the only path where the credential of your OS user works. |
| **Path B — staging** | The deployed coderunner on EKS. It needs a rebuilt image, an edit to the ConfigMap, and your credential as a Secret. The shipped image has no `claude`, and the shipped config declares no agents. |

## Before either path: your Claude credential

You need the credential in one of two forms. The containment decides which form, not your
preference.

| form | where it lives | works under |
|---|---|---|
| interactive login | `~/.claude`, keyed by the OS user. Run `claude`, then `/login`. | `-confine host` only |
| environment variable | `CLAUDE_CODE_OAUTH_TOKEN` (from `claude setup-token`), or an `ANTHROPIC_API_KEY` from your own console account | `-confine host` **and** `-confine bwrap` |

The bwrap confiner binds `/usr /bin /sbin /lib /lib64 /etc /opt` read-only, and the workspace
writable. It binds nothing else. The list is at [`confine.go:282`](confine.go). Your home
directory is not in the list, so a credential file under `~/.claude` does not exist inside the
sandbox. Under bwrap, the credential must arrive as a variable that is on the `env:` allowlist
of the environment.

⚠️ The environment-variable row of the table follows from how the sandbox is built. Nobody has
run it in this tree. The claude-driver runs on record use `-confine host` with a `~/.claude`
login. Treat the first bwrap run as a test.

## Path A — Linux workstation

Start under `-confine host` and get a run that succeeds. Then turn bwrap on as a separate step.
If you change both at once, a failure does not tell you which change caused it.

### 1. Build the CLI and the runner

```sh
git clone https://github.com/signadot/verse && cd verse
go build -o ./verse ./cmd/verse
go build -o ./coderunner ./docs/reference/examples/coderunner
```

### 2. Log in as yourself

Install Claude Code in your usual way, then log in. The login writes under your own
`~/.claude`. Every run below uses this credential.

```sh
claude                                              # then /login in the session
claude -p "say ok" --output-format json | head -c 200
```

Run the second line on its own. It has exactly the shape that the driver invokes, so it shows
that the credential works before verse is involved.

### 3. Write the runner's config

Copy [`coderunner.tony`](coderunner.tony), which already has the `agents:` block. Change the
repository path to your own clone. Two parts make this the *claude* driver and not the llm
driver: that block, and `driver: claude` on the environment.

```tony
# ~/coderunner.tony
default: verse-docs

agents:
  claude:
    argv: [claude, "-p", "$prompt", "--output-format", json,
           "--permission-mode", bypassPermissions]
    resume: ["--resume", "$session"]
    output: json

environments:
- name: verse-docs
  repos:
  - name: verse
    repo: "/home/you/src/verse"
    base: main
    push: "refs/heads/verse/auto/$sha/$task"
  driver: claude
  commands: [git, go, make, gofmt, rg]
  env: [PATH, HOME, USER, LANG, TERM]
```

`bypassPermissions` turns off the prompts of the CLI. Inside the sandbox, the confinement
refuses what is not allowed. It also leaves verse nothing inside the run to gate, because it
turns off the point where the CLI stops to decide.

`resume:` makes a second turn possible. Without it the program gets exactly one turn. A steer
then has no turn to reach, and an answer that does not match its contract cannot be repaired.

This environment does not use `tools:`, but the field matters for an `llm`-driver task.
`tools:` is the **floor** that an environment puts under an `llm`-driver task. The runner
checks it when it starts, not at the first firing. The workspace confines `read_file`,
`list_dir` and `write_file` by construction. `run_bash` gets the root as its working directory
and nothing else. So the shell is the one tool that can leave the workspace, and to withhold it
is a stronger limit than any prompt. A `claude` environment gets none of this protection,
because the agent does not use the tools of verse.

### 4. Start the runner

`-confine host` confines almost nothing. For the first run you want that: your `HOME` passes
through and `claude` finds its login.

```sh
./coderunner -config ~/coderunner.tony -data ~/.verse-coderunner \
  -addr :9802 -confine host
```

At startup the runner prints its environments and their drivers. It may say that the
environment wants a driver that is *"neither llm nor calls nor an agent this runner declares"*.
Then the `agents:` block did not parse.

### 5. Start a verse and point the CLI at it

```sh
./verse up --data ~/.verse-drive --addr :19797
export VERSE_ADDR=localhost:19797
```

`VERSE_ADDR` is host:port, not a URL. Without it the CLI talks to `:9797`. On the tailnet that
is *staging*, not your laptop. So the command reaches the wrong verse, and no connection is
refused to warn you.

### 6. Install the runner, then check it

`runner add` registers the runner. `runner check` is the acceptance test, and it prints a live
verdict.

```sh
./verse runner add code http://localhost:9802
./verse runner check code
```

The name is one word, and the runner is installed at the
[address](../../../glossary.md#address) `verse.runners.code`. A later command takes either
form. `verse runners --probe` adds the same liveness column to the list. The bare list says
nothing about health, by design.

### 7. Run a task

Give each task one file. A task about one document answers in minutes, and its diff is small
enough to judge. The same prompt against the largest document in the tree does not converge.

```sh
./verse task run \
  --runner verse.runners.code \
  --environment verse-docs \
  --driver claude \
  --as you@example.com --name docsdrift --attach \
  --answer-contract docs/reference/examples/coderunner/docs-drift.answer \
  "$(sed 's|@FILE@|docs/reference/runners.md|' docs/reference/examples/coderunner/docs-drift.prompt)"
```

`--driver claude` is redundant when the environment already defaults to it, and it does no
harm: the driver of a spawn wins. `task run --help` lists `scripted|calls|llm`. These are the
drivers that *the local supervisor* builds. Verse carries any other string without checking it,
and the runner resolves the string against `agents:`.

**`--answer-contract` is not optional.** Without a contract, the loop has one definition of
finished: the model does not call another tool. A model that narrates its next step looks the
same. Such a run ends in the middle of its checks, with a page of ✓ marks as its result and
`done` on the [event stream](../../../glossary.md#event-stream).
[`docs-drift.answer`](docs-drift.answer) has two lines, and its header says why each line is
there.

### 8. Answer the gate

The task stops at once to ask. This is the design. With no `vector:` on the spawn, the runner
supplies `default: auto` and one `ask` [gate](../../../glossary.md#gate-of-a-task) named
`agent`, which matches the `call_agent` verb.

```sh
./verse question                        # what is waiting
./verse question approve <id> --as you@example.com
```

`--vector auto` on the spawn skips the question. Watch the first few runs before you use it.

Compare the default vector of the `llm` driver,
[`agent.Vector()`](https://github.com/signadot/verse/blob/main/runner/runnerd/verserunner/agent/build.go).
It has *three* ask gates: write, shell and delegate. For a foreign agent run this way, only
`delegate` can fire. The writes and the shell commands happen inside a subprocess, and the
subprocess reports none of them to verse. The two vectors show the whole difference between the
two drivers.

### 9. Read what it produced

Nothing has left the machine. `push:` above targets a local ref in the same clone that the task
read.

```sh
git -C ~/src/verse log --format='%B' -1 verse/auto/<sha>/<task>
git -C ~/src/verse diff main..verse/auto/<sha>/<task>
```

If the branch is not there, ask the runner. Do not search for a workspace. The runner stages
work into the mirror before it tries a push, and each entry that was not delivered carries a
fetch command that you can paste:

```sh
curl -s localhost:9802/runner
```

### 10. Then turn bubblewrap on

Restart with `-confine bwrap`. On a Linux workstation the containment is then real:

- the system is read-only;
- the task writes only to the workspace;
- the task runs in its own pid, ipc and uts namespaces;
- if the environment declares `egress:`, the task has no route out except the allowlist.

```sh
./coderunner -config ~/coderunner.tony -data ~/.verse-coderunner \
  -addr :9802 -confine bwrap
```

Two things change at once. Both are the sandbox doing its job.

- **Your `~/.claude` login is no longer visible.** Switch to `CLAUDE_CODE_OAUTH_TOKEN` (or
  `ANTHROPIC_API_KEY`), and export it for the runner process. Add the variable name to the
  `env:` list of the environment. That list is all that the task inherits.
- **If you add an `egress:` allowlist, `api.anthropic.com` must be on it.** Add `github.com`
  too if the environment pushes to a remote, and the module proxy if the task builds. To
  declare `egress:` is to declare restricted networking. A confiner that cannot unshare the
  network refuses the launch. It does not run with a proxy that the task could ignore.

⚠️ Expect problems on the second turn. Each turn is a new bwrap invocation with a new `/tmp`
tmpfs. The session state of the CLI is gone by the time `--resume $session` runs. A task with
one turn is not affected. A steer or a repair of the contract is where the problem shows. There
is no way, for each task, to give the CLI a durable HOME.

## Path B — staging

The staging profile already deploys a coderunner (`coderunner: true`, image
`example/verse-coderunner:staging`). It has no `claude` binary and no `agents:` entry.
[`deploy/coderunner/coderunner.yaml`](https://github.com/signadot/verse/blob/main/deploy/coderunner/coderunner.yaml)
declares five environments, and every one is `driver: llm`. So this path is three edits and a
ship.

### 1. Put `claude` in the coderunner image

Add it to the [`Dockerfile`](Dockerfile), next to the Go toolchain that is already there. It
must land under one of the paths that bwrap binds. The default of the installer,
`~/.local/bin`, is not one of them.

```dockerfile
# after the Go install block
RUN curl -fsSL https://claude.ai/install.sh | bash \
 && mv /root/.local/bin/claude /usr/local/bin/claude \
 && chmod 0755 /usr/local/bin/claude
```

Pin the version. Do not track the latest version: this image must give the same toolchain on
every build. *(Not built or run from here.)*

### 2. Put your credential in the cluster

Put your own token in its own Secret, so you can revoke it without touching anything else. The
`verse-llm` Secret belongs to Portkey. It is not this Secret.

```sh
kubectl -n verse create secret generic verse-claude \
  --from-literal=CLAUDE_CODE_OAUTH_TOKEN=<your token>
```

On a cluster with External Secrets, the same value goes in an entry under
`staging/verse/claude`, with its own setting. A credential that a different process holds, and
that is revoked on a different day, is a separate entry. The git PAT and the coderunner PAT
follow the same rule.

### 3. Teach the deployed runner about the agent

Make two edits in `deploy/coderunner/coderunner.yaml`. First edit the ConfigMap. It needs:

- an `agents:` block;
- an environment that names it;
- the credential variable on `env:`;
- `api.anthropic.com` on the egress allowlist.

```tony
agents:
  claude:
    argv: [claude, "-p", "$prompt", "--output-format", json,
           "--permission-mode", bypassPermissions]
    resume: ["--resume", "$session"]
    output: json

environments:
- name: verse-docs-claude
  repos:
  - name: verse
    repo: "https://github.com/signadot/verse"
    base: main
    push: "refs/heads/verse/auto/$sha/$task"
  driver: claude
  commands: [go, gofmt]
  env: [PATH, HOME, USER, LANG, CLAUDE_CODE_OAUTH_TOKEN]
  egress:
  - api.anthropic.com
  - proxy.golang.org
  - sum.golang.org
  - github.com
```

Then edit the Deployment, so that the runner process holds that variable. `env:` above is an
allowlist over what the runner has. It does not supply values.

```yaml
        envFrom:
        - secretRef: {name: verse-llm, optional: true}
        - secretRef: {name: verse-claude, optional: true}   # added
        env:
        - name: HOME
          value: /tmp                                       # writable tmpfs in-sandbox
```

⚠️ Nothing checks these three things:

| What | What goes wrong |
|---|---|
| `commands:` | It is a claim about the image, and nothing verifies that the image agrees. A binary that is not there gives a task whose commands are not found. The only diagnosis is the confusion of the agent. |
| the egress list | It must name the host that the driver *dials*. |
| the pod settings | The pod needs `hostUsers: false`, `procMount: Unmasked` and `seccompProfile: Unconfined`, each one needed by the next. They are in the manifest. Do not drop them when you edit near them. |

### 4. Ship it

`make ship` refuses a dirty tree, because the tag must name a commit that somebody can check
out. So commit the three edits first. `make ship` builds and pushes both images at the current
commit, and deploys staging at that ref.

```sh
make ship
kubectl -n verse rollout status deploy/coderunner
kubectl -n verse logs deploy/coderunner | head -40
```

The startup log lists the environments and their drivers. It warns about credentials that the
runner expects and cannot see. Read it before you spawn anything.

### 5. Install and run against staging

The URL is the name of the in-cluster Service, because *versed* dials the runner. Your CLI only
needs to reach the API, which staging publishes on the tailnet.

```sh
export VERSE_ADDR=verse:9797

verse runner add code-claude http://coderunner:9802
verse runner check code-claude

verse task run \
  --runner verse.runners.code-claude \
  --environment verse-docs-claude \
  --as you@example.com --name docsdrift --attach \
  --answer-contract docs/reference/examples/coderunner/docs-drift.answer \
  "$(sed 's|@FILE@|docs/reference/runners.md|' docs/reference/examples/coderunner/docs-drift.prompt)"

verse question
verse question approve <id> --as you@example.com
```

This environment pushes to the real GitHub remote. A delivered task leaves
`refs/heads/verse/auto/<sha>/<task>` on `signadot/verse`. That branch is only a proposal. `main`
is not a destination here, by design. `receive.denyNonFastForwards` at the remote enforces
this, so nobody has to remember it.

## Sharp edges, and where each was found

These problems are open, or are pitfalls that a run can still hit.

| symptom | cause | state |
|---|---|---|
| `Not logged in · Please run /login` | The passthrough list lacks `USER`. The agent looks up its credential by OS user, so `PATH` and `HOME` are not enough. | put `USER` on `env:` |
| The event stream is blank for the whole run | `--output-format json` returns one document when the turn ends, so there is nothing to emit before then. A ten-minute task looks dead. Verse gates only the invocation, so narration is the only way to see the run, and `stream-json` is not wired. | **open**, `1qdmzhac` |
| The record of a finished run cannot say what the run was held to | `task:instance` has no field for the answer contract. The ad-hoc path records no envelope: no vector, no grant, no tools. So nobody can tell afterwards whether the run was governed, and by what. | **open**, `e3b4qmac` |
| Prompts that say "look in `docs/`" go wrong | The workspace layout is a convention of the runner, with one directory for each repository. `Workspace.Brief` tells the agent what is there. Write prompts against the brief, not against a tree with one repository. | write to the brief |
| The login file is invisible under bwrap | Only `/usr /bin /sbin /lib /lib64 /etc /opt` and the workspace are bound. | read it in [`confine.go`](confine.go) *(from the code, not a run)* |
| `--resume` across turns | Each turn has a new sandbox and a new `/tmp`, so session state does not survive between turns. | untested; runs with one turn are not affected *(inferred)* |

One file is also not a unit of work. The same prompt comes back clean against
`docs/runners.md`, a page of 240 lines. Against a document of 1126 lines, it can end on a model
timeout. It can also stop in the middle of its checks and answer `status: clean` after it has
checked a third of the file. The contract makes `done` mean something. It does not make the
checking complete.

## Which path to send first

Send Path A under `-confine host` first. It reaches a working run with a credential the reader
already has, and no cluster is in the way. Every failure on it appears in their terminal.

Path B adds an image build, a Secret, a ConfigMap and a rollout, on top of a driver that the
reader has not yet seen work. Its first failure appears as the confusion of an agent in a pod
log.

Step 10 needs the most attention, not step 1. Every step before it either works or says why it
failed. Step 10, turning bubblewrap on, changes how the credential reaches the agent, and this
tree has the least evidence for it.

---

In this directory: [`command.go`](command.go) · [`config.go`](config.go) · [`confine.go`](confine.go) ·
[`taskproc.go`](taskproc.go) · [`coderunner.tony`](coderunner.tony) · [`Dockerfile`](Dockerfile).
Elsewhere: [`deploy/coderunner/coderunner.yaml`](https://github.com/signadot/verse/blob/main/deploy/coderunner/coderunner.yaml) ·
[`deploy/README.md`](https://github.com/signadot/verse/blob/main/deploy/README.md) · [README.md](../README.md#running-the-coderunner).

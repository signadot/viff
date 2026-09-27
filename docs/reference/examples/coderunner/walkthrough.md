# Deploying the coderunner and driving Claude Code with it

A start-to-finish walkthrough for setting this up on your own Linux workstation, or pointing at
staging. Your own Claude credential throughout — there is no shared one to inherit.

## What you are actually standing up

Three separate things, and keeping them separate is what makes the rest legible.

**The coderunner** is a place work runs — one HTTP process, a workspace per task, a child process
per task, bubblewrap around the process. It is not a source and not the engine. verse reaches it by
url and knows nothing else about it.

**An environment** is a named workspace in the runner's own config file: which repositories get
checked out, from which base, which tools and binaries exist, which hosts are reachable, and where
work may be pushed. A spawn names an environment by string; verse carries the name without
resolving it.

**The claude driver** is not code. It is an entry under `agents:` in that same config — an argv, a
way to resume a session, and how to read what the program printed. [`command.go`](command.go) is
explicit that nothing in the runner knows what a coding agent is; a shell script in the same slot
would work identically.

> **The trade you are choosing.** With `driver: llm`, every file read, every write, every shell
> command is an action the vector sees, gates and narrates. With `driver: claude` as it is
> configured below, verse gates *the invocation* and nothing inside it. [`taskproc.go`](taskproc.go)
> says it where the default vector is built:
>
> > One action per turn, and that action hands the work to another program. ASK is the honest
> > default for it — verse cannot see inside a subprocess, so the invocation is the only thing
> > there is to decide about, which is the whole difference from the `llm` driver and the reason
> > both exist.
>
> That is a property of this runner and the argv below, not of foreign agents. Claude Code stops
> to decide before each tool runs — its permission rules, a `PreToolUse` hook, and in a headless
> run `--permission-prompt-tool`, which hands the decision to an MCP tool — and that is where a
> vector would be asked for verse to gate inside the run. This runner starts a subprocess and
> reads what it printed, and the argv passes `--permission-mode bypassPermissions`, which switches
> that decision point off. So here the invocation is all there is to decide about; nothing asks
> the vector from inside.
>
> Pick `claude` when you want the agent's own quality and are content with containment doing the
> supervising. It is why the `verse-dev` drive itself moved to `llm`, and why
> [task-control.md](../../../task-control.md) calls the two guarantees *enforced* and *attested*
> rather than one thing.

Two paths below:

| | |
|---|---|
| **Path A — Linux workstation** | Runner, verse and CLI all local, your own `claude` login. Fastest to a first run, and the only path where the credential-by-OS-user shortcut works. |
| **Path B — staging** | The deployed coderunner on EKS. Needs a rebuilt image, a ConfigMap edit and your credential as a Secret — the shipped image carries no `claude` and the shipped config declares no agents. |

## Before either path: your Claude credential

You need one of two forms, and *which one* is decided by the containment, not by preference.

| form | where it lives | works under |
|---|---|---|
| interactive login | `~/.claude`, keyed by OS user — `claude` then `/login` | `-confine host` only |
| environment variable | `CLAUDE_CODE_OAUTH_TOKEN` (from `claude setup-token`), or an `ANTHROPIC_API_KEY` from your own console account | `-confine host` **and** `-confine bwrap` |

**Why the containment decides this.** The bwrap confiner binds `/usr /bin /sbin /lib /lib64 /etc
/opt` read-only plus the workspace writable, and nothing else — the list is
[`confine.go:282`](confine.go). Your home directory is not among them, so a credential file under
`~/.claude` is simply not present inside the sandbox. Under bwrap the credential has to arrive as a
variable on the environment's `env:` allowlist.

⚠️ The environment-variable half of that table is inference from how the sandbox is built, not
something this tree has run. The only recorded claude-driver runs were `-confine host` with a
`~/.claude` login. Treat the first bwrap run as the experiment it is.

## Path A — Linux workstation

Start under `-confine host`, get a green run, then turn bwrap on as a separate step. Two variables
at once is how an afternoon disappears.

### 1. Build the CLI and the runner

```sh
git clone https://github.com/signadot/verse && cd verse
go build -o ./verse ./cmd/verse
go build -o ./coderunner ./docs/reference/examples/coderunner
```

### 2. Log in as yourself

Install Claude Code however you normally do, then authenticate. This writes under your own
`~/.claude` and is the credential every run below uses.

```sh
claude                                              # then /login in the session
claude -p "say ok" --output-format json | head -c 200
```

That second line is worth running on its own: it is exactly the shape the driver invokes, and it
proves the credential works before any of verse is in the picture.

### 3. Write the runner's config

Copy [`coderunner.tony`](coderunner.tony) — which already carries the `agents:` block — and change
the repo path to your own clone. The two pieces that make this the *claude* driver rather than the
llm one are that block and `driver: claude` on the environment.

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

`bypassPermissions` is not a shrug: inside the sandbox the confinement is what says no, and asking
the CLI to also prompt would park the run on a question nobody is watching for. It is also the
choice that leaves verse nothing inside the run to gate, since the CLI's own stop-and-decide point
is what it bypasses. `resume:` is what
makes a second turn possible at all — without it the program gets exactly one turn, so a steer has
nowhere to go and an answer that misses its contract cannot be repaired.

Worth knowing even though this environment does not use it: `tools:` is the **floor** an
environment can put under an `llm`-driver task, checked when the runner starts rather than at the
first firing. `read_file`, `list_dir` and `write_file` are confined to the workspace by
construction, while `run_bash` gets the root as a working directory and nothing else — so the shell
is the one tool that can leave, and withholding it is a stronger statement than any prompt. A
`claude` environment gets none of that protection, because the agent is not using verse's tools.

### 4. Start the runner

`-confine host` claims almost nothing, which is the point for the first run: your `HOME` passes
through and `claude` finds its login.

```sh
./coderunner -config ~/coderunner.tony -data ~/.verse-coderunner \
  -addr :9802 -confine host
```

It prints its environments and their drivers at startup. If it says the environment wants a driver
that is *"neither llm nor calls nor an agent this runner declares"*, the `agents:` block did not
parse.

### 5. Start a verse and point the CLI at it

```sh
./verse up --data ~/.verse-drive --addr :19797
export VERSE_ADDR=localhost:19797
```

`VERSE_ADDR` is host:port, not a url. Without it the CLI talks to `:9797` — which on the tailnet is
*staging*, not your laptop, so the failure is a surprise rather than a refused connection.

### 6. Install the runner, then check it

`runner add` registers; `runner check` is the acceptance test, and prints a live verdict.

```sh
./verse runner add pool:code http://localhost:9802
./verse runner check verse.runners.pool.code
```

The name argument is `<kind>:<name>`; the address it lands at is **dotted** —
`verse.runners.pool.code`. That is what every later command wants. `verse runners --probe` adds the
same liveness column to the list; the bare list deliberately says nothing about health.

### 7. Run a task

One file per task. A task scoped to one document answers in minutes and produces a diff small
enough to judge; the same prompt against the largest doc in the tree does not converge.

```sh
./verse task run \
  --runner verse.runners.pool.code \
  --environment verse-docs \
  --driver claude \
  --as "$USER" --name docsdrift --attach \
  --answer-contract docs/reference/examples/coderunner/docs-drift.answer \
  "$(sed 's|@FILE@|docs/reference/runners.md|' docs/reference/examples/coderunner/docs-drift.prompt)"
```

`--driver claude` is redundant if the environment already defaults to it, and harmless either way —
a spawn's own driver wins. `task run --help` advertises `scripted|calls|llm`, which are the drivers
*the local supervisor* builds; verse carries any other string without validating it, and the runner
is what resolves it against `agents:`.

**`--answer-contract` is not optional.** Without a contract, the loop's only definition of finished is the
model declining to call another tool — which is also what a model narrating its next step looks
like. Observed twice: a run ended mid-verification with a page of ✓ marks as its result and `done`
on the read plane. [`docs-drift.answer`](docs-drift.answer) is two lines and says in its own header
why each of them is there.

### 8. Answer the gate

It parks immediately. That is the design: with no `vector:` on the spawn the runner supplies
`default: auto` and a single `ask` gate named `agent`, matching the `call_agent` verb.

```sh
./verse gate                            # what is waiting
./verse gate approve <id> --as "$USER"
```

`--vector auto` on the spawn skips the asking entirely. Watch the first few instead.

Worth seeing once: the `llm` driver's default vector ([`harness.Vector()`](https://github.com/signadot/verse/blob/main/task/harness/build.go))
has *three* ask gates — write, shell and delegate. For a foreign agent run this way only `delegate`
can fire, because the writes and the shell commands happen inside a subprocess that reports none
of them to verse. That difference is the whole argument for the two drivers, in two vectors.

### 9. Read what it produced

Nothing has left the machine — `push:` above targets a local ref in the very clone the task read.

```sh
git -C ~/src/verse log --format='%B' -1 verse/auto/<sha>/<task>
git -C ~/src/verse diff main..verse/auto/<sha>/<task>
```

If nothing is there, ask the runner rather than hunting for a workspace — work is staged into the
mirror before any push is attempted, and each undelivered entry carries a pasteable fetch command:

```sh
curl -s localhost:9802/runner
```

### 10. Then turn bubblewrap on

Restart with `-confine bwrap`. On a Linux workstation this is the first time the containment is
real: read-only system, workspace-only writes, pid/ipc/uts namespaces, and — if the environment
declares `egress:` — no route out except the allowlist.

```sh
./coderunner -config ~/coderunner.tony -data ~/.verse-coderunner \
  -addr :9802 -confine bwrap
```

Two things change at once, and both are the sandbox doing its job:

- **Your `~/.claude` login stops being visible.** Switch to `CLAUDE_CODE_OAUTH_TOKEN` (or
  `ANTHROPIC_API_KEY`), export it for the runner process, and add the variable name to the
  environment's `env:` list — that list is the whole of what the task inherits.
- **If you add an `egress:` allowlist, `api.anthropic.com` has to be on it**, alongside `github.com`
  if the environment pushes to a remote and the module proxy if the task builds. Declaring `egress:`
  *is* declaring restricted networking; a confiner that cannot unshare the network refuses the
  launch rather than running with a proxy the task is free to ignore.

⚠️ Expect the second turn to be the rough one. Each turn is a fresh bwrap invocation with a fresh
`/tmp` tmpfs, so wherever the CLI keeps session state it is gone by the time `--resume $session`
runs. A single-turn task is unaffected; a steer or a contract repair is where this would show up.
There is no per-task way to hand it a durable HOME today.

## Path B — staging

The staging profile already deploys a coderunner (`coderunner: true`, image
`example/verse-coderunner:staging`). What it does not have is a `claude` binary or an `agents:`
entry — [`deploy/coderunner/coderunner.yaml`](https://github.com/signadot/verse/blob/main/deploy/coderunner/coderunner.yaml)
declares five environments and every one of them is `driver: llm`. So this path is three edits and
a ship.

### 1. Put `claude` in the coderunner image

Add it to [`Dockerfile`](Dockerfile), alongside the Go toolchain that is already there. It must land
under one of the paths bwrap binds — the installer's default `~/.local/bin` is not one of them.

```dockerfile
# after the Go install block
RUN curl -fsSL https://claude.ai/install.sh | bash \
 && mv /root/.local/bin/claude /usr/local/bin/claude \
 && chmod 0755 /usr/local/bin/claude
```

Pin the version rather than tracking latest — this image's whole job is to be the same toolchain
twice. *(Not built or run from here.)*

### 2. Put your credential in the cluster

Your own token, in its own Secret, revocable without touching anything else. The `verse-llm` Secret
is Portkey's and is not this.

```sh
kubectl -n verse create secret generic verse-claude \
  --from-literal=CLAUDE_CODE_OAUTH_TOKEN=<your token>
```

On a cluster with External Secrets, the same value belongs in an entry under
`staging/verse/claude` with its own knob — a credential held by a different process and revoked on
a different day is a separate entry, which is the rule the git PAT and the coderunner PAT already
follow.

### 3. Teach the deployed runner about the agent

Two edits in `deploy/coderunner/coderunner.yaml`. First the ConfigMap: an `agents:` block, an
environment that names it, the credential variable on `env:`, and `api.anthropic.com` on the egress
allowlist.

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

Then the Deployment, so the runner process actually holds that variable — `env:` above is an
allowlist over what the runner has, not a source of values:

```yaml
        envFrom:
        - secretRef: {name: verse-llm, optional: true}
        - secretRef: {name: verse-claude, optional: true}   # added
        env:
        - name: HOME
          value: /tmp                                       # writable tmpfs in-sandbox
```

⚠️ Three things nothing checks. `commands:` is a claim about the image and nothing verifies the
image agrees — naming a binary that is not there produces a task whose commands are not found and no
better diagnosis than the agent's confusion. The egress list must name the host the driver *dials*.
And the pod already needs `hostUsers: false`, `procMount: Unmasked` and `seccompProfile:
Unconfined`, in that order of dependency — they are in the manifest, don't drop them while editing
near them.

### 4. Ship it

`make ship` refuses a dirty tree on purpose — the tag names a commit somebody can check out — so
commit the three edits first. It builds and pushes both images at the current commit and deploys
staging at that ref.

```sh
make ship
kubectl -n verse rollout status deploy/coderunner
kubectl -n verse logs deploy/coderunner | head -40
```

The startup log lists the environments and their drivers, and warns about credentials it was told
to expect and cannot see. Read it before spawning anything.

### 5. Install and run against staging

The url is the in-cluster Service name, because *versed* dials the runner. Your CLI only needs to
reach the API, which staging publishes on the tailnet.

```sh
export VERSE_ADDR=verse:9797

verse runner add pool:code-claude http://coderunner:9802
verse runner check verse.runners.pool.code-claude

verse task run \
  --runner verse.runners.pool.code-claude \
  --environment verse-docs-claude \
  --as "$USER" --name docsdrift --attach \
  --answer-contract docs/reference/examples/coderunner/docs-drift.answer \
  "$(sed 's|@FILE@|docs/reference/runners.md|' docs/reference/examples/coderunner/docs-drift.prompt)"

verse gate
verse gate approve <id> --as "$USER"
```

This environment pushes to the real GitHub remote, so a delivered task leaves
`refs/heads/verse/auto/<sha>/<task>` on `signadot/verse`. That is a proposal and nothing more:
`main` is deliberately not a destination here, and `receive.denyNonFastForwards` at the far end is
what makes that structural rather than a matter of everyone remembering.

## Sharp edges, and where each was found

Each of these was found by a run that hit it, during the `verse-dev` coderunner drive. The ones
that are fixed are noted as such; the two that are not are tracked as issues.

| symptom | cause | state |
|---|---|---|
| `Not logged in · Please run /login` | The passthrough list is missing `USER`. The agent looks its credential up by OS user, so `PATH` and `HOME` are not enough. Found by bisecting the allowlist. | put `USER` on `env:` |
| The read plane is blank for the whole run | `--output-format json` returns one document when the turn ends, so there is nothing to emit until it is over. A ten-minute task looks dead. Since verse gates only the invocation, narration is the entire compensating mechanism — and `stream-json` is not wired. | **open**, `1qdmzhac` |
| A finished run's record cannot say what it was held to | `task:instance` has no field for the answer contract, and the ad-hoc path records no envelope at all — not vector, not grant, not tools. So "was this run governed, and by what?" is unanswerable afterwards. | **open**, `e3b4qmac` |
| Task parks the instant it starts | Designed. The runner's default vector asks on `call_agent`, because this runner sees nothing inside the subprocess it starts. | working as intended |
| A finished run left no branch at all | Cleanup ran unconditionally, and `deliver` tested for a dirty tree rather than for work — so an agent that committed its own edits read as nothing to deliver. | fixed; both now check the branch moved |
| Prompts that say "look in `docs/`" go wrong | The workspace layout is the runner's convention and holds one directory per repository. `Workspace.Brief` tells the agent what is there; write prompts against that, not against a single-repo tree. | write to the brief |
| `open pool:code: no such file or directory` from `runner add` | A pasted command with a trailing `#` comment: interactive zsh does not strip those, so the extra words arrive as arguments and `args[0]` was read as a filename. | fixed; the error now names the arguments and says `zsh` |
| Login file invisible under bwrap | Only `/usr /bin /sbin /lib /lib64 /etc /opt` and the workspace are bound. | read it in [`confine.go`](confine.go) *(from the code, not a run)* |
| `--resume` across turns | Fresh sandbox and fresh `/tmp` per turn, so session state does not survive between them. | untested; single-turn runs unaffected *(inferred)* |

One file is also not a unit of work. Three runs of the same prompt: `docs/runners.md` (240 lines)
came back clean; the same prompt against a 1126-line document died on a model timeout once and
stopped mid-verification the second time, answering `status: clean` having checked a third of the
file. The contract makes `done` mean something; it does not make the checking complete.

## Which path to send first

Path A under `-confine host` reaches a working run with one credential they already have and no
cluster in the way, and every failure along it is theirs to see in a terminal. Path B adds an image
build, a Secret, a ConfigMap and a rollout on top of a driver they have not yet watched work — and
its first failure arrives as an agent's confusion in a pod log.

The step worth their attention is 10, not 1. Everything before it is plumbing that either works or
says why; turning bubblewrap on is where the credential story genuinely changes and where this tree
has the least evidence.

---

In this directory: [`command.go`](command.go) · [`config.go`](config.go) · [`confine.go`](confine.go) ·
[`taskproc.go`](taskproc.go) · [`coderunner.tony`](coderunner.tony) · [`Dockerfile`](Dockerfile).
Elsewhere: [`deploy/coderunner/coderunner.yaml`](https://github.com/signadot/verse/blob/main/deploy/coderunner/coderunner.yaml) ·
[`deploy/README.md`](https://github.com/signadot/verse/blob/main/deploy/README.md) · [README.md](../README.md#running-the-coderunner).

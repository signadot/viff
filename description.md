# proposal: MCP servers, nix packages and secrets on a runner — three systems over one runner-side secret store

**Status:** a proposal, for discussion. Nothing is built.

Related issues: 55crejnf (three faces for an MCP server; the task face grew as named tools
through the runner), bst6zbs0 (what a gate on a command can honestly promise), hfh7km3t
(credentials a gate mints), q3k89kva (approvers answer blind).

## What this is for

A task today has the tools its runner's code implements, and the programs its runner's image
carries. Three things are missing:

- tools that an MCP server offers;
- packages the image does not carry;
- a credential for a command the task runs.

This proposal adds each as one more system in the systems model (`pkg/systems/documents`). It
adds no second vocabulary. A spawn's `environment` gives each system, the runner's `ceiling`
bounds it, and the `autonomy` decides each step.

## The shape

| System | Kind | Fixed at start, not on the dial | Verbs, each a step | Its credential |
|---|---|---|---|---|
| `mcp` | performed | which servers | `call` | held by the runner, bound by the operator |
| `pkg-nix` | provided at the start | the flake the task starts with | none at first; `install` later | held by the runner, bound by the operator |
| `secret` | performed | which secrets | `exec` | given to one command, and not to the task |

There are two kinds here:

- **Performed.** The runner acts with a credential the task never holds, and decides each act
  again from what it knows itself (`pkg/runner/runnerd/delivery/server.go`, `refused`). `mcp` is
  this, as `git` and `slack` are. `secret.exec` is this too. What acts for `slack` is the
  runner's own code. What acts for `secret.exec` is a command the environment admits.
- **Provided at the start.** The runner prepares the task's sandbox before the task's process
  starts, as it checks out a repository. The environment and the ceiling bound it. No row decides
  it. `pkg-nix` is this at first.

## Secrets: one store, bound in two ways

**The store is the runner's.** One file per secret, by name, in a directory the deployment
mounts (a Kubernetes Secret volume). The runner reads a value at each use, so a rotation needs
no restart. Today each credential is a variable with a fixed name (`GITHUB_TOKEN`,
`SLACK_BOT_TOKEN`), and no configuration names one (`pkg/runner/runnerd/stock/config.go`). The
store is the general form of that.

**The operator binds a secret to a system.** The runner's configuration says which secret an MCP
server authenticates with, and which secret a nix cache or a private flake's host takes. A spawn
gives the `mcp` server or the `pkg-nix` block, and never names the secret. The task never holds
it. This is how `slack` works today.

**A spawn binds a secret to a command.** The spawn's environment gives `secret` blocks, by name.
The offer says which names the runner holds (`builds`) and which of them a doer's tasks may use
(`ceiling`). A ceiling is closed by default. So a secret bound to an MCP server or to nix reaches
no command, unless the operator names it in that doer's `secret` ceiling.

**The rule under both:** a task never chooses which credential goes to which host.

So `mcp`, `pkg-nix` and `secret` share one store and one set of names. They do not share a
path to the task.

## `mcp`

- **The document.** A unique attribute `server`, so an environment has one block per server, as
  `git` has one per repository. One verb, `call`, with the field `tool`.
- **The servers are the operator's.** The runner's configuration names each server: its url and
  the secret it authenticates with.
- **Remote servers first.** A stdio server that the runner starts would be code outside any
  sandbox, as the runner's user, beside the runner's own tokens. Nothing confines such a helper
  today.
- **One seam reaches every doer.** The runner lists a server's tools when the task is built, and
  offers each as a `verse.Tool` whose step is `mcp.call`. That list already reaches Claude (the
  runner's own MCP server over the pipe), Codex (`dynamicTools`), the verse agent, and a script
  (`verse-call-tool`).
- **Each tool needs its step.** A tool with no step is not gated. `offered`
  (`pkg/runner/runnerd/stock/runner.go`) filters tools by name, `<system>-<verb>`, and an MCP
  tool's name does not split that way. It has to filter by the tool's step.
- **Claude's and Codex's own MCP stay shut.** Claude runs with `--strict-mcp-config`, and the
  runner refuses a tool it has no step for.
- **The channel has to become general.** See "Performed systems: what to shore up first".

## `pkg-nix`

At first it is provided at the start, and has no verbs.

- **A base, per doer.** The operator names one profile for each doer, beside its ceiling. Every
  task of that doer starts with it. It takes the place of the toolchain the image carries today.
- **What a spawn adds is one attribute.** `flake` names a flake output or a profile. The runner
  realises it before the task's process starts. It is fixed for the run, it is bounded by a
  pattern in the ceiling, and no row decides it.
- **Why one attribute, and not a list of packages.** A block's attributes are scalars. An array
  under a block's key is read as a verb's shapes (`pkg/systems/environment.go`, `readBlock`).
  The list of packages lives in the flake.
- **What the task sees.** The closure of the base and the closure of the flake are lists of
  store paths. The runner binds each of them from its store into the sandbox, read-only, at the
  launch. The store itself is the runner's alone, and is never bound whole. The task's `PATH`
  names the flake, then the base. Nothing changes during the run.
- **So what a task has is scoped to the task.** It sees its own closures and no other task's.
  It costs one mount for each store path: hundreds for a toolchain, a few thousand for a large
  one. It needs no links, and the task's directory need not be on the store's volume.
- **A task that needs more is spawned again,** with another flake.
- **A reference is remote.** The ceiling's pattern on `flake` admits no local path. Given one,
  the runner would copy a task's files into a store that other tasks draw on.
- **The sandbox changes.** bwrap binds a fixed list of system directories
  (`pkg/runner/runnerd/sandbox.go`, `systemDirs`), and `/nix` is not one. `PATH` is the runner's
  own value today, passed through per doer (`shellEnv`).
- **Its secrets are the operator's.** A private cache's token and a private flake's host
  credential come from the store, bound in the runner's configuration.
- **Substitution only, at first.** A build runs a derivation's code on the runner's side. Until
  that is confined, the runner substitutes from caches the operator trusts and builds nothing.
- **Storage.** The store needs a volume that outlives the pod. Every runner's `/state` is an
  `emptyDir` today, and only docd has PVC knobs in `deploy/build.tony`.
- **The pod.** It runs as uid 65532 with no capabilities, so the store is single-user, with no
  root daemon.
- **A root per live task, and per base,** so that the store's garbage collection keeps their
  paths.

Not tested, under bwrap in the runner's pod: how long a few thousand mounts take at a launch,
and that bwrap takes that many.

Later, one verb: `install`, with the field `package`. Not `upgrade` and not `list`:

- **`upgrade`.** A task lives for minutes or hours. It has little to upgrade.
- **`list`.** It needs no credential. The task reads its own profile.
- **A caution for when `install` arrives.** A block that names no verb gives every verb of its
  system (`pkg/systems/environment.go`, the empty block). A block written as `{flake: …}` then
  gives `install` too.

## Open: how a task installs during a run

This is open only for `install`. At the start, the binds above settle it.

**A nix package cannot be moved.** Every file names its dependencies by their absolute path in
the store, `/nix/store/<hash>-<name>`: the loader, the library paths, the first line of a
script. So whatever a task is given has to stand at `/nix/store`, under the same names. A tree
of `bin` and `lib` somewhere else does not run. A store under another name changes every hash,
and the public binary cache then serves nothing.

**A profile restricts nothing.** A profile is a tree of `bin`, `lib` and `share` that holds
symbolic links into the store, and no package. It is itself one path in the store. It says what
was chosen. What a task can reach is what its filesystem holds, and a profile's closure is the
list of that.

**Profiles compose.** A profile takes another profile as one of its parts. So a task's profile
is the base, then the spawn's flake, then what the task installed.

There are four ways. The first is tentatively preferred. Which one to build is open.

### 1. The runner installs, and links into the task's own directory. Tentatively preferred

- The base and the flake are bound at the launch, as above.
- The task also has a directory of its own, bound read-only under those mounts. An `install`
  is a performed step. The runner realises the package into its store, and makes hard links in
  the task's directory, for the paths of the package's closure that the base and the flake do
  not already give. They appear in the running sandbox.
- **So an install is scoped to its task.** No other task sees it, and the runner's store keeps
  the bytes once.

What it costs:

- **One volume.** A hard link does not cross filesystems. A task's directory of links lives on
  the store's volume, and not under `TMPDIR`, where a task's directory is today.
- **One link per file of what a task adds.** The removal pays for the links again, and a
  store's trees are read-only, which is the removal problem of 54f9wqb3.

What it does not bound: a task sees closures. A package the ceiling allows brings its
dependencies, and the task can run each of them by its path. So the ceiling bounds what a task
asks for, and not what is there.

Not tested, under bwrap in the runner's pod:

- binding single store paths on top of a directory that is itself a bind, and then read-only;
- hard links made in that directory appearing in a running sandbox;
- that a task cannot link out of its store into its workspace;
- that a single-user store lets the runner make the links.

### 2. A profile per task, over the whole store. Simpler, and installs leak

- The runner's store is bound read-only into every task, whole. Each task has a profile of its
  own, and its `PATH` names it.
- It costs the bind and the profile, and nothing else.
- **What it bounds:** what is realised, and what is on the task's `PATH`. It does not bound what
  runs. A task can list the store, and run anything in it by its path.

An install is not scoped to its task here, and that leaks tools across tasks:

- **Past the environment and the gate.** A task runs a tool that its spawn did not give, and
  that nobody was asked about, because another task installed it.
- **Across ceilings.** A ceiling is a doer's, and doers share a runner. What a script installed,
  an agent whose ceiling refuses it can run.
- **Tasks see each other.** The store's listing shows what other tasks installed.

It is not a way for a task's own data to reach another task, while the runner only substitutes
and a reference is remote.

### 3. Several stores in one runner

- nix keeps a store under another directory (`--store <dir>`), and that store still stands at
  `/nix/store` for what is in it. So the public cache still serves it. The runner keeps one
  store per environment (per doer, per ceiling, or by name), and binds one of them, whole, into
  each task.
- An install is scoped to an environment, and not to a task. Every task of that environment
  sees it. A common dependency is kept once per environment.

### 4. The task runs nix itself, over the base

- The task's `/nix` is an overlay: the doer's base store below, read-only, and a directory of
  the task's own above. The task runs nix as a command, and writes only its own layer.
- **An install is scoped to its task,** with no links, no mount per path, and no shared volume.
- **The gate is weaker.** An install is a command (`commands.run`), and that gate is advice.
  What bounds it is the task's network: which caches it reaches.
- **The operator's secret does not reach it.** A private cache's token would have to be the
  task's.
- **A build is the task's own,** inside its sandbox.
- Not tested, and from memory: that bwrap makes an overlay without privilege in the runner's
  pod. nix also has an experimental store for this (`local-overlay`). It is not examined here.

### Why it is open

The main question is whether an install can be scoped to a task.

- **The first way** says yes, and the runner enforces the ceiling on packages. It rests on
  mechanics that are not tested.
- **The fourth way** says yes with simpler mechanics. It gives up the performed step and the
  operator's secret.
- **If neither holds,** the choice is between the second way, which leaks tools across tasks,
  and the third, which scopes an install to an environment.

One limit stays in the first way: its ceiling takes in dependencies. It is worth less than a
ceiling that bounds exactly what is there. That weighs toward the fourth way, where the network
is the bound.

## `secret-exec`

- **Why it exists.** A command that needs no secret is `commands.run`, or a script. This tool is
  only for a command that needs one.
- **The goal.** The task never holds the secret. A command uses it, and the task has the
  command's result.
- **The shape.** The verb `exec` of the `secret` system, so the tool is `secret-exec` and the
  step is `secret.exec`. The call names an `argv` (a list, never a shell string), plain `env`,
  and each secret with where it goes: a variable's name, or a file.
- **One step per secret.** A call that names two secrets is two steps, and it runs only where
  both pass. The loop already decides a request of several steps this way (a Codex patch that
  touches two paths; `Supervisor.SetStepsOf`). So a row on `{system: secret, name: prod-db}`
  decides every use of that secret.
- **Where it runs.** The runner starts the command in a second sandbox, over the task's
  workspace and under the task's network. The secret is in that process alone. The task's other
  processes are in another pid namespace and cannot read it.
- **What keeps the secret.** The environment and the autonomy say which commands run with it.
  Where each of those commands keeps the secret, the task never has it.
- **A leak is the user's.** An environment and an autonomy that let through a command that
  shows the secret (`sh -c 'echo $S'`) leak it. That is the leak of whoever wrote them. The
  runner runs what they admit, and does not judge a command.
- **Redaction is a second line.** The runner knows the values and can remove them from the
  output. An encoding defeats it.
- **The output goes to a file.** The runner writes the command's output into the task's
  workspace, and answers with the exit status and the end of the output. So the channel carries
  no stream.

What the runner has to give, so that an environment can keep a secret:

- **The whole argv is matched.** A pattern on the start of it admits
  `npm publish --registry <another host>`. The forms of a field are scalars today, so an argv
  needs a form of its own.
- **The plain `env` is shaped too,** and closed by default. `LD_PRELOAD` or `NODE_OPTIONS`
  makes any command the task's.
- **The program is the runner's.** It is found in the system's directories and the nix store,
  and never in the workspace.
- **The workspace is the task's.** A command that runs what it finds there runs the task's
  code: `npm publish` runs the package's scripts, unless the argv says `--ignore-scripts`. The
  shape has to say so. The runner cannot.
- **The runner's check decides each step of an act.** It decides one today.

Two other forms, set aside:

- **A proxy that adds the credential.** The egress proxy would add a credential for a host the
  operator binds. It is not general. It works per protocol and per service, and only where
  somebody has written it. A secret in a command's environment works with whatever can use one:
  a signature, an ssh key, a database's password.
- **A secret placed in the sandbox at the start.** The task would hold it, and only the ceiling
  would bound it. A step is what gives the autonomy a use to decide.

## Performed systems: what to shore up first

Three new systems lean on the runner's side of the channel (`pkg/runner/runnerd/delivery`). It
holds by convention today.

1. **The check is each handler's to remember.** Each handler builds its step and calls `refused`
   itself, in 12 places. A new handler that forgets is not gated. Make it a table: an op gives
   its steps and its act, and `answer` checks every step before any act. Several steps to one
   act (`secret.exec`) then need nothing more.
2. **The request is a struct with a field for each system** (`Channel`, `Emoji`, `Team`), and
   each handler puts the step's fields together by hand. Make the request an op, fields and a
   body, and read the fields by the system's document. Both sides then decide the same step. An
   MCP call's arguments and an argv need this anyway.
3. **An answer is not bound to what is performed.** The runner matches an answer by the
   decision's id and the step's fields. A Slack post's text is not a field, and an MCP call's
   arguments would not be. So a task can be approved for one body and send another. Put a
   digest of the body in the step. This fits q3k89kva.
4. **What a runner performs is three lists in the code** (`performs`, `held`, `credentials` in
   `pkg/runner/runnerd/stock/config.go`). Register a performed system as one value. With the
   store, a system whose secret is absent is closed, for every system.
5. **One deadline, one act at a time.** Every op runs under `workspace.ForgeTimeout`, in turn.
   An MCP call, an install and a command do not fit that.
6. **A system's document does not say its kind.** Say it there, and publish it in the
   description: performed, inner, or provided at the start. A charter's author then knows
   whether a row is a wall or advice.
7. **One conformance test,** once the dispatch is a table: no op acts under an empty
   environment.

## The order to build in

1. The channel: items 1, 2 and 5 above.
2. The secret store, and `mcp`.
3. `pkg-nix`, at the start only.
4. `secret-exec`.
5. `install`, once its open question is settled.

## A sketch

```tony
# the runner's configuration
secrets: {dir: /secrets}
mcp:
  servers:
    linear: {url: "https://mcp.example.com/mcp", auth: {secret: linear-mcp}}
agents:
  claude:
    systems:
      mcp:     {ceiling: [{server: linear}]}
      # linear-mcp is not here: no command gets it
      secret:  {ceiling: [{name: npm-publish, exec: {argv: !regexp "^npm publish --ignore-scripts( --tag [a-z]+)?$"}}]}
      pkg-nix:
        base: "github:acme/tools#base"              # what every claude task starts with
        ceiling: {flake: !regexp "^github:acme/"}   # later: install: {package: !glob "nixpkgs#*"}

# a spawn's environment
environment:
  systems:
    mcp:     [{server: linear, call: {tool: !or [get_issue, list_issues]}}]
    pkg-nix: {flake: "github:acme/tools#ci"}
    secret:  [{name: npm-publish, exec: {argv: "npm publish --ignore-scripts"}}]
```

## What holds whatever we choose

- **The task chooses the argv, within what the environment admits.** The runner does not judge
  whether a command keeps a secret. An environment that admits one that shows it leaks it.
- **A block's attributes are scalars.**
- **A nix package cannot be moved.** What a task is given stands at `/nix/store`.
- **A description is public.** `GET /describe` answers anyone, so the names of secrets are
  public. A name must not be a secret.
- **Verse and its runners upgrade together.** The systems' documents are compiled into both.
  Versed refuses a description that names a system it does not know.
- **`-sandbox none` contains nothing.** That is `verse up`'s runner on macOS. There a secret
  given to a command is the machine's.

## Open questions

1. **How a task installs during a run.** The section above. It does not hold up the first
   version, which provides packages at the start only.
2. **Names.** `pkg-nix` or `nix-pkg`: the existing systems put the provider first
   (`linear-issue`, `github-issue`). And `secret-exec`, or another name for the same tool.
3. **What an approver sees of an MCP call.** A question shows the step's fields (q3k89kva). The
   call's arguments are fields only if the document says so, in the form `doc`. Item 3 of the
   shoring list binds the answer to them either way.
4. **Which names an offer publishes.** Every secret the runner holds, or only those some ceiling
   gives.
5. **A task's own identity at an MCP server.** A runner's secret is one identity for every
   spawner. A server that trusts verse's issuer could take the credential a gate mints
   (`credential: {audience: …}`, hfh7km3t) instead. Later.
6. **stdio MCP servers.** How the runner confines one.
7. **The empty block, when `install` arrives.** Whether a `pkg-nix` block that names no verb
   should give it.
8. **A form for an argv.** How a shape matches a whole list, with no doubt about where one
   argument ends.

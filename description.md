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

| System | Fixed at start, not on the dial | Verbs, each a step | Who performs it | Its credential |
|---|---|---|---|---|
| `mcp` | which servers | `call` | the runner, as the MCP client | held by the runner, bound by the operator |
| `pkg-nix` | the flake the task starts with | `install`, `upgrade`, `list` | the runner | held by the runner, bound by the operator |
| `secret` | which secrets | `exec` | the runner, in a second sandbox | given to one command, and readable by the task |

All three are performed systems, as `git` and `slack` are. The runner acts outside the task's
sandbox, and checks each act again against the environment, the autonomy and the ceiling
(`pkg/runner/runnerd/delivery/server.go`, `refused`).

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
- **The channel has to become general.** The task's channel to the runner is a flat struct with
  an op switch (`pkg/runner/runnerd/delivery/protocol.go`). It carries one request at a time,
  and a frame is at most 1 MiB.

## `pkg-nix`

- **A base, per doer.** The operator names one profile for each doer, beside its ceiling. Every
  task of that doer starts with it. It takes the place of the toolchain the image carries today.
- **What a spawn adds at the start is one attribute.** `flake` names a flake output or a
  profile. The runner realises it before the task's process starts, as it checks out a
  repository. It is fixed for the run, it is bounded by a pattern in the ceiling, and no row
  decides it.
- **Why one attribute, and not a list of packages.** A block's attributes are scalars. An array
  under a block's key is read as a verb's shapes (`pkg/systems/environment.go`, `readBlock`).
  The list of packages lives in the flake.
- **The verbs are steps.** `install` and `upgrade` carry the field `package`; `list` carries
  none. The runner performs each: it realises into its store and moves a profile of the task's
  own. The task's `PATH` names that profile.
- **A reference is remote.** The ceiling's patterns on `flake` and `package` admit no local
  path. Given one, the runner would copy a task's files into a store that other tasks draw on.
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

## Open: what a task sees of the nix store

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

There are three ways to give a task its packages. The first is tentatively preferred. Which one
to build is open.

### 1. A base, and the task's own installs. Tentatively preferred

**The central store is the runner's alone.** It is never bound whole into a task. The runner
realises into it, and it is a cache.

A task's `/nix/store` is put together from two parts:

- **The base, bound at the launch.** The closure of the doer's base is a list of store paths.
  The runner binds each of them from the central store into the sandbox, read-only, when the
  task starts. The closure of the spawn's `flake` is bound the same way. Nothing is linked. The
  cost is one mount for each store path: hundreds for a toolchain, a few thousand for a large
  one.
- **The task's own installs, linked during the run.** The task has a directory of its own,
  bound read-only under those mounts. An `install` makes hard links in it, for the paths of the
  package's closure that the base and the flake do not already give. They appear in the running
  sandbox.

**So an install is scoped to its task.** It changes one task's directory. No other task sees
it, and the central store keeps the bytes once.

What it costs:

- **One volume.** A hard link does not cross filesystems. A task's directory of links lives on
  the store's volume, and not under `TMPDIR`, where a task's directory is today.
- **One link per file of what a task adds.** The base costs no links. The removal pays for the
  links again, and a store's trees are read-only, which is the removal problem of 54f9wqb3.
- **A root per live task, and per base,** so that the central store's garbage collection keeps
  their paths. A task that started before a base changed keeps the paths it was bound.

What it does not bound: a task sees closures. A package the ceiling allows brings its
dependencies, and the task can run each of them by its path. So the ceiling bounds what a task
asks for, and not what is there.

Not tested, under bwrap in the runner's pod:

- binding single store paths on top of a directory that is itself a bind, and then read-only;
- hard links made in that directory appearing in a running sandbox;
- how long a few thousand mounts take at a launch;
- that a task cannot link out of its store into its workspace;
- that a single-user store lets the runner make the links.

### 2. A profile per task, over the whole store. Simpler, and installs leak

- The central store is bound read-only into every task, whole. Each task has a profile of its
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
- nix also has an experimental store that layers over a read-only lower store
  (`local-overlay`). It needs an overlay mount. It is not examined here.

### Why it is open

The main question is whether an install can be scoped to a task. The first way says yes, and
it rests on mechanics that are not tested.

- **If they hold,** the base is easy to keep: the operator names one profile for each doer, and
  a task pays only for what it adds.
- **If they do not,** the choice is between the second way, which leaks tools across tasks, and
  the third, which scopes an install to an environment.

One limit stays in the first way: its ceiling takes in dependencies. It is worth less than a
ceiling that bounds exactly what is there.

## `secret-exec`

- **Why it exists.** A command that needs no secret is `commands.run`, or a script. This tool is
  only for a command that needs one.
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
- **What it promises.** The task chooses the argv, and `sh -c 'echo $S'` prints the secret. So a
  secret given to a command is readable by the task, unless the shapes hold the argv to commands
  that do not reveal it. The shapes on `argv`, in the environment and the ceiling, are the wall.
- **Redaction is a second line.** The runner knows the values and can remove them from the
  output, which goes onto the task's record. An encoding defeats it.
- **What it needs.** The channel has to carry long output, streamed or cut. The runner's check
  decides one step of an act today, and has to decide each.

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
      secret:  {ceiling: [{name: npm-publish}]}     # linear-mcp is not here: no command gets it
      pkg-nix:
        base: "github:acme/tools#base"              # what every claude task starts with
        ceiling: {flake: !regexp "^github:acme/", install: {package: !glob "nixpkgs#*"}}

# a spawn's environment
environment:
  systems:
    mcp:     [{server: linear, call: {tool: !or [get_issue, list_issues]}}]
    pkg-nix: {flake: "github:acme/tools#ci", install: {package: "nixpkgs#jq"}}
    secret:  [{name: npm-publish, exec: {argv: !regexp "^npm publish"}}]
```

## What holds whatever we choose

- **The task chooses the argv.** A generic exec cannot keep a secret from the task that calls it.
- **A block's attributes are scalars.**
- **A nix package cannot be moved.** What a task is given stands at `/nix/store`.
- **A description is public.** `GET /describe` answers anyone, so the names of secrets are
  public. A name must not be a secret.
- **Verse and its runners upgrade together.** The systems' documents are compiled into both.
  Versed refuses a description that names a system it does not know.
- **`-sandbox none` contains nothing.** That is `verse up`'s runner on macOS. There a secret
  given to a command is the machine's.

## Open questions

1. **What a task sees of the nix store.** The section above. A base bound at the launch, with
   the task's own installs linked beside it, is tentatively preferred, and scopes an install to
   its task. It rests on mechanics that are not tested. The other ways are a profile per task
   over the whole store, which leaks installs across tasks, and several stores, which scope an
   install to an environment.
2. **Names.** `pkg-nix` or `nix-pkg`: the existing systems put the provider first
   (`linear-issue`, `github-issue`). And `secret-exec`, or another name for the same tool.
3. **What an approver sees of an MCP call.** A question shows the step's fields (q3k89kva). The
   call's arguments are fields only if the document says so, in the form `doc`.
4. **Which names an offer publishes.** Every secret the runner holds, or only those some ceiling
   gives.
5. **A task's own identity at an MCP server.** A runner's secret is one identity for every
   spawner. A server that trusts verse's issuer could take the credential a gate mints
   (`credential: {audience: …}`, hfh7km3t) instead. Later.
6. **stdio MCP servers.** How the runner confines one.

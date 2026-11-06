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

- **What a task starts with is one attribute.** `flake` names a flake output or a profile. The
  runner realises it before the task's process starts, as it checks out a repository. It is
  fixed for the run, it is bounded by a pattern in the ceiling, and no row decides it.
- **Why one attribute, and not a list of packages.** A block's attributes are scalars. An array
  under a block's key is read as a verb's shapes (`pkg/systems/environment.go`, `readBlock`).
  The list of packages lives in the flake.
- **The verbs are steps.** `install` and `upgrade` carry the field `package`; `list` carries
  none. The runner performs each: it realises into the store and moves a profile of the task's
  own. The task's `PATH` names that profile.
- **The sandbox changes.** bwrap binds a fixed list of system directories
  (`pkg/runner/runnerd/sandbox.go`, `systemDirs`), and `/nix` is not one. `readOnly:` is the
  existing slot for a host path bound read-only. `PATH` is the runner's own value today, passed
  through per doer (`shellEnv`).
- **Its secrets are the operator's.** A private cache's token and a private flake's host
  credential come from the store, bound in the runner's configuration.
- **Substitution only, at first.** A build runs a derivation's code on the runner's side. Until
  that is confined, the runner substitutes from caches the operator trusts and builds nothing.
- **Storage.** The store needs a volume that outlives the pod. Every runner's `/state` is an
  `emptyDir` today, and only docd has PVC knobs in `deploy/build.tony`.
- **The pod.** It runs as uid 65532 with no capabilities, so the store is single-user, with no
  root daemon.

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
      pkg-nix: {ceiling: {flake: !regexp "^github:acme/", install: {package: !glob "nixpkgs#*"}}}

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
- **A description is public.** `GET /describe` answers anyone, so the names of secrets are
  public. A name must not be a secret.
- **Verse and its runners upgrade together.** The systems' documents are compiled into both.
  Versed refuses a description that names a system it does not know.
- **`-sandbox none` contains nothing.** That is `verse up`'s runner on macOS. There a secret
  given to a command is the machine's.

## Open questions

1. **Shared nix installs. Open.** One store, bound read-only into every task, lets every task
   list and run whatever any task realised. The ceiling and the gate on `install` then bound
   what is realised, and not what is run. Two directions:
   - **Several nix installs in one runner.** A task is bound to one of them: per doer, per
     ceiling, or per named environment.
   - **One shared store, adapted to each task's filesystem.** A task sees only what its ceiling
     allows. A sandbox's mounts are fixed when it starts, so an `install` during the run needs a
     way to add paths. A directory of the task's own, bound at `/nix/store` and filled by the
     runner (a link farm, an overlay), is one candidate. It is not tested.
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
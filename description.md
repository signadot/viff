# verse-runner: implementation plan for the Runners doc and the spawn spec

**Status:** draft. Implements the Runners doc
((a Notion page)) and its companion,
the spawn spec, for one runner, `verse-runner`, which offers `agent: verse` (verse's own loop)
and scripts. `claude-runner` and `codex-runner` come later.

## Approach

- **Runner first.** The runner is built and settled before verse changes.
- **No backwards compatibility.** The runner API is replaced by the spec's shape. Anything that
  doesn't fit the specs is removed, not kept as an option.
- **A feature branch, `verse/runner-spawn-refactor`.** Each step is a PR against it, and verse
  is allowed to break on it. Every PR still compiles and passes its tests. Tests of removed
  concepts are deleted, and `smoke.sh` is cut back to what still works.

## Where we start

| Spec | Today |
|---|---|
| a runner process offering `agent: verse` and scripts | `localRunner` (`versed/server.go`) runs the harness in-process: `llm` is the Portkey loop and `scripted` is a shell command. The coderunner (`docs/reference/examples/coderunner/`) runs the same harness in a child process per task |
| the launch: `doer`, `with`, `autonomy`, step gates, `timeout`, `annotations` | `task.Launch` / `SpawnRequest` (`task/runner.go`, `task/wire.go`): `driver`, `environment`, `tools`, `frame`, `virtualKey`, `calls`, `vector`, `grant`, `credential`, … |
| `GET /describe` and offers | none. `runner.Probe` only calls `GET /tasks`. The coderunner has an unused `GET /runner` stub |
| runner configuration (`listen`, `verse`, `github`, `models`, `annotations`) | `config.LLMConfig` in verse, and the coderunner's `Config` with environments |

## Steps

### 1. verse-runner, its configuration, and `/describe`

The runner on **today's** API: today's launch is still accepted, and nothing in verse changes.

- A `runnerd verse` command. It serves the task contract and runs the existing harness:
  `driver: llm` runs the verse agent, and `driver: scripted` runs a script.
- The configuration from the spec, less `concurrency`, `hold` and `interpreters` (dropped after
  Scott's review):
  - Working now: `listen`, `models.portkey.virtualKey`, `model`, and the virtual-key annotation.
  - Parsed and validated, but with no effect yet: `verse`, and `GITHUB_TOKEN`.
- Scripts run by their own `#!` line, or bash: the image decides what's available.
- `GET /describe` answers the offers from the configuration: `agent: verse` (with its `with`
  values, intercepts and annotations) and `script` (with no `with`).
- No sandbox yet: step 1b.
- `task/taskd` merged into `runner/runnerd`: the runner host, embedded by `verse up` or served
  by `runnerd`.
- **Try it on real work:** register the runner and run `verse task run --runner verse-runner`,
  or point a test charter at it with `runner:`. This is the last point where the runner can run
  real charters before the spawn changes.

### 1b. One process per task, under bwrap

- Each task runs in its own process, as the coderunner does (`hop.go`, `taskproc.go`), confined
  by bwrap (`confine.go`): verse's own agent and scripts alike. This settles the spec's open
  sandbox question for verse-runner, and matches Scott's review (our own bwrap, not a vendor's
  sandbox, for every runner).
- Open: `verse up` on macOS, where bwrap doesn't run.
- **The image.** In step 1, `runnerd` rides in the verse image beside `iod`. With bwrap, and
  scripts that run whatever their `#!` names, the image is where a runner's tools come from:
  decide here between the verse image plus bwrap and toolchains, or a runner image of its own
  (as the coderunner has), and add `deploy/verse-runner`.

### 2. The new runner API (breaking)

Each of these is its own PR.

**2a. The launch.**
- `task.Launch` and `SpawnRequest` are replaced by the spec's shape:
  - `doer{agent | script, with, annotations}`
  - `with{model, effort, repos, network}`
  - `prompt`, `autonomy`, step gates, `answer`, `timeout`, provenance, and the task id.
- verse-runner implements `agent: verse` and `script` from it, using `model`, `effort` and the
  `verse.portkey/virtual-key` annotation. A script is run by its `#!` line.
- **Removed:** `driver`, `environment`, `tools`, `frame`, `virtualKey` (as a field), `calls`,
  `vector`, `grant`, `credential`. The coderunner is removed too, replaced by verse-runner. The
  code in verse that builds today's launch from a spawn goes too, so **verse can't launch work
  from here until step 4.**
- **The broker:** evaluate it here. The broker carries launches to workers, so at minimum it has
  to carry the new one. Whether it stays as a place to send work is decided at this point.
- **The embedded runner stays:** `verse up` keeps hosting verse-runner in its own process
  (`versed.Local`, a `runnerd.Daemon`), registered like any runner but reached by Go calls. A
  runner entity needs a `url` today, so an embedded one needs a way to be registered.
- **A central deployment runs no work in verse:** a `versed` setting with no embedded runner.
  The ask `then:` path (`versed/asks.go runThen`), which always runs in verse, moves to the
  runner that offers the doer.

**2b. Repos and network.**
- Per-task workspaces from `with.repos`: a mirror plus a clone, starting at `from`, taken from
  the coderunner's `workspace.go`.
- The `prepare-commit-msg` hook adds `Verse-Task`.
- `network` (`none`, `allow`, `open`) is enforced by the coderunner's egress proxy. The model API
  is always reachable.

**2c. Autonomy and step gates.**
- Gate matching is a shared function: the step table, and `*`, `**` and whole-command globs.
- `task.start` is gated by `autonomy` or by a gate.
- `files.write` and `commands.run` are gated only by a gate. They map to the harness's write and
  exec verbs.
- `repos.push` and `repos.pr` wait for step 5, which adds the tools they gate.

### 3. Verse reads and stores `/describe`

- **Decide first:** whether several runners may offer the same agent name, with verse spreading
  work among them. Scott's review expects agent versions and parallelism (`codex-3`, several
  replicas registered as runners), which the uniqueness rule below rules out.
- `verse runner add` and `check` call `/describe` and store the offers on the runner entity.
- Registration is refused if an agent name is already offered, or if a second runner offers
  scripts.
- This step depends only on step 1, so it can be done before or alongside step 2.

### 4. The spawn (verse launches work again)

**4a. The spawn shape and dispatch.**
- `trigger.Action.Spawn` becomes the spec's spawn, with a deep `CheckFields` and the install
  checks.
- Work goes to the runner that offers the doer, found from the stored offers.
- The ad-hoc path (`verse task run`) and the ask `then` path (`versed/asks.go runThen`) move
  with it.
- **Removed:** `runner:`, `driver:`, `environment:`, `provider:`, `vector:` and `grant:` on
  spawns.

**4b. Migrate charters and docs.**
- Migrate `.verse/charters`, `deploy/charters` and the docs (`action.md` §spawn, `runners.md`,
  `reference/runners.md`, `task-control.md`, `up.md`).
- `pr.tony` and `merge.tony` lose `calls:` and become a script or an agent.
- `smoke.sh` is restored using `script` doers.

**4c. In trouble, and waiting for a runner.**
- Rules get a new trouble kind, `unfit`. It is evaluated at install, when runners change and
  when verse restarts.
- Tasks get a `waiting` status. A task waits while its runner is missing or unreachable, and
  fails at `timeout`. (No "busy": runners have no concurrency limit.)
- A task whose runner no longer has it fails with "the runner lost it".

### 5. Verse's tools (later)

- The runner's parent process holds `github.token` and performs `push`, `create-pr` and
  `update-pr`, checked against `access` and `push`. Add `ask` as well.
- The verse agent gets them as harness tools, and scripts get them as `verse` commands.
- Verse side: a record of the branches each charter's runs created.
- Add the `repos.push` and `repos.pr` gates.
- Parking and resuming the verse agent, if a waiting agent's process must be freed. Each ask
  already has its own deadline, so there is no `hold` setting.
- **This is needed before verse-runner can replace the coderunner in deploy:** every deployed
  charter pushes.

### 6. Local and deploy

- `verse up` embeds verse-runner and registers it.
- `deploy/verse-runner` replaces `deploy/coderunner` (one replica, a Recreate strategy).
- Then merge `verse/runner-spawn-refactor` to main.

## Dependencies

```
1 ─ 1b ─┬─ 2a ─ 2b ─ 2c ─┬─ 4a ─ 4b ─ 4c ─ 5 ─ 6
        └─ 3 ────────────┘
```

## Known gaps, recorded as they come up

- **Refusing launches from another verse:** the `verse` key has no effect until the runner has
  an identity check.
- **The sandbox:** bwrap, in step 1b. Runs use the host until then, and bwrap doesn't run on
  macOS.
- **The broker:** evaluated in step 2a.
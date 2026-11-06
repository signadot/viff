# runner: a secret store, one file per name, beside the credentials in the environment

**Status:** a plan. Nothing is built. It blocks 1f5art3x, and the `mcp` system waits on it.

## What it is

The runner's secret store, from 1f5art3x ("Secrets: one store, bound in two ways"), and item 4
of its list "Performed systems: what to shore up first".

- **The store.** A directory the deployment mounts. One file per secret, and the file's name is
  the secret's name. The runner reads a value at each use, so a rotation needs no restart.
- **A binding is the operator's.** The runner's configuration says which secret a system acts
  with. A task never names one, and never holds one.
- **A system whose secret is absent is closed,** and the runner says so at its start.

This issue builds the store and the operator's binding. The `secret` system, where a spawn binds
a secret to a command, is a later issue.

## What is there today

All of it is in `pkg/runner/runnerd/stock`.

- **Three variables with fixed names** (`config.go`): `EnvGitHubToken`, `EnvLinearAPIKey`,
  `EnvSlackBotToken`. "No configuration names one."
- **`TokensFromEnv`** reads them once, at the runner's start, into `Tokens`. `services` copies
  them into `delivery.Services` as strings (`LinearAuth`, `SlackToken`). The git token goes to
  `runnerd.NewRepos`, which builds its `workspace.TokenAuth` then. So a rotation is a restart.
- **`held`** is a list of two pairs: `linear-issue` and `slack`, each with its variable.
  `Config.Held` closes a system whose variable is not set, for every doer, and warns. `git` is
  not in it: with no `GITHUB_TOKEN` the runner still fetches what is public, and `CheckStart`
  only warns.
- **`performs`** names `linear-issue` and `slack` again, in a list of its own. `needsFor` and
  `checkSystems` name systems again.
- **`credentials`** is every variable that holds a credential, the agents' model keys among
  them. `checkShellEnv` refuses a `shellEnv` that names one.
- **`Secrets`** (`runner.go`) is another thing with a near name. It is the model keys, which the
  runner sends to the task's process on its stdin (`taskConfig`), since that process calls the
  model. It is not a store, and what is in it the task's process holds.
- **The deployment** gives each credential as a variable from a Kubernetes Secret
  (`deploy/verse-runner/verse-runner.yaml`, and the `claude-runner` and `codex-runner` beside
  it), each `optional: true`. `verse up` gives its runner this process's environment
  (`cmd/verse/localrunner.go`, `localRunnerEnv`).

## The decision: the store stands beside the environment

The credentials in the environment stay where they are, under the names they have. Nothing
moves in a deployment that does not ask for it.

- **Every deployment and `verse up` use the variables.** The manifests, `deploy/secrets/`,
  `deploy/README.md` and `localRunnerEnv` all name them. Moving them changes every deployment,
  and gives it nothing it lacks.
- **The model keys are not the store's.** The task's process holds them, by design. The store
  holds only what a task never holds.
- **The git token is built in at the start.** `NewRepos` makes the mirrors' auth once. Reading
  it at each use is a change to `workspace`, apart from this.
- **`linear-issue` and `slack` may name a secret instead.** They are the two systems `held`
  already closes. This gives the store a real use before `mcp`, and it gives those two a
  rotation with no restart. Where the configuration names no secret, the variable is read, as
  today.

## The plan

Each step builds and passes on its own.

1. **The store, as a package of its own.** `pkg/runner/runnerd/secretstore` (not `secrets`: the
   stdin struct has that name). A `Store` over a directory. `Get(name)` reads the file each
   time, and drops one trailing newline. `Has(name)` says whether it is there. A name is a
   file's name and nothing else: lower case, digits, `-`, `_` and `.`, not starting with a `.`,
   no separator. An error names the secret and never its value. It sits under `runnerd`, so
   `delivery` can use it without `stock`.
2. **Its place in the configuration.** `secrets: {dir: /secrets}` in `stock.Config`.
   `Config.Check` takes an absolute path. `Standalone` adds the directory to
   `runnerd.KeptDirs`, so `CheckReadOnly` refuses a `readOnly` path that holds it or lies in it.
   No sandbox binds it (`pkg/runner/runnerd/sandbox.go` binds `systemDirs`, the task's
   directory, the programs and `readOnly`). `CheckStart` warns where the directory is not there.
   The task's process is sent the configuration (`taskConfig`), so it learns the path, and
   nothing in it.
3. **One table of what the runner performs.** One value for each performed system: its name,
   when the runner performs it (it checks out repos; its forge is GitHub; a ceiling names it),
   and what it acts with. `performs`, `held`, `TokensFromEnv`, `needsFor` and the test in
   `checkSystems` read the table. Nothing the runner does changes in this step, and
   `credentials_test.go` says the same as before.
4. **A credential is read where it is used.** `delivery.Services` holds a function for
   `LinearAuth` and for `SlackToken`, in place of a string. One read from the environment
   answers what was read at the start. `slack.go` and `issues.go` call it at the act.
5. **A system may name its secret.** A section for the system at the top of the configuration
   says so: `slack: {auth: {secret: slack-bot}}`, `linear-issue: {auth: {secret: linear}}`. The
   `mcp` servers' sections take the same `auth` later. The table's entry then reads the store.
   `Config.Check` refuses a secret's name that is no name, and a section with no `secrets.dir`.
6. **Closed where the secret is absent.** `Config.Held` reads the table: a system that names a
   secret the store does not have is closed for every doer, with the warning it gives today for
   a variable. A secret that goes missing later fails the act, with an error the task reads.
   `git` stays as it is: warned, not closed.
7. **The deployment.** An optional Secret volume at `/secrets`, read-only, in the three runner
   manifests, and `secrets: {dir: /secrets}` in their configuration. An entry in
   `deploy/secrets/` and a knob in `deploy/build.tony`, as the other Secrets have. The variables
   stay.
8. **The words.** `docs/reference/runnerd.md` and the credentials table in `deploy/README.md`.
   The comment on `runnerd.Config` (`pkg/runner/runnerd/config.go`) says secrets are read from
   the environment; it gains the store.

## Tests

- **The store.** A name that is no name is refused. A file changed between two reads gives the
  new value. A Kubernetes Secret's layout (each name a link into `..data`) reads. An absent
  secret's error holds its name and no value.
- **The configuration.** A `readOnly` path over the store is refused at the start. A section
  naming a secret with no `secrets.dir` is refused.
- **Closed.** `TestASystemWithNoCredentialIsClosed` (`credentials_test.go`) gains the store's
  case: named and absent is closed and warned; named and present is offered.
- **At the act.** `slack_test.go` and `issues_test.go` in `delivery`: the header sent is the
  file's value at that call, after a change with no restart.
- **The sandbox.** A task cannot read the store's directory (`sandbox_test.go`, on Linux).

## What does not change

- The variables, their names, and what a deployment sets today.
- `Secrets`, the model keys on the task's stdin.
- `checkShellEnv`, and that no task's shell is given a credential.
- The description. No offer says anything of the store, so `runner.DescriptionVersion` stays.

## Not in this issue

- **The `secret` system and `secret-exec`.** A spawn binding a secret to a command, the
  ceiling on names, and an offer publishing them. A later issue.
- **The `mcp` servers' secrets.** The `mcp` issue binds them, with the `auth` of step 5.
- **The git token and the model keys in the store.**
- **`verse up`.** Its runner has no sandbox on macOS, so a store there is the machine's.

## Open questions

1. **Where a system names its secret.** A section of its own at the top (`slack: {auth: …}`),
   as `mcp:` will have, or one list under `secrets:`.
2. **Whether the git token should follow.** It needs the mirrors' auth to be read at each fetch
   and push.
3. **The value's form.** Dropping one trailing newline suits a file written by hand. A
   Kubernetes Secret has none.
4. **Who can read the directory.** The task's uid is the runner's. Where there is no sandbox
   (`-sandbox none`), a task can read the store.
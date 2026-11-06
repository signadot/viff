# runner: a secret store, one file per name, beside the credentials in the environment

**Status:** steps 1 to 6 are written, and the runner's part of step 8, in the worktree
`.worktrees/secret-store` (branch `verse/secret-store`, on 81250f61, the commit of 1k5art3x).
Not committed. Step 7 and the deployment's part of step 8 are not begun. It blocks 1f5art3x,
and the `mcp` system waits on it.

## Built so far

- **Step 1, the store.** `pkg/runner/runnerd/secretstore`: `Open`, `Get`, `Has`, `CheckName`.
  One thing more than the plan said: a file over 64 KiB is refused as no secret's value.
- **Step 2, its place.** `secrets: {dir: …}` in `stock.Config` (`SecretStore`). `Config.Check`
  takes an absolute path under no system directory. `runnerd.CheckSecretStore` holds it apart
  from the work, mirrors and control directories, as the filesystem resolves them, and
  `Standalone` calls it. `runnerd.KeptDirs` takes the store as a fourth directory, and the
  handler holds it (`SubprocessConfig.Secrets`), so the check at each launch has it.
  `runnerd.UnderSystem` is the sandbox's own test, exported.
  - **The warnings are `Standalone`'s, not `CheckStart`'s.** A directory that is not there, and
    a runner with no sandbox, are said on the runner's log at its start, beside what it says of
    a `readOnly` path. `Standalone` has the sandbox and the directories; `CheckStart` has
    neither.
- **Step 3, the table.** `performedSystems` in `stock/config.go`. `performs`, `Held`,
  `TokensFromEnv`, `needsFor` and `checkSystems` read it, and so does `credentials`, which the
  plan did not name. The lists `held` and the one in `performs` are gone. The tests of
  `credentials_test.go` pass as they were.
  - **Left as it was:** `Runner.Build` (`stock/runner.go`) still names `linear-issue` and
    `slack` where it builds their tools.
- **Step 4, read at the act.** `delivery.Credential` is a function, and `delivery.Holds` is one
  that is a value read at the start. `Services.LinearAuth` and `Services.SlackToken` are
  credentials. `slack.go` and `issues.go` read one as the op reads its request, and the act is
  performed with what was read. One that does not read fails the act with the store's own
  word.
- **Step 5, a system names its secret.** `secrets.systems` (`SecretStore.Systems`).
  `Config.Check` refuses a system the table does not have, one that acts with the forge's
  token, one with no credential of its own, and a name that is no name. An entry of the table
  says whether its credential is its own (`performed.own`), which is both "may name a secret"
  and "closed where it has none". `Config.credential` is what the runner acts with, and only
  `Standalone` calls it.
- **Step 6, closed where absent.** `Config.Held` reads the store for a system that names a
  secret: absent or empty closes it for every doer and says which secret and where; there,
  with its variable set too, says the variable is not read. `TokensFromEnv` does not read the
  variable of a system that names a secret.
- **Step 8, the runner's part.** `docs/reference/runnerd.md`: the keys `secrets.dir` and
  `secrets.systems`, a section "The secret store", a row in "What keeps the secrets from the
  task", and a word on the two variables. The comment on `runnerd.Config`.
- **Tests.** `secretstore/store_test.go`; `TestAReadOnlyPathMayNotReachTheSecretStore` and
  `TestTheSecretStoreIsWhereNoSandboxBindsIt` (`runnerd/readonly_test.go`);
  `TestACredentialIsReadAtTheAct` (`delivery/slack_test.go`); `stock/secretstore_test.go`,
  which has the configuration, the start, the table, the closed cases, and a script that posts
  to Slack with the store's token. The runner and `cmd` suites pass, and `make check`.
  - **Not run:** `TestATaskCannotReadTheSecretStore` needs bwrap, and was written on a machine
    without it. It builds for Linux.
  - **Rule 5 has no test of its own.** The script's post is made by a task's process that was
    sent a configuration naming a secret, and it built its tools. That runner is not confined,
    so the test does not show the process could not have read the store. That the process does
    not is by where `Config.credential` is called.
  - **One failure not accounted for.** `cmd/verse` failed once in a run of every package at
    once, and passed the five runs after, alone and with the others. Which test it was is not
    known: the output was cut.

**What is left.** Step 7, the deployment: the projected volume in the three runner manifests,
`verse-runner-secrets` in `deploy/secrets/` and its knob in `deploy/build.tony`. And the
credentials table in `deploy/README.md`. Nothing here applies a manifest, so that step wants a
cluster to try it on.

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
  it), each `optional: true`. The Secrets are `verse-slack` (key `SLACK_BOT_TOKEN`),
  `verse-linear` (key `LINEAR_API_KEY`) and `verse-runner` (key `gitToken`), and the iods read
  the first two as well. `verse up` gives its runner this process's environment
  (`cmd/verse/localrunner.go`, `localRunnerEnv`).
- **What a task's sandbox has** (`pkg/runner/runnerd/sandbox.go`): `/usr`, `/bin`, `/sbin`,
  `/lib`, `/lib64`, `/etc` and `/opt`, read only (`systemDirs`); the programs; the task's own
  directory, which it may write; and each `readOnly` path. A `readOnly` path is checked against
  the runner's own directories at the start (`CheckReadOnly`, `KeptDirs`), and again at each
  launch (`withReadOnly`, `subprocess.go`), since a path may become a link after the start.

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

## The shape

```tony
secrets:
  dir: /secrets
  systems: {slack: slack-bot, linear-issue: linear}
```

- **One map, under `secrets:`.** `systems` names the secret each system acts with. A system it
  does not name reads its variable, as today.
- **Why a map and not a section for each system.** The loader refuses a key that has no field
  (`runnerd.Load`, `checkFields`). A section for each system is a new field of `stock.Config`
  for each one, which is what the table of step 3 is there to end.
- **An `mcp` server names its own.** A server has a url as well, so its secret is beside it, in
  the server's entry (1v5art3x). It names a secret of this store.

## The rules

1. **No task's sandbox has the store.** `secrets.dir` is refused where a sandbox would bind it:
   under one of the system directories (`/etc/verse/secrets` would give every task every
   secret), or where it holds, is, or lies in the runner's work, mirrors or control directory.
   It is one of the runner's kept directories, at the start and at each launch.
2. **Present means it reads and is not empty.** An empty file is an absent secret, as an empty
   variable is one that is not set. A file the runner cannot read is absent too, and the
   warning says why.
3. **A system that names a secret acts with the store, and its variable is not read.** Where
   the variable is set as well, the runner says so at its start.
4. **A rotation is live. An opening is not.** A value that changes is read at the next act. A
   secret that is absent at the start leaves its system closed until a restart: what is closed
   and what is offered are fixed at the start (`Held`).
5. **Only the runner reads the store.** The task's process is sent the configuration and reads
   it (`performs`, the tools it builds). It is sent it after `Held`, and nothing it runs looks a
   credential up.
6. **With no sandbox, the runner says so.** Under `-sandbox none` a task sees the whole machine
   (`docs/reference/runnerd.md`), the store with it. A runner started so with a `secrets.dir`
   warns at its start, and starts.

## The plan

Each step builds and passes on its own.

1. **The store, as a package of its own.** `pkg/runner/runnerd/secretstore` (not `secrets`: the
   stdin struct has that name). A `Store` over a directory. `Get(name)` reads the file each
   time and drops the newline it ends with (`\n` or `\r\n`). `Has(name)` is whether `Get` would
   give a value (rule 2). A name is a file's name and nothing else: lower case, digits, `-`,
   `_` and `.`, not starting with a `.`, no separator. An error names the secret and never its
   value. It sits under `runnerd`, so `delivery` can use it without `stock`.
2. **Its place in the configuration.** `secrets: {dir: /secrets}` in `stock.Config`.
   `Config.Check` takes an absolute path that is under no system directory (`runnerd` gives the
   test `underSystem` makes today). The directory is a fourth of the runner's kept directories:
   `KeptDirs` takes it, `Standalone` gives it at the start, and the handler holds it
   (`SubprocessConfig`) so the check at each launch has it too. So a `readOnly` path that
   reaches the store is refused at the start, and one that comes to reach it is left out of
   the launch. `CheckStart` warns where the directory is not there, and where there is no
   sandbox (rule 6). The task's process is sent the configuration (`taskConfig`), so it learns
   the path, and nothing in it.
3. **One table of what the runner performs.** One value for each performed system: its name,
   when the runner performs it (it checks out repos; its forge is GitHub; a ceiling names it),
   and what it acts with. `performs`, `held`, `TokensFromEnv`, `needsFor` and the test in
   `checkSystems` read the table. Nothing the runner does changes in this step, and
   `credentials_test.go` says the same as before. What an entry acts with is looked up in the
   runner alone (rule 5).
4. **A credential is read where it is used.** `delivery.Services` holds a function for
   `LinearAuth` and for `SlackToken`, in place of a string. One read from the environment
   answers what was read at the start. `slack.go` and `issues.go` call it when the op reads
   its request, and the act holds what was read.
5. **A system may name its secret.** `secrets.systems` (the shape above). The table's entry
   then reads the store. `Config.Check` refuses a system the table does not have, a system
   that acts with no credential of its own to name, a secret's name that is no name, and a
   `systems` with no `dir`. The start warns where a system names a secret and its variable is
   set too (rule 3).
6. **Closed where the secret is absent.** `Config.Held` reads the table: a system that names a
   secret the store does not have (rule 2) is closed for every doer, with the warning it gives
   today for a variable. A secret that goes missing later fails the act, with an error the
   task reads. `git` stays as it is: warned, not closed.
7. **The deployment.** A projected volume at `/secrets`, read only, in the three runner
   manifests, and `secrets: {dir: /secrets}` in their configuration.
   - Its sources are the Secrets there are, each `optional: true`, with `items` that give each
     key its name in the store: `verse-slack`'s `SLACK_BOT_TOKEN` as `slack-bot`,
     `verse-linear`'s `LINEAR_API_KEY` as `linear`. No token is kept twice, and the iods go on
     reading the same Secrets.
   - One more source, `verse-runner-secrets`, optional, for what has no Secret today (an `mcp`
     server's token). Its keys are the store's names. It gets an entry in `deploy/secrets/` and
     a knob in `deploy/build.tony`, as the other Secrets have.
   - Not mounted by `subPath`: such a mount does not follow a Secret that changes.
   - The variables stay, and the manifests' configuration names no `secrets.systems`: a
     deployment moves a system to the store by adding the line.
8. **The words.** `docs/reference/runnerd.md` and the credentials table in `deploy/README.md`.
   The comment on `runnerd.Config` (`pkg/runner/runnerd/config.go`) says secrets are read from
   the environment; it gains the store.

## Tests

- **The store.** A name that is no name is refused. A file changed between two reads gives the
  new value. A Kubernetes Secret's layout (each name a link into `..data`) reads. An absent
  secret's error holds its name and no value. An empty file, and one that cannot be read, are
  absent. A value that ends in `\n` or `\r\n` is read without it.
- **The configuration.** A `secrets.dir` under `/etc` or `/opt` is refused, and one that
  overlaps the work, mirrors or control directory. A `readOnly` path over the store is refused
  at the start. One that becomes a link into the store after the start is left out of the next
  launch (`withReadOnly`). A `secrets.systems` with no `dir` is refused, and one that names a
  system the table does not have.
- **Closed.** `TestASystemWithNoCredentialIsClosed` (`credentials_test.go`) gains the store's
  cases: named and absent is closed and warned; named and empty is closed and warned; named
  and present is offered; named, with the variable set too, reads the store and warns.
- **At the act.** `slack_test.go` and `issues_test.go` in `delivery`: the header sent is the
  file's value at that call, after a change with no restart.
- **The sandbox.** A task cannot read the store's directory (`sandbox_test.go`, on Linux).
- **The task's side.** The task's process, given a configuration that names secrets and no
  store to read, builds its tools and reads none (rule 5).

## What does not change

- The variables, their names, and what a deployment sets today.
- `Secrets`, the model keys on the task's stdin.
- `checkShellEnv`, and that no task's shell is given a credential.
- The description. No offer says anything of the store, so `runner.DescriptionVersion` stays.

## Not in this issue

- **The `secret` system and `secret-exec`.** A spawn binding a secret to a command, the
  ceiling on names, and an offer publishing them. A later issue.
- **The `mcp` servers' secrets.** The `mcp` issue binds them, each in its server's entry.
- **The git token and the model keys in the store.** The git token would need the mirrors'
  auth to be read at each fetch and push.
- **`verse up`.** Its runner has no sandbox on macOS, so a store there is the machine's
  (rule 6).
- **Opening a system with no restart,** when its secret appears (rule 4).

## Decided at review

- A system names its secret in one map, `secrets.systems`, not in a section of its own.
- `secrets.dir` is refused under a system directory, and is a kept directory at each launch.
- The store's names stay lower case. The deployment gives the existing Secrets' keys their
  names with `items`, in a projected volume.
- Present is "reads and is not empty".
- A named secret is read from the store alone. An opening needs a restart.
- The newline a value ends with is dropped.
- No sandbox is a warning, not a refusal.

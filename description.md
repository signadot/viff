# secret: a system for one command run with one secret of the store, by the runner

**Status:** a plan. Nothing is built. It blocks 1f5art3x. It waits on 1q5art3x (the secret
store, which it reads) and on 1k5art3x (the table of ops, built). It shares the first op
performed apart with 1v5art3x, whichever lands first.

## What it is

The `secret` system, as 1f5art3x describes it ("`secret-exec`"): a task runs one command with
one secret of the runner's store, and the task never holds the secret. The runner is what acts:
it reads the secret, starts the command in a sandbox of its own over the task's workspace and
under the task's network, and gives the task the command's exit status and its output. The
environment says which secret and which commands; the autonomy decides each run; the ceiling
bounds both.

- **For a command that needs a secret,** and nothing else. A command that needs none is
  `commands.run`, or a script.
- **Two verbs.** `exec`, with the fields `argv`, `dir` and `as`, is the command; `env`, with
  the fields `name` and `value`, is one plain variable the command is given beside the secret.
  One secret a call; a call with two variables is three steps and one act.
- **The leak is the environment's.** The runner runs what the environment admits and does not
  judge a command. An environment that admits `sh -c 'echo $S'` has leaked `S` itself.

## What is there today

- **The store** (1q5art3x): `secretstore.Store.Get` reads a secret by name at each use, and
  `secrets.dir` names the directory. The runner reads it; no task's sandbox binds it.
- **The sandbox.** `BwrapSandbox.Wrap` (`pkg/runner/runnerd/sandbox.go`) builds one bwrap command
  line: the system directories read only, the task's directory read-write and `--chdir` there,
  `/tmp` a tmpfs, pid, ipc and uts unshared, every capability dropped, and the network as the
  launch's `Network`: open, `--unshare-net`, or `--unshare-net` with the task's egress socket
  bound at `EgressPath`. It is called once, for the task's process (`subprocess.go`,
  `startProcess`). Nothing starts a second process in a sandbox today.
- **The network is decided on the runner's side.** Under `allow`, the task's proxy
  (`ServeEgress`, one socket per task in the control directory) decides each connection as the
  step `network.tcp-client`, by the environment, the autonomy and the ceiling
  (`connectionRefused`, `network.go`). A second process bound to the same socket is decided the
  same way.
- **The table of ops** (`delivery/ops.go`): an op's `ask` reads a request into the steps it asks
  about and the act; `perform` checks every step with `refused` before the act. `Request.Fields`
  are text; a body is a tony document. `apart` is on no op yet; `Task.apart` hands such an op a
  task with no workspace, so it has no `Root` (1v5art3x records the panic that waits there).
- **A tool whose step carries structure** gives `verse.Tool.Build` rather than `Fields`
  (`pkg/runner/runnerd/verse/build.go`): the use's step whole. The verse system's tools use it
  for `path` and `on`.
- **A command is a step already,** `commands.run`, with the one field `command`, form `text`,
  "the command matches as one string". Rows and shapes on it are text patterns, anchored by
  their authors (`!regexp "^git "`). The approver sees the line.
- **A doer's shell environment** is `shellEnv`, by name, and no credential's variable may be
  among them (`Config.shellEnv`, `task.ShellEnv`). The task's process is given `HOME` as its
  directory and `TMPDIR=/tmp`.
- **The performed systems are one table** (`performedSystems`, `stock/config.go`), and each
  entry says when the runner performs it, what it needs, and whether it is closed where its
  credential is absent.
- **A description is public** (`GET /describe`), so a name in a ceiling is public. A secret's
  name must not be a secret.

## The shape

```tony
# the runner's configuration
secrets: {dir: /secrets}
agents:
  claude:
    systems:
      # which secrets claude's tasks may run a command with, and which commands: closed by default,
      # and nothing bound to slack or an mcp server is here unless named
      secret:
        ceiling:
        - name: npm-token
          exec: {argv: !regexp "^npm publish --ignore-scripts( --tag [a-z]+)?$", dir: !glob "packages/*", as: NPM_TOKEN}
          # the plain variables a command may be given, by name, each a shape; a block naming no
          # `env` gives none
          env: [{name: NPM_CONFIG_REGISTRY, value: !regexp "^https://registry\\.npmjs\\.org/"}, {name: !glob "NODE_*"}]

# a spawn's environment
environment:
  systems:
    secret:
    - name: npm-token
      exec: {argv: "npm publish --ignore-scripts", as: NPM_TOKEN}
      env: [{name: NODE_OPTIONS}]

# a spawn's autonomy
autonomy:
- {match: {system: secret, verb: env, name: LD_PRELOAD}, then: deny}
- {match: {system: secret, name: npm-token}, then: ask}
```

- **`name`** is the block's unique attribute: the secret's name in the store, a literal in the
  environment and in the ceiling both. A ceiling that names a pattern is refused: the operator
  says which secrets, one by one, as a name is what a doer is trusted with.
- **`argv` is a list on the wire and one string in the step.** The call gives a list of strings,
  never a shell string, and the runner runs that list. The step's field is the list spelled as
  one line, each argument quoted where it holds a space, a quote or a character a shell reads
  (`npm publish --tag 'a b'`), so a shape, a row and an approver read a command line as they do
  `commands.run`'s, and where one argument ends is never in doubt. The task's process and the
  runner spell it with one function, and the runner refuses a request whose field is not the
  spelling of its body. No new form: `argv` is `text`.
- **`dir`** is where the command runs, a path under the workspace root (form `file`), the root
  where empty. **`as`** is the variable the secret is given in (`^[A-Z_][A-Z0-9_]*$`).
- **The program is the runner's.** `argv[0]` is a bare name; one with a `/` is refused. The
  runner finds it on the command's `PATH` and runs it only where it resolves under a system
  directory (`UnderSystem`) or a `readOnly` path, never under the workspace: a program of the
  task's would read the secret for it.
- **The command's environment** is the task's shell's, then the call's, then the secret. The
  task's shell's: the doer's `shellEnv` values, the launch's `vars`, `HOME` as the task's root
  and `TMPDIR=/tmp`. The call's: its `env`, each variable one step `secret.env {name, value}`,
  which the block's `env` shapes admit by name and, where a shape says, by value; a block that
  names no `env` gives none, so plain variables are closed by default, and the empty block
  gives any, as it gives every verb. Last the secret, under `as`, which no plain variable may
  name. `PATH` given this way is the `PATH` the program is found on, under the rule below.
  `LD_PRELOAD` and its family are admitted as any name is: a ceiling that admits a command
  admits what that command does with the secret, by construction, so a variable that changes
  what the command does adds nothing the ceiling has not granted. It is the offer's and the
  spawn's to name the variables, one by one, and a row's to deny one.
- **Where it runs.** A second bwrap of the same shape as the task's (`Wrap`), over the task's
  root, `--chdir` to `dir`, with the task's `Network` (the same egress socket, so the proxy
  decides its connections under the task's autonomy), its own pid namespace, and no capability.
  The secret is in that process's environment and nowhere else: the task's processes are in
  another pid namespace and see no `/proc` of it. The value is in the runner's memory while the
  command runs, and in no log, no reply and no file.
- **The output goes to the workspace.** stdout and stderr, interleaved, to
  `<root>/.verse/exec/<request id>.out`, with the secret's value replaced by `[secret <name>]`
  wherever it appears whole (a second line: an encoding defeats it). The reply is the exit
  status, the file's path and the last 16 KiB. The file is cut at 16 MiB and says so.
- **The run is bounded.** `timeout` on the call, seconds, 10 minutes where none, an hour at
  most (the op's deadline). Past it the command is killed and the reply says so. When the
  task's process goes, the command is killed with it (`--die-with-parent`, and the `gone`
  context an op apart runs under).
- **Closed without a sandbox.** A runner with `-sandbox none` does not perform `secret`: a
  command there runs as the runner's user on the whole machine, and the secret would be the
  machine's. `Held` closes it and says so. This is `verse up`'s runner on macOS.
- **The offer** says nothing under `builds`: the ceiling already names the secrets a doer may
  use, and a store's other names are nobody's to read.

## The plan

Each step builds and passes on its own.

1. **The document and the steps.** `pkg/systems/documents/secret.tony`: the attribute `name`
   (unique, required, form `lower`); the verb `exec` with the fields `argv` (text, the spelling
   above), `dir` (file) and `as` (text); and the verb `env` with the fields `name` (text, a
   variable's name, `varName`) and `value` (text). `task.StepSecretExec` and
   `task.StepSecretEnv` in `pkg/task/step.go`.
   `docs/systems/secret.md`. `systems.Argv(list) string` and `systems.ArgvList(line)` in one
   place, with the quoting rule, and a test that every list round-trips and that two lists
   never spell the same.
2. **The configuration.** `secret` in `performedSystems`: performed where `secrets.dir` is set,
   a ceiling names it and the sandbox is bwrap; it needs "a secret store (`secrets.dir`) and a
   sandbox". `Config.Check` refuses a ceiling block whose `name` is not a literal store name
   (`secretstore.CheckName`), and an `as` pattern no variable name matches. `Held` drops a block
   whose secret is absent at start, saying which and where, and closes the system where none is
   left; and closes it whole under `-sandbox none`.
3. **The task apart.** `delivery.Task` gains `Root` (the workspace root) and `Network`, set at
   the launch and kept by `Task.apart`: what an op apart may read, since nothing in turn writes
   them. `Services` gains the store (`*secretstore.Store`) and the sandbox. This is the fix
   1v5art3x's note asks for, and the first op apart's test.
4. **The op.** `OpSecretExec = "secret.exec"` on the table, with the steps `secret.exec` and
   `secret.env`: takes `name`, `as`, `dir`, numbers `timeout`, body (`{argv: [...], env: {...}}`,
   tony), apart, deadline one hour. `ask` reads the body, spells the argv, refuses a request
   whose `argv` field differs, checks `name`, `as` and `dir` as above and that `as` is not among
   `env`, and asks about the `exec` step and one `env` step for each variable; `perform` checks
   them all before the act, as it does. The act: read the secret (absent: a refusal naming it),
   build the environment, resolve `argv[0]` on its `PATH`, build the second sandbox, run with
   the timeout, write the output through the redacting writer, answer.
5. **The tool.** `secret-exec` in `stock`, beside `slackTools`, offered to every doer where the
   runner performs `secret`: params `name`, `argv` (an array of strings), `env` (an object of
   strings), `as`, `dir`, `timeout`. `verse.Tool` gains `Steps func(input) ([]task.Step, bool)`
   beside `Build`, for a use that is several steps, and `stepOf` (`verse/build.go`) becomes the
   list the supervisor's `SetStepsOf` already takes; `Build` and `Fields` stay as the one-step
   cases. `Do` sends the op with `Request.Decision`. The description says the task never reads
   the secret, the output is a file in its workspace, the program must be on the command's
   `PATH`, and each variable is asked about on its own.
6. **The words.** `docs/systems/secret.md`, the row in `docs/systems/README.md`,
   `docs/reference/runnerd.md` (the performed system, what a `secret` ceiling says, and that it
   is closed under `-sandbox none`), the example ceiling in `deploy/verse-runner`'s comments.

## Tests

A sandbox for the tests: `BwrapAt` with a script that runs its command, since the Linux-only
tests do not run here; the real one in `TestATaskCannotReadTheSecretStore`'s pattern on Linux.

- **The check, at the runner.** A run is refused outside the environment (another name, another
  argv, another dir, a variable the block does not give, a value its shape does not admit), by a
  `deny` row on `exec` or on one `env` step, by an `ask` row with no answer relayed, beyond the
  ceiling, for an `argv[0]` with a `/`, for one that resolves under the workspace, for a `dir`
  that leaves the root, for `as` among `env`, and for a field that is not its body's spelling.
  No command runs. A block with no `env` admits a call with none and refuses one with any; the
  empty block admits any.
- **The secret, and the plain variables.** The command reads its variable and each plain one,
  over the task's shell's; the task's process's environment, the reply, the output file and the
  log hold no value. A changed file between two calls is the next
  call's. An absent secret refuses the call naming it, with no value.
- **The output.** The file is written, the reply carries the exit status and the tail, the
  value is redacted, a long output is cut and says so, a slow command is killed at `timeout`.
- **Apart.** A run that is held does not hold a push; a run in flight when the task's process
  goes is killed; the op is handed a task with `Root` and `Network` and no workspace.
- **The network.** Under `allow`, the command's connection to a host the environment gives
  passes the proxy and one it does not is refused (`httptest`, the egress server).
- **The configuration.** A ceiling naming a pattern, a bad `as`, and a runner under `-sandbox
  none` are each refused or closed as said; the description shows `secret` under the doer with
  the ceiling and nothing under `builds`.
- **Each doer.** The patterns of `claude_test.go`, `codex_test.go` and `scripted_test.go`: the
  tool is offered, a use with one variable is the two steps `secret.exec` and `secret.env`, an
  `ask` row on either asks, the result comes back.
- **The spelling.** Round trip, uniqueness, and that `!regexp "^npm publish"` does not admit
  `npm "publish --registry evil"`.

## What does not change

- **`runner.DescriptionVersion`**: `secret` is one more name under `systems`. Verse is upgraded
  first, as for `mcp`.
- **`commands.run`, `files.write`** and what a doer's own shell runs: the sandbox around the
  task's process is as it is.
- **`refused`** and the order it reads.
- **The store's rules** (1q5art3x): read at each act, present is reads and non-empty.

## Not in this issue

- **Several secrets in one call,** each a step (1f5art3x). One a call first; a command that
  needs two is two commands, or waits.
- **A secret as a file** (`--ro-bind-data` from a pipe, into the command's sandbox alone). The
  variable first.
- **Redaction of encodings.** The whole value, as it is, only.
- **A program from the nix store** (`pkg-nix`): `UnderSystem` and `readOnly` until then.
- **`-sandbox none`.** Closed, not worked around.
- **Streaming output** over the channel: the file and the tail.

## Decided at review

- **Plain variables are in,** as the verb `env`, one step a variable, closed by default and
  named one by one in the ceiling and the environment. `LD_PRELOAD` and its family add no risk
  the ceiling has not granted by construction, since a ceiling that admits a command admits
  what it does with the secret; it is the offer's and the spawn's to limit the names.

## Open questions

1. **Spelling as text, or a form `argv`.** Text reuses every pattern a row knows; a form could
   refuse a shape that is not anchored. Text is proposed; the docs say to anchor.
2. **Whether `as` is a field.** Proposed yes: a ceiling pins the variable's name, and an
   approver sees where the secret goes. It costs one more name in each shape.
3. **The output's place.** `<root>/.verse/exec/`, under the root and outside every checkout, or
   under the task's `/tmp`, which the runner cannot write into (a tmpfs of the task's sandbox).
4. **Whether a `dir` under a checkout is enough,** or the command needs to run with the
   checkout's own branch state (it runs over the live tree, whatever the task has done to it).

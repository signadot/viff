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
- **One verb,** `exec`, with the fields `argv`, `dir` and `env`: the command, where it runs,
  and the plain variables it is given beside the secret. The block says the secret (`name`) and
  the variable it is given in (`as`), once for every command of it. One secret a call, one step
  a call.
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
- **The channel is a unix socket pair** (`delivery/protocol.go`, `syscall.Socketpair(AF_UNIX,
  SOCK_STREAM)`, `net.FileConn` on each end), so a frame can carry a descriptor (`SCM_RIGHTS`).
  Nothing does yet.
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
          as: NPM_TOKEN
          exec:
            argv: !regexp "^npm publish --ignore-scripts( --tag [a-z]+)?$"
            dir: !glob "packages/*"
            # the plain variables a command may be given, each by name with a pattern on its
            # value, null for any; a shape that says no `env` gives none
            env: {NPM_CONFIG_REGISTRY: !regexp "^https://registry\\.npmjs\\.org/", NODE_OPTIONS: null}

# a spawn's environment
environment:
  systems:
    secret: [{name: npm-token, as: NPM_TOKEN, exec: {argv: "npm publish --ignore-scripts", env: {NODE_OPTIONS: null}}}]

# a spawn's autonomy
autonomy:
- {match: {system: secret, env: {LD_PRELOAD: null}}, then: deny}
- {match: {system: secret, name: npm-token}, then: ask}
```

- **`name`** is the block's unique attribute: the secret's name in the store, a literal in the
  environment and in the ceiling both. A ceiling that names a pattern is refused: the operator
  says which secrets, one by one, as a name is what a doer is trusted with.
- **`as`** is the block's other attribute, required: the variable the secret is given in, for
  every command of the block (`NPM_TOKEN`), in the form `var` (`^[A-Za-z_][A-Za-z0-9_]*$`, the
  `varName` of the environment's `vars`). A secret has one conventional variable, so the spawn
  says it once; the ceiling patterns it as it patterns `name`; and it is a field of the step, as
  a block's attributes are, so a row and a question see where the secret goes. The call does
  not carry it: the runner and the task's process read it from the block.
- **`argv` is a list on the wire and one string in the step.** The call gives a list of strings,
  never a shell string, and the runner runs that list. The step's field is the list spelled as
  one line, each argument quoted where it holds a space, a quote or a character a shell reads
  (`npm publish --tag 'a b'`), so a shape, a row and an approver read a command line as they do
  `commands.run`'s, and where one argument ends is never in doubt. The list alone is on the
  wire, in the request's body; the task's process and the runner each spell the step's field
  from it with one function, so there is no second copy to check. No new form: `argv` is `text`.
- **`dir`** is where the command runs, a path under the workspace root (form `file`), the root
  where empty. **The command runs over the live tree**, as the task has it at that moment: what
  it acts on is not any commit, so an approver of `npm publish` approves the command and not
  its content, and the tree may change while it runs, since the task runs on. Both are said in
  the doc. Running over an export of the task's branch tip, with a `commit` field the runner
  fills, is the step after this one (below).
- **The program is the runner's.** `argv[0]` is a bare name; one with a `/` is refused. The
  runner finds it on the command's `PATH` and runs it only where it resolves under a system
  directory (`UnderSystem`) or a `readOnly` path, never under the workspace: a program of the
  task's would read the secret for it.
- **The command's environment** is the task's shell's, then the call's, then the secret. The
  task's shell's: the doer's `shellEnv` values, the launch's `vars`, `HOME` as the task's root
  and `TMPDIR=/tmp`. The call's: `env`, an object of variables by name, which is a field of the
  step with a form of its own, `env`. A pattern on it is an object: each key a variable the
  command may be given, its value a pattern on the variable's (`null` for any). It matches
  CLOSED: a variable the pattern does not name puts the call outside the shape, where tony's
  own object match lets a key it does not name by. A shape that says no `env` is read as
  `env: {}` (the field's default), so plain variables are closed by default, and `env: null` is
  any. A row matches it as tony matches, by what it names: `env: {LD_PRELOAD: null}` is every
  call that gives `LD_PRELOAD`. Last the secret, under the block's `as`, which no plain variable
  may name.
  `PATH` given this way is the `PATH` the program is found on, under the rule below.
  `LD_PRELOAD` and its family are admitted as any name is: a ceiling that admits a command
  admits what that command does with the secret, by construction, so a variable that changes
  what the command does adds nothing the ceiling has not granted. It is the offer's and the
  spawn's to name the variables, one by one, and a row's to deny one.
- **Where it runs.** A second bwrap of the same shape as the task's (`Wrap`), over the task's
  root, `--chdir` to `dir`, with the task's `Network` (the same egress socket, so the proxy
  decides its connections under the task's autonomy), its own pid namespace, and no capability.
  Its `/tmp` is a tmpfs of its own, not the task's, so what the task left in `/tmp` is not there;
  and under `allow` the proxy is the task's, so the model's hosts are on it too. The secret is in
  that process's environment and nowhere else: the task's processes are in another pid namespace
  and see no `/proc` of it. The value is in the runner's memory while the command runs, and in no
  log, no reply and no file.
- **The output is a stream the task's process serves.** The command's stdin, stdout and stderr
  are a socket pair the runner makes. The runner pumps its end: what the command writes goes on
  with the secret's value replaced by `[secret <name>]` wherever it appears whole (a second
  line: an encoding defeats it), and, later, what comes back goes to the command's stdin. The
  task's end it hands to the task's process over the channel, as a descriptor on the first frame
  of the answer. So the runner opens no path under the task's root: that tree is the task's to
  write, and a link in it is resolved by whoever opens it, with that side's reach. The task's
  process serves the descriptor as a unix socket at `<root>/.verse/exec/<id>.sock`, where a
  shell of the task's connects to read along, and to start writes what comes out to
  `<root>/.verse/exec/<id>.out`, cut at 16 MiB and said. The final frame is the exit status;
  the tool answers the model with it, the file's path and the last 16 KiB.
- **The run is bounded.** `timeout` on the call, seconds, 10 minutes where none, an hour at
  most (the op's deadline). Past it the command is killed and the reply says so. When the
  task's process goes, the command is killed with it (the `gone` context an op apart runs
  under). A kill reaches the whole of it: bwrap runs in a process group of its own and the group
  is signalled, since killing bwrap alone leaves its children to `--die-with-parent` while the
  output pipe stays open; `cmd.WaitDelay` bounds the wait on that pipe either way.
- **Closed without a sandbox.** A runner with `-sandbox none` does not perform `secret`: a
  command there runs as the runner's user on the whole machine, and the secret would be the
  machine's. `Standalone`, which has the sandbox where `Config.Held` has none, closes it at
  start and says so, beside its warning about the store under `none`. This is `verse up`'s
  runner on macOS.
- **The offer** says nothing under `builds`: the ceiling already names the secrets a doer may
  use, and a store's other names are nobody's to read.

## The plan

Each step builds and passes on its own.

1. **The document and the step.** `pkg/systems/documents/secret.tony`: the attributes `name`
   (unique, required, form `lower`) and `as` (required, form `var`), and the verb `exec` with
   the fields `argv` (text, the spelling above), `dir` (file) and `env` (form `env`, default
   `{}`). `task.StepSecretExec` in `pkg/task/step.go`, carrying both attributes as `git.push`
   carries `repo` and `from`.
   - **The form `var`** in `pkg/systems`: a variable's name, `varName`; a pattern on it is text.
   - **The form `env`** in `pkg/systems`: `Accepts` takes `null`, the combinators, and an object
     whose keys are variable names (`varName`) and whose values are text patterns; `MatchFieldAs`
     reads it closed, as `FormPath` has its reading; `FieldContains` holds an inner object within
     an outer where every key of the inner is the outer's and its pattern within the outer's, so
     a ceiling bounds an environment by name. `Field` gains `Default`, as `Attribute` has it,
     read where a shape omits the field; the generated schema follows.
   `docs/systems/secret.md`. `systems.Argv(list) string` and `systems.ArgvList(line)` in one
   place, with the quoting rule, and a test that every list round-trips and that two lists
   never spell the same.
2. **The configuration.** `secret` in `performedSystems`: performed where `secrets.dir` is set,
   a ceiling names it and the sandbox is bwrap; it needs "a secret store (`secrets.dir`) and a
   sandbox". `Config.Check` refuses a ceiling block whose `name` is not a literal store name
   (`secretstore.CheckName`), and an `as` pattern no variable's name matches. `Held` drops a block
   whose secret is absent at start, saying which and where, and closes the system where none is
   left. `Standalone` closes it whole under `-sandbox none`, since the sandbox is its to know.
3. **The task apart, and a descriptor on a frame.** `delivery.Task` gains `Root` (the
   workspace root) and `Network`, set at the launch and kept by `Task.apart`: what an op apart
   may read, since nothing in turn writes them. `Services` gains the store
   (`*secretstore.Store`) and the sandbox. This is the fix 1v5art3x's note asks for, and the
   first op apart's test. `Reply` gains `Stream bool`: a frame that says it carries one
   descriptor (`WriteMsgUnix` and `ReadMsgUnix` on the channel's `*net.UnixConn`), which
   `Client.hear` takes and hands to the asker with the reply; an answer may now be two frames, the
   stream's and the final one, by the one id. Nothing else of the protocol changes.
4. **The op.** `OpSecretExec = "secret.exec"` on the table: takes `name`, `dir`, numbers
   `timeout`, body (`{argv: [...], env: {...}}`, tony), apart, deadline one hour. `ask` finds the
   environment's block for `name` and reads `as` from it (no block: refused, saying which the
   environment has), checks `dir` as above and that each name of `env` is a variable's name and
   none is `as`, and asks about the one step: `name` and `as` from the block, `argv` spelled from
   the body's list, `env` as given (`Step.With`). The act: read the secret (absent: a refusal naming it), build the
   environment, resolve `argv[0]` on its `PATH`, make the socket pair, build the second sandbox
   with the command's stdio on the pair's one end, start it in its own process group with
   `WaitDelay` set, send the first frame with the pair's other end, pump the command's output
   through the redacting writer to it, run with the timeout, answer the final frame with the
   exit status; past the timeout or once the task is gone, signal the group.
5. **The tool.** `secret-exec` in `stock`, beside `slackTools`, offered to every doer where the
   runner performs `secret`: params `name`, `argv` (an array of strings), `env` (an object of
   strings), `dir`, `timeout`. `Build` makes the step whole, as the verse tools' do for a field
   with structure, reading `as` from the launch's environment as the runner does; `Do` sends the op with `Request.Decision`, takes the descriptor the
   first frame brings, serves it at `<root>/.verse/exec/<id>.sock` and copies it to
   `<root>/.verse/exec/<id>.out`, and answers the model the exit status, the path and the tail
   when the final frame comes. The description says the task never reads the secret, which the
   command finds under the block's `as`, the output is a socket and a file in its workspace, the
   program must be on the command's `PATH`, and the variables are part of what is asked about.
6. **The words.** `docs/systems/secret.md`, the row in `docs/systems/README.md`,
   `docs/reference/runnerd.md` (the performed system, what a `secret` ceiling says, and that it
   is closed under `-sandbox none`), the example ceiling in `deploy/verse-runner`'s comments.

## Tests

A sandbox for the tests: `BwrapAt` with a script that runs its command, since the Linux-only
tests do not run here; the real one in `TestATaskCannotReadTheSecretStore`'s pattern on Linux.

- **The check, at the runner.** A run is refused outside the environment (another name, another
  argv, another dir, a variable the shape does not name, a value its pattern does not admit), by
  a `deny` row, by a row on `env` naming one of its variables, by an `ask` row with no answer
  relayed, beyond the ceiling, for an `argv[0]` with a `/`, for one that resolves under the
  workspace, for a `dir` that leaves the root, and for the block's `as` among `env`. No command
  runs. A shape with no `env` admits a call with none and refuses
  one with any; `env: null` admits any.
- **The form.** `Accepts` refuses a key that is no variable's name and a value that is no text
  pattern; the closed match and `FieldContains` on nested objects; an environment whose `env`
  names a variable its ceiling's does not is refused at install.
- **The secret, and the plain variables.** The command reads its variable and each plain one,
  over the task's shell's; the task's process's environment, the reply, the output file and the
  log hold no value. A changed file between two calls is the next
  call's. An absent secret refuses the call naming it, with no value.
- **The output.** The task's process gets the descriptor on the first frame and the exit status
  on the final one by the same id; what the command wrote comes through it, the value redacted;
  the task's process writes the file and serves the socket, and a client on the socket reads
  along; a long output is cut by the task's side and says so; a slow command is killed at
  `timeout`, its children with it, and the op answers within `WaitDelay` of that though a child
  holds the pair. The runner opens no path under the root: a `.verse/exec` the task made a link
  changes nothing on the runner's side.
- **Apart.** A run that is held does not hold a push; a run in flight when the task's process
  goes is killed; the op is handed a task with `Root` and `Network` and no workspace.
- **The network.** Under `allow`, the command's connection to a host the environment gives
  passes the proxy and one it does not is refused (`httptest`, the egress server).
- **The configuration.** A ceiling whose `name` is a pattern, an environment whose `as` is no
  variable's name, and a ceiling `as` no variable's name matches are refused; a runner under
  `-sandbox none` starts with `secret` closed and says so (`Standalone`); the description shows `secret` under the doer with
  the ceiling and nothing under `builds`.
- **Each doer.** The patterns of `claude_test.go`, `codex_test.go` and `scripted_test.go`: the
  tool is offered, a use is the step `secret.exec` with the spelled argv and the `env` object,
  the question shows them, the result comes back.
- **The spelling.** Round trip, uniqueness, that `!regexp "^npm publish"` does not admit
  `npm "publish --registry evil"`, and that the task's side and the runner's spell one list the
  same.

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
- **A command over an export of the task's branch tip,** in a directory the task cannot
  write, with a `commit` field the runner fills as it fills a push's `exists` and `own`: then
  what is acted on is one commit, and the approver sees its sha. It needs a build inside or a
  prepared tree, since an export has no `node_modules` and no build output; the live tree first.
- **Input to the command.** The pair is duplex and the task's process holds its end, so a
  client on the socket can write once the runner pumps that way too; it does not yet.

## Decided at review

- **Plain variables are in,** as the field `env` of `exec`, not a step of their own: a form of
  its own that matches closed, `{}` where a shape says nothing, so the ceiling and the
  environment name the variables one by one. `LD_PRELOAD` and its family add no risk the
  ceiling has not granted by construction, since a ceiling that admits a command admits what it
  does with the secret; it is the offer's and the spawn's to limit the names.

- **The output is a stream, and the runner writes nothing under the task's root.** The
  command's stdio is a socket pair; the runner pumps its end, redacting, and hands the task's
  end to the task's process on a frame of the channel, which is a unix socket pair already. The
  task's process serves it as a unix socket in its workspace and, to start, writes the file.
  Input comes later over the same pair.
- **The argv is a list on the wire and text in the step,** which a shape and a row match with
  the patterns they have (`!regexp`, anchored by their author). No form of its own.
- **The argv is on the wire once,** as the body's list; the step's field is spelled from it on
  each side. A kill signals bwrap's process group, with `WaitDelay`. `-sandbox none` is closed
  in `Standalone`. The command's `/tmp` is its own, and its proxy the task's.

- **`as` is an attribute of the block,** said once for the secret, patterned by the ceiling,
  in the step's fields, and not on the call.
- **The live tree for now.** What a command acts on is unreviewed and may change while it
  runs; the doc says so, and an export with a `commit` field is the step after.

## Open questions

None.

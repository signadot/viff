# secret: a system for one command run with one secret of the store, by the runner

**Status:** a plan, reviewed twice (the second against the code: "At the second review",
below), being built in the worktree `.worktrees/secret-exec` (branch `verse/secret-exec`, over
`verse/secret-store`), step by step: steps 1 to 3 are committed (11809adf, e4272019, 4de18b41); step 4, the op, is
committed (28b7f392) on the third pass's design (stderr its own socket, a kill the task's
closing of its stream: "At the third pass", below): `delivery/secret.go` (the op, the redaction, the program found on
the command's PATH on the runner's side), `Task.Env`/`Channel.SetEnv`, `systems.IsVarName`, and tests in delivery
and an end-to-end one in runnerd under a fake bwrap. Step 5, the tool, is next. As built, the frame's field is `Streams int`, the count,
rather than `Stream bool`: the frame says how many it carries, and the client takes that many. It blocks 1f5art3x. It waits on
1q5art3x (the secret store, which it reads) and on 1k5art3x (the table of ops, built). It shares
the first op performed apart with 1v5art3x, whichever lands first.

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
  environment and in the ceiling both. A ceiling that names a pattern is refused, and so is a
  block that names none (the empty block, `secret: {ceiling: {}}`, would be every secret): the
  operator says which secrets, one by one, as a name is what a doer is trusted with. `as` a
  ceiling block may pattern or leave out, for any variable.
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
  call that gives `LD_PRELOAD`. The two readings are the two an address has (`Reading`): a
  shape, a ceiling and an `allow` row read Within, the call's variables lying among those named;
  a `deny` or an `ask` row reads Touches, the call giving one of those named; `Row.reading`
  already splits them so, and each errs toward refusing. Last the secret, under the block's
  `as`, which no plain variable may name.
  `PATH` given this way is the `PATH` the program is found on, under the rule below.
  `LD_PRELOAD` and its family are admitted as any name is: a ceiling that admits a command
  admits what that command does with the secret, by construction, so a variable that changes
  what the command does adds nothing the ceiling has not granted. It is the offer's and the
  spawn's to name the variables, one by one, and a row's to deny one.
- **Where it runs.** A second bwrap of the same shape as the task's (`Wrap`, which gains a
  working directory apart from the directory it binds: the root bound, `--chdir` to `dir`
  within it), with the task's `Network` (the same egress socket, so the proxy decides its
  connections under the task's autonomy), its own pid namespace, and no capability. `dir` is
  checked as a path under the root and nothing more: the runner does not open it, and bwrap
  resolves it inside the sandbox, where a link out of the root reaches a system directory at
  most, read only.
  The sandbox is runnerd's and the channel is delivery's, which runnerd imports, so delivery
  names what it needs itself: `Services.Confine`, an interface runnerd implements over its
  sandbox (`Confine(cmd, root, dir, network)`), which also refuses a program that resolves
  neither under a system directory nor under a `readOnly` path (`UnderSystem` stays where it
  is), and `Task.Network{Mode, Egress}` as two strings. The store it imports as it is
  (`secretstore` imports nothing of runnerd).
  Its `/tmp` is a tmpfs of its own, not the task's, so what the task left in `/tmp` is not there;
  and under `allow` the proxy is the task's, so the model's hosts are on it too. The secret is in
  that process's environment and nowhere else: the task's processes are in another pid namespace
  and see no `/proc` of it. The value is in the runner's memory while the command runs, and in no
  log, no reply and no file.
- **The output is two streams the task's process serves, and the runner is in the middle of
  them.** The command's stdout, stderr and stdin are three pipes to the runner; between the
  runner and the task's process are two socket pairs the runner makes: the I/O pair, which
  carries stdout to the task and, when input comes (below), what the task writes to stdin; and
  the stderr pair, stderr alone, which the task reads and never writes. Stderr is apart because
  a shell keeps it apart (`2>`), and a reader following `npm` along wants its progress, which
  goes there, told from what it prints; multiplexing both on one stream under a header would
  make the socket something a shell cannot `nc -U` and read as it comes, which is the point of
  it. The runner pumps both: what the command writes goes on to the task's end with the
  secret's value replaced by `[secret <name>]` wherever it appears whole (a second line: an
  encoding defeats it). The task's ends it hands to the task's process over the channel, as two
  descriptors on the first frame of the answer. So the runner opens no path under the task's
  root: that tree is the task's to write, and a link in it is resolved by whoever opens it, with
  that side's reach. The task's process serves the descriptors as unix sockets at
  `<root>/.verse/exec/<id>.sock` and `<root>/.verse/exec/<id>.err.sock`, where a shell of the
  task's connects to read along, and to start writes what comes out to
  `<root>/.verse/exec/<id>.out` and `<id>.err`, each cut at 16 MiB and said. The final frame is
  the exit status; the tool answers the model with it, the files' paths and the last 16 KiB of
  each.
  - **A descriptor rides the bytes it was sent with,** and a plain read drops it. So the task's
    side reads the channel with `ReadMsgUnix` throughout, queueing each descriptor it is handed,
    and a frame that says `Stream: true` takes the next two, the I/O end then the stderr end
    (one `SCM_RIGHTS` message carries both, `syscall.UnixRights(a, b)`, and the count is the
    op's, fixed); the kernel ends a read at the message that carried rights, so the descriptors
    are queued by the time their frame decodes. The runner writes that frame, length and body,
    in one `WriteMsgUnix`. Over a connection that is no unix socket (a test's `net.Pipe`) a
    stream frame cannot be carried, and the op says so instead. The op sends it from inside its
    act, through a `stream(id, files...)` the server sets on the task it hands the op (`Serve`'s
    write lock, as `send` takes it).
- **The run is bounded.** `timeout` on the call, seconds, 10 minutes where none, an hour at
  most (the op's deadline). Past it the command is killed and the reply says so. When the
  task's process goes, the command is killed with it (the `gone` context an op apart runs
  under). A kill reaches the whole of it by the pid namespace, not a process group:
  `--new-session` makes the sandbox's processes a session of their own, which a signal to bwrap's
  group from outside does not reach, but bwrap killed kills its init (`--die-with-parent`), and
  a pid namespace whose init dies dies whole. `cmd.Process.Kill`, then; `cmd.WaitDelay` bounds
  the wait on the output pipe either way.
- **The command lives while the task holds its stream, and a kill is the task letting go.** The
  task's side closing both descriptors kills the run, by the same path as the task's process
  dying, where the kernel closes them: one mechanism, no op and nothing on the wire. The stderr
  pair is what makes it detectable: the task never writes on it, so the runner's read there
  returns EOF only when the far end is wholly gone; a half-close on the I/O pair, when input
  comes, is EOF on the command's stdin and never a kill, and a full close on the I/O pair alone
  is unseen until the command writes (EPIPE), which a silent command never does. No graceful
  signal is on offer: bwrap forwards none from outside into the command, and the kill is the
  namespace's, whole; the graceful "please finish" a task has is closing stdin. The doc says so
  rather than offering a `signal` it cannot deliver.
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
     whose keys are variable names (`varName`) and whose values are text patterns, and refuses
     `!glob` and `!regexp` on the whole; `MatchFieldAs` reads it by its `Reading`, Within every
     variable of the call among the pattern's and matching its pattern, Touches some variable
     the pattern names given and matching; `FieldContains` holds an inner object within an outer
     where every key of the inner is the outer's and its pattern within the outer's, so a
     ceiling bounds an environment by name; `FieldMeets` is yes (a call with no variable meets
     every two). `Field` gains `Default`, as `Attribute` has it, a pattern in tony read where a
     shape or a ceiling's shape omits the field (`Shape.Matches`, `ShapeWithin`, `Shape.Meets`,
     through one `filled`); a row's match is not filled, since a row names what it names and an
     omitted field is any. The generated schema follows.
   `docs/systems/secret.md`. `systems.Argv(list) string` and `systems.ArgvList(line)` in one
   place, with the quoting rule, and a test that every list round-trips and that two lists
   never spell the same.
2. **The configuration.** `secret` in `performedSystems`: performed where `secrets.dir` is set
   and a ceiling names it (`when` reads the file alone); it needs "a secret store
   (`secrets.dir`) and a sandbox". `Config.Check` refuses a ceiling block that names no `name`,
   one whose `name` is not a literal store name (`secretstore.CheckName`), and an `as` pattern no
   variable's name matches. `Held` drops a block whose secret is absent at start, saying which
   and where, and closes the system where none is left (a rewrite of each doer's `secret`
   ceiling, beside `without`). `Standalone` closes it whole under `-sandbox none`, since the
   sandbox is its to know, before the task's configuration is marshalled, so the task's process
   offers nothing of it either.
3. **The task apart, and descriptors on a frame.** `delivery.Task` gains `Root` (the
   workspace root) and `Network{Mode, Egress}`, set at the launch (`Channel.SetNetwork`, in
   `SubprocessHandler.start` once the egress socket is there, before `Serve`) and kept by
   `Task.apart`: what an op apart may read, since nothing in turn writes them. `Services` gains
   the store (`*secretstore.Store`) and `Confine` (above). This is the fix 1v5art3x's note asks
   for, and the first op apart's test. `Reply` gains `Stream bool`: a frame that says it carries
   descriptors, written in one `WriteMsgUnix`, two for this op (the I/O end, the stderr end);
   the task's side reads with `ReadMsgUnix` throughout (a reader over the `*net.UnixConn` that
   queues what arrives), and `Client.hear` hands the queued descriptors to the asker with such a
   reply, `Ask` returning both; an answer may now be two frames, the stream's and the final
   one, by the one id. `Task` gains the unexported `stream(id int, files ...*os.File) error`
   `Serve` sets. `op` gains `apart()` and `withDeadline()`. Nothing else of the protocol
   changes.
4. **The op.** `OpSecretExec = "secret.exec"` on the table: takes `name`, `dir`, numbers
   `timeout`, body (`{argv: [...], env: {...}}`, tony, read strictly), apart, deadline one hour.
   `ask` reads the body, checks `dir` as a path under the root, `argv` non-empty with a bare
   `argv[0]`, each name of `env` a variable's name, and `timeout` within the hour; finds the
   environment's block for `name` and reads `as` from it where there is one (none: the step
   carries no `as`, and `refused` says the environment gives no such block, as it does for every
   op); refuses an `env` naming `as`; and asks about the one step: `name` and `as` from the
   block, `argv` spelled from the body's list, `env` as given (`Step.With`). The act: read the
   secret (absent: a refusal naming it), build the environment, resolve `argv[0]` on its `PATH`
   and run that path, make the pipes and the two socket pairs, `Confine` the command over the
   root in `dir`, start it with `WaitDelay` set and its stdin empty (input is the step after),
   send the stream frame with the pairs' far ends, pump stdout and stderr each through the
   redacting writer to its near end, read the stderr pair's near end for the EOF that says the
   task let go, run with the timeout, answer the final frame with the exit status; past the
   timeout, once the task let go, or once the task is gone, kill it.
5. **The tool.** `secret-exec` in `stock`, beside `slackTools`, offered to every doer where the
   runner performs `secret`: params `name`, `argv` (an array of strings), `env` (an object of
   strings), `dir`, `timeout`. `Build` makes the step whole, as the verse tools' do for a field
   with structure, reading `as` from the launch's environment as the runner does; `Do` sends
   the op with `Request.Decision`, takes the two descriptors the first frame brings, serves
   them at `<root>/.verse/exec/<id>.sock` and `<id>.err.sock` and copies them to
   `<root>/.verse/exec/<id>.out` and `<id>.err`, and answers the model the exit status, the
   paths and the tails when the final frame comes. The description says the task never reads
   the secret, which the command finds under the block's `as`, the output is two sockets and two
   files in its workspace, the program must be on the command's `PATH`, and the variables are
   part of what is asked about.
   - **The tool's shape is decided here, not before.** `secret-exec` as planned blocks until
     the command ends, and a `secret-exec-kill <id>` beside it closes the task's ends, which
     kills the run; that is reachable by a doer that runs two tool calls at once (a script,
     `verse-call-tool … &` then kill), and not by an agent whose loop runs one tool at a time.
     If agents need to kill, the shape is `secret-exec` returning at start with the paths and
     the id, beside `secret-exec-wait` and `secret-exec-kill`. That changes `Do` and nothing
     else, so steps 3 and 4 are built the same either way, and this is settled when 5 is.
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
- **The output.** The task's process gets the two descriptors on the first frame and the exit
  status on the final one by the same id; what the command wrote comes through them, stdout on
  one and stderr on the other, the value redacted on each; the task's process writes the files
  and serves the sockets, and a client on a socket reads along; a long output is cut by the
  task's side and says so; a slow command is killed at `timeout`, its children with it, and the
  op answers within `WaitDelay` of that though a child holds a pair. The task's side closing
  both ends kills a command that writes nothing, and the op answers so; closing the I/O end
  alone does not, until the command writes. The runner opens no path under the root: a
  `.verse/exec` the task made a link changes nothing on the runner's side.
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
- **Input to the command.** The I/O pair is duplex and the task's process holds its end, so a
  client on the socket can write once the runner pumps that way too; it does not yet, and the
  command's stdin is empty. When it does, a half-close on the I/O pair is EOF on stdin, and the
  kill stays the stderr pair's EOF, so nothing of the kill changes.
- **A signal to the command.** None is deliverable through bwrap from outside; the kill is the
  namespace's, whole. A graceful stop is closing stdin, once input is in.

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

- **At the second review, against the code:** the runner is in the middle of the stream (two
  pairs), since redaction is its; a descriptor rides its frame's bytes and a plain read drops it,
  so the task's side reads with `ReadMsgUnix` throughout; the kill is the pid namespace's,
  `--new-session` putting a process group out of reach; delivery names its own `Confine` and
  `Network`, since runnerd imports it; `env` reads by `Reading`, Within for a shape, a ceiling
  and an allow row, Touches for a deny or an ask row, as an address does; a `secret` ceiling
  block names its `name`; a missing block is `refused`'s to say, as for every op, so the
  table's test holds.
- **At the third pass, before step 3:** stderr is a socket pair of its own beside the I/O
  pair, both descriptors on the one stream frame, so a shell reads each as it comes and tells
  them apart as `2>` does; a kill is the task's side closing its ends, the same path as the
  task's process dying, detected on the stderr pair's EOF, with no op and nothing on the wire;
  no graceful signal, since bwrap delivers none from outside; whether the exec tool blocks, or
  splits into start, wait and kill, is settled at step 5 and changes `Do` alone.

## Open questions

None.

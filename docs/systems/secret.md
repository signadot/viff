# secret

The `secret` system is one command run with one secret of the runner's [secret
store](../reference/runnerd.md#the-secret-store). The task never holds the secret. The runner
reads it, starts the command in a sandbox of its own over the task's workspace and under the
task's network, and gives the task the command's output as it comes and its exit status, the
secret's value redacted. The environment says which secret and which commands; the autonomy decides each run; the
ceiling bounds both.

It is for a command that needs a secret, and nothing else. A command that needs none is the task's
own shell ([`commands.run`](inside.md)), or a script.

## What a task can do

A `secret` block names the secret (`name`) and the variable the command finds it in (`as`), and its
`exec` shapes say which commands, where, and with which plain variables beside the secret:

```tony
spawn:
  agent: {name: claude}
  environment:
    systems:
      network: {mode: allow, tcp-client: {host: registry.npmjs.org}}
      git: [{repo: acme/lib, from: main, fetch: {}}]
      secret:
      - name: npm-token
        as: NPM_TOKEN
        exec:
        - {argv: "npm publish --ignore-scripts", dir: !glob "packages/*"}
        - {argv: !regexp "^npm view ", env: {NODE_OPTIONS: null}}
  prompt: "Publish each package under packages/ whose version is not yet on the registry."
  autonomy:
  - {match: {system: secret, env: {LD_PRELOAD: null}}, then: deny}
  - {match: {system: secret, verb: exec, argv: !regexp "^npm publish "}, then: ask}
  - {then: allow}
```

| Tool | What it does | Step |
|---|---|---|
| `secret-exec` | starts one command with the block's secret in its environment under `as`, and answers once it has started: the run's id (`e1`) and where its sockets and files are | `secret.exec`, with `name`, `as`, `argv`, `dir`, `stdin` and `env` |
| `secret-exec-wait` | blocks until a run ends, by its id, and answers the exit status and each stream's file in the workspace, size and tail | none: it acts on a run already decided, and is offered with `secret-exec` |
| `secret-exec-kill` | kills a run, by its id | none, as the wait |

**The call answers at the start, and the command runs on.** A task does one thing at a time: an
agent's loop runs one tool, and a script's calls reach the loop one by one. So the call that
starts a command does not wait for it, and the task is free to read the output as it comes, to
give the input, to wait, or to kill. A call that is refused, or whose command could not start,
answers that, and there is no run.

**The task is told which secrets it may name.** `secret-exec` takes its secret as an argument,
where every other system's target is part of the tool's name the task is offered (`git-push`,
`slack-post`), so the brief names the environment's secrets, each with the variable its command
finds it in, and says that `name` is the secret's own name and not the variable's. Without that a
task guesses, and the variable it can see in the shapes is the wrong guess nearest to hand: a
model given `as: SIGNADOT_API_KEY` sends `SIGNADOT_API_KEY` as the `name`, which is no secret's
name, and then a lowercasing of it, which is no block's. The sentence is the offered tools' and
the environment's both, so it cannot name a secret a task has none of.

**The command is a list, and the step is one line.** The tool takes `argv` as a list of arguments,
never a shell line, and the runner runs that list. The step's `argv` is the list spelled as one
line, each argument quoted where it holds a space, a quote or a character a shell reads (`npm
publish --tag 'a b'`), so a shape, a row and a person approving read a command line as they read
`commands.run`'s, and anchor their patterns as they do there (`!regexp "^npm publish "`). Where one
argument ends is never in doubt: `npm "publish --registry evil"` spells as `npm 'publish --registry
evil'`, which no pattern on the bare line admits.

**The program is the runner's; what it runs is the shapes'.** `argv[0]` is a bare name, found on
the command's `PATH` on the runner's side, and the runner runs it only where it resolves, links
followed, under a system directory, a `readOnly` path of the runner's, or the task's install
root: never under the workspace, and never through a `PATH` the call set to point there. The
install root is the runner's writing and not the task's — the runner lays a tool it installed for
the task into it from its own side, and the task cannot write it — so such a tool is one a
command with a secret may start from, which is how a program the image does not carry gets to
run with a key. `/inst/bin` is first on the task's `PATH`, so where the name is in the install
root and in a system directory both, the installed one is what runs. That bounds `argv[0]` and no
more. What the program then runs runs with the secret too, and it may be the workspace's: `npm
publish` runs the package's lifecycle scripts, `node x.js` and `sh -c` run what they are given,
and a program reads its configuration from the task's `HOME`, which is the workspace. So the
shapes are the guard, and a shape is written for what the command does, not for its name: `npm
publish --ignore-scripts` rather than `npm publish`, and no interpreter or shell with an argument
the task chooses.

**`dir` is where the command runs,** a path under the workspace root; empty is the root. The
command runs over the live tree, as the task has it at that moment. What it acts on is not any
commit: a person who approves `npm publish` approves the command and not its content, and the tree
may change while the command runs, since the task runs on.

**`env` is the plain variables the command is given beside the secret,** by name. The command's
environment is the task's shell's (the doer's `shellEnv`, the launch's `vars`, `HOME` as the
task's root, `TMPDIR=/tmp`), then the call's `env`, then the secret under the block's `as`, which no
plain variable may name. A shape names each variable the command may be given, with a pattern on its
value (`null` for any), and **matches closed**: a variable the shape does not name puts the call
outside, where tony's own object match would let it by. A shape that says nothing of `env` gives
none, and `env: null` gives any. `LD_PRELOAD` and its family are admitted as any name is: a ceiling
that admits a command admits what that command does with the secret, by construction, and a
variable that changes what the command does adds nothing the ceiling has not granted. It is the
offer's and the spawn's to name the variables one by one, and a row's to deny one.

A row reads `env` as tony reads an object: `env: {LD_PRELOAD: null}` is every call that gives
`LD_PRELOAD`, whatever else it gives. An `allow` row reads it as a shape does, the call's variables
lying among those the row names; a `deny` or an `ask` row reads it as above, the call giving one of
them. Each errs toward refusing, as a row on an address does
([autonomy](../reference/gates.md)).

**The output is two streams.** The command's stdout and stderr each come to the task's process on
a unix socket of their own, with the secret's value replaced by `[secret <name>]` wherever it
appears whole: an encoding of it, or the value split by the command's own formatting, is not
caught, which is the shapes' to prevent and not the redaction's. The task's process writes each to
a file in the workspace, `.verse/exec/<id>.out` and `<id>.err`, cut at 16 MiB and said, and serves
each while the command runs at `<id>.sock` and `<id>.err.sock` beside them, where a shell of the
task's can connect and read. A reader reads the stream from its start, however late it connects:
it is sent the file's part first, and then what comes. One that connects once the file is cut is
closed, since the start is no longer to be had, and one that stops reading is dropped after ten
seconds; the run goes on either way, and the file holds what such a reader did not get. A file
that cannot be written, or a socket whose path is too long, is said in the answers, and the run
goes on.

**The end is a file too.** When the command ends, its end is written to `.verse/exec/<id>.exit`,
one line (`exited 3`, `killed after 30s, the timeout`), before the output's sockets close: a
reader that reads a stream to its end finds the end there, with no call. `secret-exec-wait`
answers the same end, then each stream's file, size and last 16 KiB, when the command ends or at
once where it has; the last 32 runs' answers are kept for it.

**`stdin: true` opens the command's input.** The default is a command with no input: a shape that
says nothing of `stdin` means `stdin: false`, in the environment and in the ceiling both, so an
interactive run is admitted shape by shape — and the step always says which kind it is, since a
person who approves one approves a session, not one command line. With it, the task's process
serves a third unix socket beside the outputs, `<id>.in.sock`: the first connection is the input,
whole, written along to `.verse/exec/<id>.in` and cut as the outputs are, and closing it is the
command's EOF, its output still flowing. A later connection is closed at once. Nothing is redacted
on the way in — the input goes to the process that already holds the secret — and nothing
connected leaves the command waiting on its stdin, with the timeout as the bound.

**The run lives while the task holds its output.** `timeout` on the call is in seconds, 10 minutes
where none, an hour at most, and refused where it is no whole number. Past it the command is
killed, with everything it started, and its end says so. The task lets go of the run, and the
runner kills it, when `secret-exec-kill` names its id and when the task's process goes, which
is where a task that ends or is cancelled takes its runs with it. A call given up before its
command has started lets go of it as it starts; a wait given up leaves the run. There is no
other signal: a command that wants a graceful end gets one from its own timeout.

## From a shell

A script calls the three tools with `verse-call-tool`, and the same environment and rows decide
the call. `verse-secret-exec` is the call and everything around it, for a command that is to
read as if it ran in place:

```sh
verse-secret-exec -name npm-token -dir packages/a -- npm publish --ignore-scripts
printf 'yes\n' | verse-secret-exec -name signadot-key -stdin -- signadot sandbox apply -f -
```

It starts the run, connects its own stdout and stderr to the run's two sockets and, with
`-stdin`, its stdin to the third, and exits with the command's exit status, read from the
`.exit` file. `-dir`, `-env NAME=value` (repeated) and `-timeout` are the tool's `dir`, `env`
and `timeout`. What a socket did not give it (a command over before it connected) it reads from
the stream's file, so its output is whole up to the file's 16 MiB cut.

| Exit status | What happened |
|---|---|
| the command's | it exited |
| 124 | the run's timeout killed it |
| 125 | `verse-secret-exec` failed, or the run ended another way; stderr says how |
| 126 | the call was refused, by a row or by the runner; stderr says why |
| 128 + the signal's number | a signal to `verse-secret-exec` ended the run |

On SIGINT, SIGTERM or SIGHUP it kills the run with `secret-exec-kill`, or gives the call up
where the command has not started, which withdraws a question still standing about it. The
signal itself does not reach the command.

It is found where `verse-call-tool` is, on a script's `PATH`, and like it speaks to the socket
`$VERSE_TOOLS` names, which a script's shell has.

**The network bounds where the secret can go.** The command runs under the task's network, so a
command the shapes admit sends the secret wherever the task's network reaches, configured by
files the task can write. Pair a `secret` block with a network that reaches the one host the
command needs (`tcp-client: {host: registry.npmjs.org}`, as above), and in the ceiling too.

## Where the secret lives

In the runner's [secret store](../reference/runnerd.md#the-secret-store), under the block's
`name`: one file to a secret, read at the run, so a rotated secret is the next run's. A secret's
name is public: it is in the runner's description and in every spawn that uses it, so it must not
be a secret itself. The value is in the runner's memory while the command runs, in the command's
environment, and nowhere else: in no log, no reply and no file. The task's own processes are in
another pid namespace and see no `/proc` of the command's.

A runner without a store, or whose sandbox is `none`, performs nothing of `secret`: a command
unconfined would run as the runner's user on the whole machine, and the secret would be the
machine's. The runner says so at start, and a spawn that names `secret` is refused where it is
written.

**The operator** names the secrets a doer's tasks may run a command with, one by one, and the
commands, in the runner's configuration:

```tony
agents:
  claude:
    systems:
      secret:
        ceiling:
        - name: npm-token
          as: NPM_TOKEN
          exec:
            argv: !regexp "^npm (publish --ignore-scripts|view )"
            dir: !glob "packages/*"
            env: {NPM_CONFIG_REGISTRY: !regexp "^https://registry\\.npmjs\\.org/", NODE_OPTIONS: null}
```

A ceiling block names its secret as a literal, never a pattern, and a block that names none is
refused: a name is what a doer is trusted with. `as` it may pattern or leave out. Its `exec`
shapes match variables closed, as an environment's do: a block that says nothing of `env`, or an
empty `exec: {}`, gives no variables, and one that gives any says `env: null`. A block whose
secret is not in the store when the runner starts is left out of the ceiling, and said. Verse
refuses to install a spawn whose environment gives a secret, a command or a variable the ceiling
does not ([the verse runner](../reference/runnerd.md#what-it-offers)), and the runner holds each
run to the ceiling again when it acts, whatever the autonomy said.

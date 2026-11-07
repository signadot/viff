# secret

The `secret` system is one command run with one secret of the runner's [secret
store](../reference/runnerd.md#the-secret-store). The task never holds the secret. The runner
reads it, starts the command in a sandbox of its own over the task's workspace and under the
task's network, and gives the task the command's exit status and its output, the secret's value
redacted. The environment says which secret and which commands; the autonomy decides each run; the
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
| `secret-exec` | runs one command with the block's secret in its environment under `as`, and blocks until it ends; the task reads back the run's id, the exit status, and each stream's file in its workspace, size and tail | `secret.exec`, with `name`, `as`, `argv`, `dir` and `env` |
| `secret-exec-kill` | kills a run `secret-exec` is waiting on, by its id (`e1`) | none: it acts on a run already decided, and is offered with `secret-exec` |

**The command is a list, and the step is one line.** The tool takes `argv` as a list of arguments,
never a shell line, and the runner runs that list. The step's `argv` is the list spelled as one
line, each argument quoted where it holds a space, a quote or a character a shell reads (`npm
publish --tag 'a b'`), so a shape, a row and a person approving read a command line as they read
`commands.run`'s, and anchor their patterns as they do there (`!regexp "^npm publish "`). Where one
argument ends is never in doubt: `npm "publish --registry evil"` spells as `npm 'publish --registry
evil'`, which no pattern on the bare line admits.

**The program is the runner's; what it runs is the shapes'.** `argv[0]` is a bare name, found on
the command's `PATH` on the runner's side, and the runner runs it only where it resolves, links
followed, under a system directory or a `readOnly` path of the runner's: never under the
workspace, and never through a `PATH` the call set to point there. That bounds `argv[0]` and no
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
task's can connect and read along from then on. A reader that stops reading is dropped after ten
seconds, and the run goes on. The tool answers the model with the run's id and its end (`exited
3`, `killed after 30s, the timeout`), then each stream's file, size and last 16 KiB. A file that
cannot be written, or a socket whose path is too long, is said in the answer, and the run goes on.

**The run lives while the task holds its output.** `timeout` on the call is in seconds, 10 minutes
where none, an hour at most, and refused where it is no whole number. Past it the command is
killed, with everything it started, and the answer says so. The task lets go of the run, and the
runner kills it, when `secret-exec-kill` names its id, when the call is given up (the task
cancelled), and when the task's process goes. There is no other signal: a command that wants a
graceful end gets one from its own timeout.

`secret-exec` blocks, so the kill is reachable from beside it: a script makes the call in the
background (`verse-call-tool secret-exec … &`) and kills it by id, and the same environment and
rows decide the call. An agent whose loop runs one tool at a time bounds a run with `timeout`.

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

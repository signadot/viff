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
| `secret-exec` | runs one command with the block's secret in its environment under `as`; the task reads back the exit status, the path of the output in its workspace, and the output's tail | `secret.exec`, with `name`, `as`, `argv`, `dir` and `env` |

**The command is a list, and the step is one line.** The tool takes `argv` as a list of arguments,
never a shell line, and the runner runs that list. The step's `argv` is the list spelled as one
line, each argument quoted where it holds a space, a quote or a character a shell reads (`npm
publish --tag 'a b'`), so a shape, a row and a person approving read a command line as they read
`commands.run`'s, and anchor their patterns as they do there (`!regexp "^npm publish "`). Where one
argument ends is never in doubt: `npm "publish --registry evil"` spells as `npm 'publish --registry
evil'`, which no pattern on the bare line admits.

**The program is the runner's.** `argv[0]` is a bare name, found on the command's `PATH`, and the
runner runs it only where it resolves under a system directory or a `readOnly` path of the
runner's: never under the workspace, where a program of the task's would read the secret for it.

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

**The output is a stream.** The command's output comes to the task's process with the secret's
value replaced by `[secret <name>]` wherever it appears whole (an encoding of it is not caught).
The task's process serves it as a unix socket at `.verse/exec/<id>.sock` in the workspace, where a
shell of the task's can connect to read along, and writes it to `.verse/exec/<id>.out`, cut at 16
MiB and said. The tool answers the model with the exit status, the file's path and the last 16 KiB
of the output.

**The run is bounded.** `timeout` on the call is in seconds, 10 minutes where none, an hour at
most. Past it the command is killed, with everything it started, and the answer says so. When the
task's process goes, the command is killed with it.

A script calls the same tool with `verse-call-tool secret-exec`, and the same environment and rows
decide it.

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
refused: a name is what a doer is trusted with. `as` it may pattern or leave out. A block whose
secret is not in the store when the runner starts is left out of the ceiling, and said. Verse
refuses to install a spawn whose environment gives a secret, a command or a variable the ceiling
does not ([the verse runner](../reference/runnerd.md#what-it-offers)), and the runner holds each
run to the ceiling again when it acts, whatever the autonomy said.

# Platform commands

This page lists the commands of `verse` that run a verse, extend it, or work a task as its
worker. The commands for a user are in [the reference](../reference/README.md#commands).

```
verse
  up   [-root <dir>]                         start the monolith (engine + store + tasks)
                                             the dir an llm agent reads/acts in; default: an
                                             isolated temp dir, printed at start when model
                                             credentials are configured
       [-data <dir>]                         bring logd+docd up on <dir> (default: fresh temp dir)
       [-logd-config <file>]                 a logd configuration for the daemons it spawns,
                                             in place of the one verse writes
       [-admin-addr host:port|off]           the admin and pprof face (default: the -addr port + 1)
       [-reseed-perms]                       write the root, verse and admin-group records from
                                             config OVER what stands (see up.md)
       [-unmigrate-perms]                    rewrite the perms mirror for an older binary, and
                                             EXIT without serving (see up.md)
       [-docd-addr host:port]                attach to a running docd (client face) …
       [-docd-mount-addr host:port]          … and its controller/MOUNT face
       [-docd-advertise-addr host:port]      publish THESE on /status instead, when a client
       [-docd-mount-advertise-addr h:p]      reaches docd by a different address than verse does
       [-o-bin path]                         the o binary to spawn daemons with (default: resolved —
                                             see up.md)
       [-runner <url>]                       run tasks on a remote task-control base URL …
       [-broker       ]                      … and/or SERVE an in-process dial-in broker, which a
                                             spawn reaches as `-runner broker` (needs -broker-addr;
                                             publishes each task's dialURL from it). It adds a name
                                             rather than replacing the default: a spawn naming
                                             nobody still runs here …
       [-broker-addr host:port]              … the sibling port serving this verse's broker face
                                             (/broker/<nonce>); all default off (local in-process
                                             supervisor). See ../task-control.md.
       [-broker-advertise-addr host:port]    publish THIS in each task's dialURL instead, when a
                                             worker dials the broker by a name the process cannot
                                             bind (a Service, a tailnet device)
  down                                       stop the verse on -addr (it reaps its spawned daemons)
  task (tasks, ps) [-runner <ref>]           every task, whatever runs it
          do complete|fail|note <id> [text] -as runner:<identity>    executor vantage (dial in):
          do gate <id> <name> -as runner:<identity> [-to <who>,…] [-reason <t>] [-wait]       raise a gate up
                  [-options id[:need],…] [-deadline within[:default]]    …with outcomes
                                             of its own and a clock, as a charter gate has
  runner (runners) [-probe]                  where work can run
          add <name> <url> [-identity <id>] [-no-probe] · add [-file <f>|-] [file] · show <ref>
                                             -identity: who the runner acts as, runner:<id>
                                             (default: its name)
          check <ref>                        ask a runner what it is, right now
          remove <ref> [-cancel "<why>" | -leave]     what becomes of the work still on it
  io (ios) [-probe]                          a connector between the verse and an external
                                             system: it reflects in, and it acts out
          add <name> <url> -root <root> [-out <place>] [-requesters <who>]
                                            [-as <principal>] [-no-probe] [-allow-overlap]
          add -dir <d> <name>               a local git repo, reflected in task
          add [-file <f>|-] [file] · show <ref>
          check <ref>                        ask it what it is and what it claims, right now
          remove <ref> [-forget | -keep]     what becomes of what it reflected
  resolver (resolvers)                       who answers questions
          add [-file <f>|-] [file] · show <ref> · remove <ref>
```

## The scripts

`./smoke.sh` is a different thing. It builds its own binaries and runs its own isolated
server, and it touches nothing of yours.

# Platform commands

This page lists the commands of `viff` that run a viff instance, extend it, or work a task as its
worker. The commands for a user are in [the reference](../reference/README.md#commands).

```
viff
  up                                         start the monolith (engine + store), and the local runner beside it
                                             the dir the viff agent reads/acts in; default: an
                                             isolated temp dir, printed at start when model
                                             credentials are configured
       [-data <dir>]                         bring logd+docd up on <dir> (default: ~/.viff/<host>_<port>)
       [-logd-config <file>]                 a logd configuration for the daemons it spawns,
                                             in place of the one viff instance writes
       [-admin-addr host:port|off]           the admin and pprof face (default: the -addr port + 1)
       [-reseed-perms]                       write the root, viff and admin-group records from
                                             config OVER what stands (see up.md)
       [-docd-addr host:port]                attach to a running docd (client face)
       [-docd-advertise-addr host:port]      publish THIS on /status instead, when a client
                                             reaches docd by a different address than viff does
       [-o-bin path]                         the o binary to spawn daemons with (default: resolved —
                                             see up.md)
       [-local-runner=false]                 start no local runner: route every spawn to a registered runner
       [-runnerd-bin path]                   the runnerd the local runner runs (default: beside viff, see up.md)
  down                                       stop the viff instance on -addr (it reaps its spawned daemons)
  task (tasks, ps) [-runner <ref>]           every task, whatever runs it
                  [-options id[:need],…] [-deadline within[:default]]    …with outcomes
                                             of its own and a clock, as a charter gate has
  runner (runners) [-probe]                  where work can run
          add <name> <url> [-identity <id>] [-no-probe] · add [-file <f>|-] [file] · show <ref>
                                             -identity: who the runner acts as, runner:<id>
                                             (default: its name)
          check <ref>                        ask a runner what it is, right now
          remove <ref> [-cancel "<why>" | -leave]     what becomes of the work still on it
  iod (iods) [-probe]                          a connector between the viff instance and an external
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

`./test/smoke.sh` is a different thing. It builds its own binaries and runs its own isolated
server, and it touches nothing of yours.

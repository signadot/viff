# Reference

The flat version of everything the [tutorial](../tutorial.md) walks, plus the detail it
deliberately skips. For what a charter's fields *mean*, see
[the trigger model](../trigger-model.md); for the map of everything,
[the docs index](../README.md).

| | |
|---|---|
| [up.md](up.md) | starting a verse: the store, the driver, the agent root, runners and brokers, the config layers, and the `o` binary |
| [entities.md](entities.md) | refs, payloads, merging writes, conditional writes, selection, and watching |
| [tasks.md](tasks.md) | the task surface end to end: drivers and vectors, gates, attaching, the mirror, and executing one by hand |
| [connect.md](connect.md) | federating a working tree, and scopes |
| [resolvers.md](resolvers.md) | the resolver surface: the binding, its two entities, the routes a service lives on, and the verbs |
| [runners.md](runners.md) | the runner surface: where work runs — the entity, the verbs, the routes, and what a task records |
| [sources.md](sources.md) | the source surface: what reflects an external system in — the entity, ownership globs, the verbs, the routes, and the contract a source serves |
| [sourced.md](sourced.md) | the three sources verse ships: git (and mirrors), Slack, Linear — the flags, the credentials, the delivery endpoints, and what each reflects |
| [examples/](examples/README.md) | working programs to copy and change: a runner, and a source |
| [goals.md](goals.md) | goals — a target over the reactive substrate (being redesigned) |
| [store.md](store.md) | what the store guarantees, and the entity grain the engine reacts at |
| [representation.md](representation.md) | tony, JSON and YAML: what verse reads, what it writes, and why that is asymmetric |

## Commands

```
verse
  up   [--root <dir>]                        start the monolith (engine + store + tasks)
                                             the dir an llm agent reads/acts in; default: an
                                             isolated temp dir, printed at start
       [--data <dir>]                        bring logd+docd up on <dir> (default: fresh temp dir)
       [--docd-addr host:port]               attach to a running docd (client face) …
       [--docd-mount-addr host:port]         … and its controller/MOUNT face
       [--o-bin path]                        the o binary to spawn daemons with (default: resolved —
                                             see up.md)
       [--runner <url>]                      run tasks on a remote task-control base URL …
       [--broker      ]                      … or on an in-process dial-in broker (needs --broker-addr;
                                             publishes each task's dialURL from it) …
       [--broker-addr host:port]             … the sibling port serving this verse's broker face
                                             (/broker/<nonce>); all default off (local in-process
                                             supervisor). See ../task-control.md.
  status                                     engine/store snapshot
  schema [<name>]                            the whole schema: the root — what a verse looks like,
                                             by top-level path — then every shape it names.
                                             A name prints just that one definition
  down                                       stop the verse on --addr (it reaps its spawned daemons)

  watch  [--system --kind --since <rev>]     stream store changes (cursored)
         [--if <pat>] [--if-at <field.path>] … only those whose payload matches (see below)

  Each collection is addressed twice: the PLURAL lists it, the SINGULAR acts on one member.
  The plural also answers to the singular's verbs — `verse entities get <ref>` is
  `verse entity get <ref>` — so the second command is an edit of the first, not a retype.
  Only the bare plural lists: `verse entities list` is an error naming `verse entities`.

  entities [--system <s>] [--kind <k>]       the store
           [--if <pat>] [--if-at <path>]     … only those whose payload matches (see below)
  entity (e)  get <ref> · put <ref> <payload> · rm <ref>
          put/rm [--at <field.path>]         write or remove INSIDE the payload, not the whole entity
          put [--create|--update]            only if nothing is there yet · only if something is
          put [--if <pattern>]               only if what is there matches
                                             (a put MERGES: fields it does not name are left alone,
                                              and `rm --at` is how one goes away)
  gates                                      pending decisions across the fleet
  gate    approve <id> [--say <text>] · deny <id> [--say <text>]
          approve: y, yes · deny: n, no · --say: -s
  triggers                                   the installed charter
  trigger add [--file <f>|-] [file] · show <name> · remove <name>  (add rejects a duplicate name)
  tasks (ps) [--runner <ref>]            every task, whatever runs it
  task run <prompt…> [--attach] [--wait] [--driver scripted|llm]  (honors the runner; mirrored)
          run [--runner <ref>]               …on an installed runner, not the verse default
          get <id> · attach <id> · inbox <id>            operator vantage (any backend):
          cancel <id> · say <id> <text…>
          vector <id> auto|ask|park [--as <who>] · vector <id> --file <f>
          approve|deny <id> <decision> [--say <text>]    (also y|yes, n|no)
          do complete|fail|note <id> [text] --as <identity>   executor vantage (dial in):
          do gate <id> <name> --as <identity> [--reason <t>] [--wait]   raise a gate up
  runners                                    where work can run  [--probe]
  runner  add <kind>:<name> <url> [--no-probe] · add [--file <f>|-] [file] · show <ref>
          check <ref>                        ask a runner what it is, right now
          remove <ref> [--cancel "<why>" | --leave]   what becomes of the work still on it
  sources                                    what reflects an external system in  [--probe]
  source  add <system>:<name> <url> [--no-probe] [--allow-overlap]
          add --dir <d> <system>:<name>      a local git repo, reflected in task
          add [--file <f>|-] [file] · show <ref>
          check <ref>                        ask it what it is and what it claims, right now
          remove <ref> [--forget | --keep]   what becomes of what it reflected
  flows                                      the distinct subjects with live work
  flow    show <addr> · pause <addr> · resume <addr> · cancel <addr> · say <addr> <text…>
          <addr> addresses live tasks by the subject they are about: demo:pr:1 (one),
          demo:pr:* (a kind), demo:*:* (a system) — a flow is a query, not a stored key
  goals                                      status / waitingOn / outcome, per goal
  goal    add <name> --done-when <formula> [--subject <ref>] [--deadline <dur>]
          add --file <f> (full record incl. owned triggers) · remove <name>
          pause|resume|cancel <name> · say <name> <text…>

  docket                                     list open motions (votes + provider selections)
  vote    <motion> <choice> --as <who>       cast a ballot (approve|reject, or a select-option)
  providers                                  list the provider registry (provider:<capability>:<id>)

  connect [path] [--prompt <t>] [--driver <d>]  federate a tree (own scope): scoped metadata +
          [--interval <dur>]                  content controller + a validator per file
  disconnect                                  drop the --scope overlay (else remove local:* entities)

  global:  --addr <host:port>   --scope <name>
           --ofmt, -o <tony|yaml>  output format (default tony; verse renders no json)
           --ifmt, -i <tony|json|yaml>  input format for stdin / extensionless files
           --color auto|always|never   colorize (auto = a terminal only)
```

Flags may appear before or after positionals (`gate approve <id> --as you --say ok` works).

## Selecting by what an entity says

`--system` and `--kind` narrow by where an entity lives. `--if` narrows by what it says: a
tony match against the payload, which is the same predicate a charter carries in `value` — in
its `subject`, its `on` or its `when`. So `verse entities --if X` lists exactly what
`verse watch --if X` streams, and both name what a rule with that predicate would select.

```sh
verse entities --if '{stage: !or [open, merged]}'   # an operator, not just equality
verse entities --if '{urgent: true}' --if-at labels # rooted inside the payload
verse watch --system demo --if '{stage: open}'      # the same selector, as it happens
```

`--if-at` takes a dot-separated field path and roots `--if` there, so a predicate about a
subtree need not restate the shape above it — `--if-at labels --if '{urgent: true}'` is
`--if '{labels: {urgent: true}}'`. It also reaches a subtree that is not an object, where the
predicate is a bare scalar match. A path that leads nowhere — an absent field, or a scalar in
the way — does not match, and neither does a delete on `watch`, which has no payload left to
ask about. The pattern is read in `--ifmt` and sent as tony, so operators and tags survive.

Note `entity put --if`, which is a different `--if`: a precondition on a write, not a filter
on a read. See [entities.md](entities.md#conditional-writes).

## The scripts

Three scripts write into a verse you are already running, and each one prints what to type
next. They are for reading as much as for running: each is a worked example of the surface it
seeds.

| | |
|---|---|
| `./scripts/seed-demo.sh` | the whole demo at once: a charter, the entities it reacts to, one reaction that has already happened, and one gate waiting for you |
| `./scripts/seed-entities.sh` | a dozen entities across three systems, and nothing that reacts to them ([tutorial §2](../tutorial.md#2-entities)) |
| `./scripts/charter-shapes.sh` | the coarse shapes a rule comes in, one command each ([tutorial §4](../tutorial.md#4-charters--relating-entities-to-tasks)) |
| `./scripts/lineage.sh` | a commit becomes a PR becomes a release: how ids from three systems correlate without a fourth being minted, and the two edges of doing it that way |

All four take `--scope <name>` and undo themselves (`--clean`, or `clean`). `./smoke.sh` is
a different thing: it builds its own binaries and runs its own isolated server, and touches
nothing of yours.

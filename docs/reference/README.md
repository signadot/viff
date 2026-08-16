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
| [sourced.md](sourced.md) | the five sources verse ships: git (and mirrors), git-issue, Slack, Linear, GitHub — the flags, the credentials, the delivery endpoints, and what each reflects |
| [examples/](examples/README.md) | working programs to copy and change: a runner, and a source |
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
       [--docd-advertise-addr host:port]     publish THESE on /status instead, when a client
       [--docd-mount-advertise-addr h:p]     reaches docd by a different address than verse does
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

  watch  [--since <rev>] [<path>]            stream store changes (cursored) — the same path
                                             a read takes, naming the same entities
         [--if <pat>] [--if-at <field.path>] … only those whose payload matches (see below)

  Each collection is ONE command, named in the singular. Bare it LISTS, and with a verb it
  acts on one member: `verse gate` shows the pending decisions, `verse gate approve <id>`
  answers one. The plural is an alias of that same command — `verse gates`, `verse tasks`
  — so both spellings do the same thing. `verse gate list` is an error: bare it lists.

  `state` is not one of them: it answers ONE DOCUMENT at a path rather than a list of things,
  so there is no plural to alias and its positional is an address rather than a verb.

  state   [<path>]                           the verse at a path: the subtree standing there
                                             demo · demo.pr · demo.pr.1 · demo.pr.1.labels.urgent
                                             'demo.pr.*.stage' · (none) = the whole verse
          [--if <pat>] [--if-at <path>]      … only entities whose payload matches (see below)
          [--paths]                          … one address per line instead of the document
                                             flags go on either side of the path
          get <path>                         one place, at any depth — and a read may reach into
                                             an array: merge.proposal.X.votes(scott)
          put <path> <payload>               a write reaches object fields, not array elements
          rm  <path>                         an entity, or one field of one
          put [--create|--update]            only if nothing is there yet · only if something is
          put [--if <pattern>]               only if what is there matches
                                             (a put MERGES: fields it does not name are left alone,
                                              and `rm <path>` is how one goes away)
          An address is one grammar in two delimiters: `demo:pr:1` is a ref and names the
          entity whole; `demo.pr.1.labels` continues into the payload. Whichever delimiter
          comes first is the one in use, so an id may hold either.
  gate (gates)                               pending decisions across the fleet
          approve <id> [--say <text>] · deny <id> [--say <text>]
          approve: y, yes · deny: n, no · --say: -s
  trigger (triggers)                         the installed charter
          add [--file <f>|-] [file] · show <name> · remove <name>  (add rejects a duplicate name)
          add --at <address>                 install as a PACKAGE: its own engine, and its
                                             rules may write only inside <address>
                                             (`notes`, `demo.pr`) — see ../trigger-model.md
  task (tasks, ps) [--runner <ref>]          every task, whatever runs it
          run <prompt…> [--attach] [--wait] [--driver scripted|calls|llm]  (honors the runner; mirrored)
          run [--runner <ref>]               …on an installed runner, not the verse default
          get <id> · attach <id> · inbox <id>            operator vantage (any backend):
          cancel <id> · say <id> <text…>
          vector <id> auto|ask|park [--as <who>] · vector <id> --file <f>
          approve|deny <id> <decision> [--say <text>]    (also y|yes, n|no)
          do complete|fail|note <id> [text] --as <identity>   executor vantage (dial in):
          do gate <id> <name> --as <identity> [--reason <t>] [--wait]   raise a gate up
  runner (runners) [--probe]                 where work can run
          add <kind>:<name> <url> [--no-probe] · add [--file <f>|-] [file] · show <ref>
          check <ref>                        ask a runner what it is, right now
          remove <ref> [--cancel "<why>" | --leave]   what becomes of the work still on it
  source (sources) [--probe]                 what reflects an external system in
          add <system>:<name> <url> [--no-probe] [--allow-overlap]
          add --dir <d> <system>:<name>      a local git repo, reflected in task
          add [--file <f>|-] [file] · show <ref>
          check <ref>                        ask it what it is and what it claims, right now
          remove <ref> [--forget | --keep]   what becomes of what it reflected
  resolver (resolvers)                       who answers questions
          add [--file <f>|-] [file] · show <ref> · remove <ref>
  stratum (strata)                           the declared level ordering
          add [--file <f>|-] [file] · show <name> · remove <name>
  flow (flows)                               the distinct subjects with live work
          show <addr> · pause <addr> · resume <addr> · cancel <addr> · say <addr> <text…>
          <addr> addresses live tasks by the subject they are about: demo:pr:1 (one),
          demo:pr:* (a kind), demo:*:* (a system) — a flow is a query, not a stored key
  docket                                     list open motions (votes + provider selections)
  vote    <motion> <choice> --as <who>       cast a ballot (approve|reject, or a select-option)
  provider (providers)                       the provider registry (provider:<capability>:<id>)

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

The PATH narrows by where an entity lives. `--if` narrows by what it says: a tony match
against the payload, which is the same predicate a charter carries in `value` — in its
`subject`, its `on` or its `when`. So `verse state --if X` names exactly what
`verse watch --if X` streams, and both name what a rule with that predicate would select.

```sh
verse state --if '{stage: !or [open, merged]}'   # an operator, not just equality
verse state --if '{urgent: true}' --if-at labels # rooted inside the payload
verse watch --if '{stage: open}' demo            # the same selector, as it happens
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

Four scripts write into a verse you are already running, and each one prints what to type
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

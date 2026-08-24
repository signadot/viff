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
       [--broker      ]                      … and/or SERVE an in-process dial-in broker, which a
                                             spawn reaches as `--runner broker` (needs --broker-addr;
                                             publishes each task's dialURL from it). It adds a name
                                             rather than replacing the default: a spawn naming
                                             nobody still runs here …
       [--broker-addr host:port]             … the sibling port serving this verse's broker face
                                             (/broker/<nonce>); all default off (local in-process
                                             supervisor). See ../task-control.md.
       [--broker-advertise-addr host:port]   publish THIS in each task's dialURL instead, when a
                                             worker dials the broker by a name the process cannot
                                             bind (a Service, a tailnet device)
  status                                     engine/store snapshot
  schema [<name>]                            the whole schema: the root — what a verse looks like,
                                             by top-level path — then every shape it names.
                                             A name prints just that one definition
  down                                       stop the verse on --addr (it reaps its spawned daemons)

  watch  [--since <rev>] [<path>]            stream store changes (cursored) — the same path
                                             a read takes, naming the same entities
         --ago <n>                           …the last <n> COMMITS, resolved by the store
                                             against its watermark and clamped to the history
                                             it retains — so a deep one answers with what is
                                             left rather than refusing. A narrowed watch sees
                                             only the changes under its path, so the span holds
                                             however many events it holds. Absent streams from
                                             now; `--since 0` is the whole retained log.
                                             The relative cursor is its own flag because
                                             `--since -<n>` reads as a flag named `-<n>`
         [--if <pat>] [--if-at <field.path>] … only those whose payload matches (see below)
         --until <pat>                       …and STOP at the first FRAME matching <pat>, which
                                             is printed before the stream ends. A frame is
                                             `{at op to rev gap}` — the document `--trim` names
                                             — so a payload match is `{to: {stage: merged}}`,
                                             and a bare payload pattern is refused rather than
                                             left to wait for ever. Exit 0 whether or not it
                                             matched: what happened is the output, not the
                                             status
         --nowire                            print each frame as a BLOCK, opened by `---`,
                                             rather than on one line. The default is one frame
                                             per line, which is what lets `grep` see a whole
                                             event; this is for the stream somebody is reading
                                             rather than filtering, where a frame wider than
                                             the terminal is worse than one six lines tall.
                                             The brackets go with the wire, and `---` takes
                                             over as the frame boundary — so the output is a
                                             tony document stream and parses back as one

  Each collection is ONE command, named in the singular. Bare it LISTS, and with a verb it
  acts on one member: `verse gate` shows the pending decisions, `verse gate approve <id>`
  answers one. The plural is an alias of that same command — `verse gates`, `verse tasks`
  — so both spellings do the same thing. `verse gate list` is an error: bare it lists.

  EVERY LISTING FILTERS, with the same two flags: `--if <match>` keeps the items that match,
  `--trim <match>` writes only the parts its document names. They compose, and an empty result
  is an answer rather than an error (exit 0).

    verse ps --if '{status: running}' --trim '{id status}'
    verse motion --if '{axis: whether}'
    verse data demo pr --trim '{stage}'            # …per ENTITY, not per top-level key
    verse watch demo --trim '{at to}'

  A `--trim` document NAMES keys, so write it as a KEY SET: in brackets, a key with no `:` and
  no value is that key with a null, which is exactly what a trim wants. `{id status}` and
  `{id: null, status: null}` are the same document, and the first is the one to type.

  In `--if` the same spelling is a PRESENCE test, because a null there matches any value the
  key holds: `--if '{status}'` keeps every entity that has a `status` at all, whatever it says,
  and `--if '{stage}'` is how you ask which ones have got started. What it is NOT is equality —
  `--if '{status open}'` is two presence tests, `status` and `open`, and selects the entities
  carrying both keys rather than the ones whose status is open. A key nothing has selects
  nothing, so the answer to that one is empty rather than wrong-and-plausible.

  On `data` and `watch`, `--if` is the server's selector — it says which entities the read is
  about, before any of them cross the wire — so those two take `--trim` alone. A write's
  precondition is `--expect`, a different flag for a different question: what a read selects on
  and what a write insists on are not the same question, so they are not the same flag.

  Comments need no flag: verse reads and writes them everywhere (`o list` needs `-c`).

  `data` is not one of them: it answers ONE DOCUMENT at a path rather than a list of things,
  so there is no plural to alias — and WHAT TO DO IS A FLAG, so its positional is a path in
  every case. `get`, `put` and `rm` were verbs, and a path's first segment is a system name.

  data (d) [<step>…]                         the verse at an address, given as STEPS — one
                                             place per word, its first character saying which:
                                             demo pr 1 · demo pr 1 labels urgent  (a name)
                                             demo pr 1 votes '[0]'   (array; also {0} (k))
                                             demo pr 1 '"[0]"'       (the FIELD named [0])
                                             (none) = the whole verse
          [--at <path>]                      …or whole, as a kinded path — which is how one is
                                             copied out of a document, and the one spelling
                                             that carries a SEGMENT wildcard:
                                             demo.pr.1 · 'demo.pr.*.stage'
                                             An address is given ONCE, in one of the two
          [--if <pat>] [--if-at <path>]      … only entities whose payload matches (see below)
          [--paths] [--depth <n>]            … one address per line, as STEPS, instead of the
                                             document: EVERY address under the one asked for,
                                             since a document cannot say where its entities end.
                                             The line hands straight back —
                                             `data --paths demo pr | xargs -L1 verse data`.
                                             --depth bounds the walk to addresses of at most
                                             <n> segments (default: no bound)
                                             flags go on either side of the path
          [--kpaths]                         … the same listing as one KINDED PATH per line,
                                             which is the spelling that survives a shell
                                             variable as one argument:
                                             `r=$(data --kpaths … | head -1); data --at "$r"`
          --put <payload>                    write it there: a write reaches object fields, not
                                             array elements. A put MERGES — fields it does not
                                             name are left alone, and --rm is how one goes away.
                                             It descends only through an object: naming a field
                                             under an array or a scalar is REFUSED rather than
                                             replacing it. entities.md#what-a-write-descends-through
          --rm (-r)                          remove what stands there: an entity, or one field.
                                             `-r` is `verse c -r`'s letter: removal is the same
                                             act. `--put` has none — `-p` beside `--paths` is a
                                             slip that writes
          --put [--create|--update]          only if nothing is there yet · only if something is
          --put [--expect <pattern>]         only if what is there matches. `--expect` rather
                                             than `--if`, which is the read's filter: a write's
                                             precondition is a different question
          An address is one grammar in three delimiters — `demo.pr.1` names the entity
          whole, `demo.pr.1.labels` continues into the payload, `demo pr 1 labels` is the
          same as separate words — and whichever comes FIRST is the one in use, so an id may
          hold the others. entities.md#addressing is the whole of it, including which
          commands take which spelling.
  gate (gates)                               pending decisions across the fleet
          approve <id> [--say <text>] · deny <id> [--say <text>]
          approve: y, yes · deny: n, no · --say: -s
  charter (charters, c) [<path>]             THE INSTALLED CHARTER, at a path: what may
                                             be written there, what it reads, its rules
                                             and strata in full, and the same at every
                                             address under it. Bare, that is all of it —
                                             baseline at the root, each package under the
                                             address it writes in (`c drift`,
                                             `c verse-dev drift`). A PLACE — an address that
                                             exists because charters are under it — answers
                                             with `children:` alone: every other key is a
                                             fact about a charter, and `rules: []` where
                                             there is none reads as one with no rules
          [--depth <n>]                      how far under it to go: 0 is that address
                                             alone; -1 (the default) is everything
          [--scope <name>]                   …a federation's own charter instead
          [--paths] [--kpaths]               one line per rule and level instead of the document.
                                             --paths answers in STEPS — the spelling this
                                             command takes, so `c --paths | xargs -L1 verse c`
                                             round-trips — and --kpaths in one kinded path per
                                             line, for a line captured into a shell variable. A
                                             package that holds nothing yet prints itself,
                                             since that is the state worth seeing
          <address> rules|strata [<name>]    …and INTO the node standing there: its rules, its
                                             levels, one of them by name, and on into that one
                                             (`c verse-dev.drift rules watcher action`).
                                             `c rules watcher` is baseline's, which has no
                                             address. A name that is elsewhere is named in
                                             the refusal, with the path that reaches it
          -a [<address>] -f <f>|-            INSTALL: rules, strata and `reads:` from one file,
                                             in the order that works, and as ONE STEP — a
                                             refusal anywhere puts back what stood before it,
                                             across every package the file holds. The address
                                             is the positional, and a file may name its own
                                             (`at:` below). Re-applying an edited file is how a
                                             charter changes: a rule is replaced, and so is a
                                             level (every rule faces the ordering again, and one
                                             that no longer fits refuses the change)
          -a --scope <name>                  …into a federation's own charter instead
          -r <path>                          REMOVE what the path names: a package at an
                                             address, or one rule or level in it
                                             (`c -r verse-dev.drift rules watcher`)
          -R <address>                       …the charter there AND every charter under it.
                                             What a removal leaves is what the charter WROTE —
                                             a charter's writes are the world's
                                             (-R is not `rm -r`'s recursion: -r is the removal)
          <address> -- rules|strata          where the address ENDS, for a package whose own
                                             address ends in `rules` or `strata` and would
                                             otherwise shadow that key
          A charter file may open a package with a `---` document of its own — `at: drift`
          is where its rules write, `reads: [git, other]` what they may read besides, in
          either address spelling (`!verse-path [demo pr]` too). A package reads inside its
          own address and what it declares it reads and NOWHERE else; an undeclared read does not install,
          and the graph over packages must be acyclic (a cycle is refused, showing the loop).
          Several `at:` documents in one file install several packages.
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
  flow (flows)                               the distinct subjects with live work
          show <addr> · pause <addr> · resume <addr> · cancel <addr> · say <addr> <text…>
          <addr> addresses live tasks by the subject they are about: demo.pr.1 (one),
          demo.pr.* (a kind), demo.*.* (a system) — a flow is a query, not a stored key
          ONE TOKEN, not steps: a flow address is a selector with a `*` per segment, which
          the shell would expand — so quote it. A second positional is refused rather
          than read as a step
  motion (motions)                           the motions on the docket (votes + provider
                                             selections), open and settled — a settled one
                                             carries its outcome, or `orphaned` and a `why`
          <id>                               …just that one, as one document rather than a
                                             list of one. A motion is ONE TOKEN, its id — an
                                             id that is not there is refused, not answered
                                             with nothing
          <id> --vote <choice> --as <who>    cast a ballot into it (approve|reject, or a
                                             select-option). The CHOICE IS A FLAG's value
                                             because it is data — a select's options are
                                             provider names, an open set — where a gate's
                                             approve|deny is a closed binary and so a verb.
                                             The id and the choice are both bare words, so a
                                             ballot with them the wrong way round is refused
                                             and the refusal says which way it goes
          --watch                            …the same motions as they change instead of as
                                             they stand, one frame per line, narrowed to the
                                             one named. It streams from now; for history,
                                             `verse watch verse motion channel --ago <n>` is
                                             the same stream where the cursor lives
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

The path narrows by where an entity lives. `--if` narrows by what it says: a tony match
against the payload, which is the same predicate a charter carries in `value` — in its
`subject`, its `on` or its `when`. So `verse data --if X` names exactly what
`verse watch --if X` streams, and both name what a rule with that predicate would select.

```sh
verse data --if '{stage: !or [open, merged]}'   # an operator, not just equality
verse data --if '{urgent: true}' --if-at labels # rooted inside the payload
verse watch --if '{stage: open}' demo            # the same selector, as it happens
verse watch --until '{to: {stage: merged}}' demo # …until one of them merges, then stop
```

`--until` is the exception to that symmetry, and the shape of its pattern says so. `--if` is
answered at the SERVER, over entities, before there is a frame — so it takes the payload
pattern bare. `--until` is answered at the client, over the frames that arrive, because only
the client knows when it has seen enough — so it is written in the frame's coordinates, with
the payload under `to`. Writing it the other way round (`--until '{stage: merged}'`) matches
nothing, and a watch matching nothing looks exactly like a watch waiting, so it is refused
where it is typed.

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

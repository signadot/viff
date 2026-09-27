# Reference

The flat version of everything the [tutorial](../tutorial.md) walks, plus the detail it
deliberately skips. For the model of a rule, see
[the trigger model](../trigger-model.md). For the map of everything, see
[the docs index](../README.md).

| | |
|---|---|
| [up.md](up.md) | starting a verse: the store, the driver, the agent root, runners and brokers, the config layers, and the `o` binary |
| [condition.md](condition.md) | the condition of a rule: `subject`, `on` and `when`, and what a match can say |
| [action.md](action.md) | the action of a rule: `patch`, `derive`, `spawn`, `calls` and `seq`, what an action reads, the answer contract, hooks, and ids |
| [gates.md](gates.md) | the gates of a rule: what a gate asks, who answers it, and how it settles |
| [charter.md](charter.md) | the charter: packages, `reads:`, `verse charter`, and how a charter is stored |
| [schedules.md](schedules.md) | schedules: time as a cause |
| [entities.md](entities.md) | refs, payloads, merging writes, conditional writes, selection, and watching |
| [tasks.md](tasks.md) | the task surface end to end: drivers and vectors, gates, attaching, the mirror, and executing one by hand |
| [connect.md](connect.md) | federating a working tree, and scopes |
| [resolvers.md](resolvers.md) | the resolver surface: the resolver, its two entities, the routes an answering service lives on, and the verbs |
| [runners.md](runners.md) | the runner surface: where work runs — the entity, the verbs, the routes, and what a task records |
| [perms.md](perms.md) | permissions in detail: the grammar, a record's fields, what is checked on a write and a read, installed things, questions, the seed, `verse perms` — the model itself is [../perms.md](../perms.md) |
| [io.md](io.md) | the io surface: a connector that reflects an external system in and acts out — the entity, ownership globs, the verbs, the routes, the out half's place and status, and the contract an io serves |
| [iod.md](iod.md) | the five sources verse ships: git (and mirrors), git-issue, Slack, Linear, GitHub — the flags, the credentials, the delivery endpoints, and what each reflects |
| [examples/](examples/README.md) | working programs to copy and change: a runner, a coding runner, a worker, and an io |
| [store.md](store.md) | what the store guarantees, and the entity grain the engine reacts at |
| [representation.md](representation.md) | tony, JSON and YAML: what verse reads, what it writes, and why that is asymmetric |

## Commands

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
  status                                     engine/store snapshot. `restoring:` names the
                                             phases of a start still being asked again — each
                                             read the store, the store did not answer, and what
                                             it restores is not running yet; `arming:` under a
                                             charter names its rules whose watch is still being
                                             opened. Both absent on a verse that is up
  schema [<name>]                            the whole schema: the root — what a verse looks like,
                                             by top-level path — then every shape it names.
                                             A name prints just that one definition
  down                                       stop the verse on -addr (it reaps its spawned daemons)

  watch  [-since <rev>] [<path>]             stream store changes (cursored) — the same path
                                             a read takes, naming the same entities, and
                                             REFUSED the same way: a path nothing stands at
                                             is a non-zero exit rather than a stream that
                                             never carries anything
         -wait-if-absent                     …watch a path that holds nothing YET, and report
                                             when a value arrives. The case a watch exists
                                             for — seeing what a task is about to write — and
                                             asked for rather than assumed, because the
                                             address came from a person and a typo held open
                                             for ever looks exactly like a quiet verse
         -init                               …open with what already stands at the path, a
                                             `set` per node at the revision the watch opened
                                             at, then the changes. The default, so `-until`
                                             over a state already reached ends at once;
                                             `-no-init` streams from now. Not with -since/-ago,
                                             which replay history instead, nor at a path with
                                             no literal prefix (`verse watch`, `'*' …`), which
                                             joins the verse-wide stream and has no opening state
         -ago <n>                            …the last <n> COMMITS, resolved by the store
                                             against its watermark and clamped to the history
                                             it retains — so a deep one answers with what is
                                             left rather than refusing. A narrowed watch sees
                                             only the changes under its path, so the span holds
                                             however many events it holds. Absent streams from
                                             now; `-since 0` is the whole retained log.
                                             The relative cursor is its own flag because
                                             `-since -<n>` reads as a flag named `-<n>`
         [-if <pat>] [-if-at <field.path>] … only those whose payload matches (see below)
         -until <pat>                        …and STOP at the first FRAME matching <pat>, which
                                             is printed before the stream ends. A frame is
                                             `{at op to rev gap}` — the document `-trim` names
                                             — so a payload match is `{to: {stage: merged}}`,
                                             and a bare payload pattern is refused rather than
                                             left to wait for ever. Exit 0 whether or not it
                                             matched: what happened is the output, not the
                                             status
         -nowire                             print each frame as a BLOCK, opened by `---`,
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

  EVERY LISTING FILTERS, with the same two flags: `-if <match>` keeps the items that match,
  `-trim <match>` writes only the parts its document names. They compose, and an empty result
  is an answer rather than an error (exit 0).

    verse ps -if '{status: running}' -trim '{id status}'
    verse gate -if '{gate: provider}'
    verse data demo pr '*' -trim '{stage}'         # …per pr: the `*` is what makes each one an item
    verse watch demo -trim '{at to}'

  A `-trim` document NAMES keys, so write it as a KEY SET: in brackets, a key with no `:` and
  no value is that key with a null, which is exactly what a trim wants. `{id status}` and
  `{id: null, status: null}` are the same document, and the first is the one to type.

  In `-if` the same spelling is a PRESENCE test, because a null there matches any value the
  key holds: `-if '{status}'` keeps every entity that has a `status` at all, whatever it says,
  and `-if '{stage}'` is how you ask which ones have got started. What it is NOT is equality —
  `-if '{status open}'` is two presence tests, `status` and `open`, and selects the entities
  carrying both keys rather than the ones whose status is open. A key nothing has selects
  nothing, so the answer to that one is empty rather than wrong-and-plausible.

  On `data` and `watch`, `-if` is the server's selector — it says which entities the read is
  about, before any of them cross the wire — so those two take `-trim` alone. A write's
  precondition is `-expect`, a different flag for a different question: what a read selects on
  and what a write insists on are not the same question, so they are not the same flag.

  Comments need no flag: verse reads and writes them everywhere (`o list` needs `-c`).

  `data` is not one of them: it answers ONE DOCUMENT at a path rather than a list of things,
  so there is no plural to alias — and WHAT TO DO IS A FLAG, so its positional is a path in
  every case. A path's first segment is a name like any other, and a verb there would shadow one.

  data (d) [<step>…]                         the verse at an address, given as STEPS — one
                                             place per word, its first character saying which:
                                             demo pr 1 · demo pr 1 labels urgent  (a name)
                                             demo pr 1 votes '[0]'   (array; also {0} (k))
                                             demo pr '*'             (any one segment)
                                             demo pr 1 '"[0]"'       (the FIELD named [0])
                                             demo pr '"*"'           (the name that is `*`)
                                             (none) = the whole verse.
                                             A path NOTHING STANDS AT is a non-zero exit,
                                             at every depth: `{}` is a document somebody
                                             wrote, not what a read says about a path
                                             nobody wrote. An empty `{}` with exit 0 means
                                             the path stands and a -if kept none of it
          [-at <path>]                       …or whole, as a kinded path — which is how one is
                                             copied out of a document:
                                             demo.pr.1 · 'demo.pr.*.stage'
                                             An address is given ONCE, in one of the two
          [-if <pat>] [-if-at <path>]        … only entities whose payload matches (see below)
          [-paths] [-depth <n>]              … one address per line, as STEPS, instead of the
                                             document: EVERY address under the one asked for,
                                             since a document cannot say where its entities end.
                                             The line hands straight back —
                                             `data -paths demo pr | xargs -L1 verse data`.
                                             -depth bounds the walk to addresses of at most
                                             <n> segments (default: no bound)
                                             flags go on either side of the path
          [-kpaths]                          … the same listing as one KINDED PATH per line,
                                             which is the spelling that survives a shell
                                             variable as one argument:
                                             `r=$(data -kpaths … | head -1); data -at "$r"`
          -put <payload>                     write it there: an object field, or an array element
                                             addressed as a step. A put MERGES — fields it does
                                             not name are left alone, and -rm is how one goes away.
                                             It descends only through an object: naming a field
                                             under an array or a scalar is REFUSED rather than
                                             replacing it. entities.md#what-a-write-descends-through
          -rm (-r)                           remove what stands there: an entity, or one field.
                                             `-r` is `verse c -r`'s letter: removal is the same
                                             act. `-put` has none — `-p` beside `-paths` is a
                                             slip that writes
          -put [-create|-update]             only if nothing is there yet · only if something is
          -put [-expect <pattern>]           only if what is there matches. `-expect` rather
                                             than `-if`, which is the read's filter: a write's
                                             precondition is a different question
          An address is one kinded path in two spellings — `demo.pr.1.labels` as one
          token, `demo pr 1 labels` as separate words, where a word may hold a dot — and a
          colon is an ordinary character in a name. entities.md#addressing is the whole of
          it, including which commands take which spelling.
  gate (gates)                               the docket: every question waiting on somebody —
                                             a gate's, one of a task's actions, a provider
                                             selection's — with its options and ballots
          <id>                               …just that one record, as it stands: options,
                                             ballots, deadline, outcome. ONE TOKEN, its id
          <id> -answer <option> -as <who> [-say <text>] [-override]
                                             answer it by option. -override answers over an
                                             exclusive resolver, and the record names you both.
                                             A yes-or-no question's
                                             options are proceed and deny; a selection's are
                                             its doers. A question that takes more than one
                                             voice records the ballot and says where the
                                             tally stands (`1 of 2 for proceed`)
          approve <id> [-say <text>] · deny <id> [-say <text>]
          approve: y, yes · deny: n, no · -say: -s — the two default options, as verbs
          escalate <id> [-as <who>] [-say <why>]     hand it on to the next resolver matching
                                             its subject, or to the docket
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
          [-depth <n>]                       how far under it to go: 0 is that address
                                             alone; -1 (the default) is everything
          [-scope <name>]                    …a federation's own charter instead
          [-paths] [-kpaths]                 one line per rule and level instead of the document.
                                             -paths answers in STEPS — the spelling this
                                             command takes, so `c -paths | xargs -L1 verse c`
                                             round-trips — and -kpaths in one kinded path per
                                             line, for a line captured into a shell variable. A
                                             package that holds nothing yet prints itself,
                                             since that is the state worth seeing
          <address> rules|strata|schedules [<name>]
                                             …and INTO the node standing there: its rules, its
                                             levels, one of them by name, and on into that one
                                             (`c verse-dev drift rules watcher action`).
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
          -a -scope <name>                   …into a federation's own charter instead
          -r <path>                          REMOVE what the path names: a package at an
                                             address, or one rule or level in it
                                             (`c -r verse-dev.drift rules watcher`)
          -R <address>                       …the charter there AND every charter under it.
                                             What a removal leaves is what the charter WROTE —
                                             a charter's writes are the world's
                                             (-R is not `rm -r`'s recursion: -r is the removal)
          <address> -- rules|strata|schedules
                                             where the address ENDS, for a package whose own
                                             address ends in `rules` or `strata` and would
                                             otherwise shadow that key
          A charter file may open a package with a `---` document of its own — `at: drift`
          is where its rules write, `reads: [git, demo.pr]` what they may read besides, each
          a kinded path naming a place. A package reads inside its
          own address and what it declares it reads and NOWHERE else; an undeclared read does not install,
          and the graph over packages must be acyclic (a cycle is refused, showing the loop).
          Several `at:` documents in one file install several packages.
  task (tasks, ps) [-runner <ref>]           every task, whatever runs it
          run <prompt…> [-attach] [-wait] [-driver scripted|calls|llm]     (honors the runner; mirrored)
          run [-runner <ref>]                …on an installed runner, not the verse default
          run [-name <label>] [-subject <ref>] [-as <who>]      what the run is called, what it
                                             is about, and who it is
          run [-vector auto|ask|park] [-gates <file>] [-resolver <ref>]   how far it runs by
                                             itself, which actions ask, and who answers
          run [-answer-contract <file>]      the shape its final message must have
          run [-environment <name>] [-virtual-key <address>]    the workspace the runner builds,
                                             and the model credential the run spends
          get <id> · attach <id> · inbox <id>            operator vantage (any backend):
          cancel <id> · say <id> <text…>
          vector <id> auto|ask|park [-as <who>] · vector <id> -file <f>
          approve|deny <id> <decision> [-say <text>]     (also y|yes, n|no)
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
  group (groups)                             named sets of grantees, granted as group:<name>
          add <name> <member>… · show <name> · remove <name>
  perms [-if <pattern>] [-trim <match>]      the records of every node you may read: who
                                             owns what you can see, and what it holds out
  perms <step>… [-child-grants '{…}'] [-grants '[…]'] [-grant '{…}' [-below]] [-revoke '{…}']
                                             one node's record, as its owner: read with no
                                             flags; take the place and set what it and what
                                             is created beneath it hold out
  perms -root [the same flags]               the ROOT's record, whose address is the empty path
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
  flow (flows)                               live tasks and suspensions, narrowed by the flags
          [-task <id>]                       that run only; combines with neither of the others
          [-charter <address>[:<rule>]]      a charter, and one rule of it: verse-dev.drift:claim,
                                             :review (baseline's), '*' (every charter)
          [-r | -recursive]                  …and every charter installed beneath the address
          [-subject <address>]               a selector, * open in any segment: demo.pr.*,
                                             demo.*.1, '*' (every subject) — quote it
          [-args]                            each row as the flags naming it, for xargs -L1
          suspend -task <id> | [-charter …] [-subject …] -why <text> [-as <who>]
                                             hold that run at its next boundary, or suspend the
                                             work about a charter and a subject until resumed
          resume  -task <id> | [-charter …] [-subject …] [-as <who>]
                                             lift the hold or the suspension; -task is refused
                                             while a suspension holds the run
          say     -task | -charter | -subject <text…>      an instruction to what they name
          cancel  -task <id>                 end that run (destructive)
          show <addr> [-active | -no-active]
                                             a subject's flow as a tree, and what suspends it —
                                             a held subject with no work too; -active only
                                             those with a live task, -no-active only the rest
  schedule (schedules)                       the schedules the verse ticks on: period, next, the
                                             tick standing (hourly, daily, weekly come with it)
          tick <name> -due <instant> [-at <address>]
                                             write the tick for a past instant, as the schedule
                                             would have; a rule about it fires as it would have
          A verb that names nothing prints its usage and does nothing; of -charter and
          -subject, the one left out beside the other is '*'. See ../control.md
  provider (providers)                       the provider registry (provider.<capability>.<id>)

  connect [path] [-prompt <t>] [-driver <d>]    federate a tree (own scope): scoped metadata +
          [-interval <dur>]                   content controller + a validator per file
  disconnect                                  drop the -scope overlay (else remove local.* entities)

  global:  -addr <host:port>    -scope <name>
           -ofmt, -o <tony|yaml>   output format (default tony; verse renders no json)
           -ifmt, -i <tony|json|yaml>   input format for stdin / extensionless files
           -color auto|always|never    colorize (auto = a terminal only)
```

Flags may appear before or after positionals (`gate approve <id> -as you@example.com -say ok`
works). A `<who>` is a principal in the [grammar](../perms.md#principals-and-grantees):
`user:<email>` or the bare email, `agent:<name>`, `runner:<identity>`.

## Selecting by what an entity says

The path narrows by where an entity lives. `-if` narrows by what it says: a tony match
against the payload, which is the same predicate a charter carries in `value` — in its
`subject`, its `on` or its `when`. So `verse data demo pr '*' -if X` names exactly what a rule
with the subject `{path: "demo.pr.*", value: X}` would select.

```sh
verse data demo pr '*' -if '{stage: !or [open, merged]}'   # an operator, not just equality
verse data demo pr '*' -if '{urgent: true}' -if-at labels  # rooted inside the payload
verse watch -if '{stage: open}' demo pr '*'     # the same selector, as it happens
verse watch -until '{to: {stage: merged}}' demo pr '*'  # …until one of them merges, then stop
```

`-until` is the exception to that symmetry, and the shape of its pattern says so. `-if` is
answered at the SERVER, over entities, before there is a frame — so it takes the payload
pattern bare. `-until` is answered at the client, over the frames that arrive, because only
the client knows when it has seen enough — so it is written in the frame's coordinates, with
the payload under `to`. Writing it the other way round (`-until '{stage: merged}'`) matches
nothing, and a watch matching nothing looks exactly like a watch waiting, so it is refused
where it is typed.

`-if-at` takes a dot-separated field path and roots `-if` there, so a predicate about a
subtree need not restate the shape above it — `-if-at labels -if '{urgent: true}'` is
`-if '{labels: {urgent: true}}'`. It also reaches a subtree that is not an object, where the
predicate is a bare scalar match. A path that leads nowhere — an absent field, or a scalar in
the way — does not match, and neither does a delete on `watch`, which has no payload left to
ask about. The pattern is read in `-ifmt` and sent as tony, so operators and tags survive.

A precondition on a write is a different flag: `-create`, `-update` or `-expect` on
`verse data -put`. See [entities.md](entities.md#conditional-writes).

## The scripts

Four scripts write into a verse you are already running, and each one prints what to type
next. They are for reading as much as for running: each is a worked example of the surface it
seeds.

| | |
|---|---|
| `./scripts/seed-demo.sh` | the whole demo at once: a charter, the entities it reacts to, one reaction that has already happened, and one gate waiting for you |
| `./scripts/seed-entities.sh` | a dozen entities across three systems, and nothing that reacts to them ([tutorial §2](../tutorial.md#2-entities)) |
| `./scripts/charter-shapes.sh` | the coarse shapes a rule comes in, one command each — and `suspend`/`resume` one to watch it hold and act on the world as it stands ([tutorial §4](../tutorial.md#4-charters--relating-entities-to-tasks)) |
| `./scripts/lineage.sh` | a commit becomes a PR becomes a release: how ids from three systems correlate without a fourth being minted, and the two edges of doing it that way |

All four take `--scope <name>` and undo themselves (`--clean`, or `clean`). `./smoke.sh` is
a different thing: it builds its own binaries and runs its own isolated server, and touches
nothing of yours.

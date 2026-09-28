# Reference

This section is the flat version of what the [tutorial](../tutorial.md) walks through. It also
holds the detail that the tutorial leaves out on purpose. For the model of a
[rule](../glossary.md#rule), see [the trigger model](../trigger-model.md). For the map of all
the docs, see [the docs index](../README.md). Running a verse, runners, ios and resolvers are
in [the platform docs](../platform.md), in the repository.

| | |
|---|---|
| [condition.md](condition.md) | the condition of a rule: `subject`, `on` and `when`, and what a match can say |
| [action.md](action.md) | the action of a rule: `patch`, `derive`, `spawn`, `calls` and `seq`, what an action reads, the answer contract, hooks, and ids |
| [gates.md](gates.md) | the gates of a rule: what a gate asks, who answers it, and how it settles |
| [charter.md](charter.md) | the charter: packages, `reads:`, `verse charter`, and how a charter is stored |
| [schedules.md](schedules.md) | schedules: time as a cause |
| [entities.md](entities.md) | addresses, payloads, merging writes, conditional writes, selection, and watching |
| [tasks.md](tasks.md) | the task surface: drivers and vectors, gates, attaching, the mirror, and steering |
| [connect.md](connect.md) | federating a working tree, and scopes |
| [perms.md](perms.md) | permissions in detail: the grammar, the fields of a record, what verse checks on a write and a read, installed things, questions, the seed, and `verse perms`. The model itself is in [../perms.md](../perms.md). |
| [representation.md](representation.md) | tony, JSON and YAML: what verse reads, what it writes, and why the two differ |

## Commands

```
verse
  status                                     engine/store snapshot. `restoring:` names the
                                             phases of a start still being asked again — each
                                             read the store, the store did not answer, and what
                                             it restores is not running yet; `arming:` under a
                                             charter names its rules whose watch is still being
                                             opened. Both absent on a verse that is up
  schema [<name>]                            the whole schema: the root — what a verse looks like,
                                             by top-level path — then every shape it names.
                                             A name prints just that one definition

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
         -init                               …open with what already stands at the path, then
                                             the changes. The default. `-no-init` streams
                                             from now. entities.md#watching
         -ago <n>                            …the last <n> commits that the store retains.
                                             entities.md#watching
         [-if <pat>] [-if-at <field.path>] … only those whose payload matches (see below)
         -until <pat>                        …and stop at the first frame matching <pat>, which
                                             is printed before the stream ends. The pattern is
                                             over the frame (see below). Exit 0 whether or not
                                             it matched
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
  about, before any of them cross the wire — so those two take `-trim` alone.

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
                                             A path nothing stands at is a non-zero exit.
                                             An empty `{}` with exit 0 means the path stands
                                             and a -if kept none of it
          [-at <path>]                       …or whole, as a kinded path:
                                             demo.pr.1 · 'demo.pr.*.stage'
          [-if <pat>] [-if-at <path>]        … only entities whose payload matches (see below)
          [-paths] [-depth <n>]              … every address under the one asked for, one per
                                             line, as steps. -depth bounds the walk to
                                             addresses of at most <n> segments (default: no
                                             bound)
          [-kpaths]                          … the same listing as one kinded path per line.
                                             entities.md#addressing
          -put <payload>                     write it there. A put merges.
                                             entities.md#writing
          -rm (-r)                           remove what stands there: an entity, or one field.
                                             `-r` is `verse c -r`'s letter: removal is the same
                                             act. `-put` has none — `-p` beside `-paths` is a
                                             slip that writes
          -put [-create|-update]             only if nothing is there yet · only if something is
          -put [-expect <pattern>]           only if what is there matches
          -put -grants <list>                create it holding out this list, as
                                             `perms -grants` takes it ([] for nothing), in
                                             place of child-grants. Refused where it stands.
                                             entities.md#creating-with-grants
          An address is one kinded path in two spellings. entities.md#addressing
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
  charter (charters, c) [<path>]             the installed charter at a path, and at every
                                             address under it. Bare, that is all of it.
                                             charter.md
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
                                             as one step. Re-applying an edited file is how a
                                             charter changes. charter.md
          -a -scope <name>                   …into a federation's own charter instead
          -r <path>                          REMOVE what the path names: a package at an
                                             address, or one rule or level in it
                                             (`c -r verse-dev.drift rules watcher`)
          -R <address>                       …the charter there AND every charter under it
                                             (-R is not `rm -r`'s recursion: -r is the removal)
          <address> -- rules|strata|schedules
                                             where the address ENDS, for a package whose own
                                             address ends in `rules` or `strata` and would
                                             otherwise shadow that key
          A charter file may open a package with a `---` document of its own: `at: drift`
          is where its rules write, `reads: [git, demo.pr]` what they may read besides.
          Several `at:` documents in one file install several packages.
          charter.md#a-charter-can-be-installed-at-an-address
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
  group (groups)                             named sets of grantees, granted as group:<name>
          add <name> <member>… · show <name> · remove <name>
  perms [-if <pattern>] [-trim <match>]      the records of every node you may read: who
                                             owns what you can see, and what it holds out
  perms <step>… [-child-grants '{…}'] [-grants '[…]'] [-grant '{…}' [-below]] [-revoke '{…}']
                                             one node's record, as its owner: read with no
                                             flags; take the place and set what it and what
                                             is created beneath it hold out
  perms -root [the same flags]               the ROOT's record, whose address is the empty path
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
                                             tick standing (minutely, hourly, daily, weekly come with it)
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

Flags may come before or after positionals: `gate approve <id> -as you@example.com -say ok`
works. A `<who>` is a [principal](../glossary.md#principal) in the
[grammar](../perms.md#principals-and-grantees). It is one of these:

- `user:<email>`, or the bare email;
- `agent:<name>`;
- `runner:<identity>`.

The commands that start and stop a verse, and the ones that install runners, ios and
resolvers, are for somebody who runs verse. So are the `verse task do` commands that a worker
types. They are in [commands.md](../platform/commands.md), in the repository.

## Selecting by what an entity says

The [address](../glossary.md#address) narrows by where an entity lives. `-if` narrows by what
the entity says. `-if` is a tony match against the [payload](../glossary.md#payload). It is the
same [predicate](../glossary.md#predicate) that a charter carries in `value`, in its `subject`,
its `on` or its `when`. So `verse data demo pr '*' -if X` names exactly what a rule with the
subject `{path: "demo.pr.*", value: X}` would select.

```sh
verse data demo pr '*' -if '{stage: !or [open, merged]}'   # an operator, not just equality
verse data demo pr '*' -if '{urgent: true}' -if-at labels  # rooted inside the payload
verse watch -if '{stage: open}' demo pr '*'     # the same selector, as it happens
verse watch -until '{to: {stage: merged}}' demo pr '*'  # …until one of them merges, then stop
```

`-until` is the exception, and the shape of its pattern shows it.

| Flag | Where it is answered | How its pattern is written |
|---|---|---|
| `-if` | at the **server**, over entities, before there is a frame | the payload pattern, bare |
| `-until` | at the client, over the frames that arrive | in the fields of the frame, with the payload under `to` |

Only the client knows when it has seen enough, so the client answers `-until`. A pattern
written the other way round, such as `-until '{stage: merged}'`, matches nothing. A watch
that matches nothing looks exactly like a watch that waits. So verse refuses that pattern
where you type it.

`-if-at` takes a field path separated by dots, and roots `-if` there. So a predicate about a
subtree need not repeat the shape above it. `-if-at labels -if '{urgent: true}'` is
`-if '{labels: {urgent: true}}'`. `-if-at` also reaches a subtree that is not an object. There
the predicate is a bare scalar match.

These do not match:

- a path that leads nowhere, such as an absent field, or a scalar in the way;
- a delete on `watch`, which has no payload left to test.

Verse reads the pattern in `-ifmt` and sends it as tony, so operators and tags are kept.

A precondition on a write is a different flag: `-create`, `-update` or `-expect` on
`verse data -put`. See [entities.md](entities.md#conditional-writes).

## The scripts

Four scripts write into a verse that you already run. Each one prints what to type next. They
are for reading as much as for running: each is a worked example of the surface it seeds.

| | |
|---|---|
| `./scripts/seed-demo.sh` | the whole demo at once: a charter, the entities it reacts to, one reaction that has already happened, and one gate waiting for you |
| `./scripts/seed-entities.sh` | a dozen entities across three systems, and nothing that reacts to them ([tutorial §2](../tutorial.md#2-entities)) |
| `./scripts/charter-shapes.sh` | the coarse shapes that a rule comes in, one command each. It also runs `suspend`/`resume` on one rule, so you can watch the rule hold and then act on the world as it stands ([tutorial §4](../tutorial.md#4-charters--relating-entities-to-tasks)) |
| `./scripts/lineage.sh` | a commit becomes a PR, and the PR becomes a release. It shows how ids from three systems correlate without a fourth id, and the two limits of that design. |

All four take `--scope <name>`, and each one removes what it wrote (`--clean`, or `clean`).

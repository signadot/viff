# Commands and the rest

This page has every command, how to select by what the data says, and the scripts that seed a
viff. What a charter says is in [the charter reference](../trigger-model.md), and the
[tutorial](../tutorial/README.md) walks through all of it. Running a viff, runners, iods and
resolvers are in [the platform docs](../platform.md), in the repository. For the map of all
the docs, see [the docs index](../README.md).

| | |
|---|---|
| [tasks.md](tasks.md) | the task surface: the work, the agent, the environment, the autonomy, attaching, the mirror, and steering |
| [mcp.md](mcp.md) | `viff mcp`: viff to an agent's host, as you, the tools it serves and how they wait |
| [scopes.md](scopes.md) | scopes: a private copy of the store, what runs in it, and what it owns |
| [entities.md](entities.md) | addresses, payloads, merging writes, conditional writes, selection, and watching |
| [perms.md](perms.md) | permissions in detail: the grammar, the fields of a record, what viff checks on a write and a read, installed things, questions, the seed, and `viff perms`. The model itself is in [../perms.md](../perms.md). |
| [representation.md](representation.md) | tony, JSON and YAML: what viff reads, what it writes, and why the two differ |

## Commands

Every command is `viff <command>`. Each collection is one command, named in the singular:
bare, it lists; with a verb, it acts on one member. `viff question` lists the questions
waiting on you, and `viff question approve <id>` answers one. The plural is an alias of the
same command (`viff questions`, `viff tasks`). `viff question list` is an error: bare, it
already lists.

Flags may come before or after positionals: `question approve <id> -as you@example.com -say ok`
works. A `<who>` is a [principal](../glossary.md#principal) in the
[grammar](../perms.md#principals-and-grantees): `user:<email>` or the bare email,
`agent:<name>`, or `runner:<identity>`.

The commands that start and stop a viff, and that install runners, iods and resolvers, are
for somebody who runs viff. They are in [commands.md](../platform/commands.md), in the
repository.

### Every listing filters

Every listing takes the same two flags. They combine, and an empty result is an answer, not an
error (exit 0).

| Flag | What it does |
|---|---|
| `-if <match>` | keeps the items that match |
| `-trim <match>` | writes only the parts the document names |

```sh
viff ps -if '{status: running}' -trim '{id status}'
viff data demo pr '*' -trim '{stage}'         # per pr: the `*` makes each one an item
viff watch demo -trim '{at to}'
```

Write a `-trim` document as a **key set**: in braces, a key with no `:` and no value is that
key with a null. `{id status}` and `{id: null, status: null}` are the same document; type the
first.

In `-if`, the same spelling is a **presence** test, because a null matches any value.
`-if '{status}'` keeps every item that has a `status`, whatever it says. It is not
equality: `-if '{status open}'` tests for two keys, `status` and `open`. A key nothing has
selects nothing, so that mistake gives an empty answer, not a wrong one.

On `data` and `watch`, `-if` is the server's selector: it decides which addresses the read is
about before any data crosses the wire. So those two take `-trim` alone
([selecting by what the data says](#selecting-by-what-the-data-says)).

Comments need no flag: viff reads and writes them everywhere.

### status, schema

| Command | What it does |
|---|---|
| `status` | the engine and the store, now. `restoring:` names the parts of a start that are still waiting on the store; `arming:` under a charter names armed rules whose watch the store is still opening, with what it is doing for each — in line, opening it, opening it again after a drop, or refused and asking again — and `pending:` the rules held for something else — a stratum, a runner — with what each waits for. All are absent on a viff that is up. |
| `schema [<name>]` | the whole schema: the root, by top-level path, then every shape it names. A name prints that one definition. |

### data (d)

`data` answers **one document** at a path, not a list, so it has no plural. What to do is a
flag, so its positional is always a path. The path is given as **steps**, one place per word
([addressing](entities.md#addressing)):

| Steps | What they name |
|---|---|
| `demo pr 1` · `demo pr 1 labels urgent` | names |
| `demo pr 1 votes '[0]'` | an array index (also `{0}`) |
| `demo pr '*'` | any one segment |
| `demo pr 1 '"[0]"'` | the field named `[0]` |
| `demo pr '"*"'` | the name that is `*` |
| (none) | the whole viff |

A path nothing stands at is a non-zero exit. An empty `{}` with exit 0 means the path stands
and `-if` kept none of it.

| Form | What it does |
|---|---|
| `data <step>…` | the viff at that address |
| `data -at <path>` | the same, as one kinded path: `demo.pr.1`, `'demo.pr.*.stage'` |
| `data … -if <pat> [-if-at <path>]` | only the data whose payload matches |
| `data … -paths [-depth <n>]` | every address under the one asked for, one per line, as steps. `-depth` walks at most `<n>` levels beneath the address: `0` is the address alone (default: no limit). |
| `data … -kpaths` | the same listing, one kinded path per line |
| `data … -put <payload>` | write it there. A put merges ([writing](entities.md#writing)). |
| `data … -put -create` · `-put -update` | only if nothing is there yet · only if something is |
| `data … -put -expect <pattern>` | only if what is there matches |
| `data … -put -grants <list>` | create it holding out this list (as `perms -grants` takes it; `[]` for nothing) in place of child-grants. Refused where something stands ([creating with grants](entities.md#creating-with-grants)). |
| `data … -rm` (`-r`) | remove what stands there: the data at the address, or one field of it. `-put` has no short form, because `-p` next to `-paths` is a slip that writes. |

### watch

| Form | What it does |
|---|---|
| `watch [<path>]` | stream changes to the store, with a cursor. It names the same addresses as a read, and is refused the same way: a path nothing stands at is a non-zero exit, not a stream that never carries anything. |
| `-since <rev>` | start from a revision |
| `-wait-if-absent` | watch a path that holds nothing yet, and report when a value arrives. You ask for it, because a mistyped address held open looks exactly like a quiet viff. |
| `-init` · `-no-init` | open with what already stands at the path, then the changes (the default) · stream from now ([watching](entities.md#watching)) |
| `-ago <n>` | the last `<n>` commits the store keeps ([watching](entities.md#watching)) |
| `-if <pat> [-if-at <field.path>]` | only changes whose payload matches |
| `-until <pat>` | stop at the first frame matching `<pat>`, after printing it. The pattern is over the frame. Exit 0 whether or not it matched. |
| `-nowire` | print each frame as a block opened by `---`, not on one line. The default, one frame per line, is for `grep`; this is for a person reading. A frame whose payload carries comments takes several lines either way ([watching](entities.md#watching)). The output is a tony document stream either way. |

### question (questions, qs, gate, gates)

| Form | What it does |
|---|---|
| `question` | the questions waiting on you: each ask whose `waitingOn` names you and that you did not raise, with its options and ballots. One a resolver serves for you is left out. |
| `question -served` | those too, each marked `servedBy` |
| `question -all` | the docket: every pending ask you can read, whoever raised it and whoever it waits on |
| `question <id>` | that one record, as it stands: options, ballots, deadline, outcome |
| `question <id> -answer <option> -as <who> [-say <text>] [-force]` | answer by option. A yes-or-no question's options are `proceed` and `deny`; a selection's are its doers. A question that needs more than one voice records the ballot and says where the tally stands (`1 of 2 for proceed`). `-force` answers one a resolver serves for you. |
| `question approve <id> [-say <text>]` · `deny <id> [-say <text>]` | the two default options, as verbs. Also `y`, `yes` · `n`, `no`; `-s` for `-say`. |
| `question -watch [-since <rev>] [-notify]` | a line when a question starts waiting on you, and when it settles. Runs until stopped. `-since` replays from a revision, as far back as the store keeps its log. `-notify` also says it on the desktop (D-Bus on Linux and the BSDs, Notification Center on macOS): urgent when a question starts waiting, closed when it settles, one summary for what stood when the watch opened ([asking](../asking.md)). |
| `tui [--at <address>] [--every 2s] [--all] [--config] [--commands]` | a terminal screen of the questions waiting on you, the tasks, the data an address at a time, the charters installed and the runners, answered and cancelled in place: the explorer on the left, a section's table or one row on the right, `:` for a command by name, `?` for every key; the config's `tui:` section sets its sections, bookmarks and keys ([asking](../asking.md), [example](examples/tui/viff.tony)). |

### ask (asks)

Your own questions to somebody, with a date ([asking](../asking.md)).

| Form | What it does |
|---|---|
| `ask` | your asks: each with its id, question, who is asked, its date, its `status`, and its answer, `pending` until it settles. One that settled with no answer (`orphaned`) says `why`. |
| `ask <name\|id>` | that one, and whom it waits on. A name used for two asks is refused with both ids. Whoever it asks answers with `question <id> -answer`. |
| `ask [<name>] -question (-q) <text> [-to <who>,…] [-due <when> [-on-silence proceed\|deny]] [-on <rows>] [-about <path>] [-label <name>=<value>,…] [-keep <duration>]` | ask it, under an id derived from the name, or `run-<nonce>` without one. `-to` uses the grammar of a grant. `-due` is RFC 3339 or a duration from now (`48h`). `-on-silence` is the option it settles on when nobody answers by then: `deny` (the default) or `proceed`. `-label` is what the ask says about itself for whoever watches asks (`team=payments,kind=deploy`). `-on` is what follows the answer: rows read in order, each `{match, then}`, the first whose match meets the answer running its `then`, which is one key, `{spawn: {...}}` (a spawn as a charter writes one), `{answer: {ask, choice}}` or `{ask: {...}}`, or `null` for nothing; `-` / `@<file>` reads it ([asking](../asking.md#what-follows-the-answer)). `-keep` is how long it stands once settled (default a week). |
| `ask <name\|id> -due <when>` | move its date while it waits; asks nothing again |
| `ask <name\|id> -on <rows>` · `-to <who>,…` | change what it asks while it waits. It is asked again, and answers to the earlier version do not count. |
| `ask <name\|id> -withdraw` | take it back: off the docket, and removed |
| `ask -watch [-since <rev>] [-notify]` | a line when an ask of yours is made, made again, settled or withdrawn. What waits on you is `question -watch`. `-notify` also says on the desktop when an ask of yours is answered ([asking](../asking.md)). |

### charter (charters, c)

A charter file is one document. `at:` is where its rules write, `reads:` what they may read
besides, `sends:` the outboxes an iod reads, `ttl:` how long it stands, and `strata:`,
`schedules:` and `rules:` what it holds. A bare rule, or a list of
them, installs at the path. A directory of charter files is several packages
([the charter](charter.md#a-charter-can-be-installed-at-an-address)).

| Form | What it does |
|---|---|
| `charter [<path>]` | the charter at a path, printed as the file that installs it (`at:` and all). stderr names the charters under it, marks rules that are held, suspended, in trouble or installed by a task, and says when a ttl ends. Bare: baseline's. |
| `charter [<path>] -d <dir>` | every charter at or under the path, one file each: `<dir>/<address as directories>/charter.tony` |
| `-depth <n>` | how far under it to go: `0` is that address alone, `-1` (the default) everything |
| `-scope <name>` | a scope's own charter instead |
| `-paths` · `-kpaths` | one line per rule and level, instead of the document. `-paths` answers in steps, so `c -paths \| xargs -L1 viff c` round-trips; `-kpaths` in one kinded path per line. A package that holds nothing yet prints itself. |
| `charter <address> rules\|strata\|schedules [<name>]` | into the node there: its rules, its levels, one by name, and on into that one (`c repos.viff.drift rules claim action`). `c rules claim` is baseline's. A name that is elsewhere is named in the refusal, with the path that reaches it. |
| `charter -a [<address>] -f <file>\|-` | **install** one charter from one file, as one step. Applying an edited file again is how a charter changes. |
| `charter -a -d <dir>` | every charter file under the directory, each at its own `at:`, all or none |
| `charter -a <address> -d <dir> -prune` | the same, and **remove** what the directory leaves out at and under the address: a package with no file is uninstalled first, as `-R` does, then `-a -d` |
| `charter -check <address> -d <dir>` · `-check <address> -f <file>` | whether the charters would install as `-a <address> -d <dir> -prune` installs them, and what would change, installing nothing. `-f` is a directory holding that one file. A refusal is the answer, so the command succeeds either way ([the charter](charter.md)). |
| `charter -a -scope <name>` | into a scope's own charter |
| `charter -r <path>` | **remove** what the path names: a package, or one rule or level in it (`c -r repos.viff.drift rules claim`) |
| `charter -R <address>` | the charter there and every charter under it. (`-R` is not `rm -r`: `-r` is the removal.) |
| `charter <address> -- rules\|strata\|schedules` | where the address ends, for a package whose own address ends in `rules` or `strata` |

### task (tasks, ps)

| Form | What it does |
|---|---|
| `task [-runner <ref>]` | every task, whatever runs it |
| `task run -f <spawn> [-name <label>] [-wait]` | run one: a spawn as a charter writes one, from a file or `-` for stdin. Viff routes it to a runner that offers its doer, and it is mirrored. |
| `task run … -attach [-no-converse]` | a conversation: the turn is yours whenever the model goes quiet. Ctrl-D ends it. |
| `task run … -name <label> -as <who>` | what the run is called, and who it is. What it is about, what it may reach and do, its answer and its credential are keys of the spawn (`subject`, `environment`, `autonomy`, `answer`, `virtualKey`); where it runs is viff's to say |
| `task get <id>` · `task attach <id>` | the operator's view, for any backend. `attach` prompts for each gate, and for a question asked in words, with an identity. |
| `task cancel <id>` · `task say <id> <text…>` | end it · tell it something |
| `task autonomy <id> <rows> [-as <who>]` · `task autonomy <id> -file <f>` | replace its autonomy with a whole one, each row reaching what its environment gives |
| `task approve\|deny <id> <decision> [-say <text>]` | answer its gate (also `y`, `yes`, `n`, `no`) |

[Tasks](tasks.md) has the whole task surface.

### group (groups)

| Form | What it does |
|---|---|
| `group add <name> <member>…` · `show <name>` · `remove <name>` | named sets of grantees, granted as `group:<name>` |

### perms

| Form | What it does |
|---|---|
| `perms [-if <pattern>] [-trim <match>]` | the records of every node you may read: who owns what you can see, and what it holds out |
| `perms <step>… [-child-grants '{…}'] [-grants '[…]'] [-grant '{…}' [-below]] [-revoke '{…}']` | one node's record. With no flags, read it. As its owner, take the place and set what it, and what is created under it, holds out. |
| `perms -root [the same flags]` | the root's record, whose address is the empty path |

### flow (flows)

Live tasks and suspensions, narrowed by flags ([control](../control.md)). Of `-charter` and
`-subject`, the one left out is `'*'`. A verb that names nothing prints its usage and does
nothing.

| Form | What it does |
|---|---|
| `flow -task <id>` | that run only; combines with neither of the others |
| `flow -charter <address>[:<rule>]` | a charter, and one rule of it: `repos.viff.drift:claim`, `:review` (baseline's), `'*'` (every charter) |
| `flow … -r` (`-recursive`) | and every charter installed beneath the address |
| `flow -subject <address>` | a selector, `*` open in any segment: `demo.pr.*`, `demo.*.1`, `'*'`. Quote it. |
| `flow … -args` | each row as the flags that name it, for `xargs -L1` |
| `flow suspend -task <id> \| [-charter …] [-subject …] -why <text> [-as <who>]` | hold that run at its next boundary, or suspend the work about a charter and a subject until resumed |
| `flow resume -task <id> \| [-charter …] [-subject …] [-as <who>]` | lift the hold or the suspension. `-task` is refused while a suspension holds the run. |
| `flow say -task \| -charter \| -subject <text…>` | an instruction to what they name |
| `flow cancel -task <id>` | end that run (destructive) |
| `flow show <addr> [-active \| -no-active]` | a subject's flow as a tree, and what suspends it, a held subject with no work included. `-active` only those with a live task, `-no-active` only the rest. |

### schedule (schedules)

| Form | What it does |
|---|---|
| `schedule` | the schedules the viff ticks on: period, next, the tick standing. `minutely`, `hourly`, `daily` and `weekly` come with it. |
| `schedule tick <name> -due <instant> [-at <address>]` | write the tick for a past instant, as the schedule would have. A rule about it fires as it would have. |

### Global flags

| Flag | What it does |
|---|---|
| `-addr <host:port>` | the viff to talk to |
| `-scope <name>` | work in a copy-on-write scope |
| `-ofmt`, `-o <tony\|yaml>` | output format (default tony; viff renders no JSON) |
| `-ifmt`, `-i <tony\|json\|yaml>` | input format for stdin and files with no extension |
| `-color auto\|always\|never` | color (auto: only on a terminal) |

## Selecting by what the data says

The [address](../glossary.md#address) narrows by where the data lives. `-if` narrows by what
the data says. `-if` is a tony match against the [payload](../glossary.md#payload). It is the
same [predicate](../glossary.md#predicate) that a charter carries in `value`, in its `subject`,
its `on` or its `when`. So `viff data demo pr '*' -if X` names exactly what a rule with the
subject `{path: "demo.pr.*", value: X}` would select.

```sh
viff data demo pr '*' -if '{stage: !or [open, merged]}'   # an operator, not just equality
viff data demo pr '*' -if '{urgent: true}' -if-at labels  # rooted inside the payload
viff watch -if '{stage: open}' demo pr '*'     # the same selector, as it happens
viff watch -until '{to: {stage: merged}}' demo pr '*'  # …until one of them merges, then stop
```

`-until` is the exception, and the shape of its pattern shows it.

| Flag | Where it is answered | How its pattern is written |
|---|---|---|
| `-if` | at the **server**, over the data, before there is a frame | the payload pattern, bare |
| `-until` | at the client, over the frames that arrive | in the fields of the frame, with the payload under `to` |

Only the client knows when it has seen enough, so the client answers `-until`. A pattern
written the other way round, such as `-until '{stage: merged}'`, matches nothing. A watch
that matches nothing looks exactly like a watch that waits. So viff refuses that pattern
where you type it.

`-if-at` takes a field path separated by dots, and roots `-if` there. So a predicate about a
subtree need not repeat the shape above it. `-if-at labels -if '{urgent: true}'` is
`-if '{labels: {urgent: true}}'`. `-if-at` also reaches a subtree that is not an object. There
the predicate is a bare scalar match.

These do not match:

- a path that leads nowhere, such as an absent field, or a scalar in the way;
- a delete on `watch`, which has no payload left to test.

Viff reads the pattern in `-ifmt` and sends it as tony, so operators and tags are kept.

A precondition on a write is a different flag: `-create`, `-update` or `-expect` on
`viff data -put`. See [entities.md](entities.md#conditional-writes).

## The scripts

Four scripts write into a viff that you already run. Each one prints what to type next. They
are for reading as much as for running: each is a worked example of the surface it seeds.

| | |
|---|---|
| `./scripts/seed-demo.sh` | the whole demo at once: a charter, the data it reacts to, one reaction that has already happened, and one gate waiting for you |
| `./scripts/seed-entities.sh` | a dozen records under `viff-tutorial`, and nothing that reacts to them ([the data page of the tutorial](../tutorial/data.md)) |
| `./scripts/charter-shapes.sh` | the coarse shapes that a rule comes in, one command each. It also runs `suspend`/`resume` on one rule, so you can watch the rule hold and then act on the world as it stands ([charters, continued](../tutorial/charters-2.md#stopping-a-rule)) |
| `./scripts/lineage.sh` | a commit becomes a PR, and the PR becomes a release. It shows how ids from three systems correlate without a fourth id, and the two limits of that design. |

All four take `--scope <name>`, and each one removes what it wrote (`--clean`, or `clean`).

## Use viff

- [Commands](#commands): every command, and a file per topic.
- [Tasks](tasks.md): running a task by hand, and answering its gates.
- [Asking](../asking.md): `viff ask`: a question to somebody, with a date, and what follows a
  yes run on your authority.
- [Permissions, walked](../perms-guide.md): your own data, a task and its gates, a charter
  and what it owns, a gate with nobody behind it, a gate with a resolver.
- [Scopes](scopes.md): a private copy of the store for your own work and flows.

## Charters

[The charter reference](../trigger-model.md) is the model of a rule and the page for each of
its parts.

## Elsewhere

- [tony](https://signadot.github.io/tony-format/): the document language a charter is
  written in, and [docd](https://signadot.github.io/tony-format/docd/), the store behind a
  durable viff.
- [go-tony](https://pkg.go.dev/github.com/signadot/tony-format/go-tony) on pkg.go.dev —
  including [`ir/kpath`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath)
  (the addressing a task chain uses),
  [`mergeop`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop) (the patch
  and match operators), and
  [`system/libctl`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/system/libctl)
  (the controller `Handler`).

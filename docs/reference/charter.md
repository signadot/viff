# The charter

This page describes a [charter](../glossary.md#charter) as a whole:

- how a charter is installed at an address, as a package;
- what a package may read and write;
- what `verse charter` shows;
- how verse stores a [rule](../glossary.md#rule), so that a charter survives a restart;
- what an install does not check.

[The charter reference](../trigger-model.md) describes the rules a charter holds, and
[the tutorial's charters pages](../tutorial/charters.md) write one, step by step.

## A charter can be installed at an address

A charter installed at an [address](../glossary.md#address) is a
**[package](../glossary.md#package)**. A package runs in an engine of its own, and it may
write only inside that address.

The address is where the install points: the path after `-a`, or the file's `at:`. The install
takes the address:

- The address gets an owner record that names the installer.
- What the package derives beneath the address is owned through that record.
- Verse refuses an install at an address that somebody else owns ([perms.md](../perms.md)).

```sh
verse c -a notes -f notes-charter.tony    # its rules write under notes.*
verse c -a -f charter.tony                # baseline: may write anywhere
```

So two groups can keep rules in one store without agreeing on anything to avoid a collision.
The *engine* keeps a package inside its address, not the rules installed in it. It does this in
two ways:

- A `derive` that lands outside the address does not install.
- When a rule fires, verse refuses a write that lands outside the address.

The second way matters more, because the address of a derive can be a template that only the
firing fills in.
[Confinement belongs to the engine](../design/reference-charter.md#confinement-belongs-to-the-engine) gives the reason.

**A rule may select anywhere it only reads.** The subject is a write when the action patches
it, and a read otherwise. So a rule of a package can select what an [iod](../glossary.md#iod)
wrote and derive from it. Most rules of a package do this:

```tony
at: drift                   # where this package writes — its engine is confined to it
reads: [git]                # …and what it may read besides
strata:
- name: reflected           # the level the source sits at
  level: 0
  match: {path: "git"}
- name: ours
  level: 10
  match: {path: "drift"}
rules:
- name: head-moved          # in the `drift` package
  stratum: reflected
  condition: {subject: git.verse.head}    # a source: read, never written
  action:
    derive:
    - path: "drift.head"
      value: {repo: $subject.value.repo}
```

Two packages that both react to `git.*` are normal.

**Reads are declared**, in the `reads:` line of the example. A package reads inside its own
address and what it names in `reads:`, and nothing else. A rule that reads anything else does
not install, and the refusal says which line to add:

```
rule "note-the-head" reads git.verse.*, which notes.* does not declare: a charter installed
at an address reads inside it and what it declares, so that the graph over packages can be
checked for a cycle. Add it: `reads: [git.verse]` (this package reads nothing)
```

A package without `reads:` reads only inside itself. It does not read "everything". A package
that reads nothing outside cannot interfere with anybody, and every edge of the graph below is
written down.

**Sends are declared too.** An [iod](../glossary.md#iod) works in both directions: it writes
what a system holds into the store, and it carries writes back out to the system. It reads
those writes at its own out place. A package writes only inside its address, so it cannot write
to that out place directly. It writes to an [outbox](../glossary.md#outbox) instead: a place
inside the package that the package declares the iod reads.

```tony
at: verse-dev.announce
sends:
- from: out.slack                # inside this package's address
  to: slack.Signadot.out         # an iod's out place, as its registration names it
```

- `from` is relative to the package's address. `to` is an out place that a registered iod
  reads. One `from` sends to one iod.
- The install grants that iod the outbox. The iod may read what is asked there, and own each
  status it writes beside an input. It may do nothing else in the package.
- The package's rules derive inputs into the outbox like any other write inside the address.
  They read each `~status` beside an input, to see what became of it.
- A place is an outbox only because its package says so. A package that happens to have an
  `out.slack` place sends nothing.
- The grant stands while the package declares the outbox and an iod reads the place it sends to.
  Installing the package without the entry, removing the package, or removing the iod takes it
  back. Installing the iod again grants the outboxes still declared for it.

The install refuses a `to` that no iod reads, a `from` sent twice, and `sends:` on a charter
with no address. [The out half](io.md#the-out-half) says what an iod does with an outbox.

**A charter can stand for a while.** `ttl:` says how long, as a duration:

```tony
at: repos.verse.watch
ttl: 72h
rules:
- …
```

- The install says when the charter goes: its own time plus the ttl. At that time verse removes
  the package as `verse charter -r` removes it: its rules, levels, schedules and declaration, and
  nothing it wrote. A charter installed under its address is a package of its own, and stays.
- Like `reads:` and `sends:`, the ttl is the package's declaration, and each install of the file
  replaces it. Installing the file again starts the ttl over. Installing it without `ttl:` makes
  it stand until it is removed. Rules installed at the address on their own leave the ttl as it
  is.
- `verse charter` writes the ttl in the file, which installs back as it was written. It says
  when the charter goes in a note beside the file, as it says a rule is held.
- Anyone who installs a charter may give it a ttl, and nobody has to.

The install refuses a ttl that is not a duration, and `ttl:` on a charter with no address:
baseline is not removed whole.

**A charter a task installs launches within the task.** A task installs a charter as the person
it runs for ([the verse system](../systems/verse.md)), and the charter stands after the task
ends. Every spawn among its rules is held within the task:

- its environment no wider than the task's, less `verse.charter-install`, so nothing a task
  installs installs charters;
- its autonomy no looser than the task's when it installs;
- the task's credential, named as the task's spawn named it, where the spawn spends one: a
  script spends none, and may name none;
- any runner.

The hold is on spawns. A rule's `patch`, `derive` and `send` are the person's own writes, as any
charter's are, held by nothing the task was: a task given `verse.charter-install` installs rules
that write what the person may write, whether or not the task itself has `verse.data-put`. A
spawn's author who gives a task charter-install gives it that.

The install and `verse charter --check` refuse a spawn beyond the task, and an attribute still to
be bound, such as `repo: $subject.value.repo`, is within where some value of it could be. The rule
records the task, and what the task could hand on when it installed it. At each firing the spawn,
bound, is held to that, after the task has ended too and whatever becomes of the task's record: a
firing that binds a repository the task never reached fails, with the reason in `err`. A task in a
scope installs in baseline, as everything a task does on verse is done. `verse charter` says which
task installed a rule in a note beside the file. A rule the person installs over the task's is theirs, held by
nothing the task was.

**One file is one charter, and a directory holds several.** Each file says where its charter
goes with its own `at:`, so the layout of the directory is free.

For example, `.verse/charters/comment-drift/` holds two packages: `drift`, which reviews, and
`merge`, which gates what the review delivers. One `verse c -a -d` installs both, or neither.

Each package goes in **as one step**. Its declaration, its levels and its rules install
together or not at all. So a refusal anywhere puts back what stood before it
([strata](../strata.md#installing-and-the-order-it-does-not-depend-on)).

**Its levels are its own**, and so are its records. The rules and the strata of a package are
addressed by package (`verse.trigger.rule."repos.verse.drift:claim"`). So two packages may
each declare a `reflected` level, and neither overwrites the other. The two halves of
comment-drift need this: both read the git iod, and both want a bottom level for it.

The levels are not confined. The lowest level of a package owns the place that the package
*reads*. Its rules select from there and derive above it.

Because a package writes only inside its address, no derive crosses packages. So there is no
order across packages to define. Two packages may both declare a level 10, and the two levels
mean nothing to each other.

**The reads graph has no cycles.** The `reads:` lines make a graph: P → Q when what P reads
meets what Q may write, which is the address of Q. Verse refuses a cycle at install, and shows
the loop:

```
reads: this would close a cycle between packages: drift → merge → drift — one reads what
the next writes, all the way round, so a write on any of them can wake the others and come
back. A package graph is acyclic: break the loop, or fold the two halves into one package
```

Nothing else catches such a loop. Suppose `a` reads what `b` writes, and `b` reads what `a`
writes. That is a feedback loop, and neither author can see it in their own file. Levels are per
package, and the cycle throttle limits a chain only within one engine.

A declared read of an address where no package is installed adds no edge. `git` is where an
iod writes, and reading it is not depending on a package. So two packages that both read `git`,
as in the example, are not a loop.

**What the graph does not cover** is baseline. The
[baseline charter](../glossary.md#baseline-charter) may write anywhere. So a loop through it
is not a loop between packages, and the check cannot see it. Baseline is unconfined in what it
writes for the same reason.

`verse charter` reads the charter at an address, as `verse data` reads the store at one. What
it prints is **the file that installs that charter**: the same keys, and `at:` naming the
address. So what you read is what you can keep, edit and install again:

```sh
verse charter drift > drift.tony        # the `drift` package, as its file
verse charter -a -f drift.tony          # …installs it back where it came from
verse charter                           # baseline's, which has no `at:`
verse c drift rules merge-proposal      # one rule by name, which also installs as it stands
```

```tony
at: drift
reads: [git]
rules: [ … the rules, in full, comments and all … ]
strata: [ … ]
```

Keys come out in alphabetical order, whatever order the file you installed wrote them in.

`verse charter` prints the rules themselves, not their names. The charter is the program verse
runs, and a list of names would need another command per rule to show what it *does*.

**What a file cannot hold goes to stderr.** A rule that is held, suspended, or has failed at a
firing is marked there. Those are facts about the running rule, not part of the charter, and an
install would refuse them. The charters under the address are named there too:

```
note: drift: rule head-moved is held: it waits for stratum reflected, which is not installed
note: under here: drift, merge — `verse c -d <dir>` writes every charter to a file of its own
```

**A tree of charters is a directory**, one file per charter. `-d` writes every charter at or
under an address, and `-a -d` installs every charter file under a directory in one step:

```sh
verse charter demo -d out/              # out/demo/pr/charter.tony, …
verse charter -a -d out/                # each at its own `at:`, all of them or none
verse charter -a demo -d out/ --prune   # …and remove what out/ leaves out at and under demo
```

An install adds and replaces by name, so a rule missing from the directory stays installed.
`--prune` makes the directory the whole truth under an address:

- It is `verse c -R demo` followed by `verse c -a -d out/`, as one step.
- A rule, level or schedule missing from a file is removed, and so is a package with no file.
- Rules the directory keeps go on running. They are not stopped and started again.
- Every file must be at or under the address.
- If anything is refused, the removals are put back along with the installs.

**`--check` asks what an install would do, and does none of it.** It takes `-d` or `-f`, as
`-a <address> -d <dir> --prune` would install them, and answers a verdict, `installs` or `refused`
with the install's own words, and for each package the rules and levels it would add, change,
remove or hold, and the packages the prune would take away:

```sh
verse charter --check demo -d out/      # verdict: installs, packages: […], pruned: […]
verse charter --check demo -f pr.tony   # a directory holding this one file
```

A check with `-f` is the check of a directory that holds that one file, the prune included: the
answer names what `-a demo -d` with `--prune` would take from under `demo`, which `-a -f` never
takes. The file says its own `at:`, as a file in a directory does.

A refusal is the answer, not an error, so the command succeeds either way. What the check
cannot say is what the store answers on the day, which is the install's own.

`-d` lays the files out by address. That layout is a default, not a rule: the `at:` in each file
says where it goes. A directory laid out any other way, by project and with READMEs beside the
charters, installs the same. `--paths` lists every charter as one path per rule and level.

A package is removed whole: its rules, its levels, what it reads, and the engine that runs them.
What the package *wrote* stays. `verse data -rm` removes that.

```sh
verse c -r merge
verse c -R demo                          # …and every charter under that address
```

**Two things a package does not confine:**

| Thing | Why a package does not confine it |
|---|---|
| a spawn | A task acts in the world, outside the store. What it may do there is its [environment](tasks.md#the-doer), held to its runner's ceiling, and its [autonomy](tasks.md#the-autonomy) decides each step. |
| verse's own records, such as the mirror of a task or a posed question | The engine writes them into [`verse`'s own part of the tree](entities.md). No charter may be installed there, and no charter may write there. |

A package has the same shape as a [scope](../glossary.md#scope), along another line:

| | What it says | How it comes back |
|---|---|---|
| a scope | whose *copy* of the store a charter acts in | It waits for its developer to reconnect, because nothing lists the scopes. |
| an address | which *part* of the store a charter acts on | It comes back at every start, because anyone can list the addresses. |

Both mark their records, so a restore takes only its own.

## The charter is durable

Installing a rule writes it into the store as `verse.trigger.rule.<name>`. For a package the
entity is `verse.trigger.rule.<package>:<name>`, so two packages may use one name. Removing a
rule deletes that entity. A start reads the rules back before anything else runs. So a verse
comes back running the program it stopped with.

The record holds the rule as `spec`. It also holds the `scope` and the package address `at`
that the rule was installed in, when it has them.

The record does not hold who installed the rule. The installer is the owner on the rule's
permissions record ([records](perms.md#records)): the principal the install acted as. For the
charter the deployment names, the owner is `system:charter`.

A restore installs the rule again as that owner. When verse refuses a charter file part way, it
puts a replaced rule back as its installer, not as the system.

Everything else that a restart needs is durable in the same way.

| Thing | How it is durable |
|---|---|
| a task | it is mirrored |
| the position of a sequence | a fact on its subject |
| a claim | a lease in the store |
| an open question | a record, with its ballots on it |
| an open selection | a channel, with its ballots beside it |

At a restart, verse poses each open question again. From these records it also restores the
hooks of a chain and the awaiter of a selection. The rules must be durable too, or a verse
would come back with its work in progress and nothing to react to it.

**There is one entity for each rule.** An install writes one entity, a removal deletes one, and
neither reads the others. So two installs cannot lose each other's rules, and a rule has an
address (`verse data verse trigger rule review`).

**A rule is stored as structure, under the `!raw` tag.** `!raw` says *the tags beneath me are
data*. The store keeps the subtree as written, and runs no operator beneath it at any depth. The
store removes the `!raw` itself, so the rule lands with its own tags.
[A rule is stored under `!raw`](../design/reference-charter.md#a-rule-is-stored-under-raw) gives the reason.

| Function | What it does |
|---|---|
| `entity.Raw` | wraps a rule in the escape on the way in |
| `entity.AsData` | escapes a rule that was read back, so that it can be put back. `entity.Modify` does this for its callers. |

A rule that is read back is ordinary data, so putting it back means wrapping it again. Without
`!raw`, a record that holds a rule would run the rule's own operators as soon as anything else
about the record changed.

`!raw` also *replaces*; it does not merge. So installing a rule again cannot leave a field of the
earlier version behind.

The gain is that the record is the rule:

- `verse data verse trigger rule review` shows a document that you read field by field.
- A change to a charter is an observable fact about a shape, and not about a string.
- Nothing has to parse the rule again to ask a question about it.

**A rule keeps the comments it was written with.** So the record carries the reasons, not
only the shape. The rule `review` in [The trigger](../trigger-model.md#the-trigger) reads back
with all three of its notes in place.

A comment belongs to the *value* it is written about. It stands above the value, or beside it on
the same line. Something must carry the comment: the type of the value, the field that holds the
value, or the codec of what the value sits inside. Together they cover a charter:

| a comment on | kept by |
|---|---|
| the rule, `action:`, `gating:`, `spawn:`, a gate entry | the value's own type |
| a `patch:` or `derive:` body | nothing needed: the value is stored as-is |
| `name:`, `stratum:`, `condition:`, `gates:`, `seq:`, a gate's `name:`/`mode:` | the *field* holding it. In this way a value whose own type cannot carry a comment still keeps it. |
| `subject:`, `on:`, `when:` | Condition's own codec, which is hand-written and so carries them by hand |

The two positions are one question asked twice, and verse carries both.
`name: review # the short name` reads back as it was written, and so does
`# what this rule is for` above it.

The exception is the fields of a spawn: `prompt:`, `agent:`, `environment:` and the rest. Verse
keeps a comment above or beside `spawn:`. It does not keep a comment on a field inside the
spawn.

**A comment above a list and one above its first entry are the same comment.** A block list
begins at its first element, and an object at its first field. So both comments belong to the
container: they merge, and come back together above it. Every entry after the first keeps its
own comment.

So a comment above `subject:` reads back in one of two places: above the condition when
`subject:` is written first, and beside the field when it is not. The text is kept either way.

```tony
gates:
# both of these merge onto the list…
- name: approval
  mode: ask
# …while this one stays with its entry
- name: security
  mode: ask
```

The same holds one level out, for a charter file that is a list of rules: a comment above the
first rule belongs to the file. To attach the comment to the rule, write it inside the item.

```tony
- # what this rule is for
  name: review
```

**Restoring a rule is an ordinary install**, so the rule acts on what already matches it,
including what arrived while verse was down ([the backlog](condition.md#the-backlog)).

A **charter in a scope** (`verse c -a -scope`) is restored the same way, when the scope's engine
is next built. Verse builds that engine when it is needed: in practice, when the developer
reconnects. The record of a rule in a scope carries `scope: <name>`. A scope reads baseline
beneath it, so a listing in a scope returns the rules of both, and only the record says which is
which. The restore skips the rules of baseline, because a scope runs what was installed into it
([scopes](scopes.md#the-charter-a-scope-runs)). A restart cannot bring back a scope nobody has
asked for again, because nothing lists the scopes.

**A rule can watch the charter**, because the charter is a place in the store like any other:
`subject: {path: "verse.trigger.rule.*"}`. The rule must *name* the place. `verse.trigger.*` and
`verse.task.*` are the engine's own records. A rule reaches either only with a path that begins
with `verse`, so a rule does not react to its own records by accident.

The permissions records (`verse.perms`) belong to the system, and no grant opens them to
anybody. So only the system's own charter can read them. Verse refuses at install a rule of
anybody else that names them, rather than installing a rule that matches nothing
([verse's own half](../perms.md#verses-own-half)). A rule reads as its owner, all the way down.

## What an install does not check

An install refuses an unknown field of a rule or of a spawn, an unknown operator in a `value`
predicate, a spawn with no [latch](../glossary.md#latch), and a spawn the runner does not offer
what it asks: an agent, a setting, a system, an environment beyond its ceilings. It accepts each
of these:

| What the rule holds | What happens |
|---|---|
| a key spelled wrongly inside `patch:` or inside the `value` of a `derive` | Verse writes the key as spelled. The keys of a payload are yours. |
| a reference that names nothing, such as `$subject.valeu.x` | The firing fails and writes nothing. The rule gets `trouble`. |
| a spawn with a `runner:` that is not installed | The patch of the action lands, and the task fails at once: `verse task` shows `status: failed` and the reason in `err`. The rule gets no `trouble`. |
| a condition over a place that the rule's owner may not read | The rule selects nothing, and nothing reports it. A rule reads the store as its owner does. |

In the fourth and fifth rows the subject is already past the latch, so the rule does not fire
for it again. For the fourth, give the action an `onFailed` hook, which writes a state you can
see. In the fifth no task starts and no hook runs; read the rule's `trouble`, and
[when a rule does nothing](../trigger-model.md#when-a-rule-does-nothing).

## Why it is this way

The reasons for this design are in [reference-charter.md](../design/reference-charter.md), in the repository.

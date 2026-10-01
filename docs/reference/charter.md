# The charter

This page describes what is true of a [charter](../glossary.md#charter) as a whole. It says
how a charter is installed at an address as a package, what a package may read and write, and
what `verse charter` shows. It also says how verse stores a
[rule](../glossary.md#rule), so that a charter survives a restart.
[The trigger model](../trigger-model.md) describes the rules that a charter holds.

## A charter can be installed at an address

A charter installed at an [address](../glossary.md#address) is a
**[package](../glossary.md#package)**. A package runs in an engine of its own, and it may
write only inside that address.

The address is where the install points: the positional path, or the file's `at:`. The
install takes the address:

- The address gets an owner record that names the installer.
- What the package derives beneath the address is owned through that record.
- Verse refuses an install at an address that somebody else owns ([perms.md](../perms.md)).

```sh
verse c -a notes -f notes-charter.tony    # its rules write under notes.*
verse c -a -f charter.tony                # baseline: may write anywhere
```

With packages, two groups keep rules in one store and make no arrangement to avoid a
collision. The confinement is a property of the *engine*, and not of the rules that are
installed in it. The engine confines a package in two ways:

- A `derive` that lands outside the address does not install.
- Verse refuses a write that resolves outside the address when the rule fires.

The second way matters more, because the address of a derive can be a template.
[Confinement belongs to the engine](../design/reference-charter.md#confinement-belongs-to-the-engine) gives the reason.

**A rule may select anywhere it only reads.** The subject is a write when the action patches
it, and a read otherwise. So a rule of a package can select the place of an
[io](../glossary.md#io) and derive from it. Most rules of a package do this:

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

Two packages that both react to `git.*` are the ordinary arrangement, and not an unusual one.

**Reads are declared**, in the `reads:` line of the example. A package reads inside its own
address, and what it names in `reads:`, and nothing else. An undeclared read does not install,
and the refusal says which line to add:

```
rule "note-the-head" reads git.verse.*, which notes.* does not declare: a charter installed
at an address reads inside it and what it declares, so that the graph over packages can be
checked for a cycle. Add it: `reads: [git.verse]` (this package reads nothing)
```

An absent `reads:` does not mean "everything". It means nothing: a package with no declaration
reads only inside itself.

This default is the safe one and the strict one. A package that reads nothing outside cannot
interfere with anybody. The default also makes the graph total, because every edge is written
down.

**Sends are declared too.** A package writes only inside its address, and an
[io](../glossary.md#io) reads what it is asked only at its own out place. So a package asks an io
for something through an [outbox](../glossary.md#outbox): a place inside the package that the
package declares the io reads.

```tony
at: verse-dev.announce
sends:
- from: out.slack                # inside this package's address
  to: slack.Signadot.out         # an io's out place, as its registration names it
```

- `from` is written relative to the package's address. `to` is an out place that a registered io
  reads. One `from` sends to one io.
- The install holds the outbox out to that io: it may read what is asked there and own each
  status it writes beside an input. It may do nothing else in the package.
- The package's rules derive inputs into the outbox like any other write inside the address, and
  read each `~status` beside them, so a rule can see what became of what it asked.
- A place is an outbox only because its package says so. A package that happens to have an
  `out.slack` place sends nothing.
- The grant stands while the package declares the outbox and an io reads the place it sends to.
  Installing the package without the entry, removing the package, or removing the io takes it
  back. Installing the io again grants the outboxes still declared for it.

The install refuses a `to` no io reads, a `from` sent twice, and `sends:` on a charter with no
address. See [The out half](io.md#the-out-half) for what an io does with an outbox.

**One file is one charter, and a directory holds several.** Each file says where its charter
goes with its own `at:`, so the layout of the directory is free.

`deploy/charters/verse/repo/verse/comment-drift/` holds two packages: a `drift` package that
reviews, and a `merge` package that gates what the review delivers. One `verse c -a -d`
installs them, all of them or none.

Each package goes in **as one step**. Its declaration, its levels and its rules install
together or not at all. So a refusal anywhere puts back what stood before it (see
[strata.md](../strata.md#installing-and-the-order-it-does-not-depend-on)).

**Its levels are its own**, and so are its records. The rules and the strata of a package are
addressed by package (`verse.trigger.rule."verse-dev.drift:head-moved"`). So two packages may
each declare a `reflected` level, and neither overwrites the other. The two halves of
comment-drift need this: both read the git io, and both want a bottom level for it.

The levels are not confined. The lowest level of a package owns the place that the package
*reads*. Its rules select from there and derive above it.

The write confinement makes the levels of each package coherent. A second check on the strata
does not. With no write across packages there is no derive across packages, so there is no
ordering across packages to define. Two packages may both declare a level 10, and the two
levels mean nothing to each other.

**The reads graph is acyclic.** The declaration is also a graph. P → Q when what P reads meets
what Q may write, which is the address of Q. Verse refuses a cycle at install, and shows the
loop:

```
reads: this would close a cycle between packages: drift → merge → drift — one reads what
the next writes, all the way round, so a write on any of them can wake the others and come
back. A package graph is acyclic: break the loop, or fold the two halves into one package
```

The graph exists because nothing else catches such a loop. Suppose `a` reads what `b` writes,
and `b` reads what `a` writes. That is a feedback loop, and neither author can see it in their
own file. The level discipline is per package, so a derive across packages has no ordering to
violate. The cycle throttle bounds a chain within one engine.

A declared read of an address where no package is installed adds no edge. `git` is the place
of an io, and to read an io is not to depend on a package. So two packages that both read
`git`, as in the example, are not a loop.

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

`verse charter` prints the rules themselves, and not their names. The charter is the program
that verse runs. A listing of names would need a second command for each rule to show what
the rule *does*.

**What a file cannot hold goes to stderr.** A rule that is held, suspended or has failed at a
firing is marked there. Those are facts about the running rule, not part of the charter, and
the install would refuse them. The charters under the address are named there too:

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
`--prune` makes the directory the whole truth under an address. The result is the same as
`verse c -R demo` followed by `verse c -a -d out/`, done as one step. A rule, level or schedule
missing from a file is removed, and so is a package with no file. Rules the directory keeps go
on running and are not stopped and restarted. Every file must be at or under the address. If
anything is refused, the removals are put back along with the installs.

The writer lays the files out by address. That layout is a default, not a rule: the `at:` in
each file is what says where it goes, because the rules in it name absolute paths. A directory
laid out any other way, by project and with READMEs beside the charters, installs the same.
`--paths` lists every charter as one path per rule and level.

A package is removed whole too: its rules, its levels, what it reads, and the engine that runs
them. What the package *wrote* stays, because the writes of a charter belong to the world.
`verse data -rm` removes those.

```sh
verse c -r merge
verse c -R demo                          # …and every charter under that address
```

**Two more things a package does not confine.** They are stated here because the boundary is
easy to overstate.

| Thing | Why a package does not confine it |
|---|---|
| a spawn | A task acts in the world under its `grant`. Confinement in the store is not confinement in the world (see [provider selection](../provider-selection.md)). |
| verse's own records, such as the mirror of a task or a posed question | The engine writes them into [`verse`'s own part of the tree](entities.md). No charter may be installed there, and no charter may write there. |

A package has the same shape as a `verse connect` [scope](../glossary.md#scope), on another
axis.

| | What it says | How it comes back |
|---|---|---|
| a scope | whose *copy* of the store a charter acts in | It waits for its developer to reconnect, because its layer cannot be enumerated. |
| an address | which *part* of the store a charter acts on | It comes back at every start, because its address is a stamp that anyone can list. |

Both stamp their records, so a restore takes only its own.

## The charter is durable

Installing a rule writes it into the store as `verse.trigger.rule.<name>`. For a package the
entity is `verse.trigger.rule.<package>:<name>`, so two packages may use one name. Removing a
rule deletes that entity. A start reads the rules back before anything observes. So a verse
comes back running the program that it stopped with.

The record holds the rule as `spec`. It also holds the `scope` and the package address `at`
that the rule was installed in, when it has them.

The record does not hold who installed the rule. The installer is the owner on the permissions
record of the rule itself ([perms](perms.md)). That owner is the principal that the install
acted as. For the charter that the deployment names, the owner is `system:charter`.

A restore installs the rule again as that owner. When verse refuses a charter file part way,
it puts a replaced rule back as its installer, and not as the system.

Everything else that a restart needs is durable in the same way.

| Thing | How it is durable |
|---|---|
| a task | it is mirrored |
| the position of a sequence | a fact on its subject |
| a claim | a lease in the store |
| an open question | a record, with its ballots on it |
| an open selection | a channel, with its ballots beside it |

At a resume, verse poses the open question again. From these records it also restores the
hooks of a chain and the awaiter of the selection.

The program must be durable too. Without that, a verse comes back with its work in progress
intact and nothing left to react to it.

**There is one entity for each rule**, because the operations already have that shape. An
install writes one entity, a removal deletes one, and neither reads the others. So two
installs cannot lose each other, and a rule has an address
(`verse data verse trigger rule review`).

**A rule is stored as structure, under the `!raw` escape.** `!raw` says *this tag is data*.
The store keeps the subtree verbatim, and interprets nothing beneath it at any depth. The
store consumes the escape itself, so the rule lands with its own tags.
[A rule is stored under `!raw`](../design/reference-charter.md#a-rule-is-stored-under-raw) gives the reason.

| Function | What it does |
|---|---|
| `entity.Raw` | wraps a rule in the escape on the way in |
| `entity.AsData` | escapes a rule that was read back, so that it can be put back. `entity.Modify` does this for its callers. |

A rule that is read back is ordinary data, so putting it back means escaping it again.
Without the escape, a record that holds a document would execute its own operators as soon as
anything else about the record changed.

Raw also *replaces*, and does not merge. So installing a rule again cannot leave a field of
the earlier version in the later version.

The gain is that the record is the rule:

- `verse data verse trigger rule review` shows a document that you read field by field.
- A change to a charter is an observable fact about a shape, and not about a string.
- Nothing has to parse the rule again to ask a question about it.

**A rule keeps the comments it was written with.** So the record carries the reasons, and not
only the shape. The rule `review` in [The trigger](../trigger-model.md#the-trigger) reads back
with all three of its notes in place.

A comment attaches to the *value* that it is written about. It stands above the value, or
beside it on the same line. Something must carry the comment: the type of the value, the field
that holds the value, or the codec of what the value sits inside. Together they cover a
charter:

| a comment on | kept by |
|---|---|
| the rule, `action:`, `gating:`, `spawn:`, a gate entry | the value's own type |
| a `patch:` or `derive:` body | nothing needed: the value is stored as-is |
| `name:`, `stratum:`, `condition:`, `gates:`, `seq:`, a gate's `name:`/`mode:` | the *field* holding it. In this way a value whose own type cannot carry a comment still keeps it. |
| `subject:`, `on:`, `when:` | Condition's own codec, which is hand-written and so carries them by hand |

The two positions are one question asked twice, and verse carries both.
`name: review # the short name` reads back as it was written, and so does
`# what this rule is for` above it.

The exception is the fields of a spawn: `driver:`, `prompt:`, `runner:` and the rest. Verse
keeps a comment above or beside `spawn:`. It does not keep a comment on a field inside the
spawn.

**A comment above a container and one above its first entry are the same comment.** A block
list begins at its first element, and an object at its first field. So both comments head the
container: they merge, and they come back together above it. Every entry after the first keeps
its own comment.

For this reason a comment above `subject:` reads back in one of two places. It reads back
above the condition when `subject:` is written first, and beside the field when it is not. The
text survives in both cases, and only its carrier changes.

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

**Restoring a rule is an ordinary install**, so it **replays**. A subtree watch is
level-triggered at the open, so a restored rule acts on what already matches it. That includes
what arrived while verse was down ([the backlog](condition.md#the-backlog)).

A **scoped charter** (`verse c -a -scope`) is restored in the same way, when the engine of the
scope is next built. Verse builds that engine on demand, which in practice is when the
developer reconnects.

The record of a scoped rule carries `scope: <name>`, and this makes the restore possible. A
scoped store reads baseline layered underneath. So a listing in a scope returns the rules of
baseline and the rules of the scope, and only the record says which is which. The restore
passes over the rules of baseline, because a scope runs what was installed into it (see
[connect](connect.md#the-charter-a-scope-runs)).

A restart cannot bring back a scope that nobody has reconnected to. To enumerate the scopes
that have a charter, a scope must be a thing that can be listed, and it is not.

The charter is an observable place like any other, so a rule can watch it
(`subject: {path: "verse.trigger.rule.*"}`). The rule must *name* the place.
`verse.trigger.*` and `verse.task.*` are the engine's own machinery. A rule reaches either
only with a path that begins with `verse`, so the loop cannot start from its own records. A
rule can still observe the rules, and it does so only on purpose.

The permissions records (`verse.perms`) are one step further. They belong to the system, and
no grant opens them to anybody. So only the system's own charter can read them. Verse refuses
at install a rule of anybody else that names them. It does not install the rule to match
nothing ([perms.md](../perms.md)). A rule reads as its owner, all the way down.

## Why it is this way

The reasons for this design are in [reference-charter.md](../design/reference-charter.md), in the repository.

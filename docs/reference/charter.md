# The charter

This page describes a [charter](../glossary.md#charter) as a whole:

- how a charter is installed at an address, as a package;
- what a package may read and write;
- what `viff charter` shows;
- how viff stores a [rule](../glossary.md#rule), so that a charter survives a restart;
- what an install does not check.

[The charter reference](../trigger-model.md) describes the rules a charter holds, and
[the tutorial's charters pages](../tutorial/charters.md) write one, step by step.

One example runs through the page: comment-drift, the charter in `.viff/charters/comment-drift/`
of the viff repository. It has two halves. `drift`, at `repos.viff.drift`, reviews what lands on
`main` and records what it finds. `merge`, at `repos.viff.merge`, turns each branch a review's fix
delivers into a proposal that somebody decides.

## A charter can be installed at an address

A charter installed at an [address](../glossary.md#address) is a
**[package](../glossary.md#package)**. A package runs in an engine of its own, and it may
write only inside that address.

The address is where the install points: the path after `-a`, or the file's `at:`. The install
takes the address:

- The address gets an owner record that names the installer.
- What the package derives beneath the address is owned through that record.
- Viff refuses an install at an address that somebody else owns ([perms.md](../perms.md)).

```sh
viff c -a repos.viff.drift -f drift.tony   # its rules write under repos.viff.drift
viff c -a -f charter.tony                   # baseline: may write anywhere
```

So two groups can keep rules in one store without agreeing on anything to avoid a collision.
The *engine* keeps a package inside its address, not the rules installed in it. It does this in
two ways:

- A `derive` that lands outside the address does not install.
- When a rule fires, viff refuses a write that lands outside the address.

The second way matters more, because the address of a derive can be a template that only the
firing fills in.
[Confinement belongs to the engine](../design/reference-charter.md#confinement-belongs-to-the-engine) gives the reason.

**A rule may select anywhere it only reads.** The subject is a write when the action patches
it, and a read otherwise. So a rule of a package can select what an [iod](../glossary.md#iod)
wrote and derive from it. Most rules of a package do this. The first rule of `merge` selects
each branch a fix delivered, which the git iod wrote, and derives a proposal from it:

```tony
at: repos.viff.merge       # where this package writes — its engine is confined to it
reads: [git, repos.viff.drift]   # …and what it may read besides
strata:
- name: reflected           # the level the source sits at
  level: 0
  match: {path: "git"}
- name: merge
  level: 30
  match: {path: "repos.viff.merge.proposal"}
rules:
- name: proposal-appeared
  stratum: reflected
  condition:
    subject:                # a source: read, never written
      path: "git.viff.ref.*"
      value: {name: !glob "viff/auto/*"}
  action:
    derive:
    - path: "repos.viff.merge.proposal"
      id: .[subject.value.name]
      value: {repo: .[subject.value.repo], branch: .[subject.value.name], sha: .[subject.value.sha]}
```

Two packages that both react to `git.*` are normal.

**Reads are declared**, in the `reads:` line of the example. A package reads inside its own
address and what it names in `reads:`, and nothing else. A rule that reads anything else does
not install, and the refusal says which line to add. A rule of `merge` that copies the review's
report onto a proposal reads `repos.viff.drift.range`, so `merge` declares `repos.viff.drift`.
With `reads: [git]` alone, the refusal is:

```
rule "proposal-says" reads repos.viff.drift.range.*, which repos.viff.merge.* does not
declare: a charter installed at an address reads inside it and what it declares, so that the
graph over packages can be checked for a cycle. Add it: `reads: [repos.viff.drift.range]` (it
reads git)
```

A package without `reads:` reads only inside itself. It does not read "everything". A package
that reads nothing outside cannot interfere with anybody, and every edge of the graph below is
written down.

**Sends are declared too.** An [iod](../glossary.md#iod) works in both directions: it writes
what a system holds into the store, and it carries writes back out to the system. It reads
those writes at its own out place. A package writes only inside its address, so it cannot write
to that out place directly. It writes to an [outbox](../glossary.md#outbox) instead: a place
inside the package that the package declares the iod reads.

`merge` asks about each batch of proposals in Slack through its gate's label, and so needs no
outbox. For it to post a message of its own there, it would declare one:

```tony
at: repos.viff.merge
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

**A charter can stand for a while.** `ttl:` says how long, as a duration. To try the review for
three days before it stays, install it with a ttl:

```tony
at: repos.viff.drift
ttl: 72h
rules:
- …
```

- The install says when the charter goes: its own time plus the ttl. At that time viff removes
  the package as `viff charter -r` removes it: its rules, levels, schedules and declaration, and
  nothing it wrote. A charter installed under its address is a package of its own, and stays.
- Like `reads:` and `sends:`, the ttl is the package's declaration, and each install of the file
  replaces it. Installing the file again starts the ttl over. Installing it without `ttl:` makes
  it stand until it is removed. Rules installed at the address on their own leave the ttl as it
  is.
- `viff charter` writes the ttl in the file, which installs back as it was written. It says
  when the charter goes in a note beside the file, as it says a rule is held.
- Anyone who installs a charter may give it a ttl, and nobody has to.

The install refuses three things:

| What the install is given | Why it is refused |
|---|---|
| a ttl that is not a duration | a ttl is how long the charter stands, such as `72h` or `30m` |
| `ttl:` on a charter with no address | baseline is not removed whole |
| `ttl:` on a charter in a scope | a scope's charter goes with its scope |

**A charter a task installs launches within the task.** A task installs a charter as the person
it runs for ([the viff system](../systems/viff.md)), and the charter stands after the task
ends. Every spawn among its rules is held within the task:

- its environment no wider than the task's, less `viff.charter-install`, so nothing a task
  installs installs charters;
- its autonomy no looser than the task's when it installs;
- the task's credential, named as the task's spawn named it, where the spawn spends one: a
  script spends none, and may name none;
- any runner.

The hold is on spawns. A rule's `patch` and `derive`, a write into an outbox among them, are the
person's own writes, as any charter's are, held by nothing the task was: a task given
`viff.charter-install` installs rules that write what the person may write, whether or not the
task itself has `viff.data-put`. A spawn's author who gives a task charter-install gives it
that.

The install and `viff charter --check` refuse a spawn beyond the task, and an attribute still to
be bound, such as `repo: $[subject.value.repo]`, is within where some value of it could be. The rule
records the task, and what the task could hand on when it installed it. At each firing the spawn,
bound, is held to that, after the task has ended too and whatever becomes of the task's record: a
firing that binds a repository the task never reached fails, with the reason in `err`. A task in a
scope installs in baseline, as everything a task does on viff is done. `viff charter` says which
task installed a rule in a note beside the file. A rule the person installs over the task's is theirs, held by
nothing the task was.

**One file is one charter, and a directory holds several.** Each file says where its charter
goes with its own `at:`, so the layout of the directory is free.

So `.viff/charters/comment-drift/` holds the two packages, `drift.tony` and `merge.tony`. One
`viff c -a -d .viff/charters/comment-drift` installs both, or neither.

Each package goes in **as one step**. Its declaration, its levels and its rules install
together or not at all. So a refusal anywhere puts back what stood before it
([strata](../strata.md#installing-and-the-order-it-does-not-depend-on)).

**Its levels are its own**, and so are its records. The rules and the strata of a package are
addressed by package (`mirror.trigger.rule."repos.viff.drift:claim"`). So two packages may
each declare a level of one name, and neither overwrites the other. `merge` declares
`reflected` for the git refs it selects. If `drift` came to select git refs too, it could declare
its own `reflected`.

The levels are not confined. The lowest level of a package owns the place that the package
*reads*. Its rules select from there and derive above it.

Because a package writes only inside its address, no derive crosses packages. So there is no
order across packages to define. Two packages may both declare a level 10, and the two levels
mean nothing to each other.

**The reads graph has no cycles.** The `reads:` lines make a graph: P → Q when what P reads
meets what Q may write, which is the address of Q. Viff refuses a cycle at install, and shows
the loop:

```
reads: this would close a cycle between packages: repos.viff.drift → repos.viff.merge →
repos.viff.drift — one reads what the next writes, all the way round, so a write on any of
them can wake the others and come back. A package graph is acyclic: break the loop, or fold
the two halves into one package
```

`merge` reads `drift`, so `drift` may not read `merge`. A `reads: [repos.viff.merge]` in
`drift.tony` gives this refusal.

Nothing else catches such a loop. Suppose `a` reads what `b` writes, and `b` reads what `a`
writes. That is a feedback loop, and neither author can see it in their own file. Levels are per
package, and the cycle throttle limits a chain only within one engine.

A declared read of an address where no package is installed adds no edge. `git` is where an
iod writes, and reading it is not depending on a package. So `drift` and `merge`, which both read
`git`, are not a loop.

**What the graph does not cover** is baseline. The
[baseline charter](../glossary.md#baseline-charter) may write anywhere. So a loop through it
is not a loop between packages, and the check cannot see it. Baseline is unconfined in what it
writes for the same reason.

`viff charter` reads the charter at an address, as `viff data` reads the store at one. What
it prints is **the file that installs that charter**: the same keys, and `at:` naming the
address. So what you read is what you can keep, edit and install again:

```sh
viff charter repos.viff.drift > drift.tony    # the `drift` package, as its file
viff charter -a -f drift.tony                  # …installs it back where it came from
viff charter                                   # baseline's, which has no `at:`
viff c repos.viff.drift rules claim           # one rule by name, which also installs as it stands
```

```tony
at: repos.viff.drift
reads: [git]
rules: [ … the rules, in full, comments and all … ]
strata: [ … ]
```

Keys come out in one order, whatever order the file you installed wrote them in: `at`, `reads`,
`sends`, `ttl`, `rules`, `schedules`, `strata`. A key the charter does not have is left out.

`viff charter` prints the rules themselves, not their names. The charter is the program viff
runs, and a list of names would need another command per rule to show what it *does*.

**What a file cannot hold goes to stderr.** A rule that is held, suspended, installed by a task,
or has failed at a firing is marked there, and so is when a charter's ttl ends. Those are facts
about the running charter, not part of the file, and an install would refuse them. The charters
under the address are named there too. If `merge.tony` left out its level `reflected`, bare
`viff charter` would say:

```
note: repos viff merge: rule proposal-appeared is held: waiting for stratum reflected
note: under here: repos viff drift, repos viff merge — `viff c -d <dir>` writes every charter to a file of its own
```

**A tree of charters is a directory**, one file per charter. `-d` writes every charter at or
under an address, and `-a -d` installs every charter file under a directory in one step:

```sh
viff charter repos.viff -d out/              # out/repos/viff/drift/charter.tony, …
viff charter -a -d out/                       # each at its own `at:`, all of them or none
viff charter -a repos.viff -d out/ --prune   # …and remove what out/ leaves out at and under repos.viff
```

An install adds and replaces by name, so a rule missing from the directory stays installed.
`--prune` makes the directory the whole truth under an address:

- A package with no file is uninstalled first, as `viff c -R` uninstalls it: its tasks are
  cancelled and their hooks run, except a hook that would start work, before anything of the
  directory is installed. The install waits for as long as those tasks take to end.
- Then the directory is installed, and a rule, level or schedule missing from a file is removed.
- Rules the directory keeps go on running. They are not stopped and started again.
- Every file must be at or under the address.
- If the install is refused, what it changed in the packages it keeps is put back. The packages
  it uninstalled stay uninstalled, and the refusal says which.

**An install answers once its rules are accepted, not once they are armed.** A rule's watch
opens behind the answer, and a rule held for a stratum or a runner arms when that arrives.
`--wait <duration>` holds the answer that long for the rules to arm, and the command exits 3 if
any is still arming when it runs out; a held rule is said, and waited for by nothing:

```sh
viff charter -a repos.viff -f drift.tony --wait 10s   # back when drift's rules can fire, or after 10s
```

**`--check` asks what an install would do, and does none of it.** It takes `-d` or `-f`, as
`-a <address> -d <dir> --prune` would install them, and answers a verdict, `installs` or `refused`
with the install's own words, and for each package the rules and levels it would add, change,
remove or hold, and the packages the prune would take away:

```sh
viff charter --check repos.viff -d out/        # verdict: installs, packages: […], pruned: […]
viff charter --check repos.viff -f drift.tony  # a directory holding this one file
```

A spawn no runner offers is refused by the check, as the install refuses it. When the runners
are not up yet, `--runners-to-come` checks the way viff places a rule it restores: such a rule
is held for a runner and named among the held ones, where the install would refuse it.

```sh
viff charter --check repos.viff -d out/ --runners-to-come
```

A check with `-f` is the check of a directory that holds that one file, the prune included: the
answer names what `-a repos.viff -d` with `--prune` would take from under `repos.viff`, which
`-a -f` never takes. So the check of `drift.tony` alone names `merge` among what the prune takes.
The file says its own `at:`, as a file in a directory does.

A refusal is the answer, not an error, so the command succeeds either way. What the check
cannot say is what the store answers on the day, which is the install's own.

`-d` lays the files out by address. That layout is a default, not a rule: the `at:` in each file
says where it goes. A directory laid out any other way, by project and with READMEs beside the
charters, installs the same. `--paths` lists every charter as one path per rule and level.

A package is removed whole: its rules, its levels, its schedules, its declaration (what it
reads, what it sends, its ttl), the grants on its outboxes, and the engine that runs them. What
the package *wrote* stays. `viff data -rm` removes that.

Removing a package ends its work first, in this order:

1. The package is locked. Its rules fire about nothing more, its open questions leave the
   docket, and a launch a suspension had parked is dropped.
2. Its running tasks are cancelled. Each ends as a cancelled task does: its `onCancelled` runs
   and its claim comes off. A sequence ends `cancelled` where it stands.
3. Its rules, levels, schedules and declaration are removed, with its engine.

While a package is locked, nothing of it starts. A hook that would spawn, or that carries a
sequence, is refused whole: its patch does not run either, and nothing is recorded against it.
Every change to the package is refused with `429 Too Many Requests`: an install at the address,
of a rule, a level, a schedule or a declaration, and the removal of a rule, a level or a
schedule. The same request is taken once the uninstall is over, so it can be asked again.

A task whose runner does not answer the cancel is asked again until it ends, and the removal
waits for it with the package still installed and locked. `viff c -R` waits too. If it is
interrupted, the removal of the package it was waiting on goes on, and asking again joins it
and takes the rest. If viff restarts meanwhile, the package comes back installed and unlocked,
and the removal has to be asked again.

```sh
viff c -r repos.viff.merge
viff c -R repos.viff                   # …and every charter under that address
```

**Two things a package does not confine:**

| Thing | Why a package does not confine it |
|---|---|
| a spawn | A task acts in the world, outside the store. What it may do there is its [environment](tasks.md#the-doer), held to its runner's ceiling, and its [autonomy](tasks.md#the-autonomy) decides each step. |
| viff's own records, such as the mirror of a task or a posed question | The engine writes them into [`viff`'s own part of the tree](entities.md). No charter may be installed there, and no charter may write there. |

A package has the same shape as a [scope](../glossary.md#scope), along another line:

| | What it says | How it comes back |
|---|---|---|
| a scope | whose *copy* of the store a charter acts in | It waits for its developer to reconnect, because nothing lists the scopes. |
| an address | which *part* of the store a charter acts on | It comes back at every start, because anyone can list the addresses. |

Both mark their records, so a restore takes only its own.

## The charter is durable

Installing a rule writes it into the store as the data at `mirror.trigger.rule.<name>`. For a
package the address is `mirror.trigger.rule.<package>:<name>`, so two packages may use one name.
Removing a rule removes the data at that address. A start reads the rules back before anything
else runs. So a viff comes back running the program it stopped with.

The record holds the rule as `spec`. It also holds the `scope` and the package address `at`
that the rule was installed in, when it has them.

The record does not hold who installed the rule. The installer is the owner on the rule's
permissions record ([records](perms.md#records)): the principal the install acted as. For the
charter the deployment names, the owner is `system:charter`.

A restore installs the rule again as that owner. When viff refuses a charter file part way, it
puts a replaced rule back as its installer, not as the system.

Everything else that a restart needs is durable in the same way.

| Thing | How it is durable |
|---|---|
| a task | it is mirrored |
| the position of a sequence | a fact on its subject |
| a claim | a record in the store of what viff owes |
| an open question | a record, with its ballots on it |
| an open selection | a channel, with its ballots beside it |

At a restart, viff poses each open question again. From these records it also restores the
hooks of a chain and the awaiter of a selection. The rules must be durable too, or a viff
would come back with its work in progress and nothing to react to it.

**There is one record for each rule.** An install writes one record, a removal removes one, and
neither reads the others. So two installs cannot lose each other's rules, and a rule has an
address (`viff data mirror trigger rule review`).

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

- `viff data mirror trigger rule review` shows a document that you read field by field.
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

The two positions are one question asked twice, and viff carries both.
`name: review # the short name` reads back as it was written, and so does
`# what this rule is for` above it.

The exception is the fields of a spawn: `prompt:`, `agent:`, `environment:` and the rest. Viff
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
including what arrived while viff was down ([the backlog](condition.md#the-backlog)).

A **charter in a scope** (`viff c -a -scope <name>`) is restored the same way, when the scope's
engine is next built. Viff builds that engine when it is needed: in practice, when the developer
reconnects. The record of a rule in a scope carries `scope: <name>`. A scope reads baseline
beneath it, so a listing in a scope returns the rules of both, and only the record says which is
which. The restore skips the rules of baseline, because a scope runs what was installed into it
([scopes](scopes.md#the-charter-a-scope-runs)). A restart cannot bring back a scope nobody has
asked for again, because nothing lists the scopes.

**A rule can watch the charter**, because the charter is a place in the store like any other:
`subject: {path: "mirror.trigger.rule.*"}`. The rule must *name* the place. `mirror.trigger.*` and
`mirror.task.*` are the engine's own records. A rule reaches either only with a path that begins
with `viff`, so a rule does not react to its own records by accident.

The permissions records (`mirror.perms`) belong to the system, and no grant opens them to
anybody. So only the system's own charter can read them. Viff refuses at install a rule of
anybody else that names them, rather than installing a rule that matches nothing
([viff's own half](../perms.md#viffs-own-half)). A rule reads as its owner, all the way down.

## What an install does not check

An install refuses an unknown field of a rule or of a spawn, an unknown operator in a `value`
predicate, a spawn with no [latch](../glossary.md#latch), a spawn naming a runner, and a spawn no
runner takes as written: an agent no runner offers, or one whose every runner refuses a setting,
a system, an environment beyond its ceilings. It accepts each of these:

| What the rule holds | What happens |
|---|---|
| a key spelled wrongly inside `patch:` or inside the `value` of a `derive` | Viff writes the key as spelled. The keys of a payload are yours. |
| an expression that names nothing, such as `.[subject.valeu.x]` | The firing fails and writes nothing. The rule gets `trouble`. |
| a spawn whose `runner:` was removed after the install | The patch of the action lands, and the task fails at once: `viff task` shows `status: failed` and the reason in `err`. The rule gets no `trouble`. |
| a condition over a place that the rule's owner may not read | The rule selects nothing, and nothing reports it. A rule reads the store as its owner does. |

In the second row the firing writes nothing, so the rule fires again at the next commit that
touches the subject. Read the rule's `trouble`
([when a rule does nothing](../trigger-model.md#when-a-rule-does-nothing)). In the third row the
subject is already past the latch, so the rule does not fire for it again. Give the action an
`onFailed` hook, which writes a state you can see.

## Why it is this way

The reasons for this design are in [reference-charter.md](../design/reference-charter.md), in the repository.

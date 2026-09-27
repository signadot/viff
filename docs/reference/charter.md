# The charter

This page holds what is true of a charter as a whole. It says how a charter is installed at
an address as a package, what a package may read and write, and what `verse charter` shows.
It also says how verse stores a rule, so that a charter survives a restart. The rules that a
charter holds are described in [the trigger model](../trigger-model.md).

## A charter can be installed at an address

A charter installed at an address is a **package**. It runs in an engine of its own, and it may
write only inside that address. The address is where the install points, either the positional
path or the file's own `at:` declaration, and installing takes it: the address gets an owner
record naming the installer, what the package derives beneath it is owned through that record,
and an install at an address somebody else owns is refused ([perms.md](../perms.md)).

```sh
verse c -a notes -f notes-charter.tony    # its rules write under notes.*
verse c -a -f charter.tony                # baseline: may write anywhere
```

That is what lets two groups keep rules in one verse without arranging not to collide. Isolation by
convention holds until somebody edits a rule. A package's holds because the confinement is a
property of the *engine* rather than of the rules currently installed in it: a `derive` landing
outside does not install, and a write that resolves outside at fire time is refused, which is the
half that matters, since a derive's address can be templated.

**A rule may select anywhere it only reads.** The subject is a write when the action patches it and
a read otherwise, so a package's rule can select a source and derive from it, which is what most of
them do:

```tony
at: drift                        # where this package writes — its engine is confined to it
reads: [git]                   # …and what it may read besides
---
- name: reflected           # the level the source sits at
  level: 0
  match: {path: "git"}
- name: ours
  level: 10
  match: {path: "drift"}
- name: head-moved          # in the `drift` package
  stratum: reflected
  condition: {subject: git.verse.head}    # a source: read, never written
  action: {derive: [{path: "drift.head", value: {repo: $subject.value.repo}}]}
```

Two packages both reacting to `git.*` is the ordinary arrangement, not an exotic one. What each
may do about it is write in its own address.

**Reads are declared**, which is the `reads:` line above. A package reads inside its own address and
whatever it names, and nothing else. An undeclared read does not install, and the refusal says
which line to add:

```
rule "note-the-head" reads git.verse.*, which notes.* does not declare: a charter installed
at an address reads inside it and what it declares, so that the graph over packages can be
checked for a cycle. Add it: `reads: [git.verse]` (this package reads nothing)
```

An absent `reads:` is not "everything". It is nothing: a package with no declaration reads only
inside itself. That is the safe default and the strict one, since a package that reads nothing
outside cannot interfere with anybody, and it is what makes the graph total, because every edge is
written down.

One file may hold several packages. A `---` document carrying `at:` opens one, and everything after
it belongs to that package until the next declaration. `deploy/charters/verse/repo/verse/comment-drift.tony` is two, a
`drift` package that reviews and a `merge` package that gates what the review delivers, installed
by one `verse c -a -file`, all of them or none. Each package goes in **as one step**: its
declaration, its levels and its rules install together or not at all, so a refusal anywhere puts
back what stood before it (see
[strata.md](../strata.md#installing-and-the-order-it-does-not-depend-on)).

**Its levels are its own**, and so are its records. A package's rules and strata are addressed by
package (`verse.trigger.rule."verse-dev.drift:head-moved"`), so two packages may each declare a `reflected`
level without one overwriting the other, which is what comment-drift's two halves need, since both
read the git source and both want a bottom level for it. They are not confined: a package's lowest
level owns the place it *reads*, and its rules select from there and derive above it. What makes
per-package levels coherent is the write confinement rather than a second check on the strata. With
no cross-package write there is no cross-package derive, so there is no cross-package ordering to
define, and two packages may both declare a level 10 and mean nothing to each other.

**The reads graph is acyclic.** `a` reading what `b` writes while `b` reads what `a` writes is a
feedback loop neither charter's author can see in their own file, and nothing else catches it. The
level discipline is per package, so a cross-package derive has no ordering to violate, and the
cycle throttle bounds a chain within one engine. So the declaration is also a graph — P → Q when
what P reads meets what Q may write, which is Q's address — and a cycle is refused at install,
showing the loop:

```
reads: this would close a cycle between packages: drift → merge → drift — one reads what
the next writes, all the way round, so a write on any of them can wake the others and come
back. A package graph is acyclic: break the loop, or fold the two halves into one package
```

Declaring a read of an address no package is installed at adds no edge. `git` is a source's place,
and reading a source is not depending on a package, which is why two packages both reading it — the
arrangement above — is not a loop.

**What it does not cover** is baseline. The unpackaged charter may write anywhere, so a loop
through it is not a loop between packages and this cannot see it, which is the same reason baseline
is unconfined in what it writes.

`verse charter` reads the charter at an address, the way `verse data` reads the verse at one. The
charter is one document with a place for every package, so "what is installed here" is the same
question at every depth, and every node answers the same shape.

```sh
verse charter                  # the whole charter
verse charter drift            # the `drift` package and anything under it
verse charter demo pr          # …one deeper: the package at demo.pr
verse charter drift -depth 0   # …that address alone, without what is under it
```

```sh
verse charter -depth 1
# writes: anywhere            ← baseline: no address, so it may write anywhere
# rules: []
# children:
#   drift:
#     writes: drift.*
#     reads: [git]
#     rules: [ … the three rules, in full … ]
#     strata: [ … ]
#   merge:
#     writes: merge.*
#     reads: [git, drift]
#     rules: [ … ]
```

That is comment-drift installed: the review at `drift`, the gate at `merge` reading it, and a
baseline nobody has put anything in. A package at `demo.pr` nests under a `demo` that holds only
`children:`, since nothing is installed at `demo` itself.

It prints the rules themselves rather than their names. This is the program the verse is running,
and a listing of names would leave what it *does* to a second command per rule. `-depth 0` is how
it gets short. One rule by name is still one command:

```sh
verse charter merge             # that package alone, rules and all
verse charter merge -depth 0    # …without whatever is installed under it
verse c drift rules merge-proposal
```

A package comes off whole, too: its rules, its levels, what it reads, and the engine running them.
What it *wrote* stays, because a charter's writes are the world's, and `verse data -rm` is how
those go.

```sh
verse c -r merge
verse c -R demo                          # …and every charter under that address
```

**Two more things it does not confine**, stated because the boundary is easy to overstate. A spawn:
a task acts in the world under its `grant`, and store confinement is not world confinement (see
[provider selection](../provider-selection.md)). And verse's own records — a task's mirror, a posed
question — which the engine writes into
[`verse`'s own part of the tree](entities.md), where no charter may be installed and no
charter may write.

A package is the same shape as a `verse connect` scope, one axis over. A scope says whose *copy* of
the verse a charter acts in; an address says which *part* of the verse it acts on. Both stamp their
records so a restore takes only its own. Where a scope waits for its dev to reconnect, because its
layer cannot be enumerated, a package comes back at every start, because its address is a stamp
anyone can list.

## The charter is durable

Installing a trigger writes it into the verse as `verse.trigger.rule.<name>`, or
`verse.trigger.rule.<package>:<name>` for a package, so two of them may use one name. Removing one
deletes that entity, and a start reads them back before anything observes. So the program a verse
comes back up running is the one it went down with.

The record holds the rule as `spec`, and the `scope` and the package address `at` it was
installed in when it has them. Who installed it is not in the record: it is the owner on the
rule's own permissions record ([perms](perms.md)), which is the principal the install
acted as, and `system:charter` for the charter the deployment names. A restore reinstalls the
rule as that owner, and a charter file refused part way puts a replaced rule back as its
installer rather than as the system.

Everything else a restart needs is durable in the same way. A task is mirrored, a sequence's
position is a fact on its subject, a claim is a lease in the store, an open question is a record
with its ballots on it and an open selection a channel with its beside it, and resume re-poses the
one and rehydrates a chain's hooks and the other's awaiter from them.
The program has to be durable too, or a verse comes back with its work in progress intact and
nothing left to react to it.

One entity per rule, because that is what the operations already are. An install writes one, a
removal deletes one, and neither reads the others, so two installs cannot lose each other, and a
rule is addressable (`verse data verse trigger rule review`).

**A rule is stored as structure, under the `!raw` escape.** A write is a tony *patch*, the property
that makes the store good at everything else, so operator tags in a payload are *executed* by the
store rather than stored by it. A charter is full of them: `value: {branch: !glob "hotfix-*"}`,
`patch: {tmp: !delete null}`. Stored as bare structure, a rule is not one that reads back wrong. It
is one that cannot be read back at all, because the write leaves an entity whose own patch log does
not apply (`glob patching "null" gave cannot patch with glob operation`).

`!raw` says *this tag is data*. The subtree is stored verbatim, nothing beneath it is interpreted
at any depth, and the escape itself is consumed, so the rule lands carrying its own tags.
`entity.Raw` is that wrap on the way in. A rule read back is ordinary data, which is why putting one
back means escaping it again — `entity.AsData`, which is what `entity.Modify` does for its callers,
since a record that holds a document would otherwise execute its own operators the moment anything
else about it changed. Raw also *replaces* rather than merging, so re-installing a rule cannot leave
a field of the version before it standing in the version after.

The gain is that the record is the rule. `verse data verse trigger rule review` shows a document to
read field by field, a charter change is an observable fact about a shape rather than about a
string, and nothing has to re-parse to ask a question about it.

**A rule keeps the comments it was written with**, so the record carries the reasons and not only
the shape — the example above reads back with all three of its notes in place.

A comment attaches to the *value* it is written about, above it or beside it on the same line, and
something has to carry it: the value's own type, the field holding it, or the codec of whatever it
sits inside. Between them they cover a charter:

| a comment on | kept by |
|---|---|
| the rule, `action:`, `gating:`, `spawn:`, a gate entry | the value's own type |
| a `patch:` or `derive:` body | nothing needed — the value is stored as-is |
| `name:`, `stratum:`, `condition:`, `gates:`, `seq:`, a gate's `name:`/`mode:` | the *field* holding it — which is how a value whose own type cannot carry one still keeps it |
| `subject:`, `on:`, `when:` | Condition's own codec, which is hand-written and so carries them by hand |

The two positions are one question asked twice, and both are carried: `name: review # the short
name` reads back as it was written, as `# what this rule is for` above it does.

The exception is the fields of a spawn: `driver:`, `prompt:`, `runner:` and the rest. A comment
above or beside `spawn:` is kept, and one written on a field inside it is not.

**A comment above a container and one above its first entry are the same comment.** A block list
begins at its first element and an object at its first field, so both head the container, they
merge, and they come back together above it. Everything after the first keeps its own. This is
why a comment above `subject:` reads back above the condition when `subject:` is written first,
and beside the field when it is not — the text survives either way, and only its carrier
changes.

```tony
gates:
# both of these merge onto the list…
- { name: approval, mode: ask }
# …while this one stays with its entry
- { name: security, mode: ask }
```

The same holds one level out, for a charter file that is a list of rules: a comment above the
first rule belongs to the file. Write it inside the item to attach it to the rule.

```tony
- # what this rule is for
  name: review
```

Restoring a rule is an ordinary install, which means it **replays**: a subtree watch is
level-triggered at the open, so a restored trigger acts on what already matches it — including
what arrived while verse was down. A restart is then just an install over a verse that has
moved on, which is the same property described above, and the same reason to write a latch
rather than an edge.

A **scoped charter** (`verse c -a -scope`) is restored the same way, when the scope's engine is
next built, which is on demand and so in practice when the dev reconnects. Its record carries
`scope: <name>`, which is what makes it restorable at all: a scoped store reads baseline layered
underneath, so a listing in a scope returns baseline's rules and the scope's own, and only the
record says which is which. Baseline's are passed over, because a scope runs what was installed
into it (see [connect](connect.md#the-charter-a-scope-runs)).

What a restart still cannot do is bring back a scope nobody has reconnected to: enumerating
the scopes that have a charter needs a scope to be a listable thing, and it is not one yet.

The charter is an observable place like any other, so a trigger can watch it
(`subject: {path: "verse.trigger.rule.*"}`). It has to *name* it: `verse.trigger.*` joins
`verse.task.*` as the engine's own machinery, and a rule reaches either only with a path that begins
with `verse`, so the loop cannot ignite on itself. Meta-observation stays possible, and stays
deliberate. The perms mirror (`verse.perms`) is one
step further: it is the system's, held out to nobody, so only the system's own charter can
read it, and a rule of anyone else's naming it is refused at install rather than installed to
match nothing ([perms.md](../perms.md)). A rule reads as its owner, all the way down.

# Entities

An entity is anything the flow refers to — a PR, a build, a person, a CI status, a running
task — addressed by a path (`demo.pr.1`, `verse-dev.drift.head.abc`) and carrying a document.
[Tutorial §2](../tutorial.md#2-entities) drives all of this; here is the surface flat, with
the corners that section leaves out.

**An entity is not a kind of node.** It is the word for a node a question is about: the subject a
rule selects, one row of a listing, one event of a watch. Every one of those questions carries a
path, so every one carries what depth it means — there is no entity depth, no `system`, `kind`
and `id`, and no second length beside a path's own.

## Addressing

An address is a path in a tree.

It has two spellings, which are one grammar written two ways:

```
demo.pr."1"                a kinded path — any depth, and it carries on into the payload
demo pr 1                  steps — one shell word per segment, any depth
```

The kinded path is the one a parser reads. Its grammar is
[kpath](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath), and a segment
ends where the quoting says it does. Nothing scans for a delimiter to decide how many segments
a word is; a segment that would otherwise read as something else is quoted, and reads back as
itself:

```
demo.pr."a.b"              the name `a.b` — quoted, so the dots are in the name
demo.pr.a.b                four segments, because these dots are not quoted
demo.pr."1"                the name `1`, quoted so it is a name rather than a number
git.ref.x@a:b              `@` and `:` are ordinary characters in a name; nothing reads them
demo.pr.org/repo#42        …and so are `/` and `#`
verse.task.instance.run-a1b2   a leading `verse` is a segment like any other
```

Which spelling goes where.

| | |
|---|---|
| a path | anywhere a parser reads one and no shell is involved: inside a document (`at: demo.pr."1"`), in a charter (`subject: demo.pr.1`), on the wire (`?at=`). Any depth, and it carries on into the payload — `demo.pr."1".labels.urgent` |
| steps | at a command line, where the shell's own quoting is the only quoting there is: `verse data verse-dev drift head abc`. Taken on the way *in*, and what every listing answers with |
| the same path | in a charter, where it is the `path` of a match beside a predicate: `{path: "verse-dev.drift.head.*", value: {…}}` |

A listing answers with steps, the same spelling the command line takes, so a line hands
straight back:

```sh
verse data -paths demo pr '*'       # demo pr 1
                                    # demo pr a.b
                                    # demo pr 'a b.txt'

verse data -paths demo pr '*' | xargs -L1 verse data
```

`-kpaths` is the same listing as one kinded path per line, which is the spelling that survives
being captured into a shell variable — a line of steps only reaches a read if something splits
it, and a variable passed as one argument does not:

```sh
verse data -kpaths demo pr '*'      # demo.pr."1"
                                    # demo.pr."a.b"

ref=$(verse data -kpaths demo pr '*' | head -1)
verse data -at "$ref"               # …one argument, and it reads back
```

One spelling, in and out. A step is a whole word, so a dot inside one is a character in a
name and needs no quoting — which is the ergonomic win, and it holds at every position: `verse
data dotted.sys pr 1` begins with the one segment `dotted.sys`, and `verse data a.b.c pr 1` with
the one segment `a.b.c`. The shell's own quoting is applied only where a segment would otherwise be split or expanded (a
space, a `*`), and it is single quotes, which are literal — so nothing inside is globbed or
substituted, and `xargs` understands them. A name the step clauses below would read as
something else also carries the quoted clause inside them.

A positional is a step and nothing else, because it cannot be both. A single word `w` has
two legal readings — the one segment `w`, and `split(w, delim)` — and choosing either makes the
other unsayable. So there is no delimiter scan on the command line at all, and `verse data
demo.pr.1` names the one segment `demo.pr.1`.

A step is one place, and its first character says which. Four clauses, no splitting:

```sh
verse data demo pr 1 votes '[0]'     # the array element   — also {0} sparse, (scott) keyed
verse data demo pr '*'               # every field there   — the segment wildcard, for a read
verse data demo pr 1 '"[0]"'         # the field named [0] — the name inside the quotes
verse data demo pr 1 labels urgent   # a name, as given
```

The bracketed forms are *self-delimiting* — they open and close — so recognising one needs no
guess about where the word ends. That is what tells this apart from reading a delimiter out of a
word: a delimiter changes how **many** segments a word is, and this does not. One word in, one
place out, whatever is inside it.

The quoted clause is the escape, and it reaches every name: one that would read as an access or
the wildcard, or that itself begins and ends with a quote, is spelled inside one more pair
(`'"[0]"'`, `'"*"'`, `'""x""'`). A listing prints it that way, so the line still hands back. Array access is what
people address; a field named `[0]` is not, and verse writes none.

A whole address in one token is taken where it is marked, which is unambiguous for the
reason the positional is not — a flag is a different *place* on the line, not a second reading
of the same word. Which grammar applies is declared rather than guessed:

```sh
verse data -at demo.pr.1            an address copied out of a document
verse data -at 'demo.pr.*.stage'    …wildcards and all
```

An address is given once: as steps, or as `-at`, never both.

### What stands at a path

A **node** is whatever stands at a path: an object, an array, a scalar, a null, an empty object.
All of them count the same. A **path stands** when the store holds a node there, and the store
saying not-found or path-conflict is the only definition of absence.

So a listing is every node its pattern reaches — empty objects, nulls and scalars included — and
nothing it does not. A scalar two segments deep is not in a three-segment pattern's listing; it
is a node, and a read of its own path, or of a pattern carrying `..`, answers it.

### The two questions asked of a pattern

Two, and each is asked one way, which is what keeps containment from being asked seven ways:

| | |
|---|---|
| **reach** — does this pattern reach that path; is this write under that confinement | kpath's, `Matches` and `MatchesPrefix` |
| **overlap** — can two patterns reach a common path: two strata, two sources' claims | one walk to the shorter length, asking at each position whether the two segments can meet |

A charter's subject may put a tony operator where kpath has a field (`[demo, !glob "hot-*"]`),
and a source's claim may put a literal prefix there (`ws/*`). Either stays a match on the field
name at that position; the structure around it — its length, its wildcards, its `..` — is kpath's.

> **Pending.** Descent is the corner where the tree has not caught up with this. A match compares
> a segment as a name, so `verse-dev..finding` and `verse-dev.drift.finding` do not meet, and
> kpath answers false for any pattern holding `..`, so containment cannot use it yet. One fixed
> arity also survives, in the walk behind `List` (`listWalkArity`): a listing that names no
> address has nothing to count from, where every other walk says how far *below its own place*
> its records stand. Tracked as `jabt2frr` here and
> `17hj5ygkh12ks7j7n5n0` upstream; until they close, this section says what will be true of a
> descent rather than what is.

### `*`: three layers, three rules

A `*` passes through the shell, the address grammar and a charter match, and each of the three
reads it differently. They are consistent once seen together, and surprising one at a time.

In the address grammar, a whole `*` segment is the wildcard in both spellings, and the name that
is an asterisk is quoted in both.

```sh
verse data -at 'demo.pr.*'        # every name under demo.pr — a segment wildcard
verse data demo pr '*'            # the same, as a step
verse data -at 'demo.pr."*"'      # the one node whose name IS an asterisk
verse data demo pr '"*"'          # the same, as a step — the quoted clause
verse data demo pr 1 votes '[*]'  # every element of an array, which a `*` step is not
```

Only a whole segment selects: `hotfix-*` is a name with an asterisk in it, as a step, in `-at`
and in a charter's kinded path alike. A `*` selects every *field*, which is what the names under
a node are, and `[*]` every array element, so the two are different clauses. A write names one
place, so it refuses a `*` step and names the quoted one.

A family of names is not a segment at all: a path names places, and which of them a rule wants is
a predicate on what each HOLDS (`{path: "demo.pr.*", value: {branch: !glob "hotfix-*"}}`). The one
match a path spells is the quoted clause, and it names exactly one thing: `demo.pr."*"` is the node
called `*` and nothing else.

The shell gets there first, and an unquoted `*` never reaches verse at all. It fails two
different ways, and the second is the one that bites:

```sh
verse data demo pr * stage        # the word IS the glob: it becomes every file in the cwd
                                  #   argv: demo pr demo.pr.1.stage Makefile notes.txt stage
verse data -at demo.pr.*.stage    # a glob inside a word: it expands only if a file matches,
                                  #   and then silently becomes that file
                                  #   argv: -at demo.pr.1.stage
                                  # with nothing matching, bash passes it through (right by
                                  #   luck) and zsh refuses the command outright
```

Quoting it is the whole fix, in either spelling: `demo pr '*' stage` and
`-at 'demo.pr.*.stage'` hand verse the asterisk itself. The single quotes are the SHELL's; what
verse then sees is a bare `*` segment, which is the wildcard.

`'"*"'` is a different address, not a safer way to write the same one. The inner `"*"` is the
address grammar's quoted clause — the name that is an asterisk — and the outer `'…'` is the shell
quoting that keeps it intact. That is how `-paths` prints such a node, so a listing pipes back to
that one node rather than to every name beside it:

```sh
verse data demo pr '*'            # the wildcard: every name under demo.pr
verse data demo pr '"*"'          # the one node named `*`
verse data --paths demo pr '*' | xargs -L1 verse data   # each line reads back as what it names
```

In a charter a path is the kinded path it is at a read, so the quoting means what it means there
and the same characters mean the same thing on both faces:

```tony
subject: "demo.pr.*"            # any name
subject: 'demo.pr."*"'          # the name that IS an asterisk — `verse data -at` reads it so too
subject: 'demo."..".x'          # the name that is two dots, where a bare `..` descends
```

### `verse` at the front

Everything verse keeps about its own running lives under `verse` ([below](#two-halves-one-tree)).
It is a segment like any other in every spelling, and it counts like any other:

```sh
verse data verse task instance run-a1b2       # steps
verse data -at verse.task.instance.run-a1b2   # …the same address copied out of a document
verse data verse task instance '*'            # …and every task record under it
```

The address parses; the write is refused. `verse data verse task instance x '{…}' -put` is a
well-formed address and a 403: what verse writes about itself is written by the verbs that own it
(`charter -a`, `runner add`, `source add`, `gate approve`). Reading is unrestricted.

In a charter an address is a kinded path — on its own, or as the `path` of a match that also
carries a predicate:

```tony
subject: {path: "verse-dev.drift.head.*", value: {reviewed: pending}}
subject: {path: "verse.task.instance.*"}
subject: demo.pr.1
```

So `!glob "hot-*"`, `!or [hot, cold]` and `!and [!glob "h*", !not hold]` all say at a position
what they already say about a payload. Nothing in verse enumerates the operators; `tony.Match`
owns them. One convention sits above that: a bare `*` is any segment, and inside an operator a
`*` is an ordinary character — which is how a name that *is* an asterisk is named (`!glob "[*]"`).
An operator has no string spelling, so a path carrying one is written as `path:`.

A `*` is a position, which is why the length of the path is the depth it selects at:
`demo.pr.*` picks the nodes under `demo.pr`, and `demo.pr` is the node `demo.pr` itself.

**What takes what:**

| | |
|---|---|
| `verse data`, `data/put/rm`, `verse watch` | any spelling; steps may be several arguments, at any depth |
| `verse flow show <subject>`, and `--subject` on the flow verbs | one token: a kinded path, `*` for any segment |
| `verse runner/source/resolver show/check/remove` | one token: `<kind>.<name>`, or the address as stored |
| `verse charter [<address>]` | an address in the charter tree, any depth, as steps: `verse charter verse-dev drift` |
| `verse c <address> rules <name>` | a path: the address, then into it |
| `verse perms <step>…` | the node a record is about, as steps |

`verse charter` and `verse perms` read steps as `verse data` does — a word is one segment, and
`'"x"'` is the name `x` — but they name one place made of names, so an access (`'[0]'`) or a `*`
step is refused.

<a id="two-halves-one-tree"></a>
## verse's own records, and the world's, in one tree

The top of the tree has one reserved key. Everything verse writes about its own running lives
under `verse`; everything the world is about lives beside it, at whatever path a writer chooses.

```
demo.pr.1                          what the world is about
verse.task.instance.run-a1b2       verse's own
verse.trigger.rule.review
```

Under `verse` are `task.instance.*` (the mirror of every running task), `task.run.*` (the
record of a run that happened) and `task.claim.*` (a sequence's run claim); `gate.question.*`
(every gate awaiting an answer); `resolvers.*` (who answers them) and `watch.*` (who is watching what, one record per binding
and origin — see [resolvers.md](resolvers.md)); `trigger.rule.*` (the
installed charter — `trigger.rule.<package>:<name>` for a rule installed at an address),
`trigger.strata.*` (its declared levels) and `trigger.package.*` (what each package declares it reads);
`runners.*` (where work can run) and `sources.*` (what reflects an external system in);
`schedule.<name>` (a schedule: its period, its phase, the `next` the daemon is armed against,
and under `schedule.<name>.tick.<instant>` the tick standing, written only by the schedule),
beside `schedule.date`, `schedule.time` and the other calendar facts the base charter derives
from the hourly tick. A
question's ballots live on its record (`gate.question.<id>.ballots.<who>`), written by the
answer path that tallies them.

Two more are about who: `principal.<kind>.<name>` lists every principal that has presented
itself, by the kind its name carries — `user` for a person by email, `runner` for a worker
that claimed work, `agent` for an operator agent — and `perms.node.<address>` is the perms mirror: a record
wherever ownership changes down the tree, keyed by the node's own address spelled as one
segment, holding `owner`, the `grants` its owner holds out, and for a question `under`, the
address of what posed it. The owner of any node is the nearest record up its address, and the
root is the system's, so a node owned by whoever owns the node above it has no record of its
own. Every write carries a principal (the request's, from the CLI's `user:`; the rule's own
address for a firing; the system for what verse writes unasked), and is admitted by what its
place permits or refused with a 403 saying why; an entity's owner is whoever first wrote it
holding `owner` there, fixed until the entity is deleted. A read answers only what its reader
may read. The mirror is written only by versed and read only by the system.
[perms.md](../perms.md) is the whole of it.

One thing is deliberately not under `verse`: `provider.*` — the registry of doers is written
by whoever runs the verse.

What is under `verse` is read like anything else and written only through the verbs that own it.
`verse data verse trigger rule review` reads a rule; `verse data -put` at any address under
`verse` is refused, and says which verb to use instead (`verse c -a`,
`verse resolver add`, `verse runner add`, `verse io add`, `verse gate approve`). Each verb
is where its checks live — what a rule may read, what a runner is, whether a source's claim
overlaps — and beneath every one of them the store itself checks the write against what its
place permits ([perms.md](../perms.md)), so nothing reaching the store may install a rule, stamp
a task record or answer a question on somebody else's behalf.

A charter that wants to react to verse's own state names it by its path, `verse` first:

```tony
condition:
  subject: {path: "verse.gate.question.*", value: {status: pending}}
```

A path that does not start with `verse` does not reach under it, so a rule about `demo.pr.*`
cannot quietly select the task records of the runs reviewing them.

A rule READS verse's half and does not write it. A `patch` of a subject under `verse`, or a
`derive` into it, is the generic write the api refuses, arriving by another door, and is refused
the same way: at install where the rule names the address (403), and at the write whatever
address it arrived at. Spawning about such a subject is fine — the task record is verse's own
write. The system's charter is the exception, being how the system writes there: the base
charter derives the calendar under `verse.schedule`.

`local.*` (a federated working tree) is a third thing again: it is mount-owned and served by a
controller with no history — see below.

## Reading

A verse is one document, a tree, and `verse data` answers it at a path. The path is a
[kinded path](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath) in any of
the spellings [above](#addressing), and it is the same address at every length: whatever node
stands there, a place inside a payload included.

```sh
verse data                                         # the whole document
verse data demo                                    # the subtree at demo
verse data demo pr '*'                             # each node under demo.pr, keyed by name
verse data demo pr 1                               # one node
verse data demo pr 1 labels urgent                 # one place inside it
verse data -at 'demo.pr.*.stage'                   # one field of each, still keyed by name
verse data -paths demo pr '*'                      # one address per line
verse data demo pr '*' -if '{stage: open}'         # by what it says
verse data demo pr '*' -if '{urgent: true}' -if-at labels   # …rooted inside the payload
```

The globbed line has a steps spelling too: `verse data demo pr '*' stage` is the same read, and
`'"*"'` is the name that is an asterisk. See [`*`: three layers, three rules](#three-layers-three-rules).

### `--depth`: how far beneath the address

A listing walks everything beneath what it was given, into payloads and arrays, because nothing
in an answer says where an entity ends — ask for one level and you get whatever happens to live
there and lose what is deeper. `--depth` bounds that walk, **counted from the address the path
names**, so the same number reads the same way wherever you are standing:

```sh
verse data demo --paths --depth 1        # demo's children
verse data demo pr --paths --depth 1     # …one level down, and the SAME number
verse data demo pr --paths --depth 0     # the address alone: nothing beneath it
verse data demo pr --paths               # everything beneath, into payloads
```

Walking deeper is a step, never a step and a bigger number. An access is a step like any other,
so `--depth 2` under `merge proposal X` reaches `votes[0]` and not the fields inside it. A
negative depth, and a `--depth` with no listing to bound, are each refused rather than ignored.

**The number is the store's**, handed over as you gave it, whatever the path: a place, a `*`, a
`..`, and with `--if` beside it. The store answers a listing without reading what it lists, and
the predicate is asked of each node the path REACHED — the listing is what it kept, and what is
beneath that.

```sh
verse data demo pr '*' --paths --depth 0                # the nodes the path reached, alone
verse data demo pr '*' --paths --if '{stage: open}'     # …the open ones, and what is in them
verse data demo '..' stage --paths --depth 1            # demo.stage and demo.<x>.stage
```

With a `..` in the path the depth is **one budget for the whole path**, counted from the node it
names, and only what a descent TAKES spends it: `demo '..' stage` at 1 reaches `demo a stage`,
the descent having taken `a` and the literal `stage` costing nothing — and having spent its
budget, lists nothing beneath it. That is [logd's rule](https://signadot.github.io/tony-format/),
said once, there.

### `..`: every depth beneath

A `*` opens one position. `..` opens every depth beneath where it stands, the node it stands at
included — the question that names no depth at all:

```sh
verse data '..'                              # the whole verse, walked
verse data demo '..'                         # demo and everything under it
verse data demo '..' -if '{stage: open}'     # every node that says it is open, at any depth
verse data demo '..' stage                   # every `stage` beneath demo
verse data demo '".."'                       # the node whose NAME is two dots
```

The answer nests each node where it stands, **spelled in full**: `demo '..' stage` answers
`{pr: {"1": {stage: open}}}` rather than the `{"1": open}` a `*` gives, because a descent reaches
nodes at different depths and stripping the segments after it would collide two of them into one
key. A node the read keeps whole already holds what is beneath it, so nothing is answered twice.

`..` is a QUERY, so it belongs to reads, watches and rule subjects; a write, a claim, a grant and
a permissions record each name one stored place and refuse it by that name. `verse watch demo
'..'` reports every node beneath demo that changes, at the depth each stands at, and a rule's
subject may descend the same way (see [the trigger model](../trigger-model.md)).

The answer is the subtree standing at the path, verbatim — what the store holds, not a shape
assembled for the wire. So a name is a key rather than a token inside a rendered `at:`, which
is what `-paths` is for: a script that wants the addresses gets them one per line instead of
grepping them back out of a rendering.

**A path naming nothing answers 404, at every length**, and the command exits non-zero. `{}` is
a document somebody wrote; it is not what a read says about a path nobody wrote, however long.
`demo.pr.nosuch` is not there, and neither is `nosuch.pr` — "what stands under a node that is not
there" is not a question with `none` as its answer, it is a question about a node that is not
there.

    verse data nosuch            nothing stands at nosuch
    verse data demo pr nosuch    nothing stands at demo.pr."nosuch"
    verse data                   the verse is empty: nothing has been written yet

A `*` reaches the children of what it lands on, so over a node with none it reaches nothing:
`verse data solo '*'` over `solo: {}` is a 404 too, and over `solo: {hollow: {}}` it answers
`{hollow: {}}`.

An EMPTY DOCUMENT still has a meaning, and it is the other one: the path reached nodes, and a
`-if` predicate kept none of them. That answer is about the filter rather than about the path,
so it is a 200 with `{}` — which is why the two cannot be spelled the same way. The store is
what answers absence: a read reports null only where a null was written, and absence is absence
at every length.

A read, a `-put` and a `-rm` all take the same address, at any length, because there is
only one thing to take: `demo pr 1` typed and `demo.pr."1"` written down are the one grammar,
separated by the shell in the first case and by the quoting in the second.

```sh
verse data demo pr 1
```

`-if` narrows which of the nodes the path reached the document holds, per node — it never prunes
inside what it keeps. **It asks of what the path names**: a path of N segments asks about the
nodes N segments down, a name and a `*` alike, so the question says its own depth by the `*`s it
carries. A reflected issue stands at `git-issue.<repository>.issue.<id>`, so the `*` is what
makes each issue the thing asked about:

```sh
verse data git-issue verse issue '*' -if '{status: open}'         # each open issue
verse data -at 'git-issue.verse.issue.*' -if '{status: open}'     # the same
verse data git-issue verse issue -if '{status: open}'             # the map of issues, as one: {}
```

`-trim` projects the same nodes. `-if` is a
[tony match](https://signadot.github.io/tony-format/) against the payload — the same predicate
a charter carries in `value`, so a read answers "what would a rule with this predicate
select?". Operators (`!or`, `!and`, `!not`, `!glob`, presence and absence) are
tony's, documented with
[mergeop](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop); what a match
can say inside a rule is in [the trigger model](../trigger-model.md#what-a-match-can-say).

`-if-at` roots the predicate at a dot-separated field path. See
[Selecting by what an entity says](README.md#selecting-by-what-an-entity-says).

### Reaching into an array

`[0]` addresses an array element by position:

```sh
verse data merge proposal X votes '[0]'
verse data merge proposal X votes '[0]' choice
verse data -at 'merge.proposal.X.votes[0].choice'     # the same, as one token
```

A step beginning `[`, `{` or `(` **is** that access, and `-at` carries the kinded path whole. The
shell quoting on the step is the shell's — `(` and `[` are its characters too.

**An element is addressed by position, and only by position.** `(key)` — an element by identity —
is the store's to give: logd keys an array in its schema, which is what makes a name mean an
element, refuses a position on a keyed array, and renames the elements when the keying changes.
verse declares no such schema yet, so every array it holds is positional.

A `!key(<field>)` tag in a written payload is **refused**. It used to land, because `!key` is a
tony merge operator that an ordinary write executes and a payload written as data keeps — which
left the store holding a positional array while the value claimed a name for each element, and
only verse able to read the second. One keying, and it is the store's.

A listing spells what a read takes, so `verse data merge proposal X --paths` prints
`merge proposal X votes '[0]'`, and that line reads and writes as it stands.

A write reaches an element too, and lands in place:

```sh
verse data merge proposal X votes '[0]' choice -put '"approve"'
```

The substrate is what guards it, which is why the CLI does not:

| a write at | what logd does |
|---|---|
| `votes[1].choice`, in range | patches it, and it reads back |
| `votes(scott).choice`, keyed | refuses it at the commit; nothing is harmed |
| `votes[2]` where two stand | refuses it at the patch, naming the length |

The third row is what makes the other two safe to offer. An accepted patch past the end leaves
the log unreadable ever after — a bad write that succeeds and takes every later read of the whole
store with it, unrepairably — so a refusal at the patch is what the CLI's own silence rests on.
`entity/arraywrite_test.go` pins all three answers, so the reasoning is checked at each go-tony
bump rather than inherited.

A guard rides an element write like any other, and it is about the ELEMENT the write names —
not about the entity that holds it:

```sh
verse data merge proposal X votes '[0]' -expect '{by: scott}' -put '{choice: approve}'
```

`-create`, `-update` and `-expect` reach it with tony's `!at`, which walks the path and
applies the pattern where it lands. So a guard on one ballot says nothing about the others: a
vote cast beside it neither satisfies nor refuses it, the way a pattern at a field says nothing
about its siblings. (An array-shaped pattern would say something: tony matches an array
positionally and by length, so asking about `votes[1]` that way would also swear the array holds
exactly two ballots.)

`-create` is the odd one, honestly so. An element cannot be created in place, so where nothing
stands the guard holds and the write is refused by the substrate instead, naming the array's
length — which is the answer worth hearing. Where a ballot does stand, `-create` refuses to
commit over it, as it does anywhere else.

### What the document does not cover

Mount-owned paths are not in it. `verse connect`'s content (`local.dir-content-*`) is served by
a controller outside the store's own tree, with no history, and the read walks the store's tree
only — so a mounted node is addressable by `verse data <path>` and absent from `verse data`. Saying "the document, whole" would make that pre-existing gap into a claim that
is not quite true. Unioning the second read is a real decision, not a detail.

## Writing

A put merges: fields it does not name are left alone. That is why it answers with what stands
at the address written rather than an echo of what was sent, and why two writers advancing one
entity do not clobber each other.

The address may be as deep as the tree: `verse data verse-dev drift head abc '{sha: …}' -put `
writes there, and the answer is that place. A write is a value at a path, so where an entity
"ends" is not a question a write has to answer.

```sh
verse data demo pr 1 -put '{stage: open}'       # as steps: nothing needs quoting
verse data -at demo.pr.1 -put '{owner: ana}'    # …or in one token: adds an owner, keeps the stage
verse data demo pr 1 labels urgent -put true    # write inside the payload
verse data demo pr 1 labels urgent -rm          # remove one field
verse data demo pr 1 -rm                        # the whole entity
```

A payload typed on the command line names no file, so it is read in the default input format
— tony, unless `-ifmt` or the config says otherwise. In tony, `{stage: open}` needs no
quotes on either half and commas are optional, so `{stage: open, id: 7}` and
`{stage: open id: 7}` are the same document. A payload in a `.json` or `.yaml` file is read
by the same command; see [representation.md](representation.md).

Because a write is a tony *patch*, the
[mergeop](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop) operators apply
to it — `!delete` removes a key, which is something no amount of setting fields could say —
and a charter's `patch` is the same operation from the other side.

### What a write descends through

A write names a place inside a payload by naming the fields on the way to it, and each of those
has to be somewhere a field can live. Where nothing stands yet, the write builds it; where an
object stands, it merges into it; where an **array or a scalar** stands, the write is refused.

```sh
verse data demo pr 1 -put '{votes: [{by: ana}, {by: bo}]}'
verse data demo pr 1 votes bo -put '"yes"'
# the write would replace what it descends through: demo.pr."1".votes holds an array,
# so "bo" is not a place in it — write the whole value AT demo.pr."1".votes, or address
# an element of it
```

It is refused because the alternative is silent: `bo` is not a field of an array and there is
nothing to merge into, so the array would be replaced by `{bo: yes}` — both ballots gone, exit
0, and an answer that echoes the value written rather than the entity, so nothing in the
transcript says anything was lost.

Replacing it is still available and is spelled as what it is — a put AT the array:

```sh
verse data demo pr 1 votes -put '[{by: ana}, {by: bo}, {by: cy}]'
```

The guard is a precondition carried with the write, so it is decided at the commit rather than
in a check beforehand, and `-create`/`-update`/`-expect` compose with it. `-create` is worth
naming, because it does not stand in for this one: it asks whether the NODE is absent, which
under an array it truthfully is, so it admits exactly the write that would do the damage.

A charter's `patch` is guarded on the same principle, one step looser: it is a document rather
than an address, so a nested key the subject does not carry yet is simply built, and only
landing on an array or a scalar is refused. A rule whose patch is refused fails the firing and
leaves its subject untouched — so the rule still selects it, and the next commit tries again.

## Conditional writes

Each condition is checked *with* the write, in one step at the store, so nothing can change
in between. A refused write says so, leaves the entity alone, and exits non-zero.

```sh
verse data -create demo pr 1 -put '{stage: open}' # only if nothing is there yet
verse data -update demo pr 1 -put '{stage: open}' # only if something is
verse data -expect '{stage: draft}' demo pr 2 -put '{stage: open}' # only if it still looks like this
# write refused: a condition did not hold
```

This is CAS, spelled as a predicate rather than as a revision number: the write says what it
believes about the entity rather than which revision it read. `-if` here is a precondition on
a write, and is not the `-if` that filters a listing — one narrows what is written, the other
what is read.

## Watching

```sh
verse watch                                        # from now on
verse watch -if '{stage: open}' demo pr '*'        # the same selectors as a read
verse watch -since 42                              # replay from a revision already in hand
verse watch -ago 50                                # …or the last 50 commits, whatever the head is
verse watch -since 0                               # replay everything the store still holds
```

With no cursor a watch starts from now. A cursor is how history is asked for, and asking for
it is a different act from watching: a replay of a long-lived store is a lot of lines
nobody typed a flag for, and on a store that cannot replay from the beginning it is a refusal
rather than a stream.

```tony
{op: set at: demo.pr.1 rev: 1 to: {stage: open owner: ana}}
{op: delete at: demo.pr.9 rev: 7}
```

One line per change — flow form, so a watch stays greppable and one event never wraps into
the next.

- `op` is `set` (created **or** updated) or `delete`. Create and update are not
  distinguished, because the store delivers the writer's applied patch verbatim and a create
  is just a patch onto nothing.
- `to` is the entity's whole state as of that commit, not the delta that was written — so a
  reader matching on two fields still matches when only one of them was written.
- `rev` is an opaque cursor. Revisions are monotonic but **sparse**, so treat one as a
  bookmark to hand back to `-since`, never as a count. `-ago 50` is the exception that
  proves it: it asks the store for the last fifty commits — resolved against its watermark and
  clamped to what it retains — rather than doing arithmetic on a revision, and how many events
  live in that span is still the store's business rather than the number's. It is its own flag
  because `-since -50` is not spellable: at a command line a leading dash is what an option
  looks like, so the relative cursor says so in its name.

A watch with a cursor replays: `-since <rev>` (or `-ago <n>`) delivers what the store
still holds from that revision before it streams anything new, so a reader that was away can
catch up rather than guess. A cursor it cannot replay from is refused rather than answered with
a stream carrying no history, and the refusal says which of two things happened: the revision is
below what the store still retains, so that history is gone — and the refusal names the commit
it can still serve from, with `-ago <n>` as the way to ask for what it kept without naming one;
or the store could not answer, in which case the cursor stands and the same request is worth
repeating. A catch-up costs what the path it is asked about holds, so watching
one path is cheaper than watching the verse in the replay as well as in the stream. With no
cursor there is nothing to catch up from and the stream starts at the moment it opens. The same level-triggered property is what makes an installed trigger act on
the backlog — a rule is a statement about the world, not about what happens next — and it is
why a long hold costs a cursor rather than a buffer. See
[change events](../change-events.md).

A change deeper than the path a watch asks about is reported against the node that path reaches
above it. Watching `verse-dev drift '*'` while something is written at `verse-dev.drift.head.abc`
gives

```tony
{op: set at: verse-dev.drift.head rev: 152 to: {abc: {sha: aaa}}}
```

— the node the path reaches is `head`, and what changed inside it, `abc`, is nested inside `to`.
Nothing is missed, and a rule watching that subtree still selects the deep node, because it
re-evaluates against the store rather than against the event's address alone.

A node is reported where the path reaches it, a scalar included. After `verse data a b -put c`,
`verse watch a b` reports a `set` at `a.b` holding `c` and `verse watch a` a `set` at `a` holding
`{b: c}`. `verse data -rm a b` is then a `delete` at `a.b` to a watch on `a b`. A rule reading
`demo.pr` is woken by a
write at `demo` as well as one under it, since that write replaces what the rule reads.

A `delete` carries no payload, so a `-if` filter does not match one: there is nothing left
to ask about. A node on its way out is also marked *vanishing* before removal, which is
what stops a rule from acting on — and thereby resurrecting — something that is leaving; see
[lifecycle](../trigger-model.md#lifecycle-how-an-entity-leaves).

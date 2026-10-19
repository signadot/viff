# Entities

An [entity](../glossary.md#entity) is anything the work refers to: a pull request, a build, a
person, a CI status, a running task. An entity has an [address](../glossary.md#address), such as
`demo.pr.1` or `repos.verse.drift.finding.abc`, and it holds a document.
[The data page of the tutorial](../tutorial/data.md) walks through this. This page is the whole
surface, with the details the tutorial leaves out.

**An entity is not a kind of node.** "Entity" is the word for a [node](../glossary.md#node)
that a question is about:

- the subject that a [rule](../glossary.md#rule) selects;
- one row of a listing;
- one event of a watch.

Each of those questions carries an address, and the address says the depth. There is no fixed
entity depth: no `system`, `kind` and `id`, and no length other than the length of the address.

## Addressing

An address is a path in a tree.

An address has two spellings. They are one grammar, written in two ways:

```
demo.pr."1"                a kinded path — any depth, and it carries on into the payload
demo pr 1                  steps — one shell word per segment, any depth
```

A [kinded path](../glossary.md#kinded-path) is the spelling a parser reads. Its grammar is
[kpath](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath). Quoting says where
a segment ends; nothing scans a word for a delimiter. A segment that would read as something else
is quoted, and it reads back as itself:

```
demo.pr."a.b"              the name `a.b` — quoted, so the dots are in the name
demo.pr.a.b                four segments, because these dots are not quoted
demo.pr."1"                the name `1`, quoted so it is a name rather than a number
git.ref.x@a:b              `@` and `:` are ordinary characters in a name; nothing reads them
demo.pr.org/repo#42        …and so are `/` and `#`
verse.task.instance.run-a1b2   a leading `verse` is a segment like any other
```

Which spelling goes where:

| Spelling | Where |
|---|---|
| a path | Anywhere a parser reads one and no shell is involved: inside a document (`at: demo.pr."1"`), in a charter (`subject: demo.pr.1`), on the wire (`?at=`). It has any depth, and it continues into the payload: `demo.pr."1".labels.urgent`. |
| steps | At a command line, where the shell's quoting is the only quoting: `verse data repos verse drift finding abc`. Verse takes steps as input, and every listing answers with steps. |
| the same path | In a charter, as the `path` of a match beside a predicate: `{path: "repos.verse.drift.finding.*", value: {…}}` |

A listing answers with steps, the same spelling that the command line takes. So you can pass a
line of a listing straight back:

```sh
verse data -paths demo pr '*'       # demo pr 1
                                    # demo pr a.b
                                    # demo pr 'a b.txt'

verse data -paths demo pr '*' | xargs -L1 verse data
```

`-kpaths` gives the same listing as one kinded path per line. Use it when you keep an address in a
shell variable. A line of steps works only if something splits it into words, and a variable
passed as one argument is not split:

```sh
verse data -kpaths demo pr '*'      # demo.pr."1"
                                    # demo.pr."a.b"

ref=$(verse data -kpaths demo pr '*' | head -1)
verse data -at "$ref"               # …one argument, and it reads back
```

What goes in is what comes out. A [step](../glossary.md#step) is a whole word, so a dot inside a
step is part of a name and needs no quoting. This holds at every position:

- `verse data dotted.sys pr 1` begins with the one segment `dotted.sys`;
- `verse data a.b.c pr 1` begins with the one segment `a.b.c`.

You need the shell's quoting only where the shell would split or expand a segment: a space, or a
`*`. Use single quotes. The shell expands nothing inside them, and `xargs` understands them. A
name that a step clause below would read as something else also needs the quoted clause inside
the single quotes.

**A positional argument is one step and nothing else.** There is no delimiter scan on the
command line. So `verse data demo.pr.1` names the one segment `demo.pr.1`. The reason is under
[Why it is this way](../design/reference-entities.md#a-positional-argument-is-one-step).

A step names one place, and its first character says which kind of place. There are four
clauses, and none of them splits a word:

```sh
verse data demo pr 1 votes '[0]'     # the array element   — also {0} sparse, (ana) keyed
verse data demo pr '*'               # every field there   — the segment wildcard, for a read
verse data demo pr 1 '"[0]"'         # the field named [0] — the name inside the quotes
verse data demo pr 1 labels urgent   # a name, as given
```

The bracketed forms open and close, so verse does not guess where one ends. A delimiter would
change **how many** segments a word is; a bracketed form does not. One word goes in, and one
place comes out, whatever is inside the word.

The quoted clause is the escape, and it works for every name. A name that reads as an access or
as the wildcard, or that begins and ends with a quote, goes inside one more pair of quotes:
`'"[0]"'`, `'"*"'`, `'""x""'`. A listing prints it that way, so the line still passes back. In
practice you address array elements; verse writes no field named `[0]`.

A whole address in one token goes after `-at`. The flag declares which grammar applies:

```sh
verse data -at demo.pr.1            an address copied out of a document
verse data -at 'demo.pr.*.stage'    …wildcards and all
```

Give an address once: as steps, or with `-at`, never both.

### What stands at a path

A **node** is whatever stands at an address: an object, an array, a scalar, a null, an empty
object. All of them count the same. **An address stands** when the store holds a node there.
The store answers not-found or path-conflict, and that answer is the only definition of absence.

So a listing is every node that its [pattern](../glossary.md#pattern) reaches, and nothing
else. Empty objects, nulls and scalars are included. A scalar two segments deep is not in the
listing of a three-segment pattern. It is a node, and two reads answer it: a read of its own
address, and a read of a pattern that carries `..`.

### The two questions asked of a pattern

Verse asks two questions of a pattern, and it asks each question in one way only:

| Question | How verse asks it |
|---|---|
| **reach**: does this pattern reach that address? Is this write inside that confinement? | kpath's, `Matches` and `MatchesPrefix` |
| **overlap**: can two patterns reach a common address? Two strata, the claims of two iods | one walk to the shorter length, asking at each position whether the two segments can meet |

The claim of an [iod](../glossary.md#iod) may put a literal prefix where kpath has a field
(`ws/*`). It is still a match on the field name at that position. The structure around it is
kpath's: its length, its wildcards, its `..`. The subject of a charter is a kinded path and takes
no operator: see [addressing](#addressing).

> **A limit of descent.** Descent in the tree does not follow this model. A match compares a
> segment as a name, so `repos.verse..finding` and `repos.verse.drift.finding` do not meet, though
> the first descends to the second. kpath answers false for any pattern that holds `..`, so
> containment does not use kpath for it. The walk behind `List` also keeps one fixed arity
> (`listWalkArity`), because a listing that names no address has nothing to count from; every
> other walk says how far *below its own place* its records stand. The issues are `jabt2frr` here
> and `17hj5ygkh12ks7j7n5n0` upstream. While they are open, this section says what a descent is
> meant to do, not what it does.

### `*`: three layers, three rules

A `*` passes through three layers, and each one reads it differently:

1. the shell;
2. the address grammar;
3. a match in a charter.

The three agree when you see them together. One at a time, each can surprise you.

In the address grammar, a whole `*` segment is the wildcard in both spellings. The name that is
an asterisk is quoted in both spellings.

```sh
verse data -at 'demo.pr.*'        # every name under demo.pr — a segment wildcard
verse data demo pr '*'            # the same, as a step
verse data -at 'demo.pr."*"'      # the one node whose name IS an asterisk
verse data demo pr '"*"'          # the same, as a step — the quoted clause
verse data demo pr 1 votes '[*]'  # every element of an array, which a `*` step is not
```

Only a whole segment selects. `hotfix-*` is a name with an asterisk in it: as a step, in `-at`
and in the kinded path of a charter. A `*` selects every *field*, which is what the names under
a node are. `[*]` selects every array element. So the two are different clauses. A write names
one place, so a write refuses a `*` step, and the refusal names the quoted form.

A family of names is not a segment. An address names places. Which of the places a rule wants
is a predicate on what each place holds:
`{path: "demo.pr.*", value: {branch: !glob "hotfix-*"}}`. The one match that an address can
spell is the quoted clause, and it names exactly one thing: `demo.pr."*"` is the node called `*`
and nothing else.

The shell reads the line first, so an unquoted `*` never reaches verse. It fails in two ways,
and the second is the one to watch for:

```sh
verse data demo pr * stage        # the word IS the glob: it becomes every file in the cwd
                                  #   argv: demo pr demo.pr.1.stage Makefile notes.txt stage
verse data -at demo.pr.*.stage    # a glob inside a word: it expands only if a file matches,
                                  #   and then silently becomes that file
                                  #   argv: -at demo.pr.1.stage
                                  # with nothing matching, bash passes it through (right by
                                  #   luck) and zsh refuses the command outright
```

Quoting fixes both, in either spelling. `demo pr '*' stage` and `-at 'demo.pr.*.stage'` give
verse the asterisk itself. The single quotes belong to the shell, so verse sees a bare `*`
segment, which is the wildcard.

`'"*"'` is a different address, not a safer way to write the same address. The inner `"*"` is
the quoted clause of the address grammar: the name that is an asterisk. The outer `'…'` is the
quoting of the shell, which keeps the inner quotes intact. `-paths` prints such a node this way.
So a listing passes back to that one node, and not to every name beside it:

```sh
verse data demo pr '*'            # the wildcard: every name under demo.pr
verse data demo pr '"*"'          # the one node named `*`
verse data --paths demo pr '*' | xargs -L1 verse data   # each line reads back as what it names
```

In a charter, an address is the same kinded path that a read takes. The quoting means the same
thing there, and the same characters mean the same thing in both places:

```tony
subject: "demo.pr.*"            # any name
subject: 'demo.pr."*"'          # the name that IS an asterisk — `verse data -at` reads it so too
subject: 'demo."..".x'          # the name that is two dots, where a bare `..` descends
```

### `verse` at the front

Everything verse keeps about its own running is under `verse`
([below](#two-halves-one-tree)). `verse` is a segment like any other in every spelling, and it
counts like any other:

```sh
verse data verse task instance run-a1b2       # steps
verse data -at verse.task.instance.run-a1b2   # …the same address copied out of a document
verse data verse task instance '*'            # …and every task record under it
```

**What takes what:**

| Command | What it takes |
|---|---|
| `verse data` (reading, `-put` and `-rm`), `verse watch` | steps, several arguments at any depth; or one kinded path after `-at` |
| `verse flow show <subject>`, and `--subject` on the flow verbs | one token: a kinded path, `*` for any segment |
| `verse runner/io/resolver show/check/remove` | one token: the name, or the address as stored (`verse.runners.<name>`) |
| `verse charter [<address>]` | an address in the charter tree, any depth, as steps: `verse charter repos verse drift` |
| `verse c <address> rules <name>` | a path: the address, then into it |
| `verse perms <step>…` | the node a record is about, as steps |

`verse charter` and `verse perms` read steps as `verse data` does. A word is one segment, and
`'"x"'` is the name `x`. But these two commands name one place made of names. So they refuse an
access (`'[0]'`) and a `*` step.

<a id="two-halves-one-tree"></a>
## verse's own records, and the world's, in one tree

The top of the tree has one reserved key: `verse`. Everything verse writes about its own running
is under `verse`. Everything the world is about is beside it, at whatever address a writer
chooses.

```
demo.pr.1                          what the world is about
verse.task.instance.run-a1b2       verse's own
verse.trigger.rule.review
```

These are under `verse`:

| Address | What it holds |
|---|---|
| `task.instance.*` | the mirror of every running task |
| `task.run.*` | the record of a run that happened |
| `task.claim.*` | the run claim of a sequence |
| `ask.*` | the docket: every ask, whoever raised it — a person, a rule's gate, a task's question |
| `resolvers.*` | who answers the gates |
| `covers.*` | who watches what: one record per resolver and gate stream (see [resolvers.md](resolvers.md)) |
| `trigger.rule.*` | the installed charter. A rule installed at an address is `trigger.rule.<package>:<name>`. |
| `trigger.strata.*` | the declared levels of the charter |
| `trigger.package.*` | what each package declares it reads |
| `runners.*` | where work can run |
| `io.*` | the installed [iods](../glossary.md#iod): each writes an external system into the store and carries writes back out to it |
| `schedule.<name>` | a schedule: its period, its phase, and the `next` that the daemon is armed for. Under `schedule.<name>.tick.<instant>` stands the tick, written only by the schedule. |
| `schedule.date`, `schedule.time` and the other calendar facts | what the base charter derives from the hourly tick |

The ballots of a [question](../glossary.md#question) are on its record
(`ask.<id>.ballots.<who>`). The answer path that counts them writes them.

Two more are about who.

`principal.<kind>.<name>` lists every [principal](../glossary.md#principal) that has presented
itself, by the kind its name carries:

- `user` for a person, by email;
- `runner` for a worker that claimed work;
- `agent` for an operator agent.

`perms.node.<address>` is the [mirror of permissions](../glossary.md#mirror-of-permissions).
It has a [record](../glossary.md#record) wherever ownership changes down the tree, and on a node
created with grants ([Creating with grants](#creating-with-grants)). The record is
keyed by the node's own address, spelled as one segment. It holds:

- `owner`;
- the `grants` that its owner holds out;
- for a question, `under`: the address of what posed the question.

Only versed writes the mirror, and only the system reads it. Who owns a node, and what a write
or a read is checked against, is in [perms.md](../perms.md).

You read what is under `verse` like anything else. Only the verbs that own it write it.
`verse data verse trigger rule review` reads a rule. Verse refuses `verse data -put` at any
address under `verse`, and says which verb to use instead: `verse c -a`, `verse resolver add`,
`verse runner add`, `verse io add`, `verse question approve`.

Each verb makes its own checks: what a rule may read, what a runner is, whether the claim of an
iod overlaps another. Under every verb, the store checks the write against what its place
permits ([perms.md](../perms.md)). So no write that reaches the store can install a rule, stamp a
task record or answer a question for somebody else.

A charter that reacts to the state of verse names `verse` first
([what a match can say](condition.md#what-a-match-can-say)).

A rule reads what is under `verse`, and does not write it. A `patch` of a subject under
`verse`, or a `derive` into it, is the same write the API refuses, by another route. Verse
refuses it the same way:

- at install, where the rule names the address (403);
- at the write, whatever address it arrived at.

A spawn about such a subject is allowed: the task record is verse's own write. The system's
charter is the exception, because it is how the system writes there: the base charter derives
the calendar under `verse.schedule`.

## Reading

A verse is one document, a tree, and `verse data` answers it at an address. The address is a
[kinded path](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath) in any of
the spellings [above](#addressing). It is the same address at every length: whatever node stands
there, including a place inside a payload.

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

The line with the wildcard has a spelling in steps too: `verse data demo pr '*' stage` is the
same read. `'"*"'` is the name that is an asterisk. See
[`*`: three layers, three rules](#-three-layers-three-rules).

### `--depth`: how far beneath the address

A listing walks everything beneath its address, into payloads and arrays. Nothing in an answer
says where an entity ends. `--depth` limits the walk: one level gives what is at that level, and
leaves out what is deeper. **It is counted from the address the path names**, so the same number
means the same thing at every address:

```sh
verse data demo --paths --depth 1        # demo's children
verse data demo pr --paths --depth 1     # …one level down, and the SAME number
verse data demo pr --paths --depth 0     # the address alone: nothing beneath it
verse data demo pr --paths               # everything beneath, into payloads
```

To walk deeper, add a step, not a step and a bigger number. An access is a step like any other,
so `--depth 2` under `merge proposal X` reaches `votes[0]`, and not the fields inside it. Verse
refuses a negative depth, and a `--depth` with no listing to limit; it does not ignore them.

**The number belongs to the store.** Verse passes it on as you gave it, whatever the path holds: a
place, a `*`, a `..`, or `--if` beside it. The store asks the predicate of each node the path
reached. The listing is the nodes the predicate kept, and what is beneath them.

```sh
verse data demo pr '*' --paths --depth 0                # the nodes the path reached, alone
verse data demo pr '*' --paths --if '{stage: open}'     # …the open ones, and what is in them
verse data demo '..' stage --paths --depth 1            # demo.stage and demo.<x>.stage
```

With a `..` in the path, the depth is **one budget for the whole path**, counted from the node the
path names. Only the segments a descent *takes* use the budget. So `demo '..' stage` at depth 1
reaches `demo a stage`: the descent took `a`, and the literal `stage` cost nothing. The budget is
then used up, so the listing shows nothing beneath it. This is
[logd's rule](https://signadot.github.io/tony-format/).

### `..`: every depth beneath

A `*` opens one position. `..` opens every depth beneath where it stands, including the node it
stands at. It asks a question with no depth at all:

```sh
verse data '..'                              # the whole verse, walked
verse data demo '..'                         # demo and everything under it
verse data demo '..' -if '{stage: open}'     # every node that says it is open, at any depth
verse data demo '..' stage                   # every `stage` beneath demo
verse data demo '".."'                       # the node whose NAME is two dots
```

The answer nests each node where it stands, **spelled in full**. `demo '..' stage` answers
`{pr: {"1": {stage: open}}}`, not the `{"1": open}` that a `*` gives. A descent reaches nodes at
different depths, and without the full spelling two of them could land on one key. A node the
read keeps whole already holds what is beneath it, so nothing is answered twice.

`..` is a *query*, so it belongs to reads, watches and the subjects of rules. A write, a claim, a
grant and a permissions record each name one stored place, and each refuses `..`.
`verse watch demo '..'` reports every node beneath demo that changes, at the depth where each
one stands. The subject of a rule may descend the same way (see
[the charter reference](../trigger-model.md)).

The answer is the subtree at the address, as the store holds it, not a shape built for the wire.
So a name is a key, not a token inside a rendered `at:`. For the addresses, use `-paths`: a
script gets them one per line.

**An address that names nothing answers 404, at every length**, and the command exits non-zero.
`{}` is a document that somebody wrote. A read never answers it for an address nobody wrote,
however long the address is. `demo.pr.nosuch` is not there, and neither is `nosuch.pr`.

    verse data nosuch            in your view, nothing stands at nosuch
    verse data demo pr nosuch    in your view, nothing stands at demo.pr."nosuch"
    verse data                   in your view, the verse is empty: nothing has been written yet

The message says "in your view" because a read answers what its reader may read. A wider read
leaves out a node you may not read, so that node is not in your view. A read *at* an address you
may not read into is different: verse refuses it, and the refusal names the nearest owner
([permissions](perms.md#what-is-checked-on-a-read)).

A `*` reaches the children of the node it lands on. Over a node with no children it reaches
nothing. `verse data solo '*'` over `solo: {}` is a 404 too. Over `solo: {hollow: {}}` it
answers `{hollow: {}}`.

An *empty document* means something else: the path reached nodes, and a `-if` predicate kept
none of them. That answer is about the filter, not the path, so it is a 200 with `{}`. A read
reports null only where somebody wrote a null.

`-if` keeps some of the nodes the path reached. It works per node, and never prunes inside a node
it keeps. **`-if` asks about what the path names.** A path of N segments asks about the nodes N
segments down, for a name and a `*` alike, so the `*`s a path carries set the depth of the
question. An issue the git-issue iod writes stands at `git-issue.<repository>.issue.<id>`, so the
`*` makes each issue the thing asked about:

```sh
verse data git-issue verse issue '*' -if '{status: open}'         # each open issue
verse data -at 'git-issue.verse.issue.*' -if '{status: open}'     # the same
verse data git-issue verse issue -if '{status: open}'             # the map of issues, as one: {}
```

`-trim` projects the same nodes. `-if` is a
[tony match](https://signadot.github.io/tony-format/) against the payload, the same predicate a
charter carries in `value`. So a read answers "what would a rule with this predicate select?".
The operators (`!or`, `!and`, `!not`, `!glob`, presence and absence) belong
to tony, and are documented with
[mergeop](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop). What a match can
say inside a rule is in [the condition of a rule](condition.md#what-a-match-can-say).

`-if-at` roots the predicate at a dot-separated field path. See
[Selecting by what an entity says](README.md#selecting-by-what-an-entity-says).

### Reaching into an array

`[0]` addresses an array element by position:

```sh
verse data merge proposal X votes '[0]'
verse data merge proposal X votes '[0]' choice
verse data -at 'merge.proposal.X.votes[0].choice'     # the same, as one token
```

A step that begins with `[`, `{` or `(` **is** that access. `-at` carries the kinded path whole.
Quote the step for the shell, because `(` and `[` mean something to the shell too.

**An element is addressed by position, and only by position.** An element by identity is written
`(key)`, and only the store can provide that: the store, logd, keys an array in its schema, and
the schema makes a name mean an element. logd refuses a position on a keyed array. Verse declares
no such schema, so every array it holds is positional.

Verse **refuses** a `!key(<field>)` tag in a written payload. `!key` is a tony merge operator. If
verse accepted it, the store would hold a positional array while the value claimed a name for each
element. There is one keying, and it belongs to the store.

A listing spells what a read takes. So `verse data merge proposal X --paths` prints
`merge proposal X votes '[0]'`, and that line reads and writes as it stands.

A write reaches an element too, and changes it in place:

```sh
verse data merge proposal X votes '[0]' choice -put '"approve"'
```

The store guards such a write, so the CLI does not:

| A write at | What logd does |
|---|---|
| `votes[1].choice`, in range | patches it, and it reads back |
| `votes(ana).choice`, keyed | refuses it at the commit; nothing is harmed |
| `votes[2]` where two stand | refuses it at the patch, naming the length |

`entity/arraywrite_test.go` tests all three answers, so each go-tony upgrade checks them again.
Why the CLI relies on the third row is under
[Why it is this way](../design/reference-entities.md#the-cli-and-array-writes).

A precondition applies to an element write as to any other write. It is about the *element* the
write names, not the entity that holds it:

```sh
verse data merge proposal X votes '[0]' -expect '{by: ana}' -put '{choice: approve}'
```

`-create`, `-update` and `-expect` reach the element with tony's `!at`, which walks the path and
applies the pattern where it lands. So a precondition on one ballot says nothing about the
others. (An array-shaped pattern would: tony matches an array by position and by length, so it
would also require the array to hold exactly that many ballots.)

`-create` is different, because an element cannot be created in place. Where nothing stands, the
precondition holds, and the store refuses the write instead, naming the length of the array.
Where a ballot does stand, `-create` refuses to commit over it, as it does anywhere else.


## Writing

A put merges: it leaves the fields it does not name alone. So a put answers with what stands at
the address it wrote, not an echo of what it sent, and two writers that advance one entity do not
overwrite each other.

The address may be as deep as the tree. `verse data repos verse drift finding abc -put '{status: open}'`
writes there, and the answer is that place. A write is a value at an address, so it does not have
to decide where an entity "ends".

```sh
verse data demo pr 1 -put '{stage: open}'       # as steps: nothing needs quoting
verse data -at demo.pr.1 -put '{owner: ana}'    # …or in one token: adds an owner, keeps the stage
verse data demo pr 1 labels urgent -put true    # write inside the payload
verse data demo pr 1 labels urgent -rm          # remove one field
verse data demo pr 1 -rm                        # the whole entity
```

The payload follows `-put`. Verse reads it as tony, unless `-ifmt` or the config says
otherwise. [representation.md](representation.md) has the formats verse reads.

A write is a tony *patch*, so the
[mergeop](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop) operators apply to
it. `!delete` removes a key, which setting fields cannot do. A charter's `patch` is the same
operation, written by a rule.

### What a write descends through

A write names a place inside a payload by naming the fields on the way to it. Each field on the
way must be somewhere a field can live:

| What stands on the way | What the write does |
|---|---|
| nothing yet | builds it |
| an object | merges into it |
| an **array or a scalar** | verse refuses the write |

```sh
verse data demo pr 1 -put '{votes: [{by: ana}, {by: bo}]}'
verse data demo pr 1 votes bo -put '"yes"'
# the write would replace what it descends through: demo.pr."1".votes holds an array,
# so "bo" is not a place in it — write the whole value AT demo.pr."1".votes, or address
# an element of it
```

To replace the array, write a put *at* the array:

```sh
verse data demo pr 1 votes -put '[{by: ana}, {by: bo}, {by: cy}]'
```

The check is a precondition carried with the write, so verse decides it at the commit, and
`-create`/`-update`/`-expect` combine with it. `-create` does not replace it: `-create` asks
whether the *node* is absent, and under an array the node is absent, so `-create` alone would
admit exactly the write that does the damage.

A charter's `patch` is checked the same way, one step looser. A `patch` is a document, not an
address, so a nested key the subject does not have is built, and verse refuses only a patch that
lands on an array or a scalar. When verse refuses a rule's patch, the firing fails and the subject
is unchanged. The rule still selects the subject, and the next commit tries again.

The reason verse refuses a write through an array is under
[Why it is this way](../design/reference-entities.md#a-write-through-an-array).

### Creating with grants

A node you create holds out what the nearest `child-grants` above it says
([perms.md](../perms.md#grants)). To create it holding out something else, give `-grants` with
the put. It takes the list `verse perms -grants` takes, and `[]` holds out nothing.

```sh
verse data notes ana todo -put '{todo: "buy milk"}' -grants '[]'   # nobody else reads it
verse data notes ana plan -put '{due: friday}' -grants '[{read: [user:bo@example.com]}]'
```

The node and its [record](../glossary.md#record) land in one commit, so no reader and no rule ever
sees the node holding out the default.

A put with `-grants` is a create. Where the node stands already, verse refuses the write and
leaves the node as it is; change what a standing node holds out with `verse perms`. So `-grants`
beside `-update` or `-expect` is a usage error. You may give only what an owner may set on the
node's record, and only where the node will be yours: a writer who creates on `read-write` makes
a node that the owner above owns, and verse refuses their grants. Grants change nothing about who
may create there. A `child-owner` line held out to a group makes the member
who wrote the owner, never the group, and the line does not apply above somebody else's place
([perms reference](perms.md#creating-and-taking-a-place)).

## Conditional writes

Verse checks each condition *with* the write, in one step at the store, so nothing can change in
between. A refused write leaves the entity unchanged, and the command reports it and exits
non-zero.

```sh
verse data -create demo pr 1 -put '{stage: open}' # only if nothing is there yet
verse data -update demo pr 1 -put '{stage: open}' # only if something is
verse data -expect '{stage: draft}' demo pr 2 -put '{stage: open}' # only if it still looks like this
# write refused: a condition did not hold
```

The three flags `-create`, `-update` and `-expect` are preconditions on a write. `-if` is a
different thing: it filters what a read answers.

This is compare-and-swap (CAS), written as a predicate rather than a revision number: the write
says what it believes about the entity, not which revision it read.

## Watching

```sh
verse watch                                        # from now on
verse watch -if '{stage: open}' demo pr '*'        # the same selectors as a read
verse watch -since 42                              # replay from a revision already in hand
verse watch -ago 50                                # …or the last 50 commits, whatever the head is
verse watch -since 0                               # replay everything the store still holds
```

With no cursor, a watch starts from now. A cursor asks for history, which is a different act from
watching. The reason is under [Why it is this way](../design/reference-entities.md#no-history-without-a-cursor).

A watch at an address opens with what stands now, which is not history. It sends a `set` for
each node the address reaches, at the revision where the watch opened, and then the changes. So
`-until` over a state already reached ends at once, and does not wait for it to happen again.
`-no-init` starts from the next change instead.

A watch with no literal prefix (`verse watch`, `verse watch '*' …`) joins the stream of the
whole verse. That stream has no opening state, and a cursor replays history in its place.

```tony
{op: set at: demo.pr.1 rev: 1 to: {stage: open owner: ana}}
{op: delete at: demo.pr.9 rev: 7}
```

There is one line per change, in flow form, so you can grep a watch.

| Field | Meaning |
|---|---|
| `op` | `set` (created **or** updated) or `delete` |
| `to` | the whole state of the entity as of that commit, not the delta that was written |
| `rev` | an opaque cursor |

Verse does not tell a create from an update: a create is a patch onto nothing.

`to` is the whole state, so a reader that matches on two fields still matches when a write
changed only one of them.

Revisions increase, but they are **sparse**. Treat a `rev` as a bookmark to pass back to
`-since`, never as a count.

`-ago 50` asks the store for the last fifty commits, limited to what it retains. The store
resolves it; `-ago` does no arithmetic on a revision. It is its own flag because `-since -50`
cannot be written: a leading dash marks an option.

A watch with a cursor replays. `-since <rev>` (or `-ago <n>`) delivers what the store still
holds from that revision, then streams what is new. So a reader that was away catches up.

Verse refuses a cursor it cannot replay from, rather than answer with a stream that has no
history. The refusal says which of two things happened:

| What happened | What the refusal says |
|---|---|
| The revision is below what the store still retains, so that history is gone. | It names the commit the store can still serve from. `-ago <n>` asks for what the store kept without naming a commit. |
| The store could not answer. | The cursor is still valid, and the same request is worth repeating. |

A catch-up costs what the watched address holds, so a watch of one address costs less than a
watch of the whole verse, in the replay as in the stream.

The same property makes an installed rule act on the backlog: a rule is a statement about the
world, not about what happens next. For the same reason, a long hold costs a cursor and not a
buffer. See [change events](../change-events.md).

A change deeper than the address a watch asks about is reported at the node the address reaches
above it. A watch of `repos verse drift '*'`, while something is written at
`repos.verse.drift.finding.abc`, gives:

```tony
{op: set at: repos.verse.drift.finding rev: 152 to: {abc: {status: open}}}
```

The node the address reaches is `finding`. What changed inside it, `abc`, is nested inside `to`.
Nothing is missed. A rule over that subtree still selects the deep node, because the rule
evaluates again against the store, not against the event's address alone.

A watch reports a node where the address reaches it, including a scalar:

- After `verse data a b -put c`, `verse watch a b` reports a `set` at `a.b` holding `c`.
- `verse watch a` reports a `set` at `a` holding `{b: c}`.
- `verse data -rm a b` is then a `delete` at `a.b` to a watch on `a b`.

A write at `demo` wakes a rule that reads `demo.pr`, as a write under `demo.pr` does. The
write at `demo` replaces what the rule reads.

A `delete` carries no payload, so a `-if` filter does not match it. Before a removal, a node on
its way out is marked *vanishing*, so a rule does not act on it and bring it back; see
[lifecycle](../trigger-model.md#lifecycle-how-an-entity-leaves).

## Why it is this way

The reasons for this design are in [reference-entities.md](../design/reference-entities.md), in the repository.

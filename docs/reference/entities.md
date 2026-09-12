# Entities

An entity is anything the flow refers to — a PR, a build, a person, a CI status, a running
task — addressed by a path (`demo.pr.1`, `verse-dev.drift.head.abc`) and carrying a document.
[Tutorial §2](../tutorial.md#2-entities) drives all of this; here is the surface flat, with
the corners that section leaves out.

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
demo.pr."a.b"              the id `a.b` — quoted, so the dots are in the name
demo.pr.a.b                four segments, because these dots are not quoted
demo.pr."1"                the id `1`, quoted so it is a name rather than a number
git.ref.x@git:ref:y        `@` and `:` are ordinary characters in a name; nothing reads them
github.pr.org/repo#42      …and so are `/` and `#`
verse.task.instance.run-a1b2   a leading `verse` is the reserved root, a level like any other
```

Which spelling goes where.

| | |
|---|---|
| a path | anywhere a parser reads one and no shell is involved: inside a document (`at: demo.pr."1"`), in a charter (`subject: demo.pr.1`), on the wire (`?at=`). Any depth, and it carries on into the payload — `demo.pr."1".labels.urgent` |
| steps | at a command line, where the shell's own quoting is the only quoting there is: `verse data verse-dev drift head abc`. Taken on the way *in*, and what every listing answers with |
| a vector | in a charter, where an address is a list of segments: `!verse-path [verse-dev drift head "*"]`, or `path: [verse-dev, drift, head, "*"]` |

A listing answers with steps, the same spelling the command line takes, so a line hands
straight back:

```sh
verse data --paths demo pr          # demo pr 1
                                    # demo pr a.b
                                    # demo pr 'a b.txt'

verse data --paths demo pr | xargs -L1 verse data
```

`--kpaths` is the same listing as one kinded path per line, which is the spelling that survives
being captured into a shell variable — a line of steps only reaches a read if something splits
it, and a variable passed as one argument does not:

```sh
verse data --kpaths demo pr         # demo.pr."1"
                                    # demo.pr."a.b"

ref=$(verse data --kpaths demo pr | head -1)
verse data --at "$ref"              # …one argument, and it reads back
```

One spelling, in and out. A step is a whole word, so a dot inside one is a character in a
name and needs no quoting — which is the ergonomic win, and it holds at every position: `verse
data dotted.sys pr 1` is three segments, and `verse data a.b.c pr 1` names the system `a.b.c`.
The shell's own quoting is applied only where a segment would otherwise be split or expanded (a
space, a `*`), and it is single quotes, which are literal — so nothing inside is globbed or
substituted, and `xargs` understands them.

A positional is a step and nothing else, because it cannot be both. A single word `w` has
two legal readings — the one segment `w`, and `split(w, delim)` — and choosing either makes the
other unsayable. So there is no delimiter scan on the command line at all, and `verse data
demo.pr.1` names a system called `demo.pr.1`.

A step is one place, and its first character says which. Three clauses, no splitting:

```sh
verse data demo pr 1 votes '[0]'     # the array element   — also {0} sparse, (scott) keyed
verse data demo pr 1 '"[0]"'         # the field named [0] — the name inside the quotes
verse data demo pr 1 labels urgent   # a name, as given
```

The bracketed forms are *self-delimiting* — they open and close — so recognising one needs no
guess about where the word ends. That is what tells this apart from reading a delimiter out of a
word: a delimiter changes how **many** segments a word is, and this does not. One word in, one
place out, whatever is inside it.

The quoted clause is the escape, and it costs the spelling of a field whose name literally
begins and ends with a quote. Array access is what people address; a field named `[0]` is not,
and verse writes none.

A whole address in one token is taken where it is marked, which is unambiguous for the
reason the positional is not — a flag is a different *place* on the line, not a second reading
of the same word. Which grammar applies is declared rather than guessed:

```sh
verse data --at demo.pr.1           an address copied out of a document
verse data --at 'demo.pr.*.stage'   …and the one spelling that carries a wildcard
```

An address is given once: as steps, or as `--at`, never both.

### `*`: three layers, three rules

A `*` passes through the shell, the address grammar and a charter match, and each of the three
reads it differently. They are consistent once seen together, and surprising one at a time.

In the address grammar, a bare `*` is a wildcard only where the spelling is a path.

```sh
verse data --at 'demo.pr.*'     # every id under demo.pr — a segment wildcard
verse data --at 'demo.pr."*"'   # the one entity whose id IS an asterisk
verse data demo pr '*'          # the same entity — a bare `*` step is a name, never a pattern
verse data demo pr 1 votes '[*]'  # …but a bracketed one is an access, and steps carry it
```

A bare `*` is not self-delimiting — it is a plausible id and the shell expands it — so it stays
a name in a step, and a segment wildcard is written with `--at`. That is why the `verse flow`
verbs take one token and refuse a second: a selector is exactly the case steps cannot express.

The shell gets there first, and a bare `*` as its own word is the dangerous one. Inside a
longer token it usually matches no file and is passed through (bash) or refused (zsh); standing
alone it matches *every file in the directory*:

```sh
verse data demo pr * stage        # bash: expands to every filename in the cwd
verse data --at demo.pr.*.stage   # …and so does this, unquoted — the shell does not care
                                  # which flag it is after
```

So quote it, in either form — `--at 'demo.pr.*.stage'`. `--paths` prints it quoted for the same
reason, which is why a listing containing such an id pipes back correctly.

In a charter, quoting does not change it. Tony quoting is about a value's type, not about
matching, so `"*"` and `*` are the same string and a bare `*` segment means any segment. Naming
the literal asterisk takes an operator:

```tony
path: [demo, pr, "*"]           # any id — the quotes change nothing
path: [demo, pr, !or ["*"]]     # the id that IS an asterisk
path: [demo, pr, !glob "[*]"]   # …the same, spelled as a glob
```

An id may itself glob (`demo.pr.hotfix-*`), so `verse flow demo.pr.*` and a charter's
`subject: demo.pr.*` mean the same set — and the `verse flow` verbs take one token and refuse a
second rather than reading it as a step, because a selector is exactly the case steps cannot
express.

In a charter, `!verse-path [demo pr 1]` is the steps spelling: the tag is required, because a
bare list where an address belongs would be a second thing a bare list can mean.

### The reserved root

One name at the front is not a system: `verse` is the reserved root, and everything verse
keeps about its own running lives under it ([below](#two-halves-one-tree)). It is a segment like
any other in all three spellings, so an address in verse's half is four segments rather than
three:

```sh
verse data verse task instance run-a1b2       # steps
verse data --at verse.task.instance.run-a1b2  # …the same address copied out of a document
verse data verse task instance                # …and a prefix of it: the whole kind
```

Two consequences worth knowing in advance:

- No world system may be called `verse`. `verse x 1` does not parse as an entity — it reads as
  the root plus two segments, and says so: *an address starts with system kind id, and
  "verse x 1" names 3 of the 4*.
- The address parses; the write is refused. `verse data verse task instance x '{…}' --put` is
  a well-formed address and a 403: what verse writes about itself is written by the verbs that
  own it (`charter -a`, `runner add`, `source add`, `gate approve`). Reading is unrestricted.

In a charter it is the first segment of the path — `{path: [verse, task, instance, *]}` — and a
path that does not start with it is about the world's half, so a rule that means to watch
verse's own state says so.

In a charter an address is a `path:`, and every segment of it is a match in tony's own grammar,
asked of that one segment:

```tony
subject: {path: [verse-dev, drift, head, "*"], value: {reviewed: pending}}
subject: !verse-path [verse-dev drift !glob "head-*" "*"]
subject: {path: [demo, pr, 1]}
```

So `!glob "hot-*"`, `!or [hot, cold]` and `!and [!glob "h*", !not hold]` all say at a position
what they already say about a payload. Nothing in verse enumerates the operators; `tony.Match`
owns them. One convention sits above that: a bare `*` is any segment, and inside an operator a
`*` is an ordinary character — which is how an id that *is* an asterisk is named (`!glob "[*]"`).

A `*` is a position, which is why the length of the path is the depth it selects at:
`[demo, pr, "*"]` picks three-segment entities and `[demo, pr]` is the place above them.

`{system, kind, id}` is still read as the first three positions of a path — a shorthand the
grammar keeps for the charters written in it — and `System()`/`Kind()`/`ID()` answer from
whichever spelling was used. It is not what to write: a path says the depth, and the shorthand
can only ever mean three.

**What takes what:**

| | |
|---|---|
| `verse data`, `data/put/rm`, `verse watch` | any spelling; steps may be several arguments, at any depth |
| `verse flow show/pause/resume/cancel/say` | one token — a selector (see above) |
| `verse runner/source/resolver show/check/remove` | one token: `<kind>.<name>`, or the address as stored |
| `verse charter [<address>]` | an address in the charter tree, any depth: `verse charter verse-dev drift` |
| `verse c <address> rules <name>` | a path: the address, then into it |

## Two halves, one tree

The root has one reserved key. Everything verse writes about its own running lives under
`verse`; everything the world is about lives beside it, at whatever address a writer chooses.

```sh
verse data --paths
# demo.pr.1                          the world's half
# verse.task.instance.run-a1b2       verse's own
# verse.trigger.rule.review
```

Under `verse` are `task.instance.*` (the mirror of every running task), `task.run.*` (the
record of a run that happened) and `task.claim.*` (a sequence's run claim); `gate.question.*`
(every gate awaiting an answer); `resolvers.*` (who answers them, and `resolvers.instance.*`
for who is watching one run — see [resolvers.md](resolvers.md)); `trigger.rule.*` (the
installed charter — `trigger.rule.<package>:<name>` for a rule installed at an address),
`trigger.strata.*` (its declared levels) and `trigger.package.*` (what each package declares it reads);
`runners.*` (where work can run) and `sources.*` (what reflects an external system in). A
question's ballots live on its record (`gate.question.<id>.ballots.<who>`), written by the
answer path that tallies them.

Two more are about who: `principal.<kind>.<name>` lists every principal that has presented
itself, by kind — `user` for a person by email, `runner` for a worker that claimed work,
`agent` for an operator agent — and `perms.node.<address>` is the perms mirror: a record
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

One thing is deliberately not in verse's half: `provider.*` — the registry of doers is written
by whoever runs the verse.

The reserved half is read like anything else and written only through the verbs that own it.
`verse data verse trigger rule review` reads a rule; `verse data --put` at any address under
`verse` is refused, and says which verb to use instead (`verse c -a`,
`verse resolver add`, `verse runner add`, `verse source add`, `verse gate approve`). Before the
boundary had a name, anyone who could reach the store could install a rule, stamp a task record
or answer a question on verse's behalf — with no route positioned to say no.

A charter that wants to react to verse's own state says which half it means:

```tony
condition:
  subject: {path: [verse, gate, question, *], value: {status: pending}}
```

A path that does not start with `verse` is the world's half, so a rule about `demo.pr.*` cannot
quietly select the task records of the runs reviewing them.

`local.*` (a federated working tree) is a third thing again: it is mount-owned and lives under
its own root with no history — see below.

## Reading

A verse is one document — `{system: {kind: {id: payload}}}` — and `verse data` answers it at a
path. The path is a
[kinded path](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath) in any of
the three spellings [above](#addressing), and it is the same address at every depth: a system, a kind, an
entity, a place inside a payload.

```sh
verse data                                         # the whole document
verse data demo                                    # {kind: {id: payload}}
verse data demo pr                                 # {id: payload}
verse data demo pr 1                               # one payload
verse data demo pr 1 labels urgent                 # one place inside it
verse data --at 'demo.pr.*.stage'                  # one field of each, still keyed by id
verse data --paths demo pr                         # one address per line
verse data --if '{stage: open}'                    # by what it says
verse data --if '{urgent: true}' --if-at labels    # …rooted inside the payload
```

Steps everywhere except the globbed line, and that exception is the point: a `*` typed as a
step is a name — the shell would expand it bare, and quoting it makes it the literal id `*` —
so a selector is written as one token. See the spelling table above.

The answer is the subtree standing at the path, verbatim — what the store holds, not a shape
assembled for the wire. So an id is a key rather than a token inside a rendered `at:`, which
is what `--paths` is for: a script that wants the addresses gets them one per line instead of
grepping them back out of a rendering.

**A path naming nothing answers 404, at every depth**, and the command exits non-zero. `{}` is a
document somebody wrote; it is not what a read says about a path nobody wrote, whether that path
would have named a slice or a thing. `demo.pr.nosuch` is not there, and neither is `nosuch.pr` —
"which prs does a system that is not there have" is not a question with `none` as its answer, it
is a question about a system that is not there.

    verse data nosuch            nothing stands at nosuch
    verse data demo pr nosuch    nothing stands at demo.pr."nosuch"
    verse data                   the verse is empty: nothing has been written yet

An EMPTY DOCUMENT still has a meaning, and it is the other one: the path stands, and a `--if`
predicate kept none of what it holds. That answer is about the filter rather than about the path,
so it is a 200 with `{}` — which is why the two cannot be spelled the same way.

This used to turn on depth — `{}` with 200 at 0–2 segments, 404 below — which was verse guessing
where entities end. The store answers it now: since go-tony v0.0.201 a read reports null only
where a null was written, and absence is absence at every depth.

A read, a `--put` and a `--rm` all take the same address, at the same depths, because there is
only one thing to take: `demo pr 1` typed and `demo.pr."1"` written down are the one grammar,
separated by the shell in the first case and by the quoting in the second.

```sh
verse data demo pr 1
```

`--if` narrows which entities the document holds, per entity, before the nesting — it never
prunes inside a payload, since the document's leaves are whole payloads. It is a
[tony match](https://signadot.github.io/tony-format/) against the payload — the same predicate
a charter carries in `value`, so a read answers "what would a rule with this predicate
select?". Operators (`!or`, `!and`, `!not`, `!glob`, presence and absence) are
tony's, documented with
[mergeop](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop); what a match
can say inside a rule is in [the trigger model](../trigger-model.md#what-a-match-can-say).

`--if-at` roots the predicate at a dot-separated field path. See
[Selecting by what an entity says](README.md#selecting-by-what-an-entity-says).

### Reaching into an array

`(key)` addresses an array element by identity, and `[0]` by position:

```sh
verse data merge proposal X votes '(scott)'
verse data merge proposal X votes '[0]' choice
verse data --at 'merge.proposal.X.votes[0].choice'    # the same, as one token
```

Both spellings reach it: a step beginning `[`, `{` or `(` **is** that access, and `--at` carries
the kinded path whole. The shell quoting on the step is the shell's — `(` and `[` are its
characters too.

`(key)` needs the array to carry `!key(<field>)`, and the annotation has to have been stored.
That is not automatic: `!key` is a tony merge operator, so an ordinary write executes it — the
array is merged by key and kept without the tag — which means an array put through
`verse data --put` is not keyed afterwards. A payload meant to stay addressable this way is
written as data (`entity.Raw`), which is what the engine's own writers do.

A write reaches an element too, and lands in place:

```sh
verse data merge proposal X votes '[0]' choice --put '"approve"'
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
verse data merge proposal X votes '[0]' --expect '{by: scott}' --put '{choice: approve}'
```

`--create`, `--update` and `--expect` reach it with tony's `!at`, which walks the path and
applies the pattern where it lands. So a guard on one ballot says nothing about the others: a
vote cast beside it neither satisfies nor refuses it, the way a pattern at a field says nothing
about its siblings. (An array-shaped pattern would say something: tony matches an array
positionally and by length, so asking about `votes[1]` that way would also swear the array holds
exactly two ballots.)

`--create` is the odd one, honestly so. An element cannot be created in place, so where nothing
stands the guard holds and the write is refused by the substrate instead, naming the array's
length — which is the answer worth hearing. Where a ballot does stand, `--create` refuses to
commit over it, as it does anywhere else.

### What the document does not cover

Mount-owned slices are not in it. `verse connect`'s content slice (`local.dir-content-*`) is
served by a controller under a separate root with no history, and the read walks the entity
root only — so a mounted entity is addressable by `verse data <path>` and absent from
`verse data`. Saying "the document, whole" would make that pre-existing gap into a claim that
is not quite true. Unioning the second read is a real decision, not a detail.

## Writing

A put merges: fields it does not name are left alone. That is why it answers with what stands
at the address written rather than an echo of what was sent, and why two writers advancing one
entity do not clobber each other.

The address may be as deep as the tree: `verse data verse-dev drift head abc '{sha: …}' --put `
writes there, and the answer is that place. A write is a value at a path, so where an entity
"ends" is not a question a write has to answer — see
[working/entity-tree.md](../working/entity-tree.md).

```sh
verse data demo pr 1 --put '{stage: open}'      # as steps: nothing needs quoting
verse data --at demo.pr.1 --put '{owner: ana}'  # …or in one token: adds an owner, keeps the stage
verse data demo pr 1 labels urgent --put true   # write inside the payload
verse data demo pr 1 labels urgent --rm         # remove one field
verse data demo pr 1 --rm                       # the whole entity
```

A payload typed on the command line names no file, so it is read in the default input format
— tony, unless `--ifmt` or the config says otherwise. In tony, `{stage: open}` needs no
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
verse data demo pr 1 --put '{votes: [{by: ana}, {by: bo}]}'
verse data demo pr 1 votes bo --put '"yes"'
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
verse data demo pr 1 votes --put '[{by: ana}, {by: bo}, {by: cy}]'
```

The guard is a precondition carried with the write, so it is decided at the commit rather than
in a check beforehand, and `--create`/`--update`/`--expect` compose with it. `--create` is worth
naming, because it does not stand in for this one: it asks whether the LEAF is absent, which
under an array it truthfully is, so it admits exactly the write that would do the damage.

A charter's `patch` is guarded on the same principle, one step looser: it is a document rather
than an address, so a nested key the subject does not carry yet is simply built, and only
landing on an array or a scalar is refused. A rule whose patch is refused fails the firing and
leaves its subject untouched — so the rule still selects it, and the next commit tries again.

## Conditional writes

Each condition is checked *with* the write, in one step at the store, so nothing can change
in between. A refused write says so, leaves the entity alone, and exits non-zero.

```sh
verse data --create demo pr 1 --put '{stage: open}' # only if nothing is there yet
verse data --update demo pr 1 --put '{stage: open}' # only if something is
verse data --expect '{stage: draft}' demo pr 2 --put '{stage: open}' # only if it still looks like this
# write refused: a condition did not hold
```

This is CAS, spelled as a predicate rather than as a revision number: the write says what it
believes about the entity rather than which revision it read. `--if` here is a precondition on
a write, and is not the `--if` that filters a listing — one narrows what is written, the other
what is read.

## Watching

```sh
verse watch                                        # from now on
verse watch --if '{stage: open}' demo              # the same selectors as a read
verse watch --since 42                             # replay from a revision already in hand
verse watch --ago 50                               # …or the last 50 commits, whatever the head is
verse watch --since 0                              # replay everything the store still holds
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
  bookmark to hand back to `--since`, never as a count. `--ago 50` is the exception that
  proves it: it asks the store for the last fifty commits — resolved against its watermark and
  clamped to what it retains — rather than doing arithmetic on a revision, and how many events
  live in that span is still the store's business rather than the number's. It is its own flag
  because `--since -50` is not spellable: at a command line a leading dash is what an option
  looks like, so the relative cursor says so in its name.

A watch with a cursor replays: `--since <rev>` (or `--ago <n>`) delivers what the store
still holds from that revision before it streams anything new, so a reader that was away can
catch up rather than guess. A cursor it cannot replay from is refused rather than answered with
a stream carrying no history, and the refusal says which of two things happened: the revision is
below what the store still retains, so that history is gone — and the refusal names the commit
it can still serve from, with `--ago <n>` as the way to ask for what it kept without naming one;
or the store could not answer, in which case the cursor stands and the same request is worth
repeating. A catch-up costs what the path it is asked about holds, so watching
one slice is cheaper than watching the verse in the replay as well as in the stream. With no cursor there is nothing to catch up from and the stream starts at the
moment it opens. The same level-triggered property is what makes an installed trigger act on
the backlog — a rule is a statement about the world, not about what happens next — and it is
why a long hold costs a cursor rather than a buffer. See
[change events](../change-events.md).

A change deeper than an address is reported against the address above it. Watching
`verse-dev.drift` while something is written at `verse-dev.drift.head.abc` gives

```tony
{op: set at: verse-dev.drift.head rev: 152 to: {abc: {sha: aaa}}}
```

— the entity that changed is `abc`, nested inside `to`. One stream serves every reader at
once, so it has to pick one arity, and it picks the ordinary three; a reader whose addresses
are deeper finds the change nested inside `to`. Nothing is missed, and a rule watching that
subtree still selects the deep entity, because it re-evaluates against the store rather than
against the event's address alone.

A `delete` carries no payload, so a `--if` filter does not match one: there is nothing left
to ask about. An entity on its way out is also marked *vanishing* before removal, which is
what stops a rule from acting on — and thereby resurrecting — something that is leaving; see
[lifecycle](../trigger-model.md#lifecycle-how-an-entity-leaves).

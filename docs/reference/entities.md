# Entities

An entity is anything the flow refers to — a PR, a build, a person, a CI status, a running
task — addressed by a `system:kind:id` triple and carrying a document.
[Tutorial §2](../tutorial.md#2-entities) drives all of this; here is the surface flat, with
the corners that section leaves out.

## Refs

```
demo:pr:1                     system=demo   kind=pr      id=1
github:pr:org/repo#42         only the first two colons split, so an id may hold one
task:instance:run-a1b2     what verse writes about its own tasks
```

Three segments, in one grammar shared by the CLI and the charter wire: `*` is a wildcard per
segment, and an id may glob (`demo:pr:hotfix-*`). So `verse flow demo:pr:*` and a charter's
`"subject": "demo:pr:*"` mean the same set.

Systems verse itself writes into: `task:instance:*` (the mirror of every running task)
and `task:claim:*` (its run claim), `gate:question:*` (every gate awaiting an answer),
`resolvers:*` (who answers them, and `resolvers:instance:*` for who is watching one run — see
[resolvers.md](resolvers.md)),
`trigger:rule:*` (the installed charter), `goal:*`, `motion:channel:*` and `vote:*` (the
docket and its ballots), `local:*` (a federated working tree). None of them are special — they are entities, so you can `get`, `watch` and write
rules about them.

## Reading

A verse is ONE DOCUMENT — `{system: {kind: {id: payload}}}` — and `verse state` answers it at a
path. The path is a [kinded path](https://signadot.github.io/tony-format/), and it is the same
address at every depth: a system, a kind, an entity, a place inside a payload.

```sh
verse state                                         # the whole document
verse state demo                                    # {kind: {id: payload}}
verse state demo.pr                                 # {id: payload}
verse state demo.pr.1                               # one payload
verse state demo.pr.1.labels.urgent                 # one place inside it
verse state 'demo.pr.*.stage'                       # one field of each, still keyed by id
verse state --paths demo.pr                         # one address per line
verse state --if '{stage: open}'                    # by what it says
verse state --if '{urgent: true}' --if-at labels    # …rooted inside the payload
```

The answer is the subtree standing at the path, verbatim — what the store holds, not a shape
assembled for the wire. So an id is a KEY rather than a token inside a rendered `ref:`, which
is what `--paths` is for: a script that wants the addresses gets them one per line instead of
grepping them back out of a rendering.

A path naming nothing answers `{}` with 200 at collection depth (0–2 segments) and 404 below
it. The cut is where a path stops describing a slice and starts naming a thing: "which prs does
a system that is not there have" has `none` as a true answer; `demo.pr.nosuch` names one
entity, and `{}` would say it is there and empty.

**Flags come before the path** — `verse state --paths demo.pr`. A command that has subcommands
stops parsing flags at its first positional, which is the rule that lets
`verse state put <ref> <payload> --if <pattern>` mean the write's precondition rather than the
read's filter.

A ref is that same path with `:` for a delimiter, which is why `get`/`put`/`rm` still take one
and why nothing about identity changed: `demo:pr:1` and `demo.pr.1` are one grammar spelled two
ways.

```sh
verse state get demo:pr:1
```

`--if` narrows WHICH ENTITIES the document holds, per entity, before the nesting — it never
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
verse state get 'merge.proposal.X.votes(scott)'
verse state get 'merge.proposal.X.votes[0].choice'
```

`(key)` needs the array to carry `!key(<field>)`, and the annotation has to have been STORED.
That is not automatic: `!key` is a tony merge operator, so an ordinary write EXECUTES it — the
array is merged by key and kept without the tag — which means an array put through
`verse state put` is not keyed afterwards. A payload meant to stay addressable this way is
written as data (`entity.Raw`), which is what the engine's own writers do.

Writing into an array is a different matter and is not offered: the store patches at a kpath of
object fields, so `state put` refuses `[0]` and `(scott)` rather than rendering a path the
substrate will reject. Address the field that holds the array.

### What the document does not cover

Mount-owned slices are not in it. `verse connect`'s content slice (`local:dir-content-*`) is
served by a controller under a separate root with no history, and the read walks the entity
root only — so a mounted entity is addressable by `verse state get` and absent from
`verse state`. Saying "the document, whole" would make that pre-existing gap into a claim that
is not quite true. Unioning the second read is a real decision, not a detail.

## Writing

A put **merges**: fields it does not name are left alone. That is why it answers with the
entity as it now stands rather than an echo of what you sent, and why two writers advancing
one entity do not clobber each other.

```sh
verse state put demo:pr:1 '{stage: open}'
verse state put demo:pr:1 '{owner: ana}'              # adds an owner, keeps the stage
verse state put demo.pr.1.labels.urgent true         # write INSIDE the payload
verse state rm  demo.pr.1.labels.urgent              # remove one field
verse state rm demo:pr:1                              # the whole entity
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

## Conditional writes

Each condition is checked *with* the write, in one step at the store, so nothing can change
in between. A refused write says so, leaves the entity alone, and exits non-zero.

```sh
verse state put --create demo:pr:1 '{stage: open}'                # only if nothing is there yet
verse state put --update demo:pr:1 '{stage: open}'                # only if something is
verse state put --if '{stage: draft}' demo:pr:2 '{stage: open}'   # only if it still looks like this
# write refused: a condition did not hold
```

This is CAS, spelled as a predicate rather than as a revision number: you say what you
believe about the entity rather than which revision you read it at. `--if` here is a
precondition on a write, and is not the `--if` that filters a listing — one narrows what you
write, the other what you read.

## Watching

```sh
verse watch                                        # everything
verse watch --if '{stage: open}' demo              # the same selectors as a read
verse watch --since 42                             # from a revision you already have
```

```tony
{op: set ref: demo:pr:1 rev: 1 to: {stage: open owner: ana}}
{op: delete ref: demo:pr:9 rev: 7}
```

One line per change — flow form, so a watch stays greppable and one event never wraps into
the next.

- `op` is `set` (created **or** updated) or `delete`. Create and update are not
  distinguished, because the store delivers the writer's applied patch verbatim and a create
  is just a patch onto nothing.
- `to` is the entity's whole state as of that commit, not the delta that was written — so a
  reader matching on two fields still matches when only one of them was written.
- `rev` is an opaque cursor. Revisions are monotonic but **sparse**, so treat one as a
  bookmark to hand back to `--since`, never as a count.

A watch **replays**: it is level-triggered at the open, so what already matches arrives
before anything new does. That is the same property that makes an installed trigger act on
the backlog, and it is why a long hold costs a cursor rather than a buffer. See
[change events](../change-events.md).

A `delete` carries no payload, so a `--if` filter does not match one: there is nothing left
to ask about. An entity on its way out is also marked *vanishing* before removal, which is
what stops a rule from acting on — and thereby resurrecting — something that is leaving; see
[lifecycle](../trigger-model.md#lifecycle-how-an-entity-leaves).

# Entities

An entity is anything the flow refers to — a PR, a build, a person, a CI status, a running
process — addressed by a `system:kind:id` triple and carrying a document.
[Tutorial §2](../tutorial.md#2-entities) drives all of this; here is the surface flat, with
the corners that section leaves out.

## Refs

```
demo:pr:1                     system=demo   kind=pr      id=1
github:pr:org/repo#42         only the first two colons split, so an id may hold one
process:instance:run-a1b2     what verse writes about its own processes
```

Three segments, in one grammar shared by the CLI and the charter wire: `*` is a wildcard per
segment, and an id may glob (`demo:pr:hotfix-*`). So `verse flow demo:pr:*` and a charter's
`"subject": "demo:pr:*"` mean the same set.

Systems verse itself writes into: `process:instance:*` (the mirror of every running process)
and `process:claim:*` (its run claim), `gate:question:*` (every gate awaiting an answer),
`trigger:rule:*` (the installed charter), `goal:*`, `motion:channel:*` and `vote:*` (the
docket and its ballots), `local:*` (a federated working tree). None of them are special — they are entities, so you can `get`, `watch` and write
rules about them.

## Reading

```sh
verse entity get demo:pr:1
verse entities                                        # everything
verse entities --system demo --kind pr                # by where it lives
verse entities --if '{stage: open}'                   # by what it says
verse entities --if '{urgent: true}' --if-at labels   # …rooted inside the payload
```

`--if` is a [tony match](https://signadot.github.io/tony-format/) against the payload — the
same predicate a charter carries in `value`, so a listing answers "what would a rule with
this predicate select?". Operators (`!or`, `!and`, `!not`, `!glob`, presence and absence) are
tony's, documented with
[mergeop](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop); what a match
can say inside a rule is in [the trigger model](../trigger-model.md#what-a-match-can-say).

`--if-at` roots the predicate at a dot-separated field path. See
[Selecting by what an entity says](README.md#selecting-by-what-an-entity-says).

## Writing

A put **merges**: fields it does not name are left alone. That is why it answers with the
entity as it now stands rather than an echo of what you sent, and why two writers advancing
one entity do not clobber each other.

```sh
verse entity put demo:pr:1 '{stage: open}'
verse entity put demo:pr:1 '{owner: ana}'              # adds an owner, keeps the stage
verse entity put --at labels.urgent demo:pr:1 true     # write INSIDE the payload
verse entity rm  --at labels.urgent demo:pr:1          # remove one field
verse entity rm demo:pr:1                              # the whole entity
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
verse entity put --create demo:pr:1 '{stage: open}'                # only if nothing is there yet
verse entity put --update demo:pr:1 '{stage: open}'                # only if something is
verse entity put --if '{stage: draft}' demo:pr:2 '{stage: open}'   # only if it still looks like this
# write refused: a condition did not hold
```

This is CAS, spelled as a predicate rather than as a revision number: you say what you
believe about the entity rather than which revision you read it at. `--if` here is a
precondition on a write, and is not the `--if` that filters a listing — one narrows what you
write, the other what you read.

## Watching

```sh
verse watch                                        # everything
verse watch --system demo --if '{stage: open}'     # the same selectors as a listing
verse watch --since 42                             # from a revision you already have
```

```
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

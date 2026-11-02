# Resolvers

A **[resolver](../glossary.md#resolver)** is an installed service that answers asks for whoever
grants it, at `verse.resolvers.<name>`. [Resolvers](../attached-resolvers.md) is the guide; this
page is the reference.

## The resolver

```tony
name:  carols-desk
match:
  about:  demo.pr.*
  labels: { team: payments }
method: { driver: service, url: "https://desk.internal/verse" }
```

| field | | |
|---|---|---|
| `name` | required | The whole of its address. |
| `match` | required | The asks it watches. `match: {}` is every ask put to whoever grants it. |
| `match.about` | optional | A kinded path, with no `value`. An ask about that node or beneath it is admitted. An ask about nothing is admitted only by a match with no `about`. |
| `match.labels` | optional | Labels the ask carries, each equal. |
| `method.driver` | required | `service`: a service that dials verse, wherever its installer runs it. `llm` and `script` are not built. |
| `method.url` | optional | Where the service runs, a note for whoever reads the record. Verse never dials it. |

Verse refuses these at install, each with why:

| Refused | Reason |
|---|---|
| no `match` | a resolver watches the asks its match admits, and with none it would watch nothing |
| `match.about` with a `value` | a value changes while an ask stands, so overlap could not be decided |
| `match.to` | a match does not route: whom a resolver answers for is granted |
| `condition`, `byRef`, `deadline`, `escalatesAfter`, `exclusive` | retired: nothing owns questions, nothing names a resolver, a resolver escalates by an onward ask, and whose answer counts is the party's grant |
| `method.driver: url` | the driver is `service`: verse never dials a resolver's service, which dials verse |
| `method.driver: person` or `group`, `method.who`, `method.group` | retired: a person answers for themselves, and a resolver that forwards raises an onward ask |
| `method.vector`, `.tools`, `.grant` | retired: a resolver is a service and runs nothing verse gates |
| a new `match` that would meet another resolver's where it already answers | two resolvers never answer for one party on one ask (409) |
| an unknown field | it would decode to nothing |

A charter's gate or spawn that still names a `resolver:` is refused at install as retired. A gate
names its `answerers`, and whoever is asked grants the resolver that answers for them.

## Whom it answers for

An [`answer`](perms.md#levels) grant naming `via:verse.resolvers.<name>`:

- at `verse.for.<person>`, for a person, made by their first grant and owned by them;
- at a place, by its owner, reaching beneath it as any grant does.

It is held only by being named: owning the node holds none of it, and a resolver holds none through
whoever installed it.

A grant is refused (409) where another resolver meeting the same node answers for the same party
and some ask could match both. Another resolver meets a node when its grant is at the node, above
it, or beneath it with no other owner's record between. Two matches meet unless their `about`
paths cannot, or they name one label with two values.

## Serving

A resolver serves an ask for a party where its match admits the ask and the party granted it. An
ask's parties are each principal it names, groups expanded when it is asked, or, where it names
nobody, what it is about. Each ask's `read` names `answering:` each party, so the resolvers serving
it read it.

## The routes

A resolver's service is whoever installed it, acting as it: a request presents the installer in
`Verse-Principal` and names the resolver in `Verse-Resolver`, by name or address, and is made as
`via:verse.resolvers.<name>`. Presented by anybody else, or by nobody, it is refused with 403.

```
POST   /resolvers                       install one (the document above)
GET    /resolvers                       list them
GET    /resolvers/{name}                one
DELETE /resolvers/{name}                remove it, withdrawing the onward asks it raised
POST   /resolvers/{name}/grant {for?}   let it answer for you, or for a place on record as yours
POST   /resolvers/{name}/revoke {for?}  and take that back
GET    /resolvers/{name}/asks           as the resolver: the waiting asks it serves, and for whom
GET    /watch?at=verse.ask              as the resolver: its subscription
POST   /asks/{id}/answer {choice, for?} as the resolver: an answer for a party it serves
POST   /asks {…, serves, for?}          as the resolver: an onward ask
GET    /asks/{id}/served                which resolver serves the ask for you, if any
GET    /asks?waiting=me&served=true     your questions, with what a resolver serves for you, marked servedBy
POST   /asks/{id}/answer {…, force}     answer yourself what a resolver serves for you
```

An answer as yourself to an ask a resolver serves for you — as a person it names, or as the owner
of the place it is about — is refused with 409, naming the resolver, unless it says `force`.

## The ask's fields a resolver reads and writes

| field | |
|---|---|
| `labels` | What the ask says about itself, matched by `match.labels`. |
| `subject` | What the ask is about, matched by `match.about`. |
| `to` | Whom it names, as `{grantees: […]}`; with `subject`, its parties. |
| `by`, `via` | On an answer through a resolver: the party it answered for, and the resolver. On each ballot too. |
| `serves` | On an onward ask: `{id, edit, for}`, the ask it serves and the party it was raised for. |
| `onSilence` | What its clock settles on: `deny` unless its asker said `proceed`, and on an onward ask the served ask's. |
| `carried` | On an onward ask: what became of carrying it back — the status, and why where refused. |

## The verbs

```sh
verse resolver                                        # list the resolvers
verse resolver add <file>                             # install one (- = stdin)
verse resolver show   verse.resolvers.carols-desk     # or carols-desk
verse resolver remove verse.resolvers.carols-desk
verse resolver grant  carols-desk [--for <place>]     # let it answer for you, or for a place on record as yours
verse resolver revoke carols-desk [--for <place>]     # and take that back
```

You may drop the leading `verse.resolvers.` and type only the name.

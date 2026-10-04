# Resolvers

An [ask](asking.md) waits for an answer from whom it asks. A **resolver** is a service that
answers asks for somebody: for a person who lets it answer for them, or for a place whose owner
does. It watches the asks it may answer, reads what it needs, and answers, or asks somebody else
first.

A resolver owns nothing. The asks stay their askers', and whoever lets a resolver answer for them
may still answer for themselves. What a resolver's answer counts for is exactly what the person or
place it answers for granted.

| Word | What it is |
|---|---|
| **party** | whom an ask is put to: each person it names, or, where it names nobody, the place it is [about](asking.md#who-reads-it) |
| **resolver** | an installed service, at `verse.resolvers.<name>` ([resolver](glossary.md#resolver)) |
| **match** | the asks a resolver watches: what they are about, and their labels |
| **grant** | a party letting a resolver answer for them: an [`answer`](reference/perms.md#levels) grant naming the resolver |
| **serving** | a resolver serves an ask for a party when its match admits the ask and the party granted it |
| **onward ask** | an ask a resolver raises inside an ask it serves, to consult somebody or to escalate |

This page follows one resolver, a **desk**. Dan runs a small service that answers for Carol: when
somebody asks her something, the desk checks with Erin and answers for Carol with what Erin says.

## A resolver is an entity

You install a resolver. You do not declare it in a charter, because a resolver outlives any one
rule and belongs to whoever runs it.

```tony
name:  carols-desk
match: {}                                  # every ask put to whoever grants it
method: { driver: service, url: "https://desk.internal/verse" }   # url: a note; the service dials verse
```

```sh
verse resolver add carols-desk.tony        # as dan, who runs the desk
```

`match` is required. `match: {}` watches every ask put to whoever grants the resolver. A desk
that only handles some asks says which:

```tony
match:
  about:  demo.pr.*              # asks about a pr, or something beneath one
  labels: { team: payments }     # carrying each of these labels, with these values
```

`about` is a path with no value, since a value changes while an ask stands. An ask carries labels
from its gate (`labels:` on a charter's gate or a vector's) or from `verse ask --label`.

A resolver acts as whoever installed it. Its service reads what they may read, and nothing more.

## Whom it answers for

A resolver answers for nobody until somebody grants it:

```sh
verse resolver grant carols-desk                   # as carol: it answers for her
verse resolver grant carols-desk --for demo.pr     # …or for a place on record as hers
verse resolver revoke carols-desk                  # and takes it back
```

A person's grant stands at `verse.for.<person>`, which their first grant makes, owned by them. A
place's owner on record grants at the place, and the grant reaches beneath it as any grant does.

**Two resolvers never answer for one party on one ask.** A grant is refused where another resolver
already answers for the same party and some ask could match both. Two matches cannot meet when
their `about` paths cannot meet, or when they name one label with two values. The refusal is on
the party's side: declaring a match costs nobody anything, and nobody can claim asks ahead of the
resolver a party wants.

## What it serves

A resolver serves an ask for a party when its match admits the ask and the party granted it. An
ask's `read` names [`answering:`](reference/perms.md#the-grammar) each party, so the resolver
serving it reads it, as grants stand at each read.

Its service follows what it serves by watching the asks as the resolver:

```sh
curl -N -H 'Verse-Principal: user:dan@example.com' -H 'Verse-Resolver: carols-desk' \
  "$VERSE/watch?at=verse.ask&since=0"
```

`GET /resolvers/<name>/asks`, asked the same way, lists the waiting asks it serves now, with the
parties each is for. For context — the run that asked, the history of what an ask is about — it
reads through the ordinary routes, as the resolver, and sees what its installer sees.

**A resolver's service is whoever installed it, acting as it.** A request presents the installer
in `Verse-Principal` and names the resolver in `Verse-Resolver`. Verse makes it as
`via:verse.resolvers.<name>` where that principal owns the resolver, and refuses it otherwise.

## Answering

Alice asks Carol, `verse ask ship -q "Ship it?" --to carol@example.com`, and the desk serves it.
The service answers as any answerer does, made as the resolver:

```sh
curl -H 'Verse-Principal: user:dan@example.com' -H 'Verse-Resolver: carols-desk' \
  -H 'Content-Type: application/tony' -d '{choice: proceed, for: carol@example.com}' \
  "$VERSE/asks/<ask>/answer"
```

The answer is the party's. It names one party the resolver serves the ask for, in `for`, or is for
the only one. A person answers as themselves, and a place as its owner at the moment of the
answer. From there it is held to everything that party's own answer is held to, and the record
says both: `by: <party>` and `via: via:verse.resolvers.<name>`. A threshold counts each party
once.

**What it serves for you is its to answer, until you say otherwise.** Your questions
(`verse question`) leave out an ask a resolver serves for you, and `verse question --served`
shows it, saying which resolver. `verse question --watch` shows it waiting on you,
`(served by …)`. Answering it yourself is
refused, naming the resolver, unless you pass `--force`. Beyond that, the two answers are yours to
keep apart: a resolver's policy — waiting to the last moment, saying no to silence — is its own
code, run with what you granted it.

## Onward asks

To consult somebody, or to escalate, a resolver raises an ask of its own inside the one it serves.
The desk asks Erin before it answers for Carol:

```sh
curl -H 'Verse-Principal: user:dan@example.com' -H 'Verse-Resolver: carols-desk' \
  -H 'Content-Type: application/tony' \
  -d '{name: up, question: "Ship it, for carol?", answerers: [erin@example.com], serves: <ask>}' \
  "$VERSE/asks"
```

Erin answers it as she answers anything, `verse question <up> --answer proceed`, and Alice's ask
is answered yes, `by: user:carol@example.com`, `via: via:verse.resolvers.carols-desk`.

- **It fits inside the ask it serves.** It offers the same options, each settled by one ballot.
  Its clock fits inside the served ask's: a later date is refused, and with none it takes that
  clock, and what silence settles on there. It takes no `then`.
- **What it comes to is carried back.** When it settles, versed answers the served ask as the
  resolver, for the party it was raised for, held to the grant and the match as they stand then.
  A clock that settled on no option carries nothing. The onward ask records `carried`.
- **It follows the ask it serves.** When that ask settles, is withdrawn, removed, or asked again,
  the onward ask is withdrawn, saying why. A sooner clock there moves its own.

Escalating is an onward ask to whoever is above. Their own resolver may raise one further, and the
answer comes back over each hop. Removing a resolver withdraws the onward asks it raised.

## Walk it

[`smoke.sh`](../smoke.sh)'s resolver step runs the whole of this against a fresh verse: dan
installs a desk, carol grants it, alice asks carol, the desk serves the ask, asks erin onward, and
erin's yes comes back on alice's ask as carol's, through the desk.

## Why it is this way

**A match does not route.** Which asks reach whom is the asker's to say, by naming answerers or by
what the ask is about. A resolver says which of those it serves, and a party's grant says whether
its answer counts.

**Grants, not ownership.** A resolver that owned questions could shut out the people it answered
for. A grant lends the resolver a party's answer without taking it from them, and it is revoked
the same way.

**Context through the ordinary routes.** A resolver reads the run and the subject as whoever
installed it. There is no stream of its own to keep, and nothing it reads that its installer could
not.

# Resolvers

The surface: the resolver, the two entities, the routes an answering service lives on, and the
verbs an operator types. Why it is shaped this way is
[attached resolvers](../attached-resolvers.md); the five acts you can drive are
[`scripts/attached-resolver.sh`](https://github.com/signadot/verse/blob/main/scripts/attached-resolver.sh).

## The resolver

A **resolver** is the installed record that says who answers a gate question. It is installed as
`verse.resolvers.<name>`; whoever it names — a service, a person or a group — is its
**answerer**.

```tony
name:      security-review
condition:                                       # the STANDING door
  subject: {path: "change.pr.*", value: { area: payments }}
byRef:     true                                  # the NAMED door
method:
  driver:  url
  url:     "https://review.internal/verse"
deadline:  { within: "4h", then: escalate }
exclusive: true
```

| field | | |
|---|---|---|
| `name` | required | may not contain `@` — it is the separator in a cover's address |
| `condition.subject` | optional | a path and a payload predicate, the same vocabulary a trigger's `subject` uses; the path begins with a literal, since a resolver whose subject selects from the top would claim every question in the verse |
| `byRef` | optional | may be named by a spawn's `resolver:` or a rule's `gating.resolver` |
| `method.driver` | required | the answerer: `url`, a service; `person`, one principal, from the docket; `group`, a group's members, each as themselves |
| `method.url` | | for `url`: where the service is. Verse never posts to it — the service dials in |
| `method.who` | | for `person`: the principal the question is held out to — `user:<email>`, or the bare email ([grammar](../perms.md#principals-and-grantees)) |
| `method.group` | | for `group`: the group, `group:<name>` |
| `method.vector` `.tools` `.grant` | optional | bound whatever the driver runs; inert for `url` |
| `deadline.within` | optional | a **quoted** duration (`"4h"`, `"30m"`). Unquoted `4h` is the number `4` beside a key `h`, and is refused |
| `deadline.then` | optional | `escalate` (the default and the only value today) |
| `exclusive` | optional | only this resolver's answer counts. **Requires a deadline** |

A question held out to a resolver is held out to whoever answers as it (`read-write` on the
question's record, [perms.md](../perms.md)): the resolver's address for a service, the person or
the group's members otherwise. A person or a group is given no cover — they answer from the
docket — and a person hands a question on with `verse gate escalate <id>`.

Refused at install: a resolver with neither `byRef` nor `condition` (nothing could reach it);
`exclusive` without a deadline (a service that is down would turn every question it owns into
one nobody may answer); a name containing `@`; an unparseable duration; an unknown field.

Installing a resolver whose condition overlaps another's is a **notice**, not a refusal — the
first resolver *by name* owns the questions about a subject both match.

## The two entities

| ref | what it is |
|---|---|
| `verse.resolvers.<name>` | the resolver — what is installed |
| `verse.covers.<name>@<stream>` | a **cover**: one resolver reading one gate stream, one per (resolver, stream) |

A **gate stream** is what a cover reads: a run's event stream for a run's gates (the cover
`payments-review@review-8f30…`), and for a rule's own gates the subject's commit stream,
narrowed to what the rule reads (`payments-review@change.pr.1`), because a firing has no event
stream of its own. It is the stream the gates are *raised in*, not a stream made of gates: a
cover reads everything in it, so by the time it reaches a gate at frame 36 it has read 34 and 35.

They stand apart because a cover's address is derived and a resolver's is chosen: under
one roof a resolver could be given a name that was a cover's. Nobody but verse makes a
cover, and each carries a record of its own UNDER its resolver, so whoever owns the resolver
co-owns it and nobody else writes its position. A cover carries `resolver`, `stream`,
`position`, and `notes` if any.

## The question record

A gate awaiting an answer is `verse.gate.question.<id>`, and the fields a resolver puts there
or reads from there:

| field | |
|---|---|
| `resolver` | the resolver it is held out to. **Absent means ⊤** — the docket a person reads |
| `escalatesAt` | when its owner's deadline expires. Absolute (RFC3339 with fraction), stamped at post |
| `reownedFrom` `reownedWhy` `reownedAt` | the handover. `why` is `escalated`, `overdue` or `removed` |
| `by` | who answered: the principal the request was made as, never a name the body chose — a body's `by` that names anybody else is refused 400. A service's verdict stamps the **resolver**, not the cover |
| `about` | the one line its SUBJECT says about itself (`says` on the subject, first line, cut at 140), copied onto the question when it is posed so that a reader who may not read the subject can still say what is being decided. A convention an entity opts into: one that says nothing leaves this absent |
| `pose` | what it asks, in words, as the gate wrote it and the firing expanded it against its subject. Absent for a gate that says nothing and for a question a task raises of its own accord, where `reason` is all there is |
| `answerers` | whom the gate itself named, when it named anybody. What a question held out to a resolver goes back to when that resolver lets go — to the docket's default only where this is absent |
| `waitingOn` | whom it waits on **now**: its own `read-write` line, groups expanded to the people who act as themselves, wildcards (`user:*`, the docket's default) left as they are written. Written at posing and moved by every hand-over, since the same grant carries both; gone once it settles. A change to the default above a docket of pending questions does not move it — nothing writes those questions — so each says the set it was posed with until it is asked again |
| `said` | what people said ABOUT the question without answering it, each entry under the instant it was said: `{by, text}`. Written by `POST /gate/say`, which is admitted to whoever may READ the question — the person who cannot decide this is often the one who knows why it is waiting. It carries no choice and can never settle anything |
| `overrode` | whom an answer was taken over, when `-override` was used |
| `askedAgain` | this id was posed again after settling; the prior answer is at the commit that made it |

Everything else on the record — `id`, `status`, `subject`, `task`, `origin`, `gate`, `mode`,
`tool`, `effects`, `reason`, `spawns`, `verdict`, `say`, `answeredAt` — is
[the question record](tasks.md) generally, and is not resolver-specific. An empty `origin`
means the question came from its `task` when it has one — a worker's own gate id carries no
minted origin — and a brokered gate may carry neither.

## The routes

An answering service uses three. All are addressed by **cover**, so none needs a second
parameter to say which run it is about — and "may this answer that" is structural rather than a
policy question: a cover names its resolver and its stream, so the check is whether the
question's `resolver` is this cover's resolver and the question's origin is this cover's
stream.

```
POST /gate/say                                       { id, by, say } — talk about a question
GET  /resolvers/cover/{id}/events?since=<position>   replay, then live
POST /resolvers/cover/{id}/verdicts                  { id, verdict, why?, say? }
POST /resolvers/cover/{id}/position                  { position, notes? }
```

`verdict` is one of the question's options, `proceed` or `deny` unless the record names its
own, or `escalate`. A verdict naming an option the record does not carry is refused with the
options listed. `events` is the run's own stream with the
capability check in front of it, not a projection of it — the moment it were a different stream
there would be two orders to reconcile.

Refusals worth knowing: **403** when the question is not this cover's to answer or escalate;
**409** when it was already settled, or when an answer beat an escalation to the write; **404**
when no cover exists at that address, which is also what a typo'd cover path returns.

Managing resolvers:

```
GET    /resolvers                    list
POST   /resolvers                    install (tony, json or yaml)
GET    /resolvers/{name}             show one
DELETE /resolvers/{name}             remove one, handing on what it held
```

## What an observer sees

Answering is emitted on the run's **one ordered stream**, so a supervised run is not a run with
a hidden answerer — `verse task attach <run>` shows the whole exchange in order:

```tony
{kind: pending,  id: "review-bff7…~d1", mode: ASK, tool: run_bash}
{kind: decision, id: "review-bff7…~d1", by: verse.resolvers.reviewer, proceed: true}
{kind: exec,     tool: run_bash, text: reviewing}
{kind: status,   text: done}
```

`by` on the `decision` frame is the **resolver**, stamped by verse — a service says what it
decided, never who it is. Nothing about that frame differs in shape from a person's answer, so
there is no supervised run that reads as unsupervised, and no way to answer invisibly.

What the stream does *not* show is what was **watched**: a position advancing with no verdict
emits nothing. That is the cover's own record, which is why there are two —
the task stream is what was decided, the cover is who was watching and on what evidence.

`./scripts/attached-resolver.sh attach` is this view; `watch` is the answerer's side of the same
moment.

## The verbs

```sh
verse resolver                                  # list the resolvers
verse resolver add <file>                       # install one (- = stdin)
verse resolver show   verse.resolvers.review   # or review
verse resolver remove verse.resolvers.review

verse data verse covers                         # who is watching what
verse gate                                      # the docket, resolver-owned questions included
verse gate approve <id> -as <who> [-override] # answer, or answer over an exclusive owner
```

A resolver ref is typed the way it is stored — `verse.resolvers.review` — because an
operator reading `verse data` and one typing `verse resolver show` should not have to know two
spellings of one thing. The leading `verse.resolvers.` may be dropped, leaving the name — which
is the whole of a resolver's address, so names are unique on their own.

## Holding work's questions out to one

```sh
verse task run 'check the migration' \
  -subject change.pr.1 \
  -vector ask \
  -resolver verse.resolvers.review
```

`-vector ask` is what makes the run stop and ask; without it the run is `auto` and the
resolver has nothing to answer. `-resolver` naming a resolver that is not installed is refused,
rather than left to shadow whatever matches the subject.

In a charter, the same two doors:

```tony
gating:
  resolver: verse.resolvers.review    # answers this rule's own gates
  gates: [{ name: ship, mode: ask }]
action:
  spawn:
    resolver: verse.resolvers.review  # answers this run's gates
```

## Not built

`llm` and `script` drivers, resolvers over a scope other than baseline, and resolvers over a
motion.

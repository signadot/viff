# Resolvers

The surface: the binding, the two entities, the routes a resolver service lives on, and the
verbs an operator types. Why it is shaped this way is
[attached resolvers](../attached-resolvers.md); the five acts you can drive are
[`scripts/attached-resolver.sh`](https://github.com/signadot/verse/blob/main/scripts/attached-resolver.sh).

## The binding

Installed as `verse.resolvers.<kind>:<name>`.

```tony
name:      security-review
kind:      security
condition:                                       # the STANDING door
  subject: {path: [change, pr, *], value: { area: payments }}
byRef:     true                                  # the NAMED door
method:
  driver:  url
  url:     "https://review.internal/verse"
deadline:  { within: "4h", then: escalate }
exclusive: true
```

| field | | |
|---|---|---|
| `name` | required | may not contain `@` — it is the separator in an instance's address |
| `kind` | required | the middle segment of the ref; groups bindings by what they are for |
| `condition.subject` | optional | a [tony match](https://signadot.github.io/tony-format/) over entities, the same vocabulary a trigger's `subject` uses |
| `byRef` | optional | may be named by a spawn's `resolver:` or a rule's `gating.resolver` |
| `method.driver` | required | `url` today |
| `method.url` | | where the service is. Verse never posts to it — the service dials in |
| `method.vector` `.tools` `.grant` | optional | bound whatever the driver runs; inert for `url` |
| `deadline.within` | optional | a **quoted** duration (`"4h"`, `"30m"`). Unquoted `4h` is the number `4` beside a key `h`, and is refused |
| `deadline.then` | optional | `escalate` (the default and the only value today) |
| `exclusive` | optional | only this binding's answer counts. **Requires a deadline** |

Refused at install: a binding with neither `byRef` nor `condition` (nothing could reach it);
`exclusive` without a deadline (a binding that is down would turn every question it owns into
one nobody may answer); a name containing `@`; an unparseable duration; an unknown field.

Installing a binding whose condition overlaps another's is a **notice**, not a refusal — the
first binding *by name* owns the questions they both cover.

## The two entities

| ref | what it is |
|---|---|
| `verse.resolvers.<kind>:<name>` | the binding — what is installed |
| `verse.resolvers.instance.<name>@<origin>` | who is *reading*, one per (binding, origin) |

One `verse data resolvers` shows both, which is why the instance is a reserved
*kind* inside the system rather than a second system with a singular/plural pair to mistype.

The origin is the **run** for a run's gates (`payments-review@review-8f30…`) and the **subject**
for a firing's (`payments-review@change.pr.1`), because a firing has no event stream of its own.
An instance carries `binding`, `origin`, `position`, and `notes` if any.

## The question record

A gate awaiting an answer is `verse.gate.question.<id>`, and the fields a resolver puts there
or reads from there:

| field | |
|---|---|
| `resolver` | the binding that owns it. **Absent means ⊤** — the docket a person reads |
| `escalatesAt` | when its owner's deadline expires. Absolute (RFC3339 with fraction), stamped at post |
| `reownedFrom` `reownedWhy` `reownedAt` | the handover. `why` is `escalated`, `overdue` or `removed` |
| `by` | who answered. A resolver's verdict stamps the **binding**, not the instance |
| `overrode` | whom an answer was taken over, when `--override` was used |
| `askedAgain` | this id was posed again after settling; the prior answer is at the commit that made it |

Everything else on the record — `id`, `status`, `subject`, `task`, `origin`, `gate`, `mode`,
`tool`, `effects`, `reason`, `spawns`, `verdict`, `say`, `answeredAt` — is
[the question record](tasks.md) generally, and is not resolver-specific.

## The routes

A resolver service uses three. All are addressed by **instance**, so none needs a second
parameter to say which run it is about — and "may this answer that" is structural rather than a
policy question: an instance names its binding and its origin, so the check is whether the
question's `resolver` is this instance's binding and the question's origin is this instance's
origin.

```
GET  /resolvers/instance/{id}/events?since=<position>   replay, then live
POST /resolvers/instance/{id}/verdicts                  { id, verdict, why?, say? }
POST /resolvers/instance/{id}/position                  { position, notes? }
```

`verdict` is `proceed`, `deny` or `escalate`. `events` is the run's own stream with the
capability check in front of it, not a projection of it — the moment it were a different stream
there would be two orders to reconcile.

Refusals worth knowing: **403** when the question is not this instance's to answer or escalate;
**409** when it was already settled, or when an answer beat an escalation to the write; **404**
when no instance exists at that address, which is also what a typo'd instance path returns.

Managing bindings:

```
GET    /resolvers                    list
POST   /resolvers                    install (tony, json or yaml)
GET    /resolvers/{kind}/{name}      show one
DELETE /resolvers/{kind}/{name}      remove one, handing on what it held
```

## What an observer sees

Answering is emitted on the run's **one ordered stream**, so a supervised run is not a run with
a hidden answerer — `verse task attach <run>` shows the whole exchange in order:

```tony
{kind: pending,  id: "review-bff7…~d1", mode: ASK, tool: run_bash}
{kind: decision, id: "review-bff7…~d1", by: verse.resolvers.security.reviewer, proceed: true}
{kind: exec,     tool: run_bash, text: reviewing}
{kind: status,   text: done}
```

`by` on the `decision` frame is the **binding**, stamped by verse — a service says what it
decided, never who it is. Nothing about that frame differs in shape from a person's answer, so
there is no supervised run that reads as unsupervised, and no way to answer invisibly.

What the stream does *not* show is what was **watched**: a position advancing with no verdict
emits nothing. That is the resolver's own record, which is why there are two —
the task stream is what was decided, the instance is who was watching and on what evidence.

`./scripts/attached-resolver.sh attach` is this view; `watch` is the resolver's side of the same
moment.

## The verbs

```sh
verse resolver                                  # list the bindings
verse resolver add <file>                       # install one (- = stdin)
verse resolver show   verse.resolvers.security.review # or security:review
verse resolver remove verse.resolvers.security.review

verse data verse resolvers instance            # who is watching what
verse gate                                      # the docket, resolver-owned questions included
verse gate approve <id> --as <who> [--override] # answer, or answer over an exclusive owner
```

A resolver ref is typed the way it is stored — `verse.resolvers.security.review` — because an
operator reading `verse data` and one typing `verse resolver show` should not have to know two
spellings of one thing. The leading `verse:resolvers:` may be dropped, leaving `<kind>:<name>`.

## Binding one to work

```sh
verse task run 'check the migration' \
  --subject change.pr.1 \
  --vector ask \
  --resolver verse.resolvers.security.review
```

`--vector ask` is what makes the run stop and ask; without it the run is `auto` and a bound
resolver has nothing to answer. `--resolver` naming a binding that is not installed is refused,
rather than left to shadow whatever covers the subject.

In a charter, the same two doors:

```tony
gating:
  resolver: verse.resolvers.security.review    # answers this rule's own gates
  gates: [{ name: ship, mode: ask }]
action:
  spawn:
    resolver: verse.resolvers.security.review  # answers this run's gates
```

## Not built

`llm` and `script` drivers, resolvers over a scope other than baseline, and resolvers over a
motion. See [the residual](../working/attached-resolvers-residual.md).

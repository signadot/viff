# Resolvers

This page lists the surface of resolvers:

- the resolver record,
- the two entities,
- the routes that an answering service uses,
- the verbs that an operator types.

[Attached resolvers](../attached-resolvers.md) explains why resolvers have this shape.
[`scripts/attached-resolver.sh`](https://github.com/signadot/verse/blob/main/scripts/attached-resolver.sh)
drives the five acts that you can perform.

## The resolver

A **[resolver](../glossary.md#resolver)** is the installed record that says who answers the
[question](../glossary.md#question) of a gate. Verse installs it as `verse.resolvers.<name>`.
The service, person or group that it names is its
**[answerer](../glossary.md#answerer)**.

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
| `name` | required | It may not contain `@`. The `@` is the separator in the address of a cover. |
| `condition.subject` | optional | An address and a predicate on the payload, in the same vocabulary as the `subject` of a [rule](../glossary.md#rule). The address must begin with a literal segment. A resolver whose subject selects from the top of the store would claim every question. |
| `byRef` | optional | A spawn's `resolver:` or a rule's `gating.resolver` may name the resolver. |
| `method.driver` | required | The answerer. `url` is a service. `person` is one principal, who answers from the docket. `group` is the members of a group, each answering as themselves. |
| `method.url` | | For `url`: where the service is. Verse never posts to it. The service connects to verse. |
| `method.who` | | For `person`: the principal who receives the question. It is `user:<email>`, or the bare email ([grammar](../perms.md#principals-and-grantees)). |
| `method.group` | | For `group`: the group, as `group:<name>`. |
| `method.vector` `.tools` `.grant` | optional | They bound whatever the driver runs. They have no effect for `url`. |
| `deadline.within` | optional | A **quoted** duration (`"4h"`, `"30m"`). Verse refuses an unquoted `4h`, because it reads as the number `4` beside a key `h`. |
| `deadline.then` | optional | `escalate`. It is the default, and the only value that is built. |
| `exclusive` | optional | Only the answer of this resolver counts. It **requires a deadline**. |

The *docket* is the list of open questions that people read with `verse gate`.

A question that goes to a resolver goes to whoever answers as that resolver. That party has
`read-write` on the record of the question ([perms.md](../perms.md)).

| Driver | Who answers as the resolver | How they answer |
|---|---|---|
| `url` | the address of the resolver, for a service | through a cover |
| `person` | the person | from the docket. Verse gives them no cover. |
| `group` | the members of the group | from the docket. Verse gives them no cover. |

A person passes a question on with `verse gate escalate <id>`.

Verse refuses these at install:

| Refused | Reason |
|---|---|
| a resolver with neither `byRef` nor `condition` | nothing could reach it |
| `exclusive` without a deadline | a service that is down would make every question it owns a question that nobody may answer |
| a name that contains `@` | `@` separates the parts of a cover's address |
| a duration that does not parse | |
| an unknown field | |

A resolver whose condition overlaps the condition of another resolver installs with a
**notice**. Verse does not refuse it. The first resolver *by name* owns the questions about a
subject that both conditions match.

## The two entities

| address | what it is |
|---|---|
| `verse.resolvers.<name>` | the resolver, which is what you install |
| `verse.covers.<name>@<stream>` | a **cover**: one resolver reading one gate stream. There is one cover for each pair of resolver and stream. |

A [cover](../glossary.md#cover-of-a-resolver) reads a *gate stream*. A gate stream is the
stream that verse raises the gates in:

| Gates of | Gate stream | Example cover |
|---|---|---|
| a run | the [event stream](../glossary.md#event-stream) of the run | `payments-review@review-8f30…` |
| a rule | the commit stream of the subject, narrowed to what the rule reads | `payments-review@change.pr.1` |

A rule uses the commit stream because a firing has no event stream of its own. A gate stream is
not a stream made only of gates. A cover reads everything in its stream. So when a cover reaches
a gate at frame 36, it has read frames 34 and 35.

The two entities have separate addresses because verse derives the address of a cover, and a
person chooses the address of a resolver. Under one address, a person could give a resolver a
name that is also the name of a cover.

Only verse makes a cover. Each cover carries a record of its own UNDER its resolver. So whoever
owns the resolver also owns the cover, and nobody else writes its position. A cover carries
`resolver`, `stream`, `position`, and `notes` if it has any.

## The question record

A gate that waits for an answer is `verse.gate.question.<id>`. A resolver writes or reads these
fields of it:

| field | |
|---|---|
| `resolver` | The resolver that receives the question. **Absent means ⊤**, which is the docket that a person reads. |
| `escalatesAt` | When the deadline of its owner expires. It is absolute (RFC3339 with a fraction), and verse stamps it when it posts the question. |
| `reownedFrom` `reownedWhy` `reownedAt` | The handover. `why` is `escalated`, `overdue` or `removed`. |
| `by` | Who answered. It is the principal that made the request, never a name that the body chose. Verse refuses with 400 a body whose `by` names anybody else. A verdict from a service stamps the **resolver**, not the cover. |
| `about` | The one line that the SUBJECT says about itself: the first line of `says` on the subject, cut at 140 characters. Verse copies it onto the question when it poses the question. A reader who may not read the subject can then still say what is being decided. An entity opts into this convention. For an entity that says nothing, the field is absent. |
| `pose` | What the question asks, in words: the text of the gate, expanded by the firing against its subject. It is absent for a gate that says nothing. It is also absent for a question that a task raises by itself, where `reason` is the only text. |
| `answerers` | Whom the gate itself named, if it named anybody. When a resolver lets go of a question, the question goes back to these answerers. It goes to the default of the docket only when this field is absent. |
| `waitingOn` | Whom the question waits on **now**. It is the question's own `read-write` line. Groups are expanded to the people who act as themselves. Wildcards (`user:*`, the default of the docket) stay as they are written. |
| `said` | What people said ABOUT the question without answering it. Each entry is stored under the instant it was said, as `{by, text}`. `POST /gate/say` writes it. |
| `overrode` | Whose answer this answer replaced, when somebody used `-override`. |
| `askedAgain` | Verse posed this id again after it settled. The earlier answer is at the commit that made it. |

More on `waitingOn`:

- Verse writes it when it poses the question, and moves it at every handover, because the same
  grant carries both. It is gone once the question settles.
- A change to the default above a docket of pending questions does not move it, because nothing
  writes those questions. Each question keeps the set it was posed with until somebody asks it
  again.

More on `said`:

- `POST /gate/say` admits whoever may READ the question. The person who cannot decide the
  question is often the person who knows why it waits.
- An entry carries no choice, and it never settles anything.

The other fields of the record are `id`, `status`, `subject`, `task`, `origin`, `gate`, `mode`,
`tool`, `effects`, `reason`, `spawns`, `verdict`, `say` and `answeredAt`. They belong to
[the question record](tasks.md) in general, and are not specific to resolvers.

An empty `origin` means that the question came from its `task`, when it has one. A worker's own
gate id carries no minted origin. A brokered gate may carry neither.

## The routes

An answering service uses three routes. The routes take the **cover** as their address. So no route needs a second parameter to say which run it is about.

```
POST /gate/say                                       { id, by, say } — talk about a question
GET  /resolvers/cover/{id}/events?since=<position>   replay, then live
POST /resolvers/cover/{id}/verdicts                  { id, verdict, why?, say? }
POST /resolvers/cover/{id}/position                  { position, notes? }
```

The check "may this cover answer that question" follows from the structure, and is not a
policy. A cover names its resolver and its stream. Verse checks two things:

1. The `resolver` of the question is the resolver of this cover.
2. The origin of the question is the stream of this cover.

`verdict` is one of the options of the question, or `escalate`. The options are `proceed` or
`deny`, unless the record names its own. Verse refuses a verdict that names an option the
record does not carry, and lists the options.

`events` is the run's own stream with the capability check in front of it. It is not a
projection of that stream.

These refusals are worth knowing:

| Status | When |
|---|---|
| **403** | the question is not this cover's to answer or escalate |
| **409** | the question is already settled, or an answer reached the write before an escalation |
| **404** | no cover exists at that address. A cover address with a typing error also returns 404. |

Managing resolvers:

```
GET    /resolvers                    list
POST   /resolvers                    install (tony, json or yaml)
GET    /resolvers/{name}             show one
DELETE /resolvers/{name}             remove one, handing on what it held
```

## What an observer sees

Verse emits each answer on the run's **one ordered stream**. So a supervised run has no hidden
answerer. `verse task attach <run>` shows the whole exchange in order:

```tony
{kind: pending,  id: "review-bff7…~d1", mode: ASK, tool: run_bash}
{kind: decision, id: "review-bff7…~d1", by: verse.resolvers.reviewer, proceed: true}
{kind: exec,     tool: run_bash, text: reviewing}
{kind: status,   text: done}
```

Verse stamps `by` on the `decision` frame with the **resolver**. A service says what it
decided, never who it is. The frame has the same shape as the answer of a person. So no
supervised run reads as unsupervised, and nobody can answer invisibly.

The stream does *not* show what the cover **watched**. A position that advances with no verdict
emits nothing. The cover's own record holds it. So there are two records:

| Record | What it says |
|---|---|
| the task stream | what was decided |
| the cover | who was watching, and on what evidence |

`./scripts/attached-resolver.sh attach` shows this view. `watch` shows the same moment from the
side of the answerer.

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

You type the address of a resolver as verse stores it: `verse.resolvers.review`. You may drop
the leading `verse.resolvers.` and type only the name. The name is the whole of the address of
a resolver, so each name is unique by itself.

## Holding work's questions out to one

```sh
verse task run 'check the migration' \
  -subject change.pr.1 \
  -vector ask \
  -resolver verse.resolvers.review
```

`-vector ask` makes the run stop and ask. Without it the run is `auto`, and the resolver has
nothing to answer.

Verse refuses a `-resolver` that names a resolver that is not installed. It does not let the
run fall through to whatever resolver matches the subject.

A charter has the same two doors:

```tony
gating:
  resolver: verse.resolvers.review    # answers this rule's own gates
  gates: [{ name: ship, mode: ask }]
action:
  spawn:
    resolver: verse.resolvers.review  # answers this run's gates
```

## Not built

- the `llm` and `script` drivers
- resolvers over a scope other than baseline
- resolvers over a motion

## Why it is this way

### One stream for events

`events` is the run's own stream, and not a projection of it. A different stream would give
two orders of events to reconcile.

### One spelling of an address

An operator who reads `verse data` and an operator who types `verse resolver show` should not
need to know two spellings of one thing. So you type the address as verse stores it.

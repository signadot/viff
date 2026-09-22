# gates: a question cannot say what it asks — no prose on a gate, an option or the record

Status: proposal, nothing here runs. It belongs to the `gates-reach-users` work in
`docs/working/owners.tony`: "a gate reaches its answerers in slack, and is answered there". The
path it completes is the one `sourced slack -out` now walks — a question reaches Slack, is
answered there, and the message is redrawn from the record.

## What is wrong

A question reaches somebody and cannot tell them what it is about.

The record carries `id`, `origin` (the rule), `gate` (its name), `subject` (an address), `mode`,
`options` (an id and a threshold each), `effects`, `spawns`, `deadline`, `answerers`, `waitingOn`,
and `reason` — which the engine composes as "trigger slack-try gate approval on demo.slack.1"
(`trigger/engine.go:888`). That is provenance. It says who asked and which rule, and nothing about
what is at stake, which is the thing an answerer needs (`task/vector.go:44` says so of `subject`).

And a charter author who knows exactly what they are asking has nowhere to write it: a gate
(`trigger/trigger.go:308`) is `name`, `mode`, `need`, `options`, `deadline`, `answerers`,
`onDenied` — no prose. An option (`mirror/question.go:421`) has no words either, so a presenter
offering three choices offers three ids.

So every presenter — the docket, `verse gate`, Slack — re-derives a sentence from an id and an
address, and each derives a different one. The one convention that exists is a subject's own
`says`, which `verse docket` shows ahead of the address (`06476112`). It says what the THING is,
not what is being asked about it, and nothing reads it for a gate's question.

## The record gains three things, all filled when the question is posed

- **`pose`** — the question in words, written on the gate, interpolating the subject.
- **`about`** — the subject's own `says`, copied onto the question.
- **prose per option** — `{id: careful, says: "merge, but hold the deploy"}`, so a label is words.

```tony
gates:
- name: approval
  mode: ask
  pose: !eval "Deploy $[subject.value.sha] to staging? It carries $[subject.value.changes] changes."
  options:
  - {id: proceed, says: "deploy it"}
  - {id: careful, says: "deploy, but hold the migration"}
  - {id: deny,    says: "not this one"}
```

**Versed renders the text at posing, not the presenter.** Three reasons, in order of weight. A
presenter may not be allowed to read the subject — the Slack io holds no grant on `demo.*` — while
versed is already reading it to fire the rule. The words then freeze with the asking, as `reason`
and `effects` already do, so what somebody approved is what they were shown. And the subject is
read once rather than once per presenter. The same argument gives `about`: a presenter needs no
access to the subject at all.

`reason` stays as it is. It is the machine's line and a presenter falls back to it.

## The interpolation is go-tony's, trimmed

`!eval` with `$[…]` expressions (go-tony `eval`), not a deref of verse's own. Measured against
v0.0.234:

| written | becomes |
|---|---|
| `$[subject.value.sha]` | `aaaa1111` |
| `$[subject.value.missing]` | `null`, no error |
| `$[nosuchname.x]` | an error naming the column |
| `$[subject.value.stage == "open" ? "still open" : "closed"]` | `still open` |
| `a literal $ and a [bracket]` | unchanged |

Which decides the two questions a template language otherwise leaves open. A **typo'd root is an
error**, so it is refused at install, where the author is looking at it. A **missing field renders
`null`** — visible rather than silent — and an author who wants better writes
`?? "an unknown commit"`. Neither rule is ours to invent.

**The trim is the op set, not the expressions.** A charter is installed by a person and runs as
the system, so `!exec`, `!file`, `!script` and `!osenv`/`getenv` — a shell, a filesystem and the
pod's environment — are not reachable from a gate's sentence. The environment offered is the
subject (name and value) and the gate's own facts. `getpath`/`listpath` stay out too: verse's
`!get-path` carries read-set semantics a condition depends on (`trigger/condread.go`), and a
sentence reading the store behind that is an untracked read.

**A sentence that will not render does not stop the question.** Expansion failing at pose time
keeps `reason`, records the error on the question, and asks anyway: a gate nobody is asked because
its prose broke is worse than a bare one.

## Presentation: markdown and a typed footer, not only buttons

Buttons map badly to the corners of Slack — notifications show fallback text, and a gate with five
options is five mystery ids in a row. Slack renders markdown, so the message is markdown: the
`pose` line, `about` and the subject, the deadline, who it waits on, and a footer saying how to
answer in words.

One Slack constraint decides the spelling of that footer: **a slash command is registered by name
in the app**, so `/y`, `/n` and a per-gate `/other-choice` cannot be dynamic. Two things do work:

- **A reply in the thread** — `y`, `n`, `careful` — registers nothing, works wherever Slack works,
  and the parent `ts` names the question exactly, since the io posted that message and its status
  holds the ts. It needs the event deliveries the socket already carries (`81cea076`) and the app
  does not yet send.
- **One registered `/verse <choice>`**, arriving on the same socket as a `slash_commands` envelope,
  for answering outside a thread. It carries no message reference, so it needs a rule for which
  question it means — the most recent one that channel is waiting on from that person — and that
  rule can be wrong.

Thread replies are the primary path; the footer is always printed, so the message is answerable
where blocks do not render; the buttons stay as a convenience for the small cases, since they
already carry the exact question and option. `y`/`n` are the io's aliases for the first two option
ids; the ids stay the vocabulary.

## Out of scope here

Who to DM and what the message says per person is the gate charter's derive, which wants
`waitingOn` (landed, `148661e7`) and `user:<email>` targets in the io. A select menu for many
options is a rendering decision the io can make alone, later.
# Prerequisites: what has to be true before a resolver is buildable

Status: proposal, nothing built. The design is [attached resolvers](../attached-resolvers.md);
this is the ground it stands on, and none of it is the design. Read that note for why any of
this matters — this file only says what the tree is missing and why each gap is worth closing
on its own.

**The test applied here.** A prerequisite has to be justifiable with no resolver in the
sentence. Everything below is a defect today, answerable by a person with a gate in front of
them and no supervision anywhere in the picture — and each one is something the resolver design
would otherwise have to invent as a side effect, which is the worst way to acquire a
foundation. Three of them, and a fourth that looked like one and is not.

## 1. A question is a memory address, not a fact

`process.Registry` holds the docket in memory. `verse gates` lists `s.Reg.Pending()`,
`/gate/resolve` answers through `Reg.Answer`, and the store learns only that something is
pending: `mirror` writes `pending` and `pendingGate` onto the `process:instance` entity and
nothing else about the question.

Two defects follow, both present now. An approver reading `verse gates` gets a gate name and
`lastAction`, which is not enough to answer anything — the question knows the tool, the derived
effects, the paths and the subject, and none of it survives the trip to the entity. And a
question whose work outlives the daemon does not survive a restart at all: a brokered or remote
process keeps running while `verse down; up` empties the docket, which is the case
[process control](../../process-control.md) already legislates against when it says no process
control-state may live only in memory.

**What lands:** the question published as a record on the entity — id, gate, mode, tool,
derived verbs, resources and paths, subject, spawns, and the ref of the resolver it is addressed
to — and re-posted from that record when verse reconnects to work that outlived it.
`GateQuestion` carries all of that but the last, which comes from the binding: every resolver
is an entity, so a question addressed to one carries its ref and a question nobody is bound to
carries none. Empty means unbound rather than unknown.

One field the question does not carry yet: `GateQuestion.Subject` is declared and only the
trigger side sets it, so a process gate goes out with none. Publishing a record makes that
visible rather than fixing it — the subject has to be plumbed from the launch, or read off the
process's own `flow` field, which only a `process:instance` carries. Nothing at this layer needs
it and everything above does, which is why it is named here rather than deferred silently.

This is a shape change rather than a migration, which is worth knowing before it is planned as
one. The entity's `pending` field has no readers at all today, and `pendingGate` has exactly one
— `trigger/flowtree.go`, feeding `FlowNode.Gate` for `verse flow show`. No charter, script or
example binds either. So the record replaces them in one step instead of standing beside them,
and verse does not acquire a second spelling of a fact it already has.

## 2. Answering is two paths, and neither is attributable enough

`POST /gate/resolve` answers through the registry; `POST /processes/{id}/resolve` relays to a
worker's inbox. `api/api.go` records that these two already drifted once — `{id, approve}`
against `{decisionId, proceed}` for what the CLI presents as one verb — and the bodies were
unified afterwards, which is the warning rather than the fix.

Underneath, a second answer to an already-answered question returns 404 and leaves no trace. Two
people racing on the same gate is not exotic, and today the loser learns nothing and the record
shows nothing.

**What lands:** answering is one path with one vocabulary; a resolution always carries `by`; and
a losing answer is *recorded* as a stale decision rather than dropped — the behaviour
[process control](../../process-control.md) already specifies for the runner contract, made true
of the local docket too.

There is a vocabulary gap in the same place. Effects have verbs (`read`, `write`, `create`,
`delete`, `exec`, `network`, `call-agent`) and resource kinds (`file`, `gitref`, `host`,
`process`, `agent`), and answering a question is neither. Until a resolution derives an effect
naming what it answered, no grant and no gate can say anything about resolution at all.

## 3. `?since=` means different things on different runners

The runner contract says `GET /processes/{id}/events?since=N` replays from a cursor and then
goes live, with a resync frame below the watermark. `versed` honours that for a process the
local runtime owns, by handing the request to `processd`; for a brokered or remote one it
subscribes to the handle and streams from now (`versed/server.go`).

So "attach and see what led here" depends on where the work happens to run, which is exactly the
property the base-URL contract exists to remove. It is also the one operation everything
downstream is built out of.

**What lands:** replay-then-live with resync-below-watermark on every runner, and a test that
attaches at two cursors and gets the same events either way.

## Not a prerequisite: the journal has no address

`Process.journal` is a `[]string` behind a mutex (`process/loop.go`), readable only in-process.
The event stream is a bounded ring, and a consumer below its low-watermark gets a resync marker
and current state. `mirror` is deliberately an index — `lastAction`, `lastOutput`, a `say`
preview, `result` — and says so.

So the run that led to a decision is durable nowhere. A person answering "may this deploy
proceed" holds a gate name and one line of output; a person attaching late gets a snapshot. Both
are today's experience, with no resolver in the story.

**What lands:** `process:journal:<id>` as a slice served on demand by whoever owns the run — the
split [content federation](../../content-federation.md) already makes for files, where the index
is in the store and the contents are served by their owner, bounded here by cursor range and a
size cap the way `source.Content` bounds a served file. Nothing is warehoused, and the entity
stays an index.

The half that makes it worth having is *what the range is served from*: a durable log the runner
keeps, not the live ring. The `connect` analogy is exact — a content controller serves files
that are durable because their owner keeps them on disk — and without it the address is
decoration, since a range below the watermark would get the same resync-to-snapshot as everyone
else. Two consequences to carry rather than discover: the runner retains something, so retention
is a runner policy with a cap; and a served range needs three answers, not two — here it is,
that range is *not retained*, and no such process.

**And it is not a prerequisite**, which is the part worth stating rather than assuming. It is
the most infrastructure of anything here — persistence, a retention policy with an owner, a
reaper, a new mount — and the least constrained by anything that already exists. It also writes
per-process logs into the `--data` directory, whose survival across a binary upgrade
`contributing.md` lists as untried. And the first increment of the design does without it
deliberately.

*The trigger that would make it one:* somebody judging a question on the record and what the
ring still holds, and finding it too little. That is cheap to discover, and discovering it is
evidence where building it first would be prediction.

## What is deliberately not here

A resolver's position and notes, `resolvers:<kind>:<id>` entities and the verb that manages
them, layering. Those are the design, and each
of them fails the test at the top: you cannot justify them without saying "supervisor". What is
here is the shape of the world the design assumes and finds absent.

## What it buys

With the three in place, the note's own smallest-thing stops being a foundation and becomes a
step: a question is a fact a charter can match, answering is one attributable path with an
effect that can be governed, and attachment means the same thing wherever the work runs. What
remains is the resolver, which is then a small thing standing on ordinary ground — judging on
the question record and whatever the ring still holds, which is also what a person at the docket
gets, and finding out whether that is enough.

And if none of the design is ever built, all three are still improvements to what a person sees
when verse asks them a question — which is the honest test of a prerequisite.

All five steps are built. What they cleared the way for is
[the first resolver](2-first-resolver-plan.md).

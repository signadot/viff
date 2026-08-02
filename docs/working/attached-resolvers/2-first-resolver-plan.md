# The first resolver: implementation plan

Status: plan, nothing built. The design is [attached resolvers](../attached-resolvers.md) and
the ground it stands on is [0-prerequisites](0-prerequisites.md), now complete — a question is
a published, matchable, addressable entity; answering is sayable on the hard axis and reports
what became of it; and `?since=` means one thing on every runner, including replaying the
gates still open below a reader's window.

## The slice

One resolver, named by one spawn, answering that run's gates over HTTP.

`byRef` only, `driver: url` only, one layer. No standing `condition`, no escalation, no
`escalatesAfter`, no `exclusive`, no `llm` or `script` driver.

Why `url` first, when it is not the shape most people will write. It needs no model
credentials, so the whole path is testable deterministically against an `httptest` server; and
it is the driver where verse runs *nothing* — it hands over two streams and applies what comes
back. Everything the other drivers need is this plus a runtime, so building it first means
the second driver is a driver rather than a protocol.

Why one layer. The layer rule is that layer *n* answers what layer *n−1* raises, and with one
layer there is nothing above but `⊤`, which already exists as the human docket. Escalation is
then a verdict that does what silence already does, which is a smaller thing to add second than
to design in.

## Order of work

Four steps. Each leaves the tree green, and each is visible from the CLI, which is deliberate:
a resolver that cannot be seen bound, seen reading, or seen deciding is one nobody can debug.

### 1. A resolver is an entity, and a verb manages it

`resolvers:<kind>:<id>` holding `byRef`, `method` and nothing else this increment reads.
`verse resolver add <file>`, `verse resolver remove <ref>`, `verse resolvers`.

A verb rather than a raw write, for the reason the design gives — a write has nowhere to put
the check that two standing conditions do not overlap — even though this increment has no
standing conditions to check. Installing one is the same act either way, and the check lands
in a place that already exists.

A `condition` is REFUSED at add rather than ignored, until the layer that reads it is built.
The alternative is the hazard already in the tree: `launch.Spec.Resolver` is decoded, bound and
then dropped at `versed.launchOf`, so a charter can name a resolver on a spawn today and have
it silently do nothing.

**Proves:** an operator can install, list and remove one, and a resolver that would do nothing
is refused rather than accepted. Test: add, list, remove over the CLI; a `condition` is refused
with a message that says which increment will read it.

### 2. A spawn's `resolver:` binds an INSTANCE, and the question knows it

A binding is `resolvers:<kind>:<id>`; an instance of one is
`resolvers:instance:<binding-id>@<origin>` — the same shape and the same reasoning as
`process:claim:<chain>@<subject>`: a reserved kind inside the system it belongs to, so one
`--system resolvers` shows both what is installed and who is watching what, and there is no
singular/plural pair to mistype.

The instance is minted when the run starts, not when the first question is posed, because a
resolver reads context *before* the ask — that is the whole reason it is attached rather than
handed a question. `launchOf` carries `spec.Resolver` into `process.Launch`, the spawner mints
the instance beside `mirror.Reflect`, and the runtime stamps the BINDING's ref on the questions
its registry posts, where the field already exists and is already absent-means-unbound.

**Proves:** a question knows who owns it, and who is watching is a fact with an address. Test: a
spawn naming a resolver mints one instance carrying its binding and origin, and produces
questions whose `resolver` is that binding; a spawn naming none mints nothing and produces
questions with the field absent; and `{resolver: <ref>}` selects the first and not the second.
Both halves of the selection are already true of the record — what is new is the binding, so
the test that matters is the one that fails today.

### 3. The two streams

```
GET  /resolvers/<instance>/events?since=<position>   replay, then live
POST /resolvers/<instance>/verdicts    { id, verdict, why, say? }
POST /resolvers/<instance>/position    { position, notes? }
```

Addressed by INSTANCE, which is what makes these routes need no second parameter to say which
run they are about. A binding is bound to many runs over its life and to exactly one at a time
as an instance, so the binding's ref alone never named a stream; a query parameter would have
been the instance, spelled as a pair of things the caller has to keep in step.

It also makes the capability structural rather than a question about who is asking: an instance
names its binding and its origin, so "may this answer that" is "is the question's `resolver`
this instance's binding, and is the question's process this instance's origin". Nothing here
asks the store for per-slice write authority it does not have.

`events` is the process's own stream, not a projection of it: the design's rule is that a
resolver sees what an attached observer sees, and the moment it is a different stream there are
two orders to reconcile. So this route is the run's `?since=` with that check in front of it.

**Proves:** a service dials in, reads context before the ask, and answers. Test: an `httptest`
resolver that reads until an `EvPending` and posts a verdict; the run proceeds. And the
negative that matters: a verdict naming a question this instance does not own is refused — a
stream it may read is not a docket it may answer, and after step 2 that is a comparison of two
fields rather than a policy.

### 4. A verdict is applied as control

Through `answerQuestion` — the same path a person's `verse gate approve` takes, with `by`
stamped as the resolver's ref. Not a new answering path, and not a write: the record is
written from the decision after the fact, which is what
[deployment-and-steering](../../deployment-and-steering.md) requires.

`position` and `notes` land on the INSTANCE, because the process stream says what was decided
and the resolver's record says who was watching and on what evidence — and "who was watching"
is per run, so writing it on the binding would be one resolver's second run overwriting what
its first one saw.

**Proves:** a supervised run is indistinguishable in its stream from a human-supervised one,
except in `by`. Test: the decision appears on the process's read plane with the resolver's ref
as `by`, the question record settles the way any answered question does, and `verse gates`
shows it gone. And: a position that advances with no verdict emits nothing on the process
stream and does show on the resolver's record — which is the whole of "what was watched" being
a separate fact from "what was decided".

## What this plan does not build

No standing resolvers, so no overlap check and no condition vocabulary. No escalation and no
`escalatesAfter`, so no second layer and no transfer. No `exclusive`, so no advisory mode and
no question that two parties may answer. No `llm` or `script` driver. No resolver over trigger
gates, which reads the commit stream rather than a run's and is the second origin the design
says one mechanism serves.

Each of those is additive to this, which is the test of whether the slice is the right one.

## The residual to expect

Three residuals were expected here and two of them dissolved once the instance had a name of
its own. Addressing, the capability check and where a position is written were the same
question — *what is the live thing called* — and answering it once answers all three. What is
left is smaller and genuinely open.

**When an instance is reaped.** A process entity outlives its process deliberately, because the
outcome is the point. An instance's position and notes are evidence about a run that has ended,
which argues the same way; against that, a resolver bound to a busy subject mints one per run
and nothing collects them. This increment keeps them and says so, which is a decision to
revisit with a number rather than an oversight.

**Whether a run may be bound to a resolver it did not name.** The binding here comes from the
spawn. A standing resolver binds by condition, so the instance is minted from a match rather
than from a name, and the question of which one wins when a spawn also names one is the layer
rule — which this increment does not build and should not prejudge.

**Who may install a binding.** Unchanged and not this plan's to solve: a standing binding is
exactly as strong as the ability to add and remove it, and the store has no per-slice write
authority. It is the design's own open question and
[authority](../../roadmap/authority.md) is where it lives.

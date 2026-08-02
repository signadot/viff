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

### 2. A spawn's `resolver:` reaches the question

`launchOf` carries `spec.Resolver` into `process.Launch`; the runtime stamps it on the question
its registry posts; `mirror` publishes it on the record, where the field already exists and is
already absent-means-unbound.

**Proves:** a question knows who owns it, and a resolver's docket is a plain selection. Test: a
spawn naming a resolver produces questions carrying its ref, one naming none produces questions
with the field absent, and `entity.Pattern{Predicate: {resolver: <ref>}}` selects the first and
not the second. Both halves of that are already true of the record; what is new is the binding,
so the test that matters is the one that fails today.

### 3. The two streams

```
GET  /resolvers/<ref>/events?process=<id>&since=<position>   replay, then live
POST /resolvers/<ref>/verdicts    { id, verdict, why, say? }
POST /resolvers/<ref>/position    { position, notes? }
```

The `process` parameter is the increment being honest about its scope: an instance lives for
the duration of what it resolves, and over a run that is one run. A resolver bound by spawn is
bound to many runs over its life and to exactly one at a time as an instance, so the ref alone
does not name a stream. Whether the instance should instead be addressed as
`<resolver-ref>@<process-id>` — the shape origins already use — is the open question this step
will answer with evidence rather than in advance.

`events` is the process's own stream, not a projection of it: the design's rule is that a
resolver sees what an attached observer sees, and the moment it is a different stream there are
two orders to reconcile. So this route is `?since=` with the resolver's capability check in
front of it.

**Proves:** a service dials in, reads context before the ask, and answers. Test: an `httptest`
resolver that reads until an `EvPending` and posts a verdict; the run proceeds. And the
negative that matters: a verdict naming a question the resolver does not own is refused, since
the capability is per-resolver and a stream it can read is not a docket it may answer.

### 4. A verdict is applied as control

Through `answerQuestion` — the same path a person's `verse gate approve` takes, with `by`
stamped as the resolver's ref. Not a new answering path, and not a write: the record is
written from the decision after the fact, which is what
[deployment-and-steering](../../deployment-and-steering.md) requires.

`position` and `notes` land on the resolver's own record, because the process stream says what
was decided and the resolver's record says who was watching and on what evidence.

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

**Whether the instance is addressed by ref or by ref-and-run.** Step 3 takes the `?process=`
form because it is the smaller change, and names the alternative. A resolver watching several
runs at once will decide it, and this increment is what produces one.

**What a resolver's own record is called.** `resolvers:<kind>:<id>` is the binding; the
position and notes belong to an instance, and this plan writes them onto the binding because
there is one instance. That does not survive the first resolver bound to two runs.

**Whether a capability is needed at all in the first increment.** Step 3 puts a check in front
of the stream, and the store has no per-slice write authority to hang it on — the gap
[authority](../../roadmap/authority.md) already names. The check may end up being "this ref is
bound to that process", which is a check about the binding rather than about who is asking.

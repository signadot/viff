# Attached resolvers: supervision as a cursor on a stream

Status: notes from a conversation, nothing here runs. It builds on
[supervising process gates](supervising-process-gates.md) and refines one move in it — that
note proposes answering a gate by *writing*, and the steering invariant forbids exactly that.
The charter-level counterpart that does run is [an automatic supervisor](../auto-supervisor.md).

**The ask.** Say a supervisor of process gates *in a charter*, and apply it to the process
that charter spawns. Later, point the same supervisor at trigger gates. What follows is one
mechanism serving both, which is the only reason the second half is cheap: a supervisor is a
**stream**, a **durable position on it**, a **driver** from delta to verdict, and a verdict
that leaves through the control plane. Trigger gates and process gates differ only in which
stream.

## Answering is a verb, not a write

[deployment-and-steering](../deployment-and-steering.md) lists answering a gate as *control*
and states the invariant flatly: an intervention is never a store write, and control endpoints
are distinct from read endpoints so an operator cannot steer by writing. It names this exact
case — a pending gate is *observed* through the store, because `mirror` projects it, and
*answered* out-of-band through the resolver.

The invariant is protecting latency and priority: an intervention that queues behind the
backlog is an intervention defeated, and the store's bounded buffer may drop it. A rule
answering a gate is not an operator steering by writing — it is a reaction, which is what the
store channel is for. So split what the older note collapsed. **The store carries the verdict;
the control channel carries the intervention.** Concretely, `resolve` becomes a third action
verb beside `patch` and `spawn`, and it behaves like `spawn` does: the firing reaches
out-of-band into the process plane, and the truth comes back as the mirrored `decision` event.
[Process control](../process-control.md) already states that interlock — the POST is the
request, the event is the truth.

The order matters and is easy to get backwards. The verb POSTs, and the record is written
*from* the mirrored `decision`, after the fact. Nothing reacts to a verdict on the record by
resolving — the moment something does, the command entity is back wearing a record's clothes,
queueing behind the backlog it was supposed to outrank. The record is evidence, never an
instruction.

Routing it through the runner API is not merely available, it is what makes supervision
survivable. The replay path is already restart-safe: verse replays `pending@40`, re-routes to
a resolver, re-POSTs `resolve` with the same idempotency key, and dedups by cursor. The moment
supervision becomes a firing, the answerer's lifetime decouples from the asker's — a
supervising rule can outlive the daemon — and that convergence is what carries it. Today's
docket is in-memory (`process.Registry`; `verse gate approve` lands on `Reg.Answer` in
`versed/server.go`), which is sound only while the answerer is a human standing there.

## What a resolver is: a name with a protocol behind it

A resolver is an installed object, not a bare label that rules agree to match on. `vector:` and
`grant:` already establish that a name a spawn writes must resolve to something — an unknown one
fails as the launch is built (`process.NamedVector`) — and `NamedVector`'s own comment
anticipates the next step for named things, "a registry of vector entities, which is a different
feature". The resolver registry is that shape and gets there first.

It wants the check earlier than those two get it, though. A `vector:` that fails to resolve
fails a spawn, which is loud. A `resolver:` that fails to resolve at firing time leaves work
running with nobody watching it, so the name is checked when the charter is installed, and a
resolver that is named but *absent* at firing time drops its questions to the docket rather than
holding them. Refuse the typo early; promote the outage.

Making it an object rather than a matching convention buys the second thing too: somewhere for
the fields to live. The escalation default, exclusivity, which driver, the remit — those
accumulate, and on a bare label they accumulate at every binding site instead of once where the
supervisor is defined.

Which raises the question those fields ask as soon as a binding can restate them: object or
binding, who wins? The discipline the repo already applies to authority answers it — a child's
grant is `parent ∩ req.Grant`, and a provider fragment cannot widen a ceiling — so **a binding
may tighten what the resolver declares and never loosen it.** A resolver that declares itself
exclusive cannot be bound advisory by a spawn that would rather not be watched; one that is
advisory by default can still be bound exclusive for a particular high-stakes spawn. That is
also the property the compliance case actually wants, which is not "I am exclusive here" but
"nobody may bind me weakly".

What sits behind the name is a **protocol, not a body** — the same move that makes a runner a
URL. Three things can implement it:

- **Rules in the charter.** The default, and the case the ask is about: rules matching the
  questions addressed to this name, answering with the verb above. Cheap predicates for most
  questions, a judgment spawn for the residue.
- **A URL.** What `--runner <url>` and `/broker/<nonce>` already do for running work, done for
  answering it. Anything that reads the questions addressed to it and answers through the
  control plane is a resolver, so a vendor service, an SCM bot or a team's own reviewer daemon
  plugs in without verse knowing what is behind it. It needs a capability rather than the
  operator API — the nonce minted per binding, from the observers section below.
- **The built-ins.** `auto`, and the human docket. They earn their place beyond convenience:
  they are the base case that stops the regress, and the fallback when a resolver is bound but
  absent.

A third-party implementer's obligations are small and worth stating in the same register
process control uses: read the questions addressed to you, from a cursor you keep; answer
through the control plane with an attributable `by`; tolerate replay, since a question re-posed
after a restart carries the same id and must not be answered twice; and publish the positions
your verdict was drawn from. That last one is what makes a URL resolver supervisable rather than
merely trusted — rules and judgment spawns are readable where they live, a rule as the predicate
that fired and an assessor as a mirrored process, but a resolver holding its state on its own
side offers an outer supervisor a verdict with no evidence behind it.

**Two directions, and only one of them is an authority question.** `resolver: reviewer` on a
spawn is the *pull* form — the supervised work names who may answer for it. The *push* form is
a spawn that declares its scope, `spawn: { …, resolves: <process|subject|trigger> }`: an agent
started for the express purpose of watching something, which is how an agent-shaped resolver
gets a durable identity. That is worth having, because a supervisor that is a `process:instance`
inherits the whole plane — mirrored, attachable, steerable, cancellable, with a vector and a
grant of its own — rather than being a transient spawn per question. It also states the remit on
the hard axis instead of by convention: a supervisor's grant is *may resolve questions addressed
to it on this target, and nothing else*, so its only channel into the world is the verdict.

The two do not compete, because both reduce to the same record: the cursor is the continuity, so
whether a resolver is materialised as a standing process or re-spawned per question is a runtime
choice and not a semantic one. Push is where the authority question lives, though. Attaching a
supervisor to work you did not spawn is authority over somebody else's process, and it must not
become the way to slip a permissive resolver in front of an exclusive one. The safe rule is that
push attaches only within what the charter spawned — its own flow — and anything wider is a
request the target's binding may refuse.

## `resolver:` names an addressee, not a callback

The older note rejects a per-spawn `resolver:` — "a model call inline at every gated action,
and the supervisor's own gates need supervising, with no base case but `AutoResolver`". That
is fatal to `resolver:` as a **callback**. It has no force against `resolver:` as an
**address**.

Stamp the spawn's `resolver: reviewer` onto the question, and delivery is the reactive loop
that already exists: a rule matching `pending: { resolver: reviewer }` answers with the verb
above. No inline model call, no runtime coupling, and `launch.Spec.Resolver` — declared and
read by nothing ([authority](../roadmap/authority.md)) — becomes the field that says which
supervisor a charter expects. Selection (any rule matching any question) and binding (this
spawn, that supervisor) then compose, because binding is one more match dimension rather than
a second dispatch path.

The prerequisite is that `mirror` publishes enough of the question to decide on. It writes
`pending` and `pendingGate` today, so a supervisor could route by gate name and nothing else.
The predicates worth having are the ones `GateMatch` already carries — verbs, resource
kinds and refs, paths, branches, `onPush` — so the derived effects have to ride onto the
entity with the question.

**A binding is advisory unless it says otherwise.** Binding a resolver does not take the
question off the human docket, so a person can answer first: the docket joins by id and the
first answer wins. What happens to the loser is a gap rather than a design. `Registry.Answer`
returns false for an id already answered and `/gate/resolve` turns that into a 404, so today
the second answer disappears — while the runner contract specifies the behaviour worth having,
a `decision` marked stale, a recorded no-op rather than a silent drop. Exclusivity needs the
latter regardless: an override that arrives too late is exactly the thing you want in the
record. So a losing answer is recorded, not 404'd, and that is an ask rather than a
description.

Advisory is also the wrong default for the case people bind for — a security review a person
may not rubber-stamp — so exclusivity is a dial, subject to the tightening rule above:
`resolver: { name: security-review, exclusive: true }`.

What it governs is *whose answer counts, and for how long*. It has nothing to say about who may
see the question, and that distinction is the whole of it. Under an exclusive binding an answer
from anyone else does not resolve the gate, and verse can enforce that exactly where it already
enforces the id: the resolve endpoint knows the binding and can refuse a `by` that is not the
bound resolver. Which is real against the failure the dial is for — somebody working down a
docket without noticing that this one belongs to `security-review` — and not real against
somebody holding the operator API, where pretending otherwise would make the dial a lie.
Control is an out-of-band POST by design, so an override stays possible and lands as an event
naming both the person and the resolver they went around. Exclusivity is a claim on the record,
not a lock.

Scopes make one question match several bindings at once, so exclusivity needs a composition rule
or it means nothing: all-advisory is a race, an exclusive inner binding shadows the outer one,
and an exclusive outer binding makes the inner one decorative. The rule is one this note already
has, lifted from an object to the lattice — **specificity decides who answers, authority decides
who may be shadowed.** The innermost matching binding owns the question, as the most specific
rule wins everywhere else, *unless* an outer binding is exclusive, which a narrower one may not
displace. That settles the compliance case the right way round: an org's subject-scoped
`security-review` cannot be pushed aside by a permissive process-scoped resolver that the
spawning charter declared for itself, which is the same hazard the push form names, now covered
by a general rule rather than a special case.

Two more things follow, and neither should be available without the other half.
**Exclusive requires a deadline**: exclusive with no `escalatesAt` turns a resolver that is
down into a question nobody is permitted to answer, which is worse than no supervision at all.
Ownership and exclusivity lapse on the same clock, and the question returns to the general
docket when they do. And **exclusivity is not a security boundary**: it decides who answers an
`ask`, never what an answer may authorise, so what must not happen at all belongs on the hard
axis — outside the spawn's grant and outside any sponsor's, where `Grant.Permits` refuses
rather than asks.

## Routing the question is not supervision

Handing a resolver the question hands it a keyhole and a filename. To supervise it needs the
context leading to the gate, and that context is durable nowhere it can reach: `mirror` is
deliberately an index — `lastAction`, `lastOutput`, a `say` preview, `cursor`, `result` at the
end — while the run itself lives in `Process.journal` in memory (`process/loop.go`) and in the
runner's bounded event ring.

So the binding should not summon a resolver per question. It should **attach** one at spawn,
and attachment here has a settled meaning: a cursor and an address. `resolver: reviewer` opens
a supervision record — `{resolver, cursor, verdicts[]}` — and the resolver is thereafter a
client of `GET /processes/{id}/events?since=<its cursor>` (`versed/server.go`), the same
contract verse itself consumes. When a question is posed it reads from its cursor to the
question, and *that delta is the context leading to the gate*, exactly. First gate: the whole
run from event 0. Tenth gate: what happened since it last decided.

Nothing about that is new machinery. `Subscribe()` fans out already — mirror and the reaper
are two independent subscribers to the same bus — so a third is a client, not a mechanism. And
a hold costs a cursor, not a buffer: an attached resolver that has not been needed in an hour
is an int on an entity, not a warm context. It is attached the way verse is attached to a
brokered process — durably, resumably, holding nothing.

The rest of the case file is already durable. Why the process exists and what it may do is on
the entity from `mirror.Meta`: `chain`, `trigger`, `task`, `doer`, `grant`, `tools`. The
journal supplies what it has done. Those two plus the question are the whole briefing.

**What this costs, since a round trip per gated action sounds worse than it is.** Two things
bound it before a resolver is involved at all. The vector decides what asks: an `auto` action
poses nothing and blocks on nobody, so the ask-set is a charter's own choice, written in the
same charter that names the resolver. And the delta bounds the reading — an attached resolver
sees what happened since it last decided, not the run, so the tenth gate of a long process is
not ten times the first. That is the payoff of a cursor over a fat question or a warm context,
and it is why the predicate case costs a store read and a position advance with no model in
sight.

What it does cost is latency and a held context. A question that used to be answered from
memory now waits for a commit, a firing and a POST, and the process holds its conversation
while it waits. Against a human at a docket that is noise; against an auto-resolving rule it is
the difference between instant and one commit. The shape to avoid is the one a charter can
easily write by accident: a broad ask-vector over a chatty process with a judgment-driven
resolver is a model run per tool use. The cheap layers exist precisely so the expensive one is
the residue rather than the rule.

## The journal needs an address

The ring is bounded, and the contract's answer to a cursor below the watermark is
resync-to-snapshot. For verse that is benign, because the entity is level truth. For a
resolver it is fatal in precisely the wrong place: "the fine-grained journal between N and C is
lost, but current state is exact" means being handed `lastOutput` and asked to judge a deploy.
**Give the journal an address and leave it with its owner.** `process:journal:<id>` as a slice
served on demand by the runner, the split [content federation](../content-federation.md)
already makes for files: the index is in the store, the contents are served by whoever owns
them, and nothing is warehoused. A resolver then follows the reference and pages what it needs,
bounded by cursor range — `!list-paths` reads that way already, and an assessor spawn gets a
read tool over the journal rather than a prompt-stuffed transcript.

**And the served range reads a durable log, not the live ring**, which is the half that makes
the address worth having. The `connect` analogy is exact here: a content controller serves files
that are durable because their owner keeps them on disk, and nothing about that puts them in the
verse. The ring stays what it is, a live tail for subscribers; a range is answered from what the
runner retained. Without that the address is decoration — a resolver asking for events 12–40
would get the same resync-to-snapshot the ring gives everyone else, which is the thing it was
introduced to escape.

The alternative was **keep-up** — the resolver advancing its cursor at every boundary rather
than only at gates, so it never falls below the watermark. Cheap for a predicate-shaped
resolver, expensive for an agent-shaped one, and fragile in exactly the case that most wants
supervision: a long run between gates. It is also only a way of *avoiding* the loss rather than
repairing it, and it does nothing for the party who was not already attached. A durable log
serves the resolver, the human at the docket and the late observer with one mechanism, which is
the test worth applying to all of this.

What it costs, stated rather than discovered: the runner retains something, so retention becomes
a runner policy with a cap, and a served range needs three answers rather than two — here it is,
that range is *not retained*, and no such process. Collapsing the middle one into an error is how
a supervisor comes to judge on less than it asked for without knowing.

## What a resolver may see, and what it may not

The stream carries what was *reported*: narration (`say` with an empty `by`), tool calls and
their outputs, boundaries, children, status. It does not carry the driver's internal reasoning,
and it should not. Supervising the reported record is the line
[external processes](../external-processes.md) already draws between intercepting and
attesting, and it is what makes one charter supervisor work identically over a local loop, a
k8s Job, and a human dialing into a broker. A supervisor that needs chain-of-thought is a
different design, and it would not port past the local LLM runner.

What a resolver may *do* about what it sees is a separate question, and the hard axis does not
simply stay where it is. `Grant.Permits` refuses ahead of the vector today, so an action outside
the envelope is never asked about at all — the right default, and the wrong ceiling for a
supervisor. That is the next section.

## Two grants: what a resolver may do, and what it may allow

`Grant` means one thing today — what a process may do — and a resolver needs two.

Its **acting grant** governs the resolver itself: for a judgment assessor, read the journal,
read entities, produce a verdict, nothing else. Its **sponsoring grant** governs what it may
authorise somebody *else* to do, and that one has to be able to exceed the supervised spawn's
envelope, or supervision can only ever say no more often than the vector already does.

That is the shape of every real escalation: I may not push to `main`, I ask somebody who may,
and the push happens under their authority with my hands on the keyboard. Verse forbids exactly
that today — outside the grant is "not permitted, and not something a gate can approve" — so
sponsorship is a change to the enforcement order rather than a reading of it. The loop needs a
third outcome between permitted and refused: **askable-with-a-sponsor**.

Three properties keep it from being a hole. It is **per action** — the process's own grant never
changes, so nothing ratchets and a child still delegates from the parent's narrow envelope
through `Grant.Subset`. It is **bounded** by the sponsoring grant, so a sponsor cannot authorise
what it could not authorise itself. And it is **attributed**: the effect lands on the journal as
performed under a sponsor, which is the first time "every resolution is attributable" carries
authority rather than only audit. One guard belongs in the design rather than in a later fix —
only gates the vector marks escalatable may reach a sponsor at all, or an agent enumerates the
envelope by proposing actions and reading which ones get asked about and which get refused.

**Answering is itself an action, so it needs a verb and a resource.** `resolve` derives an
effect like anything else — that is what lets a resolver's own resolution be gated, and it is
what layering rests on — and the vocabulary has to grow twice for it to be sayable. A verb of
its own, `resolve`, for the same reason `call-agent` is a verb rather than an `exec`: a vector
that cannot name resolution separately can only ask about *everything* an inner resolver does. A
resource kind for the question, `gate`, so the effect names what is being answered. Without the
second one a sponsoring grant over resolution — "may authorise resolving deploy gates on
`change:pr:*`" — is a resource predicate with no resource, which is to say unspellable.

**The binding confers the answer, the grant governs the rest.** A resolver's acting grant does
*not* have to name the `resolve` verb to answer the question it was handed: being handed one is
what the binding means, and a grant that had to restate it would produce a supervisor that reads
and then cannot speak — a silent stall, from forgetting a verb. The capability is scoped to that
question and nothing else, so it widens nothing: `grant: read-only` on an assessor reads as "may
read the world, may answer what it was given". Everything a resolver does *besides* answering —
the advisory check, a `network` call, a write — is ordinary and needs the envelope. Note this
governs the grant and not the gate: the effect still exists, so an outer vector can still ask
about a resolution.

**A sponsoring grant is written in portable dimensions.** A resolver may sit on another system
entirely, where its tools are not the target's tools and its paths are not the target's paths.
`GateMatch` spans both kinds. `Tools` is a self-reported name that means whatever the local
driver calls it, and `Paths` is somebody's filesystem — those are local vocabulary. `Verbs`,
`Resources`, and the git-state predicates `Branches` and `OnPush` describe the action's target
rather than the answerer's machine, so those travel. An acting grant may use the local
dimensions, because it governs the resolver on its own machine, and a sponsoring grant should be
restricted to the portable ones, because it is a statement about somebody else's. That settles
the confused-deputy question in the same stroke: a sponsor authorises "push to `git:repo:verse`
on `main`", not "run this bash string", and an opaque tool is a poor candidate for sponsorship
precisely because the derived effect is the only part of it that travels.

**At the trigger layer it is per subject.** `security-review` may sponsor a deploy for
`demo:pr:*` and nothing at all for `release:*`, so the sponsoring grant is a function of the
question — a match over the subject, and the grant that match sponsors — which is the shape
provider selection already has.

That turns *remit* from a rule into an object. In [the automatic
supervisor](../auto-supervisor.md) the remit is a companion rule rewriting an out-of-remit
`approve` into `escalate` after the supervisor has spoken: soft, author-maintained, and correct
only while the two rules remain exact complements. As a per-subject sponsoring grant the same
remit is refused by construction — a supervisor that says yes outside it does not produce an
over-wide approval for another rule to catch, it produces nothing.

The tightening rule composes unchanged: a binding may narrow a sponsoring grant and never widen
it. What does not compose is the bound at the top. Sponsorship ought to be limited by the
authority of whoever *installed* the resolver, and a trigger has no author identity, so "who may
declare a resolver that sponsors production deploys" has no answer in the model.
[Authority](../roadmap/authority.md) derives an envelope from a caller's request; this wants one
attached to the charter itself. Named here as unowned rather than solved.

## What a third party attached to a supervised process sees

It sees the supervision, inline, and that is not optional. Control application is emitted as an
event on the one ordered stream, so an observer on `GET /processes/{id}/events` — a person on
`verse process attach`, an auditor, a second resolver — reads the whole exchange in order:

```
pending  @40 {gate: deploy, tool: run_bash}
decision @41 {proceed: false, by: "resolver:reviewer"}
say      @42 {by: "resolver:reviewer", text: "use the staging bucket"}
exec     @43 {tool: run_bash, text: "…staging…"}
```

The interlock rule exists for races, but the property it buys here is worth naming on its own:
**supervision is never a side channel.** There is no supervised run that reads as unsupervised
to an attached observer, and no way to answer invisibly.

What that stream does *not* say is what was watched. A cursor advance with no verdict emits no
event — silence is a legitimate output — so on the process stream a resolver that read the last
twenty events and judged them fine is indistinguishable from one asleep since event 3. That is
the liveness indistinguishability of the driver signature, showing up a second time as an
observability gap.

So the resolver is a second subject with a stream of its own: the store's, `Since` over the
supervision record. Three things live there and nowhere else. **The evidence base** —
consecutive positions bracket the delta a verdict was drawn from, so the process stream shows
the conclusion and the record shows what it was made of. **Attention without intervention** — a
moving cursor with no verdicts is a resolver doing its job, a stale one is absence. And
**consistency** — whether a remit is applied the same way across twelve subjects is a question
about the resolver, not about any one run. One decision, two readers: a fact on the record, an
event on the process stream, which is the broker's own phrasing for the same shape.

Neither attachment substitutes for the other, and the rule of thumb is worth writing down: *the
process stream is what was decided; the resolver record is who was watching and on what
evidence.* Attach to whichever the question is about.

**Watching is not answering**, and that is already the architecture. Observation rides the
runner face; resolution is the operator API, and a worker on a nonce'd path never reaches the
entity or gate surface. So a third-party observer is unprivileged by construction, and a
third-party *resolver* needs a capability rather than the operator API — the nonce trick again,
minted per binding and scoped to the questions addressed to that resolver.

The late observer is also the third party the journal decision above is for. Attaching mid-run
hits the same watermark a resolver would, and gets resync-to-snapshot: current state, no run.
A durable `process:journal:<id>` serves the resolver, the human at the docket and the late
observer with one mechanism; keeping up would have served only whoever was already there.

One corner to name while it is in view: a `judgment` assessor is an ordinary process — mirrored,
attachable, with a journal of its own — so a resolver's reasoning is observable by anyone who
can observe processes, and its briefing carries whatever the supervised run's output carried.
Supervision inherits the process plane's confidentiality properties, and those are currently
none in particular.

## The trigger side is the same object

`trigger: { … resolver: … }` opens a cursor too, and it has an obvious referent: the store's
own stream — `Since(rev)` delivering `entity.Commit{Rev, Changes}` — narrowed to what the rule
reads. The engine already computes that read set, since a trigger is skipped at a commit that
touched none of the subtrees it reads. Both planes are `Observe : Since`; process control says
as much about the runner contract in its opening. So `resolver:` means one thing in both
places: open a durable position on the stream that leads to the decision.

What it buys is the same thing it buys at the process layer — the path instead of the point. A
match sees `stage: reviewed`. A resolver with a cursor sees that this PR went
`reviewed → open → reviewed` three times in ten minutes, that `tests: green` predates the last
push, that a sibling firing denied something adjacent an hour ago. None of that is expressible
as a predicate over current state, and all of it is what a person means by "let me see what has
been going on".

One thing the trigger side has to settle that the process side does not: which end of the
interval the question is. A process gate's upper bound is obvious — the cursor its `pending`
landed at. A firing's is not. The rule evaluated as-of C, and the resolver may take minutes
while the store moves on, so `[position, C]` keeps the resolver inside the firing's own
semantics and it judges the world the rule judged, while `[position, now]` is what you want
when the `deny` that matters landed after C. That is the one genuinely open fork, and it is not
the journal question in disguise: this one decides what a verdict is *about* rather than what evidence
was available to make it.

## The driver of a cursor

Signature first, because it settles the rest:

```
(position, delta, question?) → (verdict?, position', notes?)
```

Two things in that shape carry weight. **The verdict is optional** — a cursor driver may
advance without deciding, which is what makes attachment cheap at any cadence. Silence
is a legitimate output and is not the same as declining, which is a verdict. And **the position
is an output**, so the driver's entire continuity is a durable int plus what it wrote down: no
session, no warm context, resumable by construction. That is the reconciler shape verse runs
everywhere, with a stream position for its resource.

Optional-verdict is what forces the clock, and now it is structural rather than prudent: for a
driver whose legitimate output is silence, "has not decided" and "has not caught up" are
indistinguishable from outside. So the record carries both — where I am, and when this question
stops being mine. Stamp the escalation instant absolutely, the way a goal's deadline is stamped
so a restart cannot slide it.

Three implementations, and only one of them is new.

- **Agent.** An LLM cursor driver is a process driver whose conversation is a delta: spawn with
  the delta as briefing, a read tool over the stream for paging further back, an `answer`
  contract typing the verdict. That is `judgment`, the stubbed `RequirementKind` in
  `trigger/formula.go`, and it needs no new execution model — it is the automatic supervisor's
  charter with a stream for a prompt.
- **Human.** A rendering. `verse gates` shows a question today; with a cursor it shows *what
  changed since you last decided*, which is what a review surface has always been.
- **Predicate.** The common case and the cheap one: match the question, read the subject and
  whatever else the store holds, answer. No model, no spawn, no waiting — and it is what most
  of a remit is, since most of what a supervisor decides is decided by what the thing is rather
  than by who agrees.

A structural dividend of the cursor, worth knowing about and mostly not worth reaching for:
`OpEventually`, `OpAlways` and `OpSequence` are stubs in `trigger/formula.go` because they state
things about an *interval*, and a point-in-time evaluation has none. A cursor is that interval,
so `always` becomes "no commit since my position violated this" and `sequence` becomes "these
happened in this order since my position". The temporal grammar falls out rather than being
added. But a temporal or collective answer is rarely what a per-tool-use gate wants — a quorum
on `write_file` is a strange thing to ask for — and the altitude where it stops being strange is
a firing, which is where those leaves already live. So: available, seldom right, and not a
reason to reach for a formula when a predicate says it.

## Flows, and the two planes

A resolver attached above one process — at `subject` scope or wider — is what people will
actually want, since "everything that happened about `demo:pr:1`" is what a person means by
context. It is also where this stops composing for free: two planes, independent cursors, no
common order. The broker already meets this and answers honestly — the axes relate only through
its sequencer.

Do not build a global sequencer for it. Carry a cursor **pair**, and lean on a property that
already holds: a process's spawn and its terminal are both store commits, because `mirror`
writes them. The store stream is the spine — ordered, durable, carrying the anchors — and
process streams hang off it as fine grain between anchors. A flow resolver reads the spine and
descends into a process's events where it needs to know how a run reached where it is. That is
enough causality for supervision and needs nothing new.

## A charter, annotated

Nothing below runs; the point is to see whether the shape is sayable. A charter file already
holds several definitions separated by `---`, and their kind is inferred structurally — a
`condition` makes a trigger, a `level` and a `match` make a stratum — so a resolver arrives the
same way, by having neither.

### The whole loop, smallest form

```tony
# ── the resolver: named once, referred to by spawns ──────────────────────────
- name: reviewer
  escalatesAt: 10m            # unanswered by then, the question goes back to the docket
---
# ── the supervised work ──────────────────────────────────────────────────────
- name: validate
  condition:
    subject: { system: change, kind: pr, value: { stage: proposed } }
  action:
    spawn:
      driver:   llm
      task:     "Validate $subject and fix what is trivially wrong."
      grant:    no-delegate   # what the WORK may do at all
      vector:   ask           # …and how much of it asks: all of it
      resolver: reviewer      # ← the binding. opens a cursor at event 0
---
# ── the resolver's body: one rule per class of question ──────────────────────
- name: reviewer-docs
  condition:
    subject:
      system: process
      kind:   instance
      value:
        pending:
          resolver: reviewer
          verbs:    [write]
          paths:    ["docs/**"]
  action:
    resolve: { verdict: proceed, why: "docs are inside the remit" }
```

Three things are doing work. **The binding is one field** — everything else about `validate` is
what a spawn already says, which is the test of whether this is a new concept or a new address.
**`resolve:` names no target** *here*, because the rule's subject is the process carrying the
question and a blocked loop has exactly one question outstanding, so "the question on
`$subject`" is unambiguous. That is process-scope sugar and does not generalise: a firing poses
one question per gate and waits on all of them at once — `trigger/engine.go` runs a goroutine
apiece — so a trigger-layer resolver names the gate, `resolve: { gate: sign-off, verdict:
proceed }`. Gate names are what the charter declared and what an approver reads, so they are the
charter form; the question id (`origin~dN`) is the wire form and nobody should have to write it.
And **`verbs`/`paths` under `pending`** are the derived effects this
note asks `mirror` to publish; without them the rule could route by gate name and nothing else.

### The residue gets a judgment

```tony
- name: reviewer-judges
  condition:
    subject:
      system: process
      kind:   instance
      value: !and
      - { pending: { resolver: reviewer } }
      - !not { pending: { paths: ["docs/**"] } }   # …what the cheap rule did not answer
  action:
    spawn:
      driver: llm
      task:   "Decide whether the pending action on $subject should proceed."
      frame: |
        You are the reviewer for this run. The journal from your last position to the
        question is your evidence; read further back if you need it. Answer `escalate`
        whenever a person should decide this, and never to be agreeable.
      answer:
        verdict: !or [proceed, deny, escalate]
        why:     !irtype string
      grant:  read-only     # the ACTING grant: read, judge, and nothing else
      vector: auto          # its own questions are not re-supervised — the base case
      tools:  [read_journal, read_entity]
    onDone:
      resolve: { verdict: $data.verdict, why: $data.why }
    onFailed:
      resolve: { verdict: escalate, why: "the reviewer did not decide: $err" }
```

The `!not` is the load-bearing line: predicates first, judgment for the residue, and the two
must be exact complements or a question is either answered twice or not at all — the failure
mode `auto-supervisor.md`'s `remit` rule has, which is the argument for keeping the expensive
driver behind the cheap one rather than beside it.

**Three verdicts, not two.** `escalate` hands the question back to the docket instead of
answering it, which is what makes "a supervisor that fails to decide has decided to promote"
expressible rather than conventional — `onFailed` is that doctrine in one line. And the
read-only acting grant with `vector: auto` is the regress base case written as charter rather
than asserted as doctrine; read-only is enough because answering the question it was handed
comes with the binding rather than out of the envelope.

**TODO — settle the form of `resolve:` against coupled examples.** This charter spawns a judge
and resolves in its hook, so the two are separate actions joined by `onDone`. The inversion is
also sayable — `resolve:` as the frame, carrying the spawn that decides it, with `onDone:
proceed` and `onFailed: deny` — and it is not obviously worse: it states the gate first, and it
makes "spawn a judge and forget to resolve" unwritable, where the current shape leaves that as a
silent stall until `escalatesAt`. What should settle it is a set of example charters exercising
the cases where the two forms come apart, written out rather than argued:

- a judge whose spawn *fails* — which form still answers, and with what;
- a verdict of `escalate`, which `onDone: proceed` / `onFailed: deny` cannot express at all;
- a deny that carries `say`, and where that text comes from in each form;
- a verdict that proceeds *under a sponsoring grant*, which has to name the sponsor somewhere;
- two gates open on one firing, where the gate name has to be at the top;
- the cheap case — a predicate answer, no spawn — which must stay one line in either form;
- an outer resolver gating the inner's `resolve`, where the effect's resource has to be legible.

### Sponsorship, per subject

```tony
- name: security-review
  escalatesAt: 4h
  exclusive: true                  # declared on the object, so no binding can weaken it
  sponsors:                        # what it may authorise OTHERS to do, by subject
  - match: { system: change, kind: pr }
    grant:
      verbs:     [write]
      resources: [{ kind: gitref, pattern: "verse:refs/heads/main" }]
      onPush:    true
  - match: { system: release }
    grant: none                    # a release is never this resolver's to clear
---
- name: land
  condition:
    subject: { system: change, kind: pr, value: { stage: approved } }
  action:
    spawn:
      driver:   llm
      task:     "Land $subject."
      grant:    no-delegate        # NOT permitted to push main…
      vector:   ask
      escalate: [{ verbs: [write], onPush: true }]   # …but may ask a sponsor about this
      resolver: security-review
```

`land` cannot push `main`: it is outside its grant, so today the action is refused and never
asked about. `escalate:` is the vector-side half of sponsorship — it names the actions that may
become *askable-with-a-sponsor* rather than refused, and only those, which is what stops an
agent enumerating the envelope by proposing things and watching which get asked about. If
`security-review` proceeds, the push happens attributed to it, `land`'s own grant is untouched,
and a child of `land` still delegates from `no-delegate`.

`sponsors` is where remit stops being a companion rule. The same resolver clears a push for a
`change:pr` and can never clear anything for a `release:*` — not because a second rule rewrites
its answer afterwards, but because there is no envelope for it to answer within.

### The push form

```tony
- name: watch-the-migration
  condition:
    subject: { system: change, kind: migration, value: { stage: running } }
  action:
    spawn:
      driver: llm
      task:   "Watch this migration and answer the gates it raises."
      grant:  read-only
      vector: auto
      resolves: subject      # this migration's questions, however many processes raise them
```

Pull names who may answer; push starts something that answers. Same record and same cursor
either way — what differs is who declared the relationship, which is why push is bounded to what
the charter is already about.

**`resolves:` is a scope, and there are three of them.** They nest, and they select on fields of
the *question* — which is what keeps them a match rather than a mechanism, and what makes them
mean the same thing at both layers. `GateQuestion` already carries `Origin` and `Subject`: for a
process gate the origin is the process, and those fields are mirrored onto its entity as the id,
`flow` and `trigger`; for a trigger gate the origin is the rule, and the question carries them
directly, with no `process:instance` in sight. An instance keys on the question's origin, so
neither layer is a special case:

| | what it answers for | ends when |
|---|---|---|
| `process` | one origin — a run, or a firing | that origin is done |
| `subject` | every question about that entity, whatever raises them | the subject is done, which nothing says |
| `trigger` | everything the rule matches, for as long as it is installed | the rule is removed |

**A scope is bulk binding, not a bigger resolver.** The tempting reading is that a wider scope
makes one resolver watch many processes, which would mean a cursor *set* rather than a position,
a retirement rule so the set does not grow for the life of the rule, and a plural in the driver
signature. Read it instead as a selector: a scoped binding instantiates one ordinary resolver per
process entering the scope, each with the single cursor the driver already defines. Retirement is
then an instance ending with its process rather than a rule about pruning, a process that joins
late gets a fresh instance at position 0, and nothing about a resolver has to change to be bound
widely.

What that costs is worth being explicit about, because it is the reason someone reaches for a
wide scope: instances do not share a context, so nothing notices the same suspicious change
landing across three subjects. The recovery is that **scope and evidence are different axes**.
Scope says whose questions are mine; evidence says what I may read to decide them, and the store
is right there — `trigger: <name>` selects an instance's siblings, so cross-process reasoning is
a read rather than a wider attachment. That also forces such a claim to be a fact someone wrote
down rather than a side effect of what happened to be in one context window.

Lifetimes are not equally well defined, and it is worth saying which is the weak one. An
*instance* always ends with its origin, so nothing accumulates inside one. What differs is how
long the *binding* goes on instantiating: `process` stops at that origin's terminal, and
`trigger` stops when the rule is removed, which the goal reap already models — a settled goal's
owned triggers go with it rather than outliving what nobody is pursuing. `subject` has no such
moment, because an entity is durable and "this PR is finished" is a charter's opinion rather than
a fact the store carries. So a subject-scoped binding either takes an explicit end condition or
keeps spawning instances forever. That is the scope to be careful with, and the one people will
reach for first.

## What has to be structural

**Regress.** `AutoResolver` is a bad base case, and with this shape the base case is structural
instead: process control already rules that governing a process must not require another
governed process. A rule carrying `resolve:` may not carry `gates:` — a firing that answers a
question must not pose one — and that is an install-time refusal, which is this repo's habit:
stratum level clashes, provider grant ceilings, the `op` dimension.

What stops the regress is not flatness, though. **`resolve` derives an effect like any other
action**, so a resolver's own resolution can be gated by its own vector, and that question
belongs to the next scope out. A resolver's questions therefore go one scope out rather than
straight to the built-ins, and terminate there: `process` ⊂ `subject` ⊂ `trigger` ⊂ built-in is
finite and strictly widening, so the scope lattice *is* the termination argument — no depth
counter, no special case. Liveness is bounded by the deadline chain the last section describes,
where a child inherits the parent's remaining time.

**Which reaches process-shaped resolvers and not rule-shaped ones.** A vector gates a process,
and the in-charter implementation — the default, and the only one in the PoC — is a firing. It
has no vector, and its one gating surface is the `gates:` the paragraph above forbids it. So an
agent or a URL resolver can be supervised by an outer one, and a rule's answer is final within
its scope. That is a limit worth stating rather than discovering, and it is not obviously the
wrong default: a rule is a predicate somebody can read, so supervising it is closer to reviewing
the charter than to watching a judgment.

The ban is also the only thing keeping it that way, and it was written to stop a regress the
lattice now stops on its own. So the extension, when someone wants supervised rules, is to
relax it rather than to add machinery: **a resolving rule may pose a gate only to a strictly
wider scope, never to its own.** Termination survives unchanged, since the lattice is finite and
strictly widening, and layering becomes uniform across all three implementations. Not now —
nothing runs yet, and loosening a ban before that is how a design acquires a hole it cannot
locate later.

**Layering is opt-in, and must stay that way.** Deriving an effect for `resolve` *enables* the
stack; it must not impose it. An outer scope whose vector is `auto` for `resolve` — the default
— changes nothing and every question still costs one round trip. A charter that wants its
supervisor supervised says so, and pays for it, because the cost is multiplicative in the scopes
bound.

**An outer supervisor reads down, it does not attach sideways.** Deciding "R may resolve this"
needs the context that produced the question, and none of that requires a second attachment
relationship: the effect names the question, the question's id names the process, the process
entity carries subject, trigger, chain, task, grant and the journal address, and the deciding
resolver's own record says what it read and what it decided before. So **a resolver attaches
only ever to a stream of questions, never to another resolver** — the layering lives in the
questions and the context comes from following references, which is the reading surface
[§What a third party sees](#what-a-third-party-attached-to-a-supervised-process-sees) already
defines. The one thing that has to be true for it: `(resolver, process)` needs a derivable
address, or an outer supervisor can see *that* a verdict was reached and never what it was drawn
from.

**Liveness.** Promotion is free in the sense the older note means — an unanswered question
stays in `verse gates` — but only within one daemon lifetime, because that list is
`s.Reg.Pending()`, read out of memory. A question whose process outlived verse comes back on
the entity and not on the docket, so promotion is free while nothing restarts and free
afterwards only once the question itself is durable. That is the same conclusion the rest of
this note reaches from the other direction. What is never free is a *bound* resolver silently
owning a question nobody is looking at, which is what the absolute `escalatesAt` above is for:
a supervisor that fails to decide has thereby decided to promote, made structural rather than
charter-authored.

**Inheritance.** `call_agent` children already take the parent's resolver under the
inherit/careful child modes (`Supervisor.Resolver()` exists for exactly that), so the charter
binding has to mirror it: attach to the tree, cursor per process. Otherwise the escalations
that most want supervision — the ones from children — arrive unsupervised.

**Deny is not terminal, and should be conversational.** `process/loop.go` hands back `Blocked`
plus a reason and the driver re-plans, and `Resolution.Steer` folds guidance in whether or not
it proceeded. So `resolve: { proceed: false, say: "use the staging bucket" }` belongs in the
grammar from the first cut. It is also why the resolver's prior verdicts belong on the record:
the next question is judged with the last instruction in hand, and the journal shows whether it
was heeded. Supervision is a conversation over a shared record, and the record is the only
continuity it has.

## Names

`--driver scripted|llm` already means the thing that proposes actions inside a process. If a
cursor driver proposes verdicts from a delta, those are one interface with different context
and proposal types, and that is worth saying out loud rather than minting a third word for the
same shape. `Supervisor`, `Resolver` and `Driver` are all now in play at two altitudes:
`process.Supervisor` is the in-loop enforcement object, the automatic-supervisor note already
needs a table to keep two meanings of *supervisor* apart, and the charter object is a
`Resolver` on the wire because the field and the interface already say so. Settle it before any
of it ships — the `policy` rename is the argument for doing it now.

## The smallest thing that would prove it

Publish the question richly on the entity, honour `resolver:` as an address that opens a
cursor, add `resolve` as an action verb going out through the runner API, and write the
supervising charter as ordinary rules with predicate answers only — no formulas, no judgment
spawn, no journal federation, and no exclusivity (advisory binding is the honest default to
start from, since it cannot lock anyone out of a question). `process` scope only, and `resolve`
ungated at the scope above: the derived effect is what makes layering *possible*, and layering
is not what proves the idea. One implementation behind the name,
the in-charter one — a URL resolver and a `resolves:` spawn are the same object reached
differently, and neither is what proves the idea. No sponsoring grant either: a first cut that
can only answer within the spawn's own envelope is the one that needs no change to the
enforcement order, and finding out whether that is too weak to be interesting is itself a
result. If that runs, the interval fork becomes a real question rather than a guess, and the
assessor, exclusive bindings, sponsorship, trigger-gate resolvers, the other implementations
and — where anyone genuinely wants one — the temporal leaves are all additive over the same
record.

## After the PoC: a supervisor that investigates

Everything above assumes a supervisor decides on evidence already in hand — the read-only acting
grant with `vector: auto`. That forecloses a case worth having: a supervisor that has been
approving, becomes suspicious that a run of generated changes is smuggling something in, and
wants to *check* — say, for new advisories against a dependency it just saw appear. Checking is
`network`, outside `read-only`, and `auto` means it would not ask about it in any case. A
supervisor that can only read what it was handed is one you cannot ask to be suspicious.

So the extension is that a supervisor is an ordinary process with a real acting grant and a real
vector, and the questions it raises *while deciding* have to thread somewhere.

**Its own band, but as a child of the question it is deciding.** The check is the supervisor's
own action, so it is its own question on its own band, answered by its own chain. Posed flat it
is unanswerable — "some process wants network" — where what a person needs to see is "the
reviewer of `change:pr:9` wants to check advisories before deciding, and that PR's gate is
waiting on this". So the question carries a parent link and the docket becomes a tree rather
than a list. `GateQuestion` already carries `Origin` and ids are already structured around
`GateSep`, so this is a field and a rendering rather than a mechanism.

Two things must not collapse into one. They are **two authorisations**: answering the child
authorises the investigation and never the merge, and a person who clears a nested question and
thereby approves the thing being investigated is the worst outcome available here. And the
**supervised band shows the wait** — an observer of the supervised run sees its `pending`
blocked on the reviewer's own question, or the run reads as stalled for no reason, which is the
side-channel rule applied to waiting rather than to deciding.

**It threads rather than recurses**, because the stack nests and the resolver chain does not. A
charter resolver answers the questions of the work; a resolver's own questions go to the
built-ins. That is the anti-regress rule doing work rather than being asserted: the depth is
bounded by construction, and a person is always the terminal answerer of a supervisor's own
doubt — which is the right allocation, since a supervisor that needs supervising is a human's
job.

**The clock is the part that bites.** The parent question has an `escalatesAt` and its owner is
now blocked, which naively reads as "failed to decide" and is false. Pausing the parent's clock
is the tempting fix and is wrong, because an unanswered child would then hold it open forever.
Instead a child inherits a deadline bounded by the parent's remaining time, so no sub-question
can extend the ownership window it was raised inside. If the child outlives it the parent
escalates carrying the partial reasoning — "I was checking for advisories affecting this
dependency and did not finish" — so the person who picks it up inherits the suspicion, which is
the useful part.

**Retraction is not on offer.** If the verdict has already landed, a resolution is CAS-once and
the action it cleared has happened. What remains is prospective: `say` to steer, `SetVector` to
slide autonomy down so what did not ask now asks, `cancel` if it is bad enough. All three exist
today and each lands as an event carrying its `by`, so a supervisor that turns suspicious after
approving does not retract — it tightens, visibly.

That is the strongest case for the record holding more than positions and verdicts. The
suspicion, the check and its result belong there, so the next question from that run is judged
with "I checked advisories at 14:02 and found nothing" in hand rather than re-derived, and
whoever takes over reads why the vector got tightened.

One boundary, because it cuts across the two grants: an advisory check is an **acting** grant
question. The supervisor is doing something itself, so it needs `network` in its own envelope —
sponsorship only ever concerns what it authorises the supervised work to do, and a supervisor
whose acting grant is too narrow to investigate cannot sponsor its way out of that.

## What this note has not settled

Collected rather than scattered, because each of these is a choice someone should make
deliberately and none of them is made here.

- **Which end of the interval a trigger-side verdict is about.** `[position, C]` keeps the
  resolver inside the firing's own semantics; `[position, now]` catches the `deny` that landed
  after C. See [the trigger side](#the-trigger-side-is-the-same-object).
- **Where a lapsed question is promoted to.** §Liveness says the human docket, which was written
  before there was a lattice. One scope out is the other reading, and it is the one "failure
  promotes" plus "innermost owns" implies.
- **Whose sponsoring grant applies** when several bindings match a question — the owner's, or
  any matching binding's. The second lets a narrow resolver draw on an envelope it was never
  given, so this is not a detail.
- **Where a stale verdict lands.** A losing answer should be recorded rather than 404'd, but a
  record mixing verdicts that applied with verdicts that did not blunts the evidence surface. A
  flag on the entry, or somewhere that is not the record.
- **How deep layering is usable.** A child inherits `min(own, parent remaining)`, so the
  innermost deadline bounds the whole stack: two layers of judgment under a ten-minute inner
  deadline is a configuration that always escalates. The inheritance is safety at depth one and
  a squeeze at depth two.
- **The form of `resolve:`** — the TODO in [the annotated
  charter](#the-residue-gets-a-judgment), to be settled by writing the coupled examples rather
  than by arguing about them.
- **Whether rule-shaped resolvers can be supervised**, which is the relaxation §Regress
  describes and deliberately does not take.

Two smaller ones, both accuracy rather than design. "Supervision is never a side channel" is
true per stream: an observer of a base process sees its verdict and cannot see that the verdict
was itself supervised, because that happened on the inner resolver's stream. And a restart
mid-judgment does not produce two assessors only because spawn ids are derived from the trigger
and subject — the resolve POST's idempotency key dedups the answer, not the judge.

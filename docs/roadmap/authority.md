# Authority: where a process's permission comes from

Status: design note, partly implemented. The **override** path under
[The override](#the-override) has landed: a spawn may now *declare* a vector and a grant, both
are resolved by name, and a grant is enforced per action. The **derivation** path — inferring
an envelope from the request text — is not implemented and is the substance of what remains.

It concerns the hard authority axis (`process.Grant`) and its relationship to the soft gate
vector (`process.Vector`) — specifically, that the hard axis was fully wired and never
produced, which is why restraint still reaches an undeclared process as a prompt.
[Tutorial §3](../tutorial.md#3-processes) states that rough edge from the user's side; this is
what should fix it.

## The complaint

```sh
verse process run "go test ./..."
#   ⏸ gate "shell" (run_bash) — approve?
```

You typed a command; you were asked to confirm the command. With LLM creds configured the
default driver is `llm` (`cmd/verse/up.go`), so the task became an agent's prompt, the agent's
first action was `run_bash`, and that action met `harness.Vector()`, whose own comment calls it
*the demo gate vector*: ASK before writing a file, running a shell command, or delegating.

Nothing was decided at that prompt that the caller had not already decided. The prompt's only
information content is that verse chose to satisfy the command with an agent loop.

## Two boundaries, two contracts

The same prompt is reasonable or ridiculous depending on which boundary the request crossed.

- The step boundary — inside the loop, between actions. An agent proposes an action it chose;
  a gate asks. This is the convention every agent runtime already has, and it is *expected*:
  the caller addressed an agent, in agent register, and is typically watching the transcript
  (`run --attach`). Approving `write_file` in the tutorial's `'Write a file hello.txt'` example
  is a real decision about a real choice the agent made. Leave it alone.
- The call boundary — outside → verse, the crossing that *introduces* a loop. Here the
  request is not a proposal to be judged; it is the authorization itself. The caller addressed a
  *runner*, and the loop is an implementation detail of satisfying them. A gate here re-asks a
  question the caller answered by typing it, and leaks the implementation choice while doing so.

The distinguishing signal is the register of the request, not the transport — both examples
above are the same CLI. `go test ./...` is a command the caller could have run themselves;
"Write a file hello.txt containing hi" is an instruction to an agent. Today the runtime cannot
tell them apart, because it never asks.

`process say` is also a call-boundary crossing: a new instruction mid-run is new authorization,
arriving from outside, and carries the same weight as the task that started the process.

## The hard axis exists and has no producer

Verse already models authority in two layers (see also the axes discussion in
[expose](expose.md)):

- `Grant` — the hard envelope. What the process may do *at all*. `Grant.Subset` enforces
  monotonicity down a delegation chain, checked in `NewChild` and again at the `call_agent`
  boundary in the loop.
- `Vector` — the soft dial. Of what is permitted, which actions must *ask*. Slidable mid-run
  via `SetVector`.

The hard layer was complete and inert — in two distinct ways, and the second was the deeper
one. A root process's scope is initialized to a wildcard (`Grant{Allow: []GateMatch{{}}}` —
`allowsAll`) in `process/loop.go`, and nothing but `NewChild` ever set it, so every process
began unbounded. But even a process whose scope *was* set would not have been bounded by it:
`Grant.Subset`, at the `call_agent` boundary, was the only reader of `p.scope` in the tree. A
grant constrained what a process could **delegate** and never what it could **do**. Wiring the
declarative fields through without noticing that would have reproduced the same defect one
layer deeper — a field that resolves, installs, and still stops nothing.

Both are now closed for a *declared* envelope: `Grant.Permits` checks each action against the
scope in `process/loop.go`, ahead of the vector, and a spawn's `grant` installs the scope
(`process.Launch`, applied in `processd`). What has not changed is the default: a process that
declares no grant still begins unbounded, so the soft per-action gate is still the only thing
between an undeclared agent and an effect. That is what derivation is for.

That is the whole defect, stated structurally: the soft dial is doing the hard axis's job.
An envelope you could have decided once, at spawn, from information the caller already gave you,
is instead re-litigated per action, interactively, with a human in the loop. Nothing is more
secure for it — a wildcard grant with an `ask` vector stops exactly one impatient operator — and
the cost is a prompt that reads as ridiculous.

The declarative side had the same hole from the other end: `launch.Spec.Vector` and
`launch.Spec.Grant` were expanded by `Bind` and read by nothing, so a trigger could not state
the envelope of what it spawns either — and, worse, could state one and be told it had. Both
now resolve (`process.NamedVector` / `process.NamedGrant`, called from versed's `launchOf`)
and reach the runtime as predicates rather than names, so the two ends cannot drift apart on
what a name means. `launch.Spec.Resolver` and `launch.Spec.Environment` are still declared and
still read by nothing; neither has semantics to resolve *to* yet.

## The principle

Authority enters with the request. A gate should ask only about what its answerer has not
already authorized.

- A human who types a command authorized that command.
- An agent that invents an action authorized nothing; that is what a gate is for.
- A trigger that fired already answered its own `gates` — per firing — and should be able to say
  so on the spawn.

## Deriving the envelope at the call boundary

At spawn, convert the request into a `Grant`, install it as the process's scope, and let the
loop gate only what exceeds it.

### Why spawn is the sound place for that inference

The inference "is this action what the caller asked for?" is security-relevant, and where it
runs decides whether it can be trusted:

- Once per spawn, not once per action — it can afford to be careful.
- The request is complete and present. Twenty steps in, the original intent survives only as
  paraphrase inside a context the agent has been writing to.
- It runs before the agent has said anything. The only input is text the caller wrote. The
  same judgment made mid-loop reasons over the agent's own output, inside the agent's own
  context — asking the thing being judged to supply the evidence. An envelope fixed at spawn
  cannot be argued open later; it can only be narrowed by delegation (`Grant.Subset`) or widened
  by a new call-boundary crossing, which is a human act.

### Stage 1 — register, no model required

Classify the request as *command* or *instruction*. A task that parses as a command the caller
could have run themselves is a command execution: authorize that command, gate nothing for
realizing it. This is the `--driver scripted` heuristic promoted from a flag the caller must
remember to a property the runtime infers — and it already matches how the scripted driver is
built (`Vector{Default: auto}`, no gates, because its task *is* the command).

### Stage 2 — a derived envelope

For an instruction, derive what it authorizes: *"exec `go test ./...`; read under root; write
under root"* — a `Grant` of `GateMatch` predicates, which is what `Grant.Allow` already holds.
The loop then gates escalation only: network, writes outside the root, delegation, anything
the envelope does not name.

Fail closed. An inference that cannot produce a confident envelope produces a *narrow* one, and
the gate asks — today's behavior, which remains the correct fallback rather than a failure.

### The override

**Implemented.** An explicit vector or grant on the spawn wins over anything inferred. The
`vector` / `grant` axes of `launch.Spec` finally have a consumer: a charter writes a name,
versed resolves it, and the resolved predicate travels to whichever runtime hosts the process.
The vocabulary is small on purpose — `auto|ask|park` for a vector, matching what `process
vector` already accepts, and `read-only|no-delegate|none|all` for a grant — because every name
is a promise the enforcement keeps, and a name that over-promises is worse than no name.

An unrecognised name **fails the spawn**. It does not fall back to the wildcard: silently
widening an envelope the author narrowed is the original defect wearing an error message.

Two constraints this respects. Resolution runs on the engine side rather than at the runtime,
so the two ends cannot disagree about what `read-only` means across a version skew. And,
per the risk below, only the *declared* grant is honored — nothing is derived from the bound
task, so a `$var` expanded out of an entity payload cannot widen anything.

Still to do here: `process run --vector auto|ask|park` for the CLI (the trigger side is done),
and recording the installed envelope on the process entity so `process get` shows it.

## What the loop gates afterward

Unchanged in kind, narrower in scope: the vector still evaluates every action, still poses a
`GateQuestion`, still records denials in the ledger. What changes is that actions *within the
authorized envelope* resolve `auto` instead of `ask`. A denial stays visible and re-plannable;
an action outside the envelope is refused hard rather than asked about, because the caller never
granted it and the agent is not the party who may.

## Where this meets the rest

- Delegation — a child's grant is `parent ∩ req.Grant` already. A derived root envelope makes
  that check mean something for the first time, instead of a wildcard admitting everything.
- The agent's frame — what an agent is *told* is not authority, but it must not contradict it.
  The harness's system prompt used to be a package constant recommending `call_agent` while
  `harness.Vector()`, ten lines below, ASK-gated the call-agent verb: the standing instruction
  named the one action guaranteed to suspend an unattended firing. No wording fixes that,
  because the vector is per-spawn and slidable and a constant is neither. The frame is now
  derived from the vector it will run under, so a gated tool is described as gated and a
  refusal outside the grant is described as final rather than as something to re-plan around.
  A spawn writes its own `frame` when it wants something else.
- Triggers — a firing's `gates` authorize the firing; the spawn's `vector`/`grant` should
  carry that authorization into the process, closing the double-ask.
- Environments — the unbypassable floor from [expose](expose.md) sits outside both:
  *possible* ⊇ *granted* ⊇ *auto*. The floor is enforced by construction, the grant by refusal,
  the vector by asking. A floor is also *infrastructural* and therefore non-semantic — it can
  withhold a network, never a particular command — which is precisely the room the grant layer
  exists to fill.
- Where a derived grant lives — on the *claim*, not the environment. An environment
  definition is shared and pre-warmable ([expose](expose.md)); a grant is derived from one
  request and belongs to that spawn alone. Folding it into the environment would make every
  environment single-use.
- Naming — `Process.scope` (the field holding a `Grant`, `process/loop.go`) is the last
  place in the process package where "scope" means authority rather than store isolation; it
  wants the same rename `launch.Spec.Grant` got.

## Prior art: the same gap, approached from identity

The authorization world reached this problem from the other direction and named it. Worth
knowing, because the vocabulary is becoming standard and because our position differs from the
default one in a way we should state deliberately rather than by accident.

The failure has a name: the confused deputy. A program acting on someone's behalf, holding
that party's authority, induced into using it for something they did not intend. The current
framing of the agent version — *a non-deterministic system acting on a user's behalf with
implicitly delegated access, failing at machine speed and scale* — is a precise description of a
wildcard root grant with a soft per-action dial, which is what we have.

The standard delegation patterns are two, and we are proposing a third.

- On-behalf-of — the agent authenticates in the user's context and inherits *the user's
  permissions*. Simple, and structurally over-broad: the agent may do anything the user may do,
  so every restraint has to be clawed back afterward, per action, by a policy check.
- Autonomous — the agent holds its own standing identity, independent of any human session.
  Appropriate for scheduled and unattended work; says nothing about a request the human just
  typed.

The position of this note is neither: the envelope derives from what the user asked for, not
from what the user is *able* to do. That is a far tighter envelope from the outset, and it is
available only at the call boundary, where the request exists as text. Say this explicitly
wherever the design is presented, because a reader will otherwise pattern-match it to
on-behalf-of and conclude we are re-deriving something ordinary.

We already have the enforcement pieces, under our own names. The OpenID Foundation's AuthZEN
working group standardizes the interface between two roles: a *policy enforcement point* (the
code that intercepts an action before it happens) and a *policy decision point* (the service that
answers permit/deny). Our vector evaluation is the enforcement point, the resolver and its motions
are the decision point, and `GateQuestion` is the decision request travelling between them. Two of
their profiles map directly:

- an approval profile (Access Request and Approval) for decisions that require a human —
  our `ask` mode, `gate approve`/`gate deny`, and the docket;
- a tool-authorization profile for Model Context Protocol servers, whose question is
  literally *may this agent, acting for this user, call this tool with these arguments* — which
  is our `Effect` (a verb applied to a typed resource), better typed than a string tool name.

The identity model behind all of it asks three questions of an agent: who deployed it, what is it
authorized to do, and on whose behalf is it currently acting. The third is the open question
at the end of this note — whether an envelope belongs to the process or to the (requester,
request) pair — arriving from the other side.

Runtimes are converging on the mechanics too. Google's Agent Executor (an open-source
distributed agent runtime, early as of 2026) keeps a durable event log with snapshots held
separately, resumes a session from a step rather than replaying it, and — the relevant part —
treats *suspended while awaiting an approval* as a first-class runtime state rather than a
blocked thread. That is `park`. Its single-writer session state is the same conclusion as the
claim-and-report, compare-and-swap model in [external processes](../external-processes.md).
None of these systems, however, derives authority from the request; they carry a decision made
elsewhere. The derivation is the part that is ours to design.

## Risks

- A bound task is attacker-influenced text. This is the sharp edge. A trigger's task is a
  *template* whose `$vars` expand from the changed entity's payload — so if authority is derived
  from the task string after binding, anyone who can write an entity field can widen an
  envelope by writing into it. Derivation must therefore run over the authored template and
  the authoring party, never over bound values; a binding may narrow what it substitutes into
  but must never enlarge the envelope. Until that is settled, trigger spawns take a *declared*
  grant only — which is what shipped. The same reasoning is why `launch.Spec.Tools` is the one
  string field `Bind` does **not** expand: a `$var` there could only name a tool that does not
  exist, or name one chosen by whoever last wrote the entity being matched.
- Silent over-authorization. A wrong envelope does not surface as a prompt — that is the
  point, and also the danger. The derived envelope must be recorded on the process entity so it
  is inspectable (`process get`), reviewable after the fact, and diffable against what the
  process actually did.
- Register misread. "Run the tests and fix whatever breaks" is both a command and an
  instruction. Misclassifying an instruction as a command authorizes a shell verb the caller
  only half-intended. Prefer classifying as *instruction* on doubt.

## Open questions

- Envelope from `say` — a mid-run instruction is a new authorization from outside. Does it
  widen the envelope (matching the call-boundary principle), or is widening a distinct,
  explicitly-attributed act? The `--as` attribution on `process vector` suggests the latter.
- Who answers when nobody is attached. An unattended run that hits a gate parks. With
  envelopes, the interesting case inverts: an action outside the envelope with no one to ask
  should probably fail fast rather than park indefinitely.
- Escalation vocabulary — what counts as "beyond" needs to be predicate-level, and
  `Grant.Subset` is deliberately sound-but-incomplete (identity or wildcard only). Real
  predicate subsumption is the same frontier flagged in `process/action.go`.
- Does the envelope belong to the process or the request? Two `process run`s of the same
  task by different callers may deserve different envelopes; that argues the envelope is a
  property of the (requester, request) pair, which is where an identity model would enter.

## References

- [AuthZEN at Identiverse 2026: authorization in the agent era](https://openid.net/authzen-at-identiverse-2026-authorization-in-the-agent-era/)
  — the policy enforcement point / policy decision point interface, the approval profile, and the
  Model Context Protocol tool-authorization profile.
- [Agent2Agent (A2A) protocol specification](https://a2a-protocol.org/latest/specification/) — how
  a peer protocol represents an action needing authorization or human approval.
- [Agent Executor](https://github.com/google/ax) (`google/ax`) and its
  [announcement](https://cloud.google.com/blog/products/ai-machine-learning/agent-executor-googles-distributed-agent-runtime)
  — durable event log, snapshot-and-resume, single-writer session state, and suspension while
  awaiting approval.

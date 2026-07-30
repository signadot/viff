# Supervising process gates

Status: notes from a conversation. Nothing here needs building to be useful — most of it runs
already, badly documented. The charter-level counterpart does run:
[an automatic supervisor](../auto-supervisor.md).

**The question.** That charter answers *trigger* gates with an agent. Process gates — per tool
use, inside a running process — have no charter-reachable answerer: `launch.Spec.Resolver` is
declared and unread ([authority](../roadmap/authority.md)).

**What already works.** Deploy a service implementing the process-control base URL, point
`verse up --runner <url>` at it, and it owns derive → `Vector.Evaluate` → `Resolver.Resolve`
for every process it hosts. Substituting that last call is what the interface is for
(`process/resolver.go`: supervisor-agnostic by construction). A lighter variant holds no
processes and just answers pending gates over `verse gate approve|deny <id> --say`, with its
own `by`.

**Three things are easier here than at the charter layer.**

- *Promotion is free.* A question stays pending until answered and is already in `verse
  gates`. A supervisor that declines simply does not answer. No escalate route to write —
  contrast `remit`/`onFailed` in the charter note, which exist because a firing with no gate
  poses nothing at all.
- *Attribution exists.* `Resolution.By` is documented with `"process:ci"`: a non-human
  answerer was in the design.
- *It can redirect.* `Steer` folds instructions into the next turn whether or not it
  proceeded, and a refusal is not terminal (`process/loop.go` hands back `Blocked` + reason;
  the driver re-plans). So a supervisor here can be deny-biased. The charter one cannot: there
  "no" ends the firing.

**Three limits.**

- *Deployment decides supervision, not the charter.* One runner per verse (`--runner` and
  `--broker` are mutually exclusive), and `resolver:` is unread — so two rules cannot run
  under different supervisors, and a charter cannot state which supervisor it expects.
- *Attest, not intercept.* Out-of-process, verse gates *reported* effects and CAS-enforces
  terminal-once / claim-holder-only / gate-before-effect. A supervising service's compliance
  is contractual. Fine for a service you deployed; not a claim a charter can make.
- *No clock.* A supervisor that is down is indistinguishable from one that declined, and both
  park a live turn. The deadline has to live in the service. Same gap the charter note names,
  and it bites harder here because what waits holds its context in memory.

**The asymmetry worth fixing.** The question is an entity (`pending`, `pendingGate` on
`process:instance`) and the answer is an HTTP call. A charter can already react to a process
gate being raised — and then cannot answer it, because actions are patch/spawn/seq. The
broker is the argument for adding that verb rather than a substitute for it: routing an answer
to an uninterceptable party, attributing it, clearing `pending`, resuming the worker from its
cursor are all built (`broker/broker.go`, `Resolve` → inbox `resolve` item + `EvDecision`).
Make answering a *write* and the charter supervisor becomes a process-gate supervisor with no
new supervision concept. Naming a `resolver:` per spawn is the tempting alternative and worse:
a model call inline at every gated action, and the supervisor's own gates need supervising,
with no base case but `AutoResolver`.

**Where to keep the remit either way.** On the hard axis — `grant`, `tools`. It is the only
part no supervisor at either layer can widen (`process/loop.go`: outside the grant is "not
permitted, and not something a gate can approve").

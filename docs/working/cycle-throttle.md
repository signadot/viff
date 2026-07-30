# Throttling firing cycles: bound the causal depth, do not detect the cycle

Status: design note, nothing here runs. It proposes a runtime throttle for the in-stratum
cycle that [strata](strata.md) cannot rule out statically, and names the store↔{engine, api}
interface changes it needs.

## The gap it closes

[Convergence](../convergence.md#the-cycle-which-is-the-one-that-is-not-checked) names the cycle
across rules as the one hazard nothing checks.
[Strata](strata.md) answer it *across* levels, by construction: a derive is literal and its
target is checked against a strictly higher level's match, so no cross-level cycle can be
written. What strata deliberately do not answer is a cycle *within* one level, where two rules
patch their subjects into each other's selection — the `fwd`/`back` pair, unchanged.

Static analysis is the wrong tool there, and
[firing residual §1](firing-residual.md#1-the-cycle-check-and-the-clock-it-needs) already
concedes why: the graph it proposes is decidable over literal patches and blind through
templates, so it lands as a warning rather than a refusal. A patch whose values are computed can
satisfy a predicate the key-level check cannot see through.

The dynamic answer sidesteps the analysis entirely. If a write carries the chain of firings that
produced it, a runaway is a chain that keeps growing — no inspection of what a patch might
compute, no reasoning about what a predicate might come to satisfy.

## The mechanism

**Each engine keeps, in memory, a map from store revision to causal depth.** A firing looks up
the revision of the commit that woke it, and stamps depth+1 on every write it makes. A commit
whose revision is not in the map is external — a person, a process, a mirror — and starts a
fresh chain at zero.

**The cap is the number of triggers in the stratum.** A chain that never repeats a trigger
visits at most |stratum| of them, so a chain longer than that has necessarily repeated one.
Depth > |stratum| ⟺ some trigger fired twice in one chain. The cap is therefore exactly as
precise as searching the chain for a repeat would be, at the cost of an integer. Nothing has to
be detected; the counter *is* the detection.

### Why the trigger name alone is enough

The obvious objection is a trigger that legitimately appears twice in one chain about *different*
subjects — a transitive closure walking up a dependency tree. Keying the chain on (trigger,
subject) instead would answer it and would destroy the bound, since the chain length would then
be limited by the number of subjects rather than the number of rules.

It does not arise, for a structural reason. **The match grammar has no joins.** A `Match` selects
on an entity's own system, kind, id and value; a `Formula` composes matches with
quorum/fact/until/deadline. Nothing correlates one entity's field with another's. So within a
stratum, where an action writes only its subject, a write to S can never bring S′ into a
trigger's selection — a repeat of trigger T in a chain is necessarily a repeat on the same
subject.

The token therefore carries the trigger name and nothing else, and loses no precision doing it.
(The chain of names is still worth carrying, but for the diagnostic rather than for the decision
— see *what tripping does*.)

### Bursts need no special case

Depth is not rate. A mirror landing two hundred GitHub entities is two hundred external writes,
each starting its own chain at zero; even folded into one commit it is one chain of length 1
fanning out to two hundred firings. Width, not depth. The throttle never sees a burst, and
nothing has to recognize one.

### Pacing breaks the chain, so a control loop never trips

The token propagates only where the engine hands it forward — engine write, engine read.
Everywhere real time enters, it does not survive:

- through a **spawned process**, whose writes arrive later through the API as external;
- through the **world**, where an effector's write returns via the mirror as external;
- through a **ticker**, whose tick is a machinery write with no chain.

So a paced cycle restarts at depth 0 on every lap and never trips, while an unpaced one climbs
1, 2, 3 and hits the cap. That is firing residual §1's rule — *a combinational loop is an error,
a cycle through a register is a state machine* — obtained without the engine learning what a
ticker is, and without the ticker having to exist first. The ticker is still wanted for the
reaper's grace period and for temporal conditions; it stops being a prerequisite for the cycle
check.

### Strata tighten the bound, they do not merely localize it

A chain may cross levels: hops inside stratum 0, a derive into 10, hops inside 10, a derive into
100. Since a derive strictly increases level and nothing writes downward, the chain visits strata
in increasing order and never re-enters one it has left. So the counter can **reset on every
level crossing**, and the cap becomes max|stratum| rather than Σ|stratum|. The reset is sound
precisely because a crossing is irreversible — which is the same well-foundedness the static
check rests on, spent a second time.

With no strata declared there is one implicit stratum and the cap is the installed trigger count.

## What the interface has to change

**A write must report the revision it landed at.** `Store.Write` returns `(committed bool, err
error)`. Underneath, `sess.Patch` and `sess.PatchIf` both return a `*logdapi.PatchResult` whose
`Commit int64` is exactly that revision — and both call sites discard it
(`entity/logdstore.go:387`, `entity/logdstore.go:396`). Without it a writer cannot correlate its
own write with the commit it will later observe, and the rev→depth map has no key. This is the
smallest change on the list and the one everything else waits on.

**There must be one place a write goes through.** There are ten-odd direct `store.Write` call
sites — five in the engine (`trigger/charter.go`, `trigger/seq.go`, `trigger/goal.go` ×2,
`trigger/engine.go` ×2), three in `versed/server.go`, plus `mirror`, `source/git` and
`cmd/verse/connect.go`. A stamp added at ten sites is a stamp the eleventh bypasses, and
`maxFires` is engine-wide rather than per-trigger
([firing residual §2](firing-residual.md#2-the-runaway-backstop)) precisely because there is no
seam holding a key to count against.

**Adjacent, and wanting the same seam:** the engine buys back state the store already computed.
`LogdStore.handleEvent` keeps `s.doc` — the whole verse rolled forward — and emits a `Commit`
carrying only `Changes`, dropping the document (`entity/logdstore.go:1206-1211`); `Since` does
the same on the catch-up path. The engine then calls `AsOf(c.Rev, reads...)` **per trigger**
(`trigger/edge.go:49`), and `AsOf` with `within` issues **one read per named subtree**
(`entity/logdstore.go:1093-1101`), with an `on` trigger reading twice. So a commit costs
O(triggers-touched × subtrees-each) reads at ~73 ms apiece. Firing residual §3 frames this as a
constant factor on observation; it is a multiplier over the charter, and the multiplication
exists *because* the interface makes every trigger ask separately. The throttle does not need
this fixed. An inbound stream of `(Commit, Snapshot)` pairs fixes it, closes convergence's named
`$references` looseness, hands the `on` edge its C-1 for free, and is where a ticker would
naturally live — so the two directions of the same seam have two customers.

## Why in-memory provenance is enough

Per-engine, in memory, discarded on restart. Three reasons that holds:

- **A restart is a fresh cause.** Losing the map on restart resets every chain to zero, which is
  the correct reading of a restart rather than a limitation of the implementation.
- **Scopes cannot cycle.** `Store.Scope`'s contract is that writes land in the overlay while
  reads see baseline layered with the scope (`entity/store.go`). The causal edge between a
  baseline engine and a scoped one is therefore one-directional: baseline writes are visible to
  the scope, scope writes are invisible to baseline. There is no cycle to close, so per-engine
  throttling composes.
- **Everything else is external, and external is the safe default.** A write an engine did not
  make starts a fresh chain, which under-throttles rather than over-throttles.

The composition that does *not* hold is two baseline engines on one store: each throttles its own
chains, but A's write looks external to B, so an A→B→A cycle is unthrottled by both. The
condition is that every engine throttles and no cycle runs between engines; scopes satisfy it
structurally, two peers on one baseline do not. Named rather than hidden.

## What tripping does

Pace, do not refuse. A trip costs latency rather than correctness, which is the forgiving side to
err on — though note the argument above says a false trip should not be constructible in the
first place.

But pacing alone converts a runaway into an infinite loop that runs politely forever, so **the
report is the actual deliverable**: name the trigger chain that exceeded the bound. That is why
the token carries the names as well as the count. The diagnostic is strictly better than what the
static graph would have produced, because it names the cycle that *happened* rather than one that
might.

## Sequencing

This ships before strata and does not wait on them. With one implicit stratum the cap is the
installed trigger count, which is a usable bound today; declaring strata later tightens it to
max|stratum| via the reset above. That independence is new — when the cycle check was a static
graph, it wanted the charter's structure first.

Within this note, the order is: the write reports its revision, then the single write seam, then
the counter. The first is small and unblocks the rest.

## Open

- **One commit, one chain — today.** `Store.Write` writes one path per call and the store emits
  one commit per delta, so a commit has exactly one cause and a reader inherits it unambiguously.
  logd has transactions (`NewTxResult.TxID`), so a multi-path atomic write would let one commit
  carry several chains, and the reader would need a merge rule. Max is the obvious one and errs
  toward throttling.
- **Where the counter is checked.** Before the gates is right by analogy with `working` and
  `claimFlight` — a firing that will be throttled should not put a question on the docket or
  spend budget on its way to being told no — but that is an argument by symmetry, not a measured
  one.
- **What pacing means numerically.** A fixed delay, a backoff, a token bucket per trigger. None
  of these is obviously right, and the choice interacts with whether a paced cycle should
  eventually stop rather than run forever at a low rate.
- **Whether the depth should be observable.** It is engine-internal state today by construction.
  A verse that could be asked "what chains are deep right now" would make a developing runaway
  visible before it trips, and everything else about a firing is already a fact in the store.

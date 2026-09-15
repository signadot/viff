# The cycle throttle

[Convergence](convergence.md#the-cycle-and-how-it-is-caught) names one hazard that no other
protection catches: two rules each writing exactly what the other selects on. The substrate's
no-op suppression does not engage, because every write is a real change. `claimFlight` and
`working` do not engage, because every firing genuinely completes. The install-time latch check
passes, because both rules *do* write a field their subject reads, into each other's.

The throttle catches it, and it does not analyse the charter. Every write an engine makes during
a firing carries the **causal depth** of the chain that produced it, so a runaway is a chain that
keeps growing. Nothing reasons about what a patch might compute, or what a predicate might come
to satisfy.

## The mechanism

Each engine keeps in memory a map from store revision to the chain that commit belongs to: how
deep it stands, which firing wrote it, and the commit that woke that firing. A firing looks up
the revision of the commit that woke it and stamps depth+1 on everything it writes. A commit that
is not in the map was caused by somebody else — a person, a task completing, a mirror, a peer
engine, or this engine before a restart — and starts a fresh chain.

A chain that crosses engines is a different question with a different answer. Between charter
packages the loop is excluded rather than bounded, because a package declares what it reads and
the graph over packages must be acyclic at install
([trigger-model.md](trigger-model.md#a-charter-can-be-installed-at-an-address)). What is left
here is a chain within one engine, which is what the depth cap is for.

This works because a write reports the revision it landed at (`Store.Write`), and that is the
same revision the change stream later carries for it. Without that correspondence a writer could
not recognize its own write coming back around, and there would be nothing to key on.

The stamp is applied by the store a firing writes *through*, not at each call site. That matters:
`entity.Modify` takes a `Store`, so every compare-and-swap write inherits the stamp without
knowing it exists, and a stamp added at ten call sites is a stamp the eleventh bypasses.

### The bound is pigeonhole

**The cap is the number of installed triggers.** A chain is a path — a firing, the commit it
wrote, the firing that commit woke — and each hop is one trigger firing, drawn from the triggers
installed. A path longer than that has used some trigger twice.

Two things about that argument are what make the counter cheap. It assumes nothing about what a
firing writes, what a trigger selects, or how the engine's own machinery fans out, so no later
change to any of those can quietly invalidate it. And a commit that wakes several triggers
branches the chain into several paths, each counted separately, which is what a depth stamped on
a write and inherited by its readers measures.

The trip is at **depth > cap**, not `>=`, and the margin is what the ordinary converging rule
needs. A rule whose write leaves its subject still selected fires a second time, writes the same
value, produces no commit, and dies there, which is how a vacuous `on` settles. Depth counts
*commits*, so that rule never reaches depth 2 while a runaway commits every lap. Trip at `>=` and
the healthy second firing is refused and reported as a runaway. The cost of `>` is letting a real
cycle run one further hop, which is one no-op write.

The cap is read at the check rather than at install, because the charter moves: a rule added or
removed changes how long a chain can legitimately be.

### A chain is per subject

The chain a firing belongs to is keyed to the **subject**, and this is enforced rather than
assumed.

The tempting argument is that it needs no enforcing. The match grammar has no joins — a `Match`
selects on an entity's own system, kind, id and value, and nothing correlates one entity's field
with another's — so where an action writes only its subject, a write to S cannot bring S′ into a
trigger's selection, and a repeat of trigger T must be a repeat on the same subject. That is true
and it is not enough. **A firing is woken by a commit, not by a subject**, and the rule re-selects
the whole set at every commit. So a rule acting on member A writes A; that commit re-evaluates the
rule, which selects B, already matching and never brought in by anything, and fires about B off
A's commit. Counted naively that is a repeat one hop deeper, and a fan-out over n members walks
itself to depth n. Measured without the distinction below, a burst of 30 external writes trips 17
times.

So a firing inherits the waking commit's depth only when that commit **changed the subject it is
about**, and otherwise begins a fresh chain. Nothing is lost, because a real loop advances its own
subject every lap, which is what makes it a loop, and pigeonhole still holds over a same-subject
chain.

The consequence is worth stating on its own: **width is not depth**. A mirror landing two hundred
entities is two hundred external writes, each starting its own chain. One rule fanning out over
two hundred members is one chain of length 1 fanning out to two hundred firings. Neither trips.

### Pacing breaks the chain, so a control loop never trips

A cycle is not wrong. A cycle is how you write a control loop — poll, compare, correct, poll again
— and a charter that could not express one would be missing something. What makes the runaway
pathological is not that it cycles but that it cycles *at the speed of the store*, at no declared
rate at all.

The chain propagates only where the engine hands it straight on. Everywhere real time enters, it
does not survive:

- through a **spawned task**, whose writes arrive later through the API as external;
- through the **world**, where an effector's write returns via the mirror as external;
- through a **blocking gate** — an ask, a park, a threshold, a deadline. A person answering an approval
  paces a cycle exactly as a clock would. A firing that will wait begins a fresh chain, for the
  check and for its writes alike, decided in one place so the two cannot disagree;
- through a **ticker**, when one exists: a tick is a machinery write with no chain.

So a paced cycle restarts on every lap and never trips, while an unpaced one climbs and hits the
cap. That is the rule synchronous digital design already uses — a combinational loop is an error,
a cycle through a register is a state machine — reached without the engine learning what a ticker
is, and without a ticker having to exist first.

## What a trip does

**It paces, it does not refuse.** A tripped firing is held for `tripDuration` (config; default 1s)
and then proceeds, so a runaway settles into a dull steady rate rather than stopping dead. A trip
costs latency rather than the state transition, which is the forgiving side to err on. Setting it
to `0` refuses instead, which is the harder stop.

A held firing **aborts if its chain breaks under it**. A write to its subject during the wait means
the world moved and has already raised whatever firing is now correct, so the held one is stale.
The wait happens in the firing's own goroutine and before it claims the subject: sleeping on the
dispatch path would stall every other rule behind one runaway, and holding the claim would block
the very firing that supersedes it.

**The report is the deliverable.** Pacing alone converts a runaway into a loop that runs politely
forever, so the trip walks the chain back and names the rules that were firing each other:

```
INFO cycle: rules kept firing each other past the point where one must have repeated
     cycle="fwd → back → fwd → back" subject=demo.pr.1 depth=3 cap=2 from=4 complete=true
```

Both rules appear twice, which is the finding: one of them ran a second time about the same
subject.

**INFO rather than WARN**, because a tripped cycle is not per se a fault. Recursion inside a
[stratum](strata.md) is what a level ordering permits, a loop through the world is the shape a
person would draw on a whiteboard, and a charter may go on looping deliberately for as long as it
likes once paced. The report says which rules are doing it; whether that is a problem is the
reader's call. Warn is kept for what an operator has to act on: a write that did not land, a rule
held for a stratum that never arrives. The report comes once per chain, keyed on the chain's root,
so the same pair looping from a different origin is news again, and `complete` says whether the
walk reached the beginning or ran out of retained history.

This names the cycle that *happened*, which is better than what a static graph over templated
patches could produce: a warning about one that might.

## Why in-memory provenance is enough

The map is per-engine, in memory, discarded on restart, and bounded, with the oldest half dropped
once it grows past a few thousand entries. Losing an entry makes a commit read as external, which
starts a fresh chain: it under-throttles and never the reverse. The decision does not degrade,
because each entry carries its own depth rather than deriving it by walking. Only the report's walk
can come up short, and it says so.

- **A restart is a fresh cause.** Losing the map resets every chain, which is the correct reading
  of a restart rather than a limitation.
- **Scopes cannot cycle.** `Store.Scope`'s contract is that writes land in the overlay while reads
  see baseline layered with the scope, so the causal edge runs one way: baseline writes are visible
  to the scope, scope writes are invisible to baseline. Per-engine throttling composes.
- **Everything else is external, and external is the safe default.**

## Corners that do not run

- **Two baseline engines on one store are not covered.** Each throttles its own chains, but A's
  write looks external to B, so an A→B→A cycle is bounded by neither. Scopes satisfy the condition
  structurally; two peers on one baseline do not. Nothing in the tree runs two peers, and the
  constraint is recorded on the `Engine` type.
- **Two rules writing one field is a different hazard and is not caught.** They settle wherever the
  last write lands, without any chain growing.
- **Strata would tighten the bound, and the throttle does not use them.** A chain that crossed
  levels could reset its counter on every crossing, since a derive strictly increases level and
  nothing writes downward, making the cap max|stratum| rather than the whole charter. That part is
  not built: the cap is the installed trigger count whether or not a charter declares
  [strata](strata.md), which is a usable bound.
- **One commit, one chain.** `Store.Write` writes one path and the store emits one commit per delta,
  so a commit has exactly one cause. logd has transactions, so a multi-path atomic write would let
  one commit carry several chains and the reader would need a merge rule, max being the obvious one,
  erring toward throttling.
- **The depth is not observable.** It is engine-internal by construction. A verse that could be
  asked "what chains are deep right now" would make a developing runaway visible before it trips,
  and everything else about a firing is already a fact in the store.

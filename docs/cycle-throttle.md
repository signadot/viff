# The cycle throttle

[Convergence](convergence.md#the-cycle-and-how-it-is-caught) names one hazard that no other
protection catches. Two [rules](glossary.md#rule) each write exactly what the other selects
on. The other protections do not see it:

| Protection | Why it does not engage |
|---|---|
| the no-op suppression of the substrate | Every write is a real change. |
| `claimFlight` and `working` | Every [firing](glossary.md#firing) completes. |
| the latch check at install | Both rules *do* write a field that their subject reads. Each writes it into the other's subject. |

The cycle throttle catches this hazard, and it does not analyse the
[charter](glossary.md#charter). Every write that an engine makes during a firing carries the
**causal depth** of the chain that produced it. A runaway is a chain that keeps growing. Nothing
reasons about what a patch might compute, or about what a predicate might come to satisfy.

## The mechanism

Each engine keeps a map in memory. The map goes from a store [revision](glossary.md#revision)
to the chain that the commit at that revision belongs to. An entry holds three things:

- how deep the commit stands in its chain;
- which firing wrote the commit;
- the commit that woke that firing.

A firing looks up the revision of the commit that woke it. It stamps depth+1 on everything it
writes. A commit that is not in the map has another cause, and it starts a fresh
[chain](glossary.md#chain-of-firings). The other causes are:

- a person;
- a task that completes;
- a mirror;
- a peer engine;
- this engine before a restart.

A chain that crosses engines is a different question, with a different answer. Between charter
packages, viff excludes the loop instead of bounding it. A package declares what it reads, and
the graph over packages must be acyclic at install
([the charter reference](reference/charter.md#a-charter-can-be-installed-at-an-address)). What
is left here is a chain within one engine. The depth cap is for that chain.

The mechanism depends on one fact. A write reports the revision that it landed at
(`Store.Write`), and the change stream later carries the write at that same revision. Without
this correspondence, a writer could not recognize its own write when the write comes back. There
would be nothing to key the map on.

The store that a firing writes *through* applies the stamp. The stamp is not applied at each
call site. `entity.Modify` takes a `Store`, so every compare-and-swap write gets the stamp
without knowing that the stamp exists.

A stamp at each call site would be fragile. If ten call sites add it, the eleventh can bypass
it.

### The bound is pigeonhole

**The cap is the number of installed rules.** A chain is a path: a firing, the commit it wrote,
the firing that commit woke, and so on. Each step of the path is one firing of one rule, and the
rule is one of the installed rules. A path longer than the number of rules has used some rule
twice.

Two properties of this argument make the counter cheap:

- The argument assumes nothing about what a firing writes, what a rule selects, or how the
  machinery of the engine fans out. So no later change to any of those can make the argument
  false without notice.
- A commit that wakes several rules branches the chain into several paths. Each path is counted
  on its own. A depth that is stamped on a write and inherited by its readers measures exactly
  this.

The trip is at **depth > cap**, not `>=`. An ordinary rule that converges needs this margin.

A rule whose write leaves its subject still selected fires a second time. The second firing
writes the same value, produces no commit, and ends there. A vacuous `on` settles this way.
Depth counts *commits*, so this rule never reaches depth 2. A runaway commits on every lap. With
a trip at `>=`, viff would refuse the healthy second firing and report it as a runaway. The cost
of `>` is that a real cycle runs one step further. That step is one no-op write.

Viff reads the cap at the check, not at install. The charter changes: a rule that is added or
removed changes how long a chain can legitimately be.

### A chain is per subject

The chain of a firing is keyed to its [subject](glossary.md#subject). Viff enforces this. It
does not assume it.

A firing inherits the depth of the commit that woke it only when that commit **changed the
subject that the firing is about**. Otherwise the firing begins a fresh chain. Nothing is lost.
A real loop changes its own subject on every lap, because that is what makes it a loop. The
pigeonhole argument still holds over a chain about one subject.

It might seem that this needs no enforcing. The match grammar has no joins. A `Match` selects on
the path and value of one node. Nothing relates a field of one node to a field of another. So
where an action writes only its subject, a write to S cannot bring S′ into the selection of a
rule. A repeat of rule T must then be a repeat about the same subject.

That argument is true, and it is not enough. **A firing is woken by a commit, not by a
subject**, and the rule selects the whole set again at every commit. Here is an example:

1. A rule acts on member A. It writes A.
2. That commit evaluates the rule again. The rule selects B, which already matched. Nothing
   brought B in.
3. The rule fires about B, woken by the commit of A.

Counted without the subject, the firing about B is a repeat one step deeper. A fan-out over n
members would count itself to depth n.

A consequence follows: **width is not depth**.

- A mirror that lands two hundred entities makes two hundred external writes. Each write starts
  its own chain.
- One rule that fans out over two hundred members is one chain of length 1. The chain fans out to
  two hundred firings.

Neither trips.

### Pacing breaks the chain, so a control loop never trips

A cycle is not wrong. A control loop is a cycle: poll, compare, correct, poll again. A charter
that could not express one would lack something. A runaway is a problem because it cycles *at
the speed of the store*, at no declared rate. The cycle itself is not the problem.

The chain continues only where the engine passes it on directly. The chain does not survive a
step that takes real time:

- a **spawned task**. Its writes arrive later through the API, as external writes.
- the **world**. The write of an effector comes back through the mirror as an external write.
- a **blocking gate**: an ask, a threshold or a deadline. A person who answers an
  approval paces a cycle exactly as a clock does. A firing that will wait begins a fresh chain,
  both for the check and for its writes. One place in the code decides this for both, so the two
  cannot disagree.
- a **ticker**, where one exists. A tick is a write of the machinery with no chain.

So a paced cycle starts again on every lap and never trips. An unpaced cycle climbs and reaches
the cap.

Synchronous digital design uses the same rule. A combinational loop is an error. A cycle through
a register is a state machine. The engine reaches this rule without knowing what a ticker is,
and without the need for a ticker to exist first.

## What a trip does

**A trip paces the firing. It does not refuse it.** Viff holds a tripped firing for
`tripDuration`, and then the firing proceeds. `tripDuration` is set in config, and its default
is 1s. A runaway settles at a slow, steady rate. It does not stop. A value of `0` refuses the
firing instead, which is a harder stop.

A trip costs latency, and the state transition still happens. When viff is wrong about a trip,
this is the less harmful error.

A held firing **aborts if its chain breaks during the wait**. A write to its subject during the
wait means the world changed. That write has already started the firing that is now correct, so
the held firing is stale.

The firing waits in its own goroutine, before it claims its subject. A wait on the dispatch path
would make every other rule wait behind one runaway. A wait while holding the claim would block
the firing that replaces it.

**The report is what a trip is for.** Pacing alone turns a runaway into a loop that runs slowly
forever. So the trip walks the chain back and names the rules that were firing each other:

```
INFO cycle: rules kept firing each other past the point where one must have repeated
     cycle="fwd → back → fwd → back" subject=demo.pr.1 depth=3 cap=2 from=4 complete=true
```

Both rules appear twice. That is the finding: one of them ran a second time about the same
subject.

Viff reports the cycle once per chain, keyed on the root of the chain. The same pair of rules
looping from a different origin gets a new report. The `complete` field says whether the walk
reached the beginning of the chain, or ran out of retained history.

The report is at **INFO, not WARN**. A tripped cycle is not always a fault:

- Recursion inside a [stratum](strata.md) is what an ordering of levels permits.
- A loop through the world is the shape a person would draw to explain the system.
- A charter may loop on purpose, for as long as it likes, once the loop is paced.

The report says which rules loop. The reader decides whether that is a problem. Viff keeps WARN
for what an operator has to act on. Examples are a write that did not land, and a rule that is
held for a stratum that never arrives.

The report names the cycle that *happened*. A static graph over templated patches could only
warn about a cycle that might happen, so the report is more useful.

## Why in-memory provenance is enough

The map has these properties:

- It is per engine.
- It is in memory, and viff discards it on restart.
- It is bounded. Once it grows past a few thousand entries, viff drops the oldest half.

A lost entry makes a commit read as external, and an external commit starts a fresh chain. So a
lost entry makes viff throttle less, never more. The decision does not degrade, because each
entry carries its own depth. Viff does not compute the depth by walking. Only the walk of the
report can come up short, and the report says so.

- **A restart is a fresh cause.** A restart loses the map and resets every chain. That is the
  correct reading of a restart, not a limitation.
- **Scopes cannot cycle.** The contract of `Store.Scope` is that writes land in the overlay,
  and reads see baseline with the scope on top. So the causal edge runs one way. Baseline writes
  are visible to the scope. Scope writes are invisible to baseline. Throttling per engine
  therefore composes.
- **Everything else is external, and external is the safe default.**

## Corners that do not run

- **Two baseline engines on one store are not covered.** Each engine throttles its own chains.
  But a write of engine A looks external to engine B, so neither engine bounds a cycle
  A→B→A. Scopes meet the condition by their structure. Two peers on one baseline do not. Nothing
  in the tree runs two peers, and the `Engine` type records the constraint.
- **Two rules that write one field are a different hazard, and the throttle does not catch
  them.** The field settles wherever the last write lands, and no chain grows.
- **Strata would tighten the bound, and the throttle does not use them.** A derive strictly
  increases the level, and nothing writes downward. So a chain that crosses levels could reset
  its counter at every crossing. The cap would then be max|stratum| instead of the whole
  charter. That part is not built. The cap is the number of installed rules, whether or not a
  charter declares [strata](strata.md). That cap is a usable bound.
- **One commit, one chain.** `Store.Write` writes one path, and the store emits one commit per
  delta. So a commit has exactly one cause. logd has transactions. A write of several paths in
  one atomic step would let one commit carry several chains. The reader would then need a rule
  to merge them. The obvious rule is the maximum, which errs toward throttling.
- **The depth is not observable.** By construction, it is internal to the engine. A viff instance that
  could answer "which chains are deep right now" would show a runaway as it develops, before it
  trips. Everything else about a firing is already a fact in the store.

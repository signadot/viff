# Throttling firing cycles: implementation plan

Status: plan, nothing built. The design is [cycle-throttle.md](cycle-throttle.md); this is how it
lands. Read that first — this file does not restate the argument, only the order of work, what
each step must prove, and the three places where reading the code changed the design.

The shape being built: every write an engine makes during a firing records, in memory, the causal
depth of the chain it belongs to; a firing woken by a commit deeper than the installed trigger
count is paced and reported by name.

## Three things the code says that the note does not

**logd already returns the commit, and the store's comment denying it is stale.**
`LogdStore.Write`'s doc says "there is no commit number in the result because libctl discards it
(`PatchResult.Commit` is dropped by `doPatch`)" (`entity/logdstore.go:376`). At go-tony v0.0.111
that is no longer true: `doPatch` returns `resp.Result.Patch` whole and documents `Commit` as "the
commit the patch committed at" (`system/libctl/logd.go:364-386`), and the server populates it
(`system/logd/api/session.go:392`). So step 1 is plumbing plus one test that pins the equality,
not a wait on upstream. Correcting that comment is part of the step: it is the reason a reader
would conclude this whole plan is blocked.

**The waking commit's revision is already threaded to every action write.** The note asks for a
seam so the depth can reach the writer; two thirds of it exist. `evaluate` hands the firing
`entity.Event{Rev: c.Rev}` (`trigger/edge.go:89`), and that `ev` flows through `fireOne` →
`Action.apply` → `applyOne` (`trigger/trigger.go:385,408`) → `advanceSeq`. The parent revision is
in scope at every action write already; what is missing is only that `patchEntity`/`modify`/`write`
drop it on the floor. Better still, `procEvent` leaves `Rev` at zero
(`trigger/engine.go:1409-1411`), so a completion hook — the world returning through the mirror —
starts a fresh chain with no code at all. The note's ten-odd call sites are real but most are
external by design (`versed`, `mirror`, `source/git`, `cmd/verse/connect`) and must *stay*
unstamped; the seam that matters is the engine's action-write path, which is three functions.

**A blocking gate is real time entering, and the note's own rule says the token should not survive
it.** The note lists three places pacing breaks the chain — a spawned process, the world via the
mirror, a ticker — and all three are places the engine hands off. A human answering an ASK gate is
the same thing and is not on the list. The counter-model is the note's own `fwd`/`back` pair with
an ASK gate on each: it advances only as fast as a person approves, which is the paced control
loop the design exists to permit, and yet the chain is unbroken engine writes climbing 1, 2, 3 —
so it trips. **A firing that blocked on a gate must start a fresh chain**, on exactly the argument
the note makes for the ticker. The corollary is that motion-channel writes stay unstamped, which
also removes the awkward question of what depth a gate ledger write carries.

## Order of work

Five steps. Steps 1–2 are invisible; step 3 is the first that can refuse anything. The dependency
order is real: 2 cannot record a chain without 1's revision, and 3 has nothing to check without 2.

### 1. `Store.Write` reports the revision it landed at — LANDED

`Write(w Write) (committed bool, err error)` becomes `(committed bool, rev int64, err error)`.
`rev` is meaningful only when `committed` is true; a refused precondition has no commit and reports
zero. Three returns is uglier than a result struct and is the smaller diff: most of the 38 call
sites (17 in the tree, 21 in tests) already read `_, err :=` and become `_, _, err :=`.

There is exactly one implementation (`LogdStore`) and no test fakes — every test runs against a
real logd (`entity/entitytest/store.go`) — so this is a compile-error-driven sweep with no
capability-interface fallout. Take `PatchResult.Commit` from both `sess.Patch` and `sess.PatchIf`
(`entity/logdstore.go:387,396`) and rewrite the doc paragraph that says it cannot be had.

**Proves:** the revision a write reports is the revision the watch stream reports for it. Test:
subscribe with `Observe`, write, assert the returned `rev` equals the `Commit.Rev` delivered — this
is the load-bearing assumption of everything downstream and the only one that depends on upstream
behaviour. Also assert a refused `PatchIf` reports `(false, 0, nil)`.

*Landed* in `entity/writerev_test.go`, four tests: the stream equality above, the conditional
(`PatchIf`) path reporting separately from the unconditional one, a refusal reporting no revision,
and `AsOf(rev)`/`AsOf(rev-1)` bracketing the write at the revision it reported — the last because
the throttle's reader will be handed revisions a writer supplied rather than ones it observed.

**`Store.Rev()` is not a cursor these revisions can be compared against**, in either direction. It
is the highest commit the root watch has delivered, over every writer, so it may stand ahead of a
write this client just made (a peer committed and was observed first) or behind it (the delta has
not arrived). Nothing orders the two. `AsOf` is unaffected — it reads server-side at a revision the
server assigned (`matchAt` → `sess.MatchAt`) and never consults the local cursor — which is why
the test above needs no wait. Step 3's map inherits the same freedom: it is keyed on what the write
reported, and reads nothing from `Rev()`. The trap to avoid is any check of the form
`Rev() >= reported`, which looks like a synchronization and is a coin flip.

### 2. The causal writer — LANDED

A firing writes through a `Store` that knows what woke it:

```go
type causal struct {
    entity.Store       // everything but Write delegates to the engine's store
    e      *Engine
    parent int64       // the commit that woke the firing; 0 = no chain
}
```

`Write` calls through, and on a committed write records `rev → {depth: depth(parent)+1, trigger,
parent}` in the engine's chain map. Embedding `entity.Store` is what keeps this cheap, and is why
the decorator beats a new engine method: `entity.Modify` takes a `Store`
(`entity/write.go:111`), so compare-and-swap writes go through the stamp without knowing it exists.
Note that `Modify` retries, so one `patchEntity` can issue several `Write` calls and only the
committing one is recorded.

The engine side: `e.caused(rev) entity.Store`, and `patchEntity`, `modify`, `write`
(`trigger/engine.go:1621,1644,1673`) take the parent revision and use it. Call sites pass `ev.Rev`,
which is already in scope. Two invariants to write down where they can be read:

- **One firing, one parent, however many writes.** A firing's machinery writes and its action write
  are siblings at `depth+1`, not a chain among themselves. Depth counts hops between firings.
- **`Scope` is a trap.** The embedded `Store.Scope` returns the *inner* store, so
  `caused(r).Scope(x).Write(...)` silently loses the stamp. Nothing in the engine scopes inside a
  firing today (`versed/server.go:673` does it per request, outside), but override `Scope` to
  re-wrap or to panic rather than leave the hole for the first caller who wants it.

Everything not on the action-write path keeps writing through the bare store and is external:
charter install (`trigger/charter.go:76`), goal records (`trigger/goal.go:248`), motion commits
(`trigger/engine.go:765`), the reaper, and every non-engine writer. That is the safe default —
external under-throttles.

**Proves:** an action's patch records an entry whose parent is the commit that woke the trigger,
and whose rev is the one the store reports; a hook firing (`ev.Rev == 0`) records depth 1, not
depth 1 off some stale parent; a charter install records nothing.

*Landed* as `trigger/causal.go` (`link`, `causal`, `caused`, `recordCause`, `depthOf`, `causeOf`)
with `trigger/causal_test.go`. Three departures from the plan above, all in the direction of a
narrower seam:

- **`write` and `modify` were left alone.** The plan had them take a parent revision; every one of
  their call sites is machinery and would have passed 0, which is a parameter that hides the
  decision rather than recording it. The seam is `patchEntity`, plus a `modifyOn(store, …)` that
  `modifyErr` now delegates to — so the engine's machinery keeps writing through `e.store` and
  stays outside every chain by construction, and `modifyOn`'s doc is where that cut is argued.
- **`publishSeq` carries the chain**, which the plan did not anticipate. Sequence progress is not
  bookkeeping off to one side: it lands on the SUBJECT under `verse.charter.<trigger>`, a root
  charters are documented to match on (`$verse.review.done`), so a progress write can wake a rule
  exactly as an action's write can. Its four call sites pass `ev.Rev`, which is 0 wherever the
  sequence resumed off a process completing — the world coming back, and the right place for a
  chain to end.
- **`patchEntity` now reports a failed write.** It used to reach the store via `modify`, which
  logged a generic Warn; going through `modifyOn` returns the error, so it is logged here instead
  and named by chain. The patch error is still what the firing stops on — behaviour unchanged,
  only the message improved.

`Scope` re-wraps rather than delegating, so a scoped write off a causal store keeps its stamp.

**`TestGoalConcurrentLifecycle` under `-race`, found here and now fixed.** It failed 3/3 at HEAD
before any of this, and it was not a data race — the detector reports none. Chased down, it is a
fact about the substrate worth writing where the next step can read it:

**Every store operation is one round trip, and ONE SESSION serves them strictly one at a time —
in the client, not the server.** Measured on a single session, 24 concurrent `List`s take 24× a
single `List`, and breadth is irrelevant: the whole verse root, one kinded slice, and a single
`Get` all cost the same, because the cost is the round trip and not the payload. Per operation
~3.0ms, and ~12.6ms under `-race` (4.2×, spent in tony encode/decode).

The serialization is **`libctl.LogdSession`'s read pump**, and it is worth naming precisely
because the obvious reading is wrong. Sends are pipelined — `request` releases the session mutex
before awaiting its reply — but `readPump` is a single goroutine that does `readDocument` (wire
parse) and `resp.FromTonyIR` (typed decode) inline, one response at a time, for every response
AND every watch event on that session. So decode is the choke point, and watch traffic and reads
contend for it.

Pinned by experiment rather than inferred, since linear scaling alone is equally consistent with a
serial server: spreading the same 24 concurrent reads over 24 sessions against ONE logd drops
87ms→34ms (and 298ms→112ms under `-race`). The server is concurrent. One session is not — its
24-goroutine per-op cost equals its serial baseline exactly, which is the signature of no
parallelism at all. (The 2.5× ceiling with 24 sessions is this 4-performance-core machine, not
another lock.)

`LogdStore` holds exactly one session, and its own doc says so: *"sess multiplexes everything:
reads, writes, the live root watch, per-trigger subtree watches, and Since's FromCommit
replays."* That is a deliberate and sound choice for watch fan-out; the cost of it is that the
whole engine's store throughput is bounded by one decode loop. Worth knowing before anything
tries to fix read amplification by reading in parallel — it will not help on one session.

A goal's lifecycle spends of the order of ten round trips (`Formula.Eval` → `Match.Select` →
`Pattern.Select` → `List`, twice on the startup path alone, plus the status modify, the watch
open, and the commit). The test's 24 goals are therefore ~240 serialized operations — about 0.7s
normally and 3.0s under `-race`, against a 3s budget.

Two things followed, both now in the test:

- The 3s wait is now 30s, sized for the work rather than tuned to a passing run.
- **The 3s deadline was letting it pass for the wrong reason.** When the watch is slow the
  deadline fires, its re-check finds the fact, and the goal latches `done` anyway — so a
  completely broken watch would still have produced the facts the test asserts on. The deadline
  is now 60s, out of reach, which makes the assertion mean *the watch delivered*.

For step 3 the useful part is the cost model: the throttle's own work is a map lookup and adds no
round trip, which is the thing that would actually hurt. `go test -race ./trigger/` now passes
clean, so it is a usable signal for the steps that follow.

### 3. The chain map, the cap, and the check — LANDED

The map is `map[int64]link` where `link{depth int, trigger string, parent int64}`, guarded by its
own mutex — firings run on their own goroutines (`fireSubjects` at `trigger/edge.go:89` spawns one
per subject) while `dispatch` reads it.

**Where the check goes.** The note's Open leaves this at "before the gates, by analogy with
`working` and `claimFlight`". Put it in `evaluate`, before `fireSubjects` — one revision-keyed
lookup per (trigger, commit) rather than one per subject, and it is the right altitude because
depth is a property of the *commit*, not of the subject. It is still before every gate, so the
symmetry argument is satisfied; it is simply cheaper on a fan-out.

**The cap** is `len(e.triggers)` read under `trigMu` at check time, with a floor of 1. Goals
install triggers of their own (`trigger/goal.go`), so the count moves at runtime and reading it
late is the correct reading.

**The gate reset.** A firing that blocked on any gate — an ASK/PARK resolver call, or a temporal
`Formula` gate — passes `0` as the parent for its action write instead of `ev.Rev`. Decide this in
`fireOne` where `allProceed` is known (`trigger/engine.go:587`): if any gate blocked, the chain
ends here and the action starts a new one.

**Eviction.** The map is unbounded otherwise. Keep the most recent N entries (N on the order of a
few thousand) and drop the oldest. Losing an entry makes an old chain read as external, which
under-throttles — the safe direction — and the *decision* never degrades, because each entry
carries its own depth rather than deriving it by walking. Only the report loses ancestors, and it
renders what remains.

**Proves:** the `fwd`/`back` charter from
[convergence §the cycle](../convergence.md#the-cycle-which-is-the-one-that-is-not-checked) stops
climbing at depth = 2 (two triggers) instead of running to `maxFires` or forever; the same pair
with an ASK gate on each never trips, however many laps it runs; a mirror landing 200 entities in
one commit fires 200 times at depth 1 and trips nothing (the note's burst case, which is the
regression this design is most likely to break); a chain through a completion hook restarts at 0.

*Landed*, and the two things above that turned out to be wrong were both found by writing those
tests rather than by reasoning — which is the argument for having written the burst one at all.

**The burst case was a real false positive: 30 external writes tripped 17 times.** A firing is
woken by a COMMIT, not by a subject, and the rule re-selects the whole set at every commit — so
acting on member A writes A, that commit re-evaluates the trigger, which selects B (already
matching, never *brought in*, so the no-joins argument is untouched), and fires about B off A's
commit. Counted naively that is a repeat one hop deeper, and a fan-out over n members walks itself
to depth n. Fixed by making the chain **per subject**: a firing inherits the waking commit's depth
only when that commit changed the subject it is about. Nothing is lost — a real loop advances its
own subject every lap, which is what makes it a loop — and pigeonhole still holds over a
same-subject chain. The design note's "bursts need no special case" section is corrected in place.

**The bound is `depth > cap`, not `depth >= cap`.** I tightened it to `>=` on the reasoning that
the (N+1)-th firing must repeat a trigger, so refusing it is exact. It is exact and it is wrong:
a repeat is not per se a fault. The ordinary CONVERGING rule — one whose write leaves its subject
still selected — fires a second time, writes the same value, produces no commit, and dies there.
Under `>=` with one trigger installed, that healthy second firing was refused and reported as a
runaway (`TestTriggerFiresOnTheBacklogAtInstall` tripped twice). `>` excludes it exactly, because
**depth counts commits**: a converging rule commits once and never reaches depth 2, a runaway
commits every lap. The cost is one extra no-op hop before a real cycle is caught.

**Gates are handled at chain formation, not at the trip.** The alternative — let depth accumulate
and check for gates in the chain before tripping — was considered and rejected twice over: a chain
that passed a gate long ago and has since become an unpaced runaway would be suppressed forever by
its own history, and a trip-time walk can lose the gated ancestor to eviction and then
OVER-throttle, which is the wrong direction to fail in. Resetting at the gate cuts the chain
there, so a loop that contains a gate never accumulates while "one gated hop, then A→B→A→B" does.
What the trip-time version would have bought is chain continuity for the report; step 4 can have
that instead by keeping `parent` truthful while resetting only the depth.

`TestBudgetBoundsRunaway` was rewritten rather than adjusted: the throttle now stops that
oscillation at 3 firings against a budget of 5, so what the test asserted is no longer what
happens. It is now `TestRunawayOscillationStopsBeforeTheBudget`, and it fails if the budget is
ever what stops it again.

### 4. The report — LANDED

The deliverable, per the note. Walk `parent` back from the tripping revision, collecting trigger
names, and log at Warn: the trigger chain that exceeded the bound, the cap, and the revision it
started from. Emit it once per chain rather than per lap — key on the deepest ancestor still in the
map — or a paced runaway writes a log line every interval forever, which is the same failure in a
different file.

A truncated walk (evicted ancestors) says so rather than presenting a partial chain as whole.

**Proves:** `fwd`/`back` produces one Warn naming both triggers, in causal order, and a second
runaway on unrelated triggers produces its own rather than being folded into the first.

*Landed* as `walkChain` plus the `reported` set. What an operator sees:

```
WARN cycle: rules kept firing each other past the point where one must have repeated
     cycle="fwd → back → fwd → back" subject=demo:pr:1 depth=3 cap=2 from=4 complete=true
```

Both rules appear twice, which IS the finding: one of them ran a second time about the same
subject. Suppression is keyed on the chain ROOT rather than on the trigger, because it is the
chain that has been explained — the same pair looping from a different origin is news again.

**`complete` nearly shipped inverted.** The first cut ended the walk when a link's parent was
absent from the map, and reported `complete=false` for a perfectly whole chain: the origin of
every healthy chain is an EXTERNAL write, which is by definition not in the map. Ending on
`depth <= 1` is the exact test — depth 1 means the parent had depth 0, which is the external
write — and it keeps `complete=false` meaning what it should, that eviction ate the ancestors.
A truncated walk that claimed to be whole would name the wrong rule as the origin, and the first
name is the one a reader goes and looks at.

Not done, and noted at step 2's expense: a paced firing resets by passing parent 0, so the walk
stops at the pacing rather than showing the hop before it. Keeping `parent` truthful while
resetting only the depth would give the report continuity across a gate — it needs `caused` to
take the base depth separately from the parent revision, which `ev.Rev` alone cannot carry.

### 5. Pacing — LANDED

Last, because it is the one thing the note leaves genuinely open and everything above is testable
without it. Until this step a tripped chain is reported and proceeds, which is strictly better than
today.

Proposal, to be argued rather than assumed: a fixed delay before the firing proceeds, growing with
how far past the cap the chain is, ceilinged in the tens of seconds. It converts a runaway into a
slow loop with an operator-readable Warn, which is the note's "pace, do not refuse" taken
literally. The alternatives — a token bucket per trigger, or eventually stopping — both change what
the throttle *is* rather than how fast it runs, and neither should be settled by whoever happens to
write step 5.

One thing to check when it lands: a paced firing holds `claimFlight` and a goroutine while it
sleeps. Chains are sequential (a write, its commit, the next firing), so the count in flight stays
small — but assert it rather than reasoning about it, because a wide fan-out at depth is the shape
that would disprove it.

*Landed*, as a FIXED D — `config.tripDuration`, `Engine.SetTripPace`, default
`trigger.DefaultTripPace` = 1s. The escalation proposed above was dropped: depth is tracked and
reported because it says how badly a charter is looping, but the pace is what the loop settles at,
and a settled loop should look the same whether it is two rules or ten.

**1s is chosen against a measured floor, not derived from it.** `entity.TestLoadUnderWatches`
(behind `GOTEST_LONG`) writes under N live subtree watches; on the substrate as it stood, a
charter's worth of them sustained order tens of writes per second, which put a paced chain at a
few percent of capacity and put D=100ms at all of it. That floor is substrate-specific and is
moving — logd throughput is being worked on — so the figures are deliberately not written into the
code or relied on here. What survives the substrate getting faster is the intent: one firing per
second is a rate an operator can watch scroll past, which is what "polite steady state" has to
mean. `config.tripDuration` is there for a deployment that disagrees.

**A paced firing aborts if its chain breaks under it.** While it holds D, a write to its subject
means the world moved: that write already re-evaluated the rule and raised whatever firing is now
correct, so the held one is stale. `pauseFor` registers the subject in a map that holds only what
is waiting; `dispatch` signals it per change. Two placement constraints, both load-bearing: the
wait is in the firing's own goroutine, because sleeping on the dispatch path would stall every
other rule behind one runaway; and it is before `claimFlight`, because holding the claim would
block the very firing that supersedes it.

**The engine's default is the paced one, and two tests now say `SetTripPace(0)` out loud.**
They passed either way — their windows are shorter than 1s — but "stopped" and "not yet released"
are different claims, and a test that cannot tell them apart is not asserting what it says.

Layering worth noting: `config.TripPace()` returns `(d, set, err)` and does NOT own the default.
An unset value leaves the engine on its own, so the number lives with the behaviour it paces
rather than being duplicated into a config package that would have to be kept in step. A
malformed value fails startup rather than falling back, since a server quietly running at a pace
nobody asked for is what the setting exists to prevent. `versed.SetTripPace` applies to the
baseline engine and to every scope engine as it is lazily built, so a developer's federated scope
cannot run a different policy from the verse it overlays.

### 6. Cleanup: retire what the throttle replaces — LANDED

Not in the original plan. It belongs here because the throttle does not merely add a check — it
takes over from two things, and leaving them in place is worse than never having had them.

**`maxFires` should go, and `Fired()` should stay.** They are separable and only one is a
problem. The counter is observability and half the trigger tests read it; the BUDGET is
`SetMaxFires`/`overBudget`/`reserveN`, and it has become actively harmful:

- It is **cumulative and engine-wide, and never resets**. Once exhausted the engine stops firing
  everything, forever, for every rule — the failure convergence already names ("a single looping
  rule would exhaust the budget for every other rule as well").
- With paced loops now explicitly blessed, **any finite budget is a time bomb**. A healthy control
  loop — the exact shape this design exists to permit — burns budget every lap and will
  eventually kill the whole charter. That is the "accidentally kill a perfectly nice loop" case,
  and it is not hypothetical: it is what a ticker-driven charter does by construction.
- It was standing in for the cycle check. That is now built, and better: it names the rules that
  looped instead of silently stopping at a number.

What it still bounds and the throttle does not is **fan-out width** — one firing over 200 members
is 200 actions, and depth sees a burst as width, correctly. If that bound is wanted it should be
per-firing (a cap on members acted on in one firing, refused loudly) rather than a cumulative
engine-wide counter, because width is a property of a firing and not of an engine's lifetime.

Scope: `SetMaxFires` has four call sites, all in tests (`engine_test.go` ×3, `fanout_test.go`
×1); `TestFanoutBudget` is the one that tests something real and is the one to re-point at a
per-firing cap or to retire deliberately.

**The cycle lint does not need retiring, because it was never built.** Worth stating since it is
the obvious thing to look for: `AddTrigger` runs `checkLatch` (fatal, and strictly per-trigger —
a spawning action must write a field its subject reads) and `adviseSampling` (the advisory `on`
coverage warning). Both are single-rule. The cross-rule cycle graph exists only as a proposal in
[firing residual §1](firing-residual.md#1-the-cycle-check-and-the-clock-it-needs), and
convergence's "nothing in the model sees it" was literally true until now. So there is no dead
lint to remove — but firing residual §1 and convergence's closing section both now describe a gap
that is closed, and should be updated to point here rather than at an unbuilt graph.

*Landed.* `maxFires`, `SetMaxFires`, `overBudget` and `reserveN` are gone; `fired`/`Fired()`
survive as `countFire`, a counter with no cap. Four tests changed, and each was re-pointed at the
property it was really protecting rather than deleted:

- `TestBudgetNotConsumedByDenials` → `TestDenialsDoNotCountAsFirings`. The property outlives the
  budget: a question answered "no" leaves no mark on the counter.
- `TestBudgetBoundsRunaway` → `TestRunawayOscillationIsStoppedByTheThrottle`. It used to assert
  the chain ran to exactly `maxFires` — a number reached without anything having understood why.
  Now a throttle regression makes it hang rather than settle at a larger number, which is the
  honest failure.
- `TestIdempotentWriteDoesNotLoop` keeps its subject and gains `trips == 0`: the ordinary
  converging rule must not read as a runaway, which is the `>` vs `>=` distinction this shape
  caught.
- `TestFanOutBudgetMetersEntities` → `TestFanOutActsOnEveryMemberWithoutTripping`. It asserted a
  fan-out was TRUNCATED to the budget (4 of 10 members); with no budget there is no truncation,
  so it now asserts all ten advance and nothing trips — the design note's burst case at its exact
  shape, one chain of length 1 fanning out to n firings.

One thing that test surfaced, pre-existing and left alone: the fan-out fires **26** times for 10
members, because each member's write re-evaluates the rule and a member whose own write has not
landed yet is still selected. The extra firings write the same value, produce no commit, and
settle — ordinary convergence. The old budget of 4 capped this test below the point where it was
visible.

`docs/convergence.md` is updated where it had become false: the backstop paragraph, the "not yet
detected" claim, and the pointer to an install-time graph that will not now be built. Its
reasoning is kept — it is what led here — and marked as history rather than deleted.

## What this plan deliberately does not do

- **No stratum reset.** Nothing in the tree knows what a stratum is. The cap is the installed
  trigger count, which the note says is usable today; `max|stratum|` is a later tightening at one
  call site.
- **No merge rule for multi-path commits.** `Store.Write` writes one path and logd emits one commit
  per patch, so a commit has one cause. When transactions (`NewTxResult.TxID`) admit several, max
  is the obvious rule — but writing it now would be code with no reachable input.
- **No observable depth.** The note's last Open. Engine-internal state, and exposing it is a
  question about what a verse can be asked, not about throttling.
- **No per-trigger fire budget** *while the throttle lands*. `maxFires` stays engine-wide and
  unbounded-by-default ([firing residual §2](firing-residual.md#2-the-runaway-backstop)) through
  steps 1–5, because fixing both at once would leave neither's effect legible. Step 6 then retires
  it, now that the throttle's effect can be read on its own.
- **No `(Commit, Snapshot)` inbound stream.** The note names it as wanting the same seam and says
  the throttle does not need it (`trigger/edge.go:49` calling `AsOf` per trigger, one read per
  subtree at `entity/logdstore.go:1093`). It is a bigger change with its own customers and should
  not ride in on this one.

## Found while benchmarking: the orphaned subtree watch

Building the load probe for step 5 turned up a defect with nothing to do with the throttle, and it
outranks it: **a subtree watch can die silently and never come back, and a trigger over that slice
then simply stops firing** — with no error anywhere a charter author would see. `ObserveScoped` is
versed's production observation path, so this is not a corner.

Writing N entities into one watched slice and then waiting three minutes idle, a slice of a few
hundred arrived in full but slowly, and a larger one stalled partway through and never moved
again.

**Two things I reported earlier were wrong, and the way they were wrong is worth keeping.** I first
measured with a twenty-second budget and concluded the watch loses events permanently past a few
hundred entities. The smaller slice disproves that: it was *latency*, and it arrives. The larger
one is the real defect, and it is the one a short window made look like the same thing. I also
blamed `emitDiff` for being O(slice) per commit and so making us the slow consumer; timing it says
tens of microseconds per event, a rounding error across a whole run. It is not the bottleneck and
that story was invented.

What is actually happening, read out of the code and confirmed by instrumentation (the client's
watch never ends: `ended=0`, and events received ≈ events delivered, so nothing is lost inside
verse):

1. `WatchHub.Broadcast` runs on the committing goroutine and must not block, so a watcher whose
   buffer it cannot drain is **failed and removed** (`logd/server/watch.go`, "fail it loudly").
   The trigger is the server's own forwarding rate, not ours — putting the watcher on its own
   session changes nothing (411 vs 413 delivered).
2. It reports that with `s.send(api.NewErrorResponse(watcher.ID, …))` — an **error response
   stamped with the watch id**, not a watch event carrying `Ended`.
3. libctl's read pump routes anything with no `Event` to `deliverResponse`, which looks the id up
   in the table of **in-flight requests**. A watch id was never in that table — its request
   completed when the watch opened. So the failure is logged as `"dropping response with no
   matching request"` and thrown away.

The watch is then orphaned: gone on the server, alive on the client, its channel silent forever.
Nothing re-establishes because nothing was told.

**Fixed on our side by not only waiting.** `subtreeHealAfter`/`subtreeHealMax`: a whole interval of
silence — from a slice that may merely be idle, or from one whose watch is dead — is answered by
re-establishing, whose State resyncs the document and whose diff against the last snapshot emits
everything missed. The interval doubles while nothing arrives (30s → 5min) so an idle slice
settles into a cheap heartbeat; any event resets it. N=2000 goes from permanently stuck at 172 to
complete. Pinned by `TestSubtreeWatchHealsAfterSilentDeath` (long), and by mutation: with the
interval pushed out of reach the test fails at 171 of 2000.

Three things this does not do, all worth knowing:

- **It does not fix the cause**, which is upstream and belongs on the go-tony defects issue: an id
  that names a watch should reach that watch, and a client that can only be told is a client that
  waits forever. The heal is a floor under that, not a substitute.
- **A resync re-emits entities already reported** — N=2000 delivered 2172 events. `emitDiff`
  compares payloads with `reflect.DeepEqual` over `ir.Node`, which is sensitive to structure a
  re-read rebuilds, so identical values compare unequal. Level-correct (each entity is reported as
  it stands) and it costs spurious firings.
- **It says nothing about the latency**, which is the other half of the measurement: delivery runs
  at tens of events per second and can lag minutes behind a burst.

**Both halves were upstream's, and both are fixed.** go-tony v0.0.112 made the watch's end a
proper terminal event carrying a resume commit, so a dropped watcher is now something the client
is told about rather than something it has to notice. v0.0.113 brought the watch-forwarding
speedup: the burst-survival test went from minutes to seconds, and the whole suite runs clean
under `-race`, including a store test that had been failing there on timing alone.

Specific figures are deliberately not recorded here or in the code. What they measure belongs to
the substrate and moves with it; the harness is `entity.TestLoadUnderWatches`, behind
`GOTEST_LONG`, with `BENCH_WRITES` and `BENCH_WATCHES`. Run it rather than trusting a number
written down in a plan.

Two things on our side came out of it and stay:

- **`emitDiff` compares with `ir.Node.DeepEqual`, not `reflect.DeepEqual`.** A node carries
  `Parent`/`ParentIndex`/`ParentField` — where it sits, not what it holds — and a document rebuilt
  from a fresh read wires those up anew. `reflect` compares them; ir's `DeepEqual` documents that
  it does not. Under `reflect`, every entity in a slice compared unequal to its own identical
  value after a resync, so re-establishing a watch re-emitted the whole slice as changed (twenty
  entities, forty events) and every trigger over it would have fired again for nothing.
- **A re-established watch re-initializes rather than resuming.** logd offers an exact resume
  point and libctl's own doc says who it is for: *"a snapshot-diffing consumer needs neither and
  can just re-watch and reconcile against its last snapshot."* That is this watch. Measured while
  the substrate was still slow, resuming replayed the missed deltas one at a time into the stream
  that was already the bottleneck and fell far behind a single State conveying net state.

## Risks worth watching

**Under-throttling is invisible.** Every conservative choice here (external default, eviction, gate
reset, unstamped machinery) fails toward not throttling. That is the right direction, but it means
a cycle that slips through leaves no trace that it slipped — the throttle cannot report a chain it
never recorded. The `fwd`/`back` test is the only thing standing between "correctly conservative"
and "quietly inert", so it should be the first test written, not the last.

**Two baseline engines on one store.** Named in the note and unfixed here: A's write looks external
to B, so an A→B→A cycle is throttled by neither. Scopes are safe structurally
(`entity/store.go:97-102`). Nothing in the tree runs two peer engines on one baseline today; if
something starts to, this is the constraint it breaks.

Recorded on the `Engine` type itself (`trigger/engine.go`) rather than left here, because the
constraint is already load-bearing without the throttle — `seen`, `inflight` and the fire budget
are all per-engine and all read a peer's write as somebody else's business — and a working note is
not where someone about to start a second engine would look.

package trigger

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/signadot/verse/entity"
)

// Causal provenance: which commits this engine caused, and how far each one stands from the
// write that started it.
//
// The problem it exists to solve is the one hazard convergence names and nothing checks — two
// rules each writing what the other selects on, which runs at the speed of the store until a
// budget stops it. The answer here does not look for the cycle. If a write carries the depth of
// the chain that produced it, a runaway is a chain that keeps growing, and no analysis of what a
// patch might compute or what a predicate might come to satisfy is needed to see it. Nothing has
// to be detected; the counter IS the detection. See docs/working/cycle-throttle.md.
//
// This file records the chain, checks it, paces what trips, and reports it. A trip holds the
// firing for D (SetTripPace) and then lets it through, so a runaway settles into a dull steady
// rate instead of running at the speed of the store — a trip costs latency rather than the state
// transition, which is the forgiving side to err on. D=0 refuses instead, the harder stop.

// link is one commit's place in a causal chain: how far it stands from the external write that
// started the chain, the firing that wrote it, and the commit that woke that firing.
//
// parent makes the chain walkable, which is what the diagnostic needs — the deliverable of the
// throttle is naming the rules that looped, and a chain of names reconstructed by walking back
// costs nothing per write, where carrying the names forward on every write would copy a growing
// slice at every hop.
type link struct {
	depth  int
	chain  string // the firing's kpath: the trigger name, then any hook/sequence descent
	parent int64  // the commit that woke the firing that wrote this one; 0 = it began a chain
}

// caused returns the store a firing writes through: one that remembers what woke it, and stamps
// the chain onto whatever it commits.
//
// It is a Store rather than an engine method because entity.Modify takes a Store — so every
// compare-and-swap write goes through the stamp without knowing it exists, which is the property
// that keeps this from being a stamp at ten call sites that an eleventh bypasses.
//
// parent is the revision of the commit that woke the firing, and 0 means the firing had no
// engine-caused cause: a person, a process completing, a mirror, a peer. Both are handled by the
// same lookup — an absent parent has depth 0, so its children stamp 1, which is exactly "an
// external write starts a fresh chain".
func (e *Engine) caused(chain string, parent int64) entity.Store {
	return causal{Store: e.store, e: e, chain: chain, parent: parent}
}

// causal is the store a firing writes through. Everything but Write and Scope delegates to the
// engine's own store, so it is a stamp and not a second store implementation.
type causal struct {
	entity.Store
	e      *Engine
	chain  string
	parent int64
}

// Write commits through the underlying store and records what the commit descends from.
//
// Only a committed write is recorded, and only one that reported a revision: a refused
// precondition wrote nothing, and entity.Modify retries, so one patch may issue several writes
// of which at most one lands. An unrecorded commit reads as external later, which under-throttles
// rather than over-throttles — the direction to fail in.
func (c causal) Write(w entity.Write) (bool, int64, error) {
	committed, rev, err := c.Store.Write(w)
	if committed && rev != 0 {
		c.e.recordCause(rev, c.chain, c.parent)
	}
	return committed, rev, err
}

// Scope re-wraps rather than delegating, which is the whole reason it is spelled out. The
// embedded Store's Scope would hand back the INNER store, so a scoped write off a causal store
// would silently lose the stamp and read as external — a hole that would open the first time
// anything scoped inside a firing, and would look like the throttle simply not noticing.
func (c causal) Scope(name string) entity.Store {
	return causal{Store: c.Store.Scope(name), e: c.e, chain: c.chain, parent: c.parent}
}

// recordCause places a commit in its chain: one hop further from the external write than the
// commit that caused it.
//
// A parent this engine did not cause is not in the map and reads as depth 0, so its children
// stamp 1. That covers every way a chain can begin — a person writing over the API, a spawned
// process's result arriving through the mirror, a git source reflecting, another engine — with
// no case analysis and no way to be told which is which.
func (e *Engine) recordCause(rev int64, chain string, parent int64) {
	e.chainMu.Lock()
	defer e.chainMu.Unlock()
	if e.causes == nil {
		e.causes = map[int64]link{}
	}
	e.causes[rev] = link{depth: e.causes[parent].depth + 1, chain: chain, parent: parent}
	e.evictCausesLocked()
}

// maxCauses bounds the chain map. It is a backstop on memory, not a tuning knob: a chain deep
// enough to matter is bounded by the trigger count, and what fills this map is BREADTH — every
// commit the engine writes, however unrelated.
const maxCauses = 4096

// evictCausesLocked drops the oldest half of the map once it passes the bound, so the cost is
// amortized rather than paid per write.
//
// Losing an entry makes a commit read as external, which starts a fresh chain — it
// UNDER-throttles, and never the reverse. The DECISION does not degrade with eviction either,
// because each entry carries its own depth rather than deriving it by walking: only the report's
// walk back over parents can come up short, and it says so rather than presenting a partial
// chain as whole. What that costs in practice is nothing: the commits a live chain needs are the
// most recent ones, and this drops the oldest.
func (e *Engine) evictCausesLocked() {
	if len(e.causes) <= maxCauses {
		return
	}
	revs := make([]int64, 0, len(e.causes))
	for r := range e.causes {
		revs = append(revs, r)
	}
	slices.Sort(revs)
	for _, r := range revs[:len(revs)/2] {
		delete(e.causes, r)
	}
}

// chainCap is the depth bound: the number of installed triggers, floored at 1.
//
// Read LATE — at the check rather than at install — because the charter moves at runtime: goals
// install triggers of their own (see goal.go), and a rule added or removed changes how long a
// chain can legitimately be. The count now is the count the pigeonhole argument is about.
func (e *Engine) chainCap() int {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	if len(e.triggers) < 1 {
		return 1
	}
	return len(e.triggers)
}

// causeFor settles which chain a firing about subj belongs to: the revision it inherits its
// depth from, or 0 to begin a fresh one.
//
// A CHAIN IS PER SUBJECT, and this is where that is enforced rather than assumed. The design
// argues that a repeat of trigger T in a chain is necessarily a repeat on the same subject,
// because the match grammar has no joins and an action writes only its subject — so a write to S
// cannot bring S′ into a trigger's selection. That is true and it is not enough. A firing is
// woken by a COMMIT, not by a subject, and the rule re-selects the whole set at every commit: so
// a trigger acting on member A writes A, and that commit re-evaluates the trigger, which selects
// B — already matching, never brought in by anything — and fires about B off A's commit.
//
// Counted naively that is a repeat of T at depth+1, and a fan-out over n members walks itself up
// to depth n: a burst of 30 external writes tripped 17 times before this existed, which is
// exactly the "width, not depth" case the design says needs no special case. It needs this one.
// Inheriting only when the waking commit CHANGED this subject restores the property the argument
// assumed, and costs nothing that matters: a real loop advances its own subject every lap, which
// is what makes it a loop.
//
// A PACED FIRING BEGINS A FRESH CHAIN, for the check and for its writes alike — decided here, in
// one place, so the two cannot disagree. The token propagates only where the engine hands it
// straight on; everywhere real time enters it does not survive, and a blocking gate is real time
// entering. A person answering an ask paces a cycle exactly as a ticker would, and a cycle
// running at the speed of human approval is a control loop rather than a runaway. (A temporal
// gate whose formula already holds resolves without waiting, and still counts as pacing — which
// under-throttles, the direction to err in.)
func (e *Engine) causeFor(t Trigger, c entity.Commit, subj entity.Ref) int64 {
	if t.blocks() {
		return 0 // it will wait on a decision: real time, so the chain ends here
	}
	if !changedIn(c, subj) {
		return 0 // woken by a change to something else: not this subject's chain
	}
	return c.Rev
}

// changedIn reports whether the commit changed this entity.
func changedIn(c entity.Commit, ref entity.Ref) bool {
	for _, ev := range c.Changes {
		if ev.Ref == ref {
			return true
		}
	}
	return false
}

// blocks reports whether a firing of this trigger will wait on a decision — an ask or a park
// posed to a resolver, or a temporal formula monitored on the store.
func (t Trigger) blocks() bool {
	for _, g := range t.Gates {
		if g.Formula != nil {
			return true
		}
		if m, err := g.mode(); err == nil && m.Blocks() {
			return true
		}
	}
	return false
}

// throttled reports whether a firing off this parent would extend its chain past the point where
// it must have repeated a trigger — and records the trip.
//
// THE TEST IS d > N, NOT d >= N, and the difference is the whole margin a converging rule needs.
// A commit at depth d was written by the d-th firing in its chain, so a firing off it is the
// (d+1)-th, and with N triggers installed that firing must repeat one exactly when d+1 > N. The
// tighter d >= N therefore looks right — it refuses the first firing that cannot be a fresh
// trigger — and it is wrong, because a repeat is not per se a fault.
//
// The ordinary converging rule is a repeat. A rule whose write leaves its subject still selected
// fires a second time, writes the same value, produces NO COMMIT, and the chain dies there — that
// is the documented way a vacuous `on` settles (see evaluate, and docs/change-events.md). Under
// d >= N with a single trigger installed, that healthy second firing is refused and reported as a
// runaway. It cost nothing behaviourally, since the firing was a no-op either way; it cost the
// report, which is the actual deliverable, and a throttle that cries wolf on convergence is worse
// than none.
//
// d > N excludes it exactly, because DEPTH COUNTS COMMITS. A converging rule commits once and
// never reaches depth 2; a runaway commits every lap. So the bound distinguishes them without
// having to know which is which — at the cost of letting a chain run one hop past the repeat,
// which is one no-op write.
//
// A fresh chain has depth 0 and the cap is at least 1, so it never trips.
func (e *Engine) throttled(t Trigger, subj entity.Ref, parent int64) bool {
	depth := e.depthOf(parent)
	if depth == 0 {
		return false // this firing begins a chain
	}
	cap := e.chainCap()
	if depth <= cap {
		return false
	}
	names, root, complete := e.walkChain(parent)
	e.chainMu.Lock()
	e.trips++
	if e.reported == nil {
		e.reported = map[int64]bool{}
	}
	first := !e.reported[root]
	e.reported[root] = true
	e.chainMu.Unlock()
	if !first {
		// Same chain, reported already. A runaway that keeps being re-ignited from the same
		// root would otherwise write the same paragraph at the rate the store accepts writes,
		// which is the original failure in a different file.
		e.tracef("chain from rev %d tripped again at %s on %s", root, t.Name, subj)
		return true
	}
	// INFO, not Warn. A cycle that trips is a charter doing something the model allows and the
	// throttle then paces — recursion inside a stratum is exactly what a level ordering permits
	// (stratum.go), and a loop through the world is the shape a person would draw on a
	// whiteboard. What the report is FOR is telling someone which rules were firing each other,
	// not telling them something is broken; a charter can loop deliberately and run this way for
	// as long as it likes. Warn is for the things an operator has to act on — a write that did
	// not land, a rule held for a stratum that never arrives.
	e.logger.Info("cycle: rules kept firing each other past the point where one must have repeated",
		"cycle", strings.Join(append(names, t.Name), " → "),
		"subject", subj.String(), "depth", depth, "cap", cap,
		"from", root, "complete", complete)
	return true
}

// walkChain reconstructs a chain from its most recent commit back to where the engine's record
// of it starts: the firings that wrote it, oldest first, the revision it began at, and whether
// the walk reached a genuine beginning rather than running out of map.
//
// THIS IS THE DELIVERABLE. Pacing a runaway leaves it running politely forever; what an operator
// can act on is the list of rules that were firing each other, and this names the cycle that
// HAPPENED rather than one a static graph says might. It is also why link carries a parent: the
// names are reconstructed by walking back, at a cost paid once per trip, rather than by copying
// a growing slice onto every write.
//
// complete is false when the walk stopped because an ancestor had been evicted. Saying so
// matters — a partial chain presented as whole would name the wrong rule as the one that started
// it, and the first name is the one a reader will go and look at.
func (e *Engine) walkChain(from int64) (names []string, chainRoot int64, complete bool) {
	e.chainMu.Lock()
	defer e.chainMu.Unlock()
	rev, root := from, from
	for range maxCauses { // the map is bounded, so a cycle in it cannot spin this
		l, ok := e.causes[rev]
		if !ok {
			break // evicted, or the chain began here
		}
		names = append(names, l.chain)
		root = rev
		if l.depth <= 1 {
			// Depth 1 IS the beginning: its parent had depth 0, which is the external write —
			// a person, a process completing, a mirror — that started this off. Testing the
			// parent for absence instead would report every healthy chain as truncated, since
			// an external commit is by definition not in the map.
			complete = true
			break
		}
		rev = l.parent
	}
	slices.Reverse(names)
	return names, root, complete
}

// DefaultTripPace is D unless something says otherwise: the interval a tripped chain settles at.
//
// One firing per second is a rate an operator can watch scroll past and a store can absorb
// without noticing, which is what "polite steady state" has to mean — a looping charter should
// cost a dull background hum while somebody reads the report naming the rules, not a hammering
// and not a stall.
//
// It was chosen against a measured floor: on the substrate as it stood, a charter's worth of live
// subtree watches sustained order tens of writes per second, which left a paced chain at a few
// percent of capacity and a tenth of this interval at all of it. That floor is substrate-specific
// and moves — logd's throughput is being worked on — so the number is deliberately not derived
// from it, and config.tripDuration exists for the deployment that wants a different one.
const DefaultTripPace = time.Second

// SetTripPace sets how long a firing waits when its chain has run past the bound: D, the
// interval a tripped chain then runs at. Zero REFUSES the firing instead, which is the harder
// stop and what a test wants when it needs the runaway to end rather than to hum.
//
// Pacing rather than refusing is the forgiving side to err on — a trip then costs latency rather
// than the state transition — and D is what turns a runaway into a steady, dull background rate
// an operator can live with while they read the report. It is not a rate limit for load: see
// DefaultTripPace for what the default is chosen against, and why it is not derived from a
// throughput figure that moves.
//
// D does NOT vary with how far past the bound the chain has gone. The depth is tracked and
// reported, because it says how badly a charter is looping; the pace is what the loop settles
// at, and a settled loop should look the same whether it is two rules or ten.
func (e *Engine) SetTripPace(d time.Duration) { e.tripPace = d }

// pauseFor waits D before letting a tripped firing proceed, and reports whether it should.
//
// It aborts when THE CHAIN BREAKS UNDER IT — when the subject is written by anything else while
// we hold the firing. That write has already re-evaluated the rule, so proceeding would act on a
// state that has moved, and would do it with a second firing about a subject that already has a
// fresh one. Aborting is not a loss: if the loop is still looping, the write that broke the wait
// raised its own firing.
//
// The wait happens OFF THE DISPATCH PATH, in the firing's own goroutine, and before claimFlight —
// sleeping on the dispatch goroutine would stall every other rule behind one runaway, and holding
// the claim would block the very firing that supersedes this one.
func (e *Engine) pauseFor(ctx context.Context, subj entity.Ref, d time.Duration) bool {
	broke := make(chan struct{})
	e.pacingMu.Lock()
	if e.pacing == nil {
		e.pacing = map[entity.Ref][]chan struct{}{}
	}
	e.pacing[subj] = append(e.pacing[subj], broke)
	e.pacingMu.Unlock()
	defer func() {
		e.pacingMu.Lock()
		defer e.pacingMu.Unlock()
		rest := e.pacing[subj][:0]
		for _, c := range e.pacing[subj] {
			if c != broke {
				rest = append(rest, c)
			}
		}
		if len(rest) == 0 {
			delete(e.pacing, subj) // the map holds only what is waiting, so it stays small
		} else {
			e.pacing[subj] = rest
		}
	}()

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-broke:
		return false
	case <-ctx.Done():
		return false
	}
}

// subjectMoved tells any paced firing about this entity that its subject was written. Called
// from dispatch for every change, so it must stay a map lookup on a map that holds only the
// firings currently waiting — which is at most one per (trigger, subject) mid-trip.
func (e *Engine) subjectMoved(ref entity.Ref) {
	e.pacingMu.Lock()
	waiters := e.pacing[ref]
	delete(e.pacing, ref)
	e.pacingMu.Unlock()
	for _, c := range waiters {
		close(c)
	}
}

// tripCount reports how many firings have been refused for running past the bound.
func (e *Engine) tripCount() int {
	e.chainMu.Lock()
	defer e.chainMu.Unlock()
	return e.trips
}

// depthOf reports how far a commit stands from the external write that started its chain: 0 for
// a commit this engine did not cause, 1 for one written by a firing that an external write woke,
// and so on.
//
// The zero answer is load-bearing rather than a fallback. A commit absent from the map IS an
// external cause as far as this engine is concerned, and the firings it wakes should start a
// fresh chain — which is the correct reading of a restart, of a peer's write, and of a write
// this engine made before an eviction dropped it.
func (e *Engine) depthOf(rev int64) int {
	e.chainMu.Lock()
	defer e.chainMu.Unlock()
	return e.causes[rev].depth
}

// causeOf returns a commit's place in its chain, and whether this engine caused it at all. It is
// what a walk back over parents reads; depthOf is the same lookup for the caller that only wants
// the number.
func (e *Engine) causeOf(rev int64) (link, bool) {
	e.chainMu.Lock()
	defer e.chainMu.Unlock()
	l, ok := e.causes[rev]
	return l, ok
}

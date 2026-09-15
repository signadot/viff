package trigger

import (
	"context"
	"strings"
	"sync"

	"github.com/signadot/verse/entity"
)

// evaluate is THE firing rule, and the only one:
//
//	at commit C, if `on` is vacuous or ¬on(C-1) ∧ on(C), and `when` holds at C,
//	fire once for every entity the subject selects at C.
//
// Three things distinguish it from the per-change dispatch it replaced, and all three come
// from the rule rather than from the implementation.
//
// It is asked ONCE PER COMMIT, not once per changed entity. `on` and `when` are predicates
// over the whole store, so "does this hold" is a question about the commit; asking it per
// entity would answer it once for each and fire that many times.
//
// It reads ONE SNAPSHOT PAIR. The guard and the selection both come from store@C, so a firing
// cannot act on a member under a condition that stopped holding between two reads; and the
// edge is C-1 against C rather than "what I saw last time", so a missed or replayed commit
// re-derives the same answer instead of depending on what this engine happened to observe.
//
// It fires per SUBJECT MEMBER. One commit can put N firings in flight, each about one entity,
// which is what makes a firing's identity — its spawn id, its gate question, its motion — a
// function of the subject.
//
// A VACUOUS `on` is the ordinary case, and it means the trigger is considered at every commit
// it reads anything of. What stops it acting forever is not the dispatcher but the charter: an
// action whose write takes its subject out of the selection fires once and then selects
// nothing, and one that writes the same value again produces no commit and so raises no next
// evaluation. Latch, or run at every commit — see the install-time lint in AddTrigger.
// subjectAddr is a subject's ADDRESS for a trace line — without the predicate, which
// subjectText appends and which is a whole charter paragraph once a rule has comments in it.
// A trace says which rule and where; the predicate is in `verse c rules <name>`.
func subjectAddr(m Match) string {
	if s, ok := m.address(); ok {
		return s
	}
	if m.Deep() {
		return strings.Join(m.Segs(), ".")
	}
	return subjectText(m)
}

func (e *Engine) evaluate(ctx context.Context, t Trigger, c entity.Commit) {
	if c.Rev <= 0 {
		return // a synthesized commit with no revision cannot be asked "and as of when?"
	}
	// A RULE SUSPENDED WHOLE is skipped BEFORE its watermark moves, so the replay a lift opens
	// at the head is admitted whether or not the lift wrote a commit. Placed after, the skip
	// would burn the revision, and a re-arm whose seed lands at that same head — a scope's rules
	// armed before its table loaded, a rule installed during a restore — would be refused and
	// never evaluate its backlog. A suspension of some subjects is asked per subject below.
	if held := e.holdingRule(t.Name); len(held) > 0 {
		e.tracef("%s: suspended (%s); not evaluated at rev %d", t.Name, strings.Join(held, ", "), c.Rev)
		return
	}
	// One verdict per (trigger, commit). A trigger reading several subtrees is delivered the
	// same commit once per subtree it watches, and the rule is a function of the commit — so
	// without this it would reach the same verdict twice and fire twice for it.
	if !e.firstSight(t.Name, c.Rev) {
		return
	}
	// Scoped to what this rule reads. This bounds what crosses the wire and does NOT make the
	// read cheaper: a read costs the log replay to that commit whatever path it names (see
	// LogdStore.AsOf). The cost of evaluating per commit is the read itself.
	reads := t.reads()
	// AS THE RULE'S OWNER: the snapshot holds what the owner may read and nothing else, so a
	// condition over what they may not read does not hold, and a subject they may not read is
	// not selected.
	store := e.reader(t.Name)
	now, err := store.AsOf(c.Rev, paths(reads)...)
	if err != nil {
		e.tracef("%s: cannot read the verse at rev %d (%v); not firing", t.Name, c.Rev, err)
		return
	}
	if t.Condition.On != nil {
		if ok, _ := t.Condition.On.Eval(now); !ok {
			return // the cause does not hold here, so there is no edge to have risen
		}
		// C-1 rather than "the value I last saw": logd revisions are monotonic but sparse, so
		// this is the state as of the commit before C whether or not C-1 is itself a commit.
		// Deriving the edge from the store instead of from engine memory is what makes a
		// replayed commit (a catch-up, a restart) reach the same verdict as the live one.
		before, err := store.AsOf(c.Rev-1, paths(reads)...)
		if err != nil {
			e.tracef("%s: cannot read the verse at rev %d (%v); not firing", t.Name, c.Rev-1, err)
			return
		}
		if ok, _ := t.Condition.On.Eval(before); ok {
			return // it already held: a level, not an edge
		}
	}

	if t.Condition.When != nil {
		if ok, _ := t.Condition.When.Eval(now); !ok {
			return // the condition does not hold at this commit
		}
	}
	// A condition that reads another entity is answered HERE, from the same snapshot the rest
	// of the condition was evaluated against, so the selection and the guard are decisions about
	// one world. Ordinary conditions carry no read and this costs a nil check. See condread.go.
	subject, err := e.conditionMatch(t.Condition.Subject, now)
	if err != nil {
		e.tracef("%s: subject read failed (%v); not firing", t.Name, err)
		return
	}
	subjects := subject.Select(now)
	if len(subjects) == 0 {
		// SAID, NOT SKIPPED. "Selected nothing" is the ordinary answer at most commits and is
		// also what a rule that never fires looks like from outside, and the two were
		// indistinguishable: the trace showed a firing and every failure PAST selection, and
		// went quiet exactly where a charter author's question is. Debug-level, so it costs a
		// level check when nobody is listening.
		e.tracef("%s selected nothing at rev %d (subject %s)", t.Name, c.Rev,
			subjectAddr(t.Condition.Subject))
		return
	}
	e.tracef("fire %s at rev %d (%d subject(s))", t.Name, c.Rev, len(subjects))
	// The commit stands in for the change an older firing carried: it names WHERE this
	// happened, which is what a gate's reason and a trace read. There is no cause entity to
	// hand on — that is the point of the rule — so the action's references read the subject.
	//
	// The commit is also what the cycle throttle reads, per subject — see fireSubjects.
	e.fireSubjects(ctx, t, c, subjects)
}

// firstSight reports whether this is the first time the trigger has been evaluated at rev, and
// records it. A trigger reading several subtrees is delivered one commit once per subtree it
// watches; the rule is a function of the commit, so the second delivery would reach the same
// verdict and fire again for it.
//
// The map is bounded by keeping only the highest rev seen per trigger: deliveries of one
// commit arrive together, and a lower rev after a higher one is a replay whose verdict has
// already been reached. That makes this a watermark rather than a set, so it cannot grow.
func (e *Engine) firstSight(name string, rev int64) bool {
	e.seenMu.Lock()
	defer e.seenMu.Unlock()
	if e.seen == nil {
		e.seen = map[string]int64{}
	}
	if rev <= e.seen[name] {
		return false
	}
	e.seen[name] = rev
	return true
}

// reads returns the (system, kind) subtrees a trigger's condition reads: its cause, its
// condition, and the set it acts on. It is both what a scoped engine watches and what the
// commit skip is asked about — the same list, because a trigger's verdict can only change when
// something it reads does.
func (t Trigger) reads() []Subtree {
	seen := map[Subtree]bool{}
	var out []Subtree
	add := func(sts []Subtree) {
		for _, st := range sts {
			if !seen[st] {
				seen[st] = true
				out = append(out, st)
			}
		}
	}
	add(t.Condition.On.Subtrees())
	add(t.Condition.When.Subtrees())
	add(matchFormula(t.Condition.Subject).Subtrees())
	// What the SUBJECT's own predicate reads, which is a read like any other for this purpose.
	// `touches` skips a commit on the argument that a trigger whose reads did not change reaches
	// the same verdict — and a condition comparing against another entity's field reaches a
	// different one the moment that field moves. Leaving it out would make the skip a lie in
	// exactly the case the feature exists for: main moving is what makes a proposal stale.
	add(conditionReads(t.Condition.Subject))
	return out
}

// touches reports whether a commit changed anything in the subtrees a trigger reads.
//
// This is the skip, and it is exact rather than a heuristic. If nothing the trigger reads
// changed, then on(C) = on(C-1) — no edge — and for a vacuous `on` the evaluation would read
// the same predicates over the same state and produce the same writes as the last one, which
// being the same values produce no commit (docs/change-events.md, edge-on-real-change). Either
// way the outcome is indistinguishable from not running, which is what makes per-commit
// evaluation affordable: a trigger costs nothing at a commit that cannot change its answer.
func (t Trigger) touches(c entity.Commit) bool {
	reads := t.reads()
	for _, ev := range c.Changes {
		for _, st := range reads {
			// Segment by segment against the address's own, so the two halves of the verse are
			// told apart here as everywhere else: a rule about `demo:pr:*` is not woken by the
			// task record of the run reviewing it.
			//
			// And a place ABOVE the subtree: a scalar written at `demo`, or `demo` removed,
			// replaces everything a rule about `demo.pr.*` reads though no address under it is
			// named, so the rule's answer can change there too.
			if st.covers(ev.Ref) || st.within(ev.Ref) {
				return true
			}
		}
	}
	return false
}

// working reports whether this trigger already has a live task about this subject.
//
// It completes the firing guard, which is otherwise too short-lived: claimFlight is held for
// the duration of the firing FUNCTION, but a firing that spawns hands off work that outlives
// it — the function returns as soon as the task starts. Every commit meanwhile re-selects
// the subject, because the write that would release it is what the task has not finished
// doing yet.
//
// Nothing new is prevented by this: spawnHooked already refuses to start a second task
// under an id that is live, and the id is a pure function of (chain, subject). What changes is
// WHEN the refusal happens — before the gates rather than after them, so a firing for work
// already under way does not put a question on the docket, spend budget, or re-apply a patch
// on the way to being told no.
//
// It asks about the id the top-level action's spawn WOULD use, which is the same id
// spawnLabel publishes on the gate. A sequence is not asked about here: it keeps its own
// durable run claim (startSeq), which is the same idea one level down.
func (e *Engine) working(t Trigger, subj entity.Ref) bool {
	if t.Action.Spawn == nil {
		return false // nothing it starts could still be running
	}
	id := spawnID(e.idOwner(), chainID(t.Name), subj)
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	_, live := e.live[id]
	return live
}

// flight is one (trigger, subject) firing under way, and the questions it has in front of people
// right now. A suspension that lands withdraws them (withdraw), and the firing, still holding its
// flight, waits for the lift and asks again.
type flight struct {
	rule string
	subj entity.Ref

	mu    sync.Mutex
	poses map[int]context.CancelCauseFunc
	next  int
	// withdrawn is the suspensions its question stands withdrawn by, until it is asked again.
	withdrawn []string
}

// setWithdrawn records what the firing's question stands withdrawn by; nil once asked again.
func (f *flight) setWithdrawn(ids []string) {
	f.mu.Lock()
	f.withdrawn = ids
	f.mu.Unlock()
}

// posing registers the cancel of one question being asked, until done is called.
func (f *flight) posing(cancel context.CancelCauseFunc) (done func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.poses == nil {
		f.poses = map[int]context.CancelCauseFunc{}
	}
	n := f.next
	f.next++
	f.poses[n] = cancel
	return func() {
		f.mu.Lock()
		delete(f.poses, n)
		f.mu.Unlock()
	}
}

// withdraw takes every question the firing is asking off the docket, with cause as the reason.
func (f *flight) withdraw(cause error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cancel := range f.poses {
		cancel(cause)
	}
}

// claimFlight reserves a (trigger, subject) firing, handing back its flight when it was free.
// release gives it back when the firing ends.
func (e *Engine) claimFlight(name string, subj entity.Ref) (*flight, bool) {
	key := name + "\x00" + subj.String()
	e.flightMu.Lock()
	defer e.flightMu.Unlock()
	if e.inflight == nil {
		e.inflight = map[string]*flight{}
	}
	if e.inflight[key] != nil {
		return nil, false
	}
	f := &flight{rule: name, subj: subj}
	e.inflight[key] = f
	return f, true
}

// flights is every firing under way.
func (e *Engine) flights() []*flight {
	e.flightMu.Lock()
	defer e.flightMu.Unlock()
	out := make([]*flight, 0, len(e.inflight))
	for _, f := range e.inflight {
		out = append(out, f)
	}
	return out
}

func (e *Engine) releaseFlight(name string, subj entity.Ref) {
	e.flightMu.Lock()
	delete(e.inflight, name+"\x00"+subj.String())
	e.flightMu.Unlock()
}

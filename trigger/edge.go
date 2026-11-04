package trigger

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
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
	if segs := m.Segs(); len(segs) > 0 {
		return strings.Join(segs, ".")
	}
	return subjectText(m)
}

func (e *Engine) evaluate(ctx context.Context, t Trigger, c entity.Commit) {
	if c.Rev <= 0 {
		return // a synthesized commit with no revision cannot be asked "and as of when?"
	}
	// A RULE SUSPENDED WHOLE is skipped BEFORE firstSight records the commit, so the replay a
	// lift opens at the head is admitted whether or not the lift wrote a commit. Placed after,
	// the skip would burn the revision, and a re-arm whose seed lands at that same head — a
	// scope's rules armed before its table loaded, a rule installed during a restore — would be
	// refused and never evaluate its backlog. A suspension of some subjects is asked per subject below.
	if held := e.holdingRule(t.Name); len(held) > 0 {
		e.tracef("%s: suspended (%s); not evaluated at rev %d", t.Name, strings.Join(held, ", "), c.Rev)
		return
	}
	// One verdict per (trigger, commit). A trigger reading several subtrees is delivered the
	// same commit once per subtree it watches, and the rule is a function of the commit — so
	// without this it would reach the same verdict twice and fire twice for it.
	first, late := e.firstSight(t.Name, c.Rev)
	if !first {
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
		// NOT EVALUATED, AND NOT LOOKED AT AGAIN: the commit is marked seen above. It is
		// kept on the rule so that its author, and whoever counts how often the store does
		// not answer, can see a commit went by.
		e.unread(t.Name, c.Rev, err)
		return
	}
	if e.onlyOwnBookkeeping(t, c, store, paths(reads), now) {
		e.tracef("%s: rev %d changed only its own bookkeeping; not a cause", t.Name, c.Rev)
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
			e.unread(t.Name, c.Rev, fmt.Errorf("reading the commit before it, rev %d: %w", c.Rev-1, err))
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
		e.unread(t.Name, c.Rev, fmt.Errorf("reading what its condition reads: %w", err))
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
	if late {
		// A LATE COMMIT IS ANSWERED AS OF NOW. It arrived after a newer one this rule was
		// evaluated at, on another of its watches, so its snapshot is older than what the rule
		// has already acted on: a latch written since stands in the store and not in it. Each
		// subject it selects fires only if the rule still selects it, which is the check
		// endFlight makes of a change deferred behind a firing, and for the same reason.
		subjects = slices.DeleteFunc(subjects, func(subj entity.Ref) bool { return !e.selectsNow(t, subj) })
		if len(subjects) == 0 {
			e.tracef("%s: late commit %d selects nothing now", t.Name, c.Rev)
			return
		}
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
// late says rev is below a commit the trigger has already been evaluated at. That is not a
// replay. Each subtree a rule reads is its own watch, and the watches do not order their
// deliveries against each other, so a commit to what the condition reads can arrive after the
// rule's own write to its subject. That write, landing during the firing that made it, is
// dropped (claimOrDefer); the earlier commit is then the only news the rule will get. The
// caller answers a late commit against the store as it is now (see evaluate).
//
// What is remembered is bounded: the revisions within sightWindow of the newest. A delivery
// older than that is taken for a replay.
func (e *Engine) firstSight(name string, rev int64) (first, late bool) {
	e.seenMu.Lock()
	defer e.seenMu.Unlock()
	if e.seen == nil {
		e.seen = map[string]*sight{}
	}
	s := e.seen[name]
	if s == nil {
		s = &sight{at: map[int64]bool{}}
		e.seen[name] = s
	}
	if s.at[rev] || rev <= s.high-sightWindow {
		return false, false
	}
	s.at[rev] = true
	if rev < s.high {
		return true, true
	}
	s.high = rev
	for r := range s.at {
		if r <= s.high-sightWindow {
			delete(s.at, r)
		}
	}
	return true, false
}

// sightWindow is how far below the newest commit a trigger has been evaluated at it still tells
// a late commit from a replay. Revisions are the store's, and sparse, so it is a span of the
// log rather than a count of commits: wide enough for any delivery one watch can trail another
// by, and the memory it costs is the commits a rule was evaluated at within it.
const sightWindow = 4096

// sight is the commits one trigger has been evaluated at: the newest, and those within
// sightWindow of it.
type sight struct {
	high int64
	at   map[int64]bool
}

// forgetFrom unremembers every commit at or after rev, so a replay of them is evaluated again.
func (s *sight) forgetFrom(rev int64) {
	for r := range s.at {
		if r >= rev {
			delete(s.at, r)
		}
	}
	if s.high >= rev {
		s.high = rev - 1
	}
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
	for _, st := range t.reads() {
		// ASKED AS A PATH, which is what the commit can answer exactly: a write at the subtree,
		// beneath it, or one that replaced or removed a place ABOVE it — a scalar written at
		// `demo`, or `demo` removed, replaces everything a rule about `demo.pr.*` reads though
		// no address under it is named. A rule about `demo.pr.*` is still not woken by the task
		// record of the run reviewing it: the segments are compared as segments.
		if c.Touches(st.pattern()) {
			return true
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

// heldForHook is working as the store knows it: whether a task of the top-level action's spawn
// about subj holds the subject until its completion hook has run — the claim applyOne takes and
// applyHook releases. Asked where working is, before the gates, and answered from the store
// rather than the live registry, so it holds across a restart and through the moment between a
// task ending and its hook writing, where the registry has already let the id go. The claim
// itself settles a race; this spares a question on the docket for a firing it would refuse.
func (e *Engine) heldForHook(t Trigger, subj entity.Ref) bool {
	if !t.Action.hooked() {
		return false
	}
	_, held, err := e.store.Get(mirror.ClaimRef(spawnID(e.idOwner(), chainID(t.Name), subj)))
	return err == nil && held
}

// heldOff reports whether a hold refuses a firing of t about subj — a task of the rule's spawn
// running (working), or standing between its end and its hook's write (heldForHook) — and
// records the refusal where the hold's end takes it (released), for reconsider.
//
// A refused firing is a commit that touched what the rule reads while a task of its spawn held
// the subject — a change somebody made meanwhile, or the hook's own write, whose firing can
// arrive before the claim's release has landed. Dropped, that change would wait for the next
// commit to touch the subject, which may never come. In memory only: a restart evaluates every
// rule against what stands when it arms, which covers what a hold refused before it.
//
// CHECKED AND RECORDED UNDER ONE LOCK, the one released holds across its release and its take:
// otherwise a release could land between this check seeing the hold and the record, and the
// record would stand with nobody to take it. Recorded for a hooked spawn only: an unhooked
// spawn's end takes nothing, so a record there would stand for ever and mean nothing.
func (e *Engine) heldOff(t Trigger, subj entity.Ref) bool {
	e.refusedMu.Lock()
	defer e.refusedMu.Unlock()
	held := false
	if e.working(t, subj) {
		e.tracef("%s: %s already has work running; not firing again", t.Name, subj)
		held = true
	} else if e.heldForHook(t, subj) {
		e.tracef("%s: a task of its spawn holds %s until its hook has run; not firing", t.Name, subj)
		held = true
	}
	if held && t.Action.hooked() {
		if e.refused == nil {
			e.refused = map[string]bool{}
		}
		e.refused[t.Name+"\x00"+subj.String()] = true
	}
	return held
}

// released drops a hooked spawn's claim on subj and, where the claim is the rule's own (its
// top-level action's, the one firings are refused for), reports whether the hold refused a firing
// of the rule about subj meanwhile, which the caller then owes the subject (reconsider). Under
// the lock heldOff checks and records under, so no refusal slips between the two.
//
// A hook's own hooked spawn is held at a chain of its own, and its end takes nothing: the record
// is the rule's, and a nested task ending while a second task of the rule runs would otherwise
// wipe what that task's end is to take.
func (e *Engine) released(chain string, subj entity.Ref, own bool) bool {
	e.refusedMu.Lock()
	defer e.refusedMu.Unlock()
	e.releaseRun(chain, subj)
	if !own {
		return false
	}
	key := chain + "\x00" + subj.String()
	was := e.refused[key]
	delete(e.refused, key)
	return was
}

// reconsider fires a rule about a subject it still selects, at the head: what a hold owes the
// subject once it is off, for the firings it refused (heldOff). Nothing where the rule
// no longer selects it — the hook's own patch is usually what took it out — and nothing where
// no firing was refused, so a hook that writes nothing does not fire its rule again by itself:
// a rule whose done path does not latch would otherwise run without end.
func (e *Engine) reconsider(name string, subj entity.Ref) {
	t, ok := e.triggerNamed(name)
	if !ok || !e.selectsNow(t, subj) {
		return
	}
	ctx := e.bgctx
	if ctx == nil {
		ctx = context.Background()
	}
	oc := occasion{commit: entity.Commit{Rev: e.store.Head()}, snap: &snapCache{},
		within: firingWithin(t), reader: e.reader(t.Name)}
	e.tracef("%s: %s was held and has changed; firing at rev %d", t.Name, subj, oc.commit.Rev)
	go e.fireOne(ctx, t, oc, subj)
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
	// again is the latest firing about this subject that arrived while this one ran and was woken
	// by a change to something ELSE (a `when`, a condition read). It is fired when this
	// one ends, if the rule still selects the subject then (endFlight). nil is none.
	again *occasion
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
	return e.claimOrDefer(name, subj, nil)
}

// claimOrDefer is claimFlight that, finding the subject already firing, keeps oc to fire when
// that firing ends, where oc was woken by a change to something other than the subject.
//
// A FIRING ARRIVING DURING ANOTHER ABOUT ITS SUBJECT USED TO BE DROPPED, on the argument that the
// running one's write re-selects the subject and the next commit asks again. That holds for a
// change to the subject, which the running firing is usually the author of. It does not hold for
// a change elsewhere — a `when`, or what a condition reads — since nothing commits after it and
// the rule never sees it (git-issue 8z4azhtjh12krmmqqsn0).
//
// The subject's own changes are still dropped. Firing again on them would, for a gated rule,
// ask its question a second time about the write its own answer caused.
func (e *Engine) claimOrDefer(name string, subj entity.Ref, oc *occasion) (*flight, bool) {
	key := name + "\x00" + subj.String()
	e.flightMu.Lock()
	defer e.flightMu.Unlock()
	if e.inflight == nil {
		e.inflight = map[string]*flight{}
	}
	if f := e.inflight[key]; f != nil {
		if oc != nil && !changedIn(oc.commit, subj) {
			f.mu.Lock()
			if f.again == nil || f.again.commit.Rev < oc.commit.Rev {
				later := *oc
				f.again = &later
			}
			f.mu.Unlock()
		}
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

func (e *Engine) releaseFlight(name string, subj entity.Ref) *occasion {
	key := name + "\x00" + subj.String()
	e.flightMu.Lock()
	f := e.inflight[key]
	delete(e.inflight, key)
	e.flightMu.Unlock()
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.again
}

// endFlight releases a firing and fires what arrived during it (claimOrDefer), with the rule as
// it now stands and only if it still selects the subject — checked against the store as it is
// now, so a firing whose own write moved the subject out of the selection is not followed by one
// about the world before it.
func (e *Engine) endFlight(ctx context.Context, name string, subj entity.Ref) {
	again := e.releaseFlight(name, subj)
	if again == nil || ctx.Err() != nil {
		return
	}
	t, ok := e.triggerNamed(name)
	if !ok || !e.selectsNow(t, subj) {
		return
	}
	go e.fireOne(ctx, t, *again, subj)
}

package trigger

import (
	"context"

	"github.com/signadot/verse/entity"
)

// dispatchEdge is the firing rule for a trigger carrying an `on`:
//
//	at commit C, if ¬on(C-1) ∧ on(C), and when holds at C,
//	fire once for every entity the subject selects at C.
//
// Three things distinguish it from the older per-change dispatch, and all three come from the
// rule rather than from the implementation.
//
// It is asked ONCE PER COMMIT, not once per changed entity. `on` is a predicate over the whole
// store, so "did it change" is a question about the commit — asking it per entity would answer
// it once for each and fire that many times.
//
// It reads ONE SNAPSHOT PAIR. The guard and the selection both come from store@C, so a firing
// cannot act on a member under a condition that stopped holding between two reads; and the
// edge is C-1 against C rather than "what I saw last time", so a missed or replayed commit
// re-derives the same answer instead of depending on what this engine happened to observe.
//
// It fires per SUBJECT MEMBER. One commit can put N firings in flight, each about one entity,
// which is what makes a firing's identity — its spawn id, its gate question, its motion — a
// function of the subject.
func (e *Engine) dispatchEdge(ctx context.Context, t Trigger, c entity.Commit) {
	if c.Rev <= 0 {
		return // a synthesized commit with no revision cannot be asked "and before?"
	}
	now, err := e.store.AsOf(c.Rev)
	if err != nil {
		e.tracef("edge %s: cannot read the verse at rev %d (%v); not firing", t.Name, c.Rev, err)
		return
	}
	if ok, _ := t.Condition.On.Eval(now); !ok {
		return // the cause does not hold here, so there is no edge to have risen
	}
	// C-1 rather than "the value I last saw": logd revisions are monotonic but sparse, so this
	// is the state as of the commit before C whether or not C-1 is itself a commit. Deriving
	// the edge from the store instead of from engine memory is what makes a replayed commit
	// (a catch-up, a restart) reach the same verdict as the live one.
	before, err := e.store.AsOf(c.Rev - 1)
	if err != nil {
		e.tracef("edge %s: cannot read the verse at rev %d (%v); not firing", t.Name, c.Rev-1, err)
		return
	}
	if ok, _ := t.Condition.On.Eval(before); ok {
		return // it already held: a level, not an edge
	}

	if t.Condition.When != nil {
		if ok, _ := t.Condition.When.Eval(now); !ok {
			e.tracef("edge %s: cause rose at %d but the condition does not hold; not firing",
				t.Name, c.Rev)
			return
		}
	}
	subjects := t.Condition.Subject.Select(now)
	if len(subjects) == 0 {
		e.tracef("edge %s: cause rose at %d and the subject selects nothing; not firing",
			t.Name, c.Rev)
		return
	}
	if e.overBudget() {
		e.tracef("budget exhausted (%d); not firing %s at rev %d", e.maxFires, t.Name, c.Rev)
		return
	}
	e.tracef("edge %s rose at rev %d (%d subject(s))", t.Name, c.Rev, len(subjects))
	// The commit stands in for the change an older firing carried: it names WHERE this
	// happened, which is what a gate's reason and a trace read. There is no cause entity to
	// hand on — that is the point of the rule — so the action's references read the subject.
	go e.fireSubjects(ctx, t, entity.Event{Rev: c.Rev}, subjects)
}

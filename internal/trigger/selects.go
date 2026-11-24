package trigger

import (
	"slices"

	"go.viff.dev/viff/pkg/entity"
)

// Whether a rule's subject predicate picks an entity out of the viff instance as it stands: asked of a
// gate's subject before its firing is resumed, and of a firing about to happen.

// stillSelects reports whether a trigger's subject predicate still picks this entity out of the
// viff as it stands — the same question the dispatcher asks per commit, asked once at startup
// against the current revision.
//
// It answers TRUE when it cannot tell (an unreadable store, a predicate whose read fails).
// Leaving a question standing costs a stale docket entry; settling one wrongly takes a live
// question away from the people it is in front of, and this is not the code that should be
// deciding that on a guess.
//
// It asks about the SUBJECT alone, not the cause: a rule fired by an `on` edge may still select
// its subject and never see that edge again. So a question this keeps is not a promise that
// the rule will re-fire — only that nothing about the subject says it cannot.
func (e *Engine) stillSelects(t Trigger, subj entity.Ref) bool {
	// HEAD, not Rev, and this is the one call that makes a resume expensive. Rev SYNCS — it
	// opens a watch on the root and is seeded with a read — and this runs once per question, so
	// a viff instance coming back with a docket would pay that per question: the session budget, so a
	// timeout rather than work.
	//
	// Head is the session's known commit (libctl's mark, raised by every answer that carries
	// one), so it costs nothing and is the store's head rather than the last CHANGED commit —
	// which is what "as of now" means here. It can lag an idle store by a heartbeat; a resume
	// is not that, and this function already errs toward keeping a question when it cannot tell.
	now, err := e.reader(t.Name).AsOf(e.store.Head(), paths(t.reads())...)
	if err != nil {
		return true
	}
	m, err := e.conditionMatch(t.Condition.Subject, now)
	if err != nil {
		return true
	}
	for _, r := range m.Select(now) {
		if r == subj {
			return true
		}
	}
	return false
}

// selectsNow is stillSelects for a firing about to happen: whether the rule's `when` holds and its
// subject picks this entity out of the viff instance as it stands. It answers FALSE when it cannot tell,
// where stillSelects answers true: here the cost of a wrong yes is an action applied, and the
// next commit that touches what the rule reads asks again.
func (e *Engine) selectsNow(t Trigger, subj entity.Ref) bool {
	now, err := e.reader(t.Name).AsOf(e.store.Head(), paths(t.reads())...)
	if err != nil {
		return false
	}
	if t.Condition.When != nil {
		if ok, _ := t.Condition.When.Eval(now); !ok {
			return false
		}
	}
	m, err := e.conditionMatch(t.Condition.Subject, now)
	if err != nil {
		return false
	}
	return slices.Contains(m.Select(now), subj)
}

// Knows reports whether this engine's charter carries a rule by this name. It is knowsTrigger
// under a name another package can say, for the callers that ask an engine about a rule it
// may not hold: viffd, over the engines it runs.
func (e *Engine) Knows(name string) bool { return e.knowsTrigger(name) }

// knowsTrigger reports whether the charter still carries a rule by this name — installed, or
// HELD for a stratum that has not arrived. A held rule installs the moment its stratum does.
func (e *Engine) knowsTrigger(name string) bool {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	for _, t := range e.triggers {
		if t.Name == name {
			return true
		}
	}
	for _, t := range e.pending {
		if t.Name == name {
			return true
		}
	}
	return false
}

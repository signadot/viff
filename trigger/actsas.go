package trigger

import (
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
)

// ActsAsSubjectOver answers the place this rule selects its subjects in, where any of its
// actions spawns `as: subject` — a task that acts on the subject owner's authority — and false
// where none does.
//
// The place is the subject's literal prefix: the settled part of the address, the last segment
// left out, as a stratum's place is (literalPrefix). The installer has to own it. A rule's task
// acting as whoever wrote a record is safe only where writing that record is writing into the
// charter's own place; a rule over somebody else's space (`spaces.victor.*`) would otherwise
// run a task of its author's design as Victor.
func (t Trigger) ActsAsSubjectOver() (entity.Ref, bool) {
	if !t.spawnsAsSubject() {
		return entity.Ref{}, false
	}
	segs := t.Condition.Subject.SegNodes()
	if len(segs) == 0 {
		return entity.RefAt(), true
	}
	var place []string
	for _, n := range literalPrefix(segs) {
		s, _ := entity.PathLiteral(n)
		place = append(place, s)
	}
	return entity.RefAt(place...), true
}

func (t Trigger) spawnsAsSubject() bool {
	if asSubject(&t.Action) {
		return true
	}
	gates := t.Gates
	if t.Gating != nil {
		gates = append(append([]Gate(nil), gates...), t.Gating.Gates...)
	}
	for _, g := range gates {
		if asSubject(g.OnDenied) {
			return true
		}
	}
	return false
}

// asSubject says whether an action, or anything it runs after, spawns `as: subject`.
func asSubject(a *Action) bool {
	if a == nil {
		return false
	}
	if a.Spawn != nil && a.Spawn.As == launch.AsSubject {
		return true
	}
	for i := range a.Seq {
		if asSubject(&a.Seq[i]) {
			return true
		}
	}
	for _, h := range []*Action{a.OnDone, a.OnFailed, a.OnCancelled, a.OnOrphaned} {
		if asSubject(h) {
			return true
		}
	}
	return false
}

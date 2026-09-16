package trigger

import (
	"fmt"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
)

// A CONDITION may read another entity, and this is what makes that mean something.
//
// `!get-path` and `!list-paths` already reach an entity the firing is not about — but only from
// an ACTION, which is where a value gets copied. A condition could NAME another entity (`when`
// and `on` are matches over the whole verse) and could not compare against one: a match tests a
// field against a literal or an operator, so "this field differs from that entity's field" was
// unsayable. The predicate that wants it is not exotic. It is the ordinary shape of one thing
// being stale with respect to another:
//
//	condition:
//	  subject:
//	    system: merge
//	    kind:   proposal
//	    value: !let
//	      let: [{tip: !get-path {at: "git.verse.head", path: sha}}]
//	      in:  {state: open, base: !not .[tip]}
//
// — every open proposal cut from a commit that is no longer the tip of main, which is exactly
// "which of these branches are stale" with no git, no ancestry walk and no task
// (docs/working/merge-gate-residual.md).
//
// `!let` is tony's, `!get-path` is verse's, and neither is new. What is new is that the read is
// answered before the predicate is matched, so the binding holds a value from the store rather
// than a constant somebody had to know when they wrote the rule.
//
// TWO THINGS ARE DIFFERENT HERE FROM A READ IN AN ACTION, and both follow from a condition being
// a question rather than a consequence:
//
//   - IT JOINS THE READ SET. An action's read copies and does not track (see path.go): the value
//     is taken at the firing and nothing re-runs when it changes. A condition's read decides
//     WHETHER the rule fires and over WHAT, so a change to it changes the answer — and `touches`
//     skips a commit on the argument that a trigger whose reads did not change would reach the
//     same verdict. That argument is only true if this counts as a read. See Trigger.reads.
//   - IT CANNOT NAME $subject. In an action the subject is settled and its fields are the useful
//     thing to address by; in a condition the subject is what is being SELECTED, so there is no
//     one entity to be about yet. Refused by name below rather than resolving to nothing.
func (e *Engine) conditionMatch(m Match, snap entity.Snapshot) (Match, error) {
	pred := m.Predicate()
	if pred == nil {
		return m, nil
	}
	specs, err := readTargets(pred)
	if err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return m, nil // the ordinary case: no read, no snapshot, nothing to do
	}
	expanded, err := expandPatch(pred, conditionRoot(), readerAt(snap))
	if err != nil {
		return nil, err
	}
	out := make(Match, len(m))
	for k, v := range m {
		out[k] = v
	}
	out[mKeyValue] = expanded
	return out, nil
}

// readerAt binds a snapshot into the reader the expander calls.
func readerAt(snap entity.Snapshot) readFn {
	return func(node *ir.Node, root entity.Payload) (entity.Payload, error) {
		return resolveReadAt(node, root, snap)
	}
}

// conditionRoot is what a condition's read may address by reference: nothing. It is an empty
// record rather than the subject's, so `$subject` in a condition fails as an unknown reference —
// see checkConditionReads, which says why in the rule's own terms at install.
func conditionRoot() entity.Payload { return ir.FromMap(map[string]*ir.Node{}) }

// conditionReads is the subtrees a condition's own reads touch, for Trigger.reads.
//
// System and kind are always literal in a path read — path.go turns the level check on that —
// so the subtree is knowable without firing anything, which is what lets a read participate in
// the skip rather than defeating it.
func conditionReads(m Match) []Subtree {
	specs, err := readTargets(m.Predicate())
	if err != nil {
		return nil // a malformed read is refused at install; here it simply reads nothing
	}
	out := make([]Subtree, 0, len(specs))
	for _, s := range specs {
		out = append(out, SubtreeAt(s.place().Segs()...)) // the read's place, at whatever depth
	}
	return out
}

// checkConditionReads is the install-time half: a read in a condition is checked for shape like
// any other, and refused outright if it names the subject.
//
// The refusal is by NAME rather than by letting the expansion fail at fire time, because the two
// failures read differently to whoever wrote the rule. "unknown reference $subject" invites them
// to go looking for a typo; this says the thing that is actually true — a condition is what
// chooses the subject, so there is not one yet to read a field from.
func checkConditionReads(m Match) error {
	pred := m.Predicate()
	if pred == nil {
		return nil
	}
	specs, err := readTargets(pred)
	if err != nil {
		return err
	}
	for _, s := range specs {
		// EVERY SEGMENT OF THE ADDRESS, in whichever spelling it was written: `s.match().Segs()`
		// reads the `at:` vector and the three dimensions alike, so a read that named the
		// subject in an `at:` is refused the same way one that named it in an `id:` is. Asking
		// the three dimensions alone was the same question only while they were the only
		// spelling — an `at:` walked straight past this and failed at fire time instead.
		segs := append(s.match().Segs(), s.Path)
		for _, seg := range segs {
			if strings.Contains(seg, "$"+refSubject) {
				return fmt.Errorf("a condition's read cannot name $%s (%s): the subject is what "+
					"the condition selects, so the firing has none yet — address the entity "+
					"literally, or read it in the action instead", refSubject, s.target())
			}
		}
	}
	return nil
}

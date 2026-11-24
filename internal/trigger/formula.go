package trigger

import (
	"go.viff.dev/viff/pkg/entity"
)

// Formula is what `on` and `when` hold: a point-in-time Match, asked of the store's evolution
// rather than of one entity — at two revisions for a cause (`on` fires on the rising edge)
// and at one for a condition (`when` must hold at the commit). It is a match with a
// consequence, and nothing composes it over time: a gate's threshold and deadline are the
// question's (Gate.Need, Gate.Deadline), not an operator's.
//
// The shape is a single atom carrying its match, kept as a named type so a condition's
// codec and the engine's edge evaluation have one thing to hold. The temporal grammar it once
// stood at the root of — `!until`, `!quorum`, `!fact`, `!deadline` — is refused by name
// where a charter is read (Formula.FromTonyIR), with the spelling that replaced it.
//
//tony:schemagen=viff-formula,notag,codec=custom
type Formula struct {
	// Neither field is on the wire: the codec renders the match itself (tonycodec.go).
	Op  FormulaOp    `tony:"field=op"`
	Req *Requirement `tony:"field=req,omitzero"` // Op == OpAtom: the leaf requirement
}

// FormulaOp tags the node. Only OpAtom exists.
type FormulaOp string

const OpAtom FormulaOp = "atom"

// Requirement is a Formula's leaf: a Match, and how it is evaluated. Only ReqMatch exists:
// the match is an edge on a change (Formula.MatchEvent) and, as a store predicate, presence.
// Not on the wire of its own: Formula's codec renders the match.
//
//tony:schemagen=viff-requirement,notag,codec=custom
type Requirement struct {
	Kind  RequirementKind
	Facts Match // the store facts this requirement reads (a point-in-time Match)
}

// RequirementKind names how a leaf is evaluated.
type RequirementKind string

// ReqMatch is the point-in-time leaf: its Facts match is evaluated as an EDGE on a change
// (Formula.MatchEvent — the `when` face) and, as a store predicate, as presence.
const ReqMatch RequirementKind = "match"

// retiredFormulaTags is the temporal grammar a charter may still carry from before a gate
// became a question. Each is refused where it is read, naming what replaced it, so a
// charter is told rather than matched on a field literally named `quorum`.
var retiredFormulaTags = map[string]bool{
	"until": true, "eventually": true, "always": true, "sequence": true,
	"quorum": true, "fact": true, "deadline": true, "judgment": true, "ask": true, "default": true,
}

// matchFormula wraps a point-in-time Match as the atom Formula — the form a `when` takes.
func matchFormula(m Match) *Formula {
	return &Formula{Op: OpAtom, Req: &Requirement{Kind: ReqMatch, Facts: m}}
}

// MatchWhen builds a Condition.When from a point-in-time Match — the Go-side equivalent of
// writing the match object inline in a charter. For a Go caller assembling a trigger
// programmatically (tests) rather than parsing JSON.
func MatchWhen(m Match) *Formula { return matchFormula(m) }

// MatchEvent is the EDGE face of a formula: whether one observed change satisfies it.
func (f *Formula) MatchEvent(ev entity.Event) bool {
	if !f.IsMatchAtom() {
		return false
	}
	return f.Req.Facts.MatchEvent(ev)
}

// IsMatchAtom reports whether the formula is a bare point-in-time match, i.e. dispatchable
// per change event. Every well-formed formula is; a zero one is not.
func (f *Formula) IsMatchAtom() bool {
	return f != nil && f.Op == OpAtom && f.Req != nil && f.Req.Kind == ReqMatch
}

// Eval reports whether the formula currently holds against the store — a matching entity
// stands there — and the refs that stood. A match that NAMES one entity is deliberately a set
// of one whether or not it exists (an action's patch on a named subject is how a trigger
// publishes a fact nothing has written yet), so presence is asked of the store rather than
// granted by the spelling. A read that errors drops the ref: an unanswerable read is not
// evidence that a fact holds.
func (f *Formula) Eval(r entity.Reader) (bool, []entity.Ref) {
	if !f.IsMatchAtom() {
		return false, nil
	}
	var out []entity.Ref
	for _, ref := range f.Req.Facts.Select(r) {
		if _, ok, err := r.Get(ref); err == nil && ok {
			out = append(out, ref)
		}
	}
	return len(out) >= 1, out
}

// Subtrees returns the (system, kind) subtrees this formula reads — what an engine watches
// to know when to re-evaluate. Kind may be "" (a whole-system watch).
func (f *Formula) Subtrees() []Subtree {
	if !f.IsMatchAtom() {
		return nil
	}
	return []Subtree{subtreeOf(f.Req.Facts)}
}

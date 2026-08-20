package trigger

import (
	"github.com/signadot/verse/entity"
)

// Formula is the TEMPORAL grammar: matches composed with operators over the verse's
// evolution. A Trigger's Condition.When and a Gate's Formula are both
// Formulas, differing only in consequence — a satisfied When *fires*, a satisfied gate
// *proceeds*.
//
// Its leaves are Requirements, and a Requirement is A MATCH PLUS HOW IT IS EVALUATED
// (see RequirementKind): `match` is an edge on a change, `fact` is presence, `quorum` is a
// tally, `deadline` is the clock. There is exactly ONE atom carrier — Req — so "a formula's
// leaves are matches" holds literally; a second carrier would make an atom two things.
// Boolean composition (and/or) lives inside the leaf match, not as Formula nodes.
//
//tony:schemagen=verse-formula,notag,codec=custom
type Formula struct {
	Op   FormulaOp    `tony:"field=op"`
	Req  *Requirement `tony:"field=req,omitzero"`  // Op == OpAtom: the leaf requirement
	Args []*Formula   `tony:"field=args,omitzero"` // operands for temporal operators
}

// FormulaOp tags the parse node. OpAtom is a leaf requirement; the rest are temporal
// operators over the commit stream (boolean composition is inside a Requirement, not
// a Formula op). Only OpAtom is evaluated in this first pass.
type FormulaOp string

const (
	OpAtom       FormulaOp = "atom"
	OpUntil      FormulaOp = "until"      // Args[0] until Args[1]
	OpEventually FormulaOp = "eventually" // stub
	OpAlways     FormulaOp = "always"     // stub
	OpSequence   FormulaOp = "sequence"   // stub
)

// isRequirementKind reports whether a tag names a leaf requirement.
func isRequirementKind(tag string) bool {
	switch RequirementKind(tag) {
	case ReqMatch, ReqQuorum, ReqFact, ReqDeadline, ReqJudgment, ReqAsk, ReqDefault:
		return true
	}
	return false
}

// matchFormula wraps a point-in-time Match as the degenerate (atom) Formula — the form a
// `when` takes when it names entities rather than relating the verse over time.
func matchFormula(m Match) *Formula {
	return &Formula{Op: OpAtom, Req: &Requirement{Kind: ReqMatch, Facts: m}}
}

// MatchWhen builds a Condition.When from a point-in-time Match — the Go-side equivalent of
// writing the match object inline in a charter. For a Go caller assembling a trigger
// programmatically (verse connect's per-file validator, tests) rather than parsing JSON.
func MatchWhen(m Match) *Formula { return matchFormula(m) }

// MatchEvent is the EDGE face of a formula: whether one observed change satisfies it. Only a
// match atom has an edge — a temporal formula relates the verse's evolution and is resolved by
// Monitor instead, so it reports false here and is routed to that path.
func (f *Formula) MatchEvent(ev entity.Event) bool {
	if f == nil || f.Op != OpAtom || f.Req == nil || f.Req.Kind != ReqMatch {
		return false
	}
	return f.Req.Facts.MatchEvent(ev)
}

// IsMatchAtom reports whether the formula is a bare point-in-time match (no temporal
// operators), i.e. dispatchable per change event.
func (f *Formula) IsMatchAtom() bool {
	return f != nil && f.Op == OpAtom && f.Req != nil && f.Req.Kind == ReqMatch
}

// factSelFromRef parses an address into a fact-selecting Match, at whatever depth it names —
// `!fact ci.status.green` names the entity a reader thinks it names, and `!fact ci.status` the
// kind above it.
//
// IT IS ParseExpr, and it did not used to be: it had its own splitter, which stopped at two
// segments and folded everything after the first colon into the KIND. That did not fail — it
// produced a requirement selecting kind "status:green", which no entity can have, so the leaf
// was simply never satisfied. A gate that installs, round-trips, reads correctly, and waits
// forever. One reader is how that stops being possible.
//
// A malformed address selects nothing rather than erroring, which is this function's contract
// with its one caller: a requirement's ref is validated where the requirement is.
func factSelFromRef(s string) Match {
	m, err := ParseExpr(s)
	if err != nil {
		return Match{mKeySystem: s}
	}
	return m
}

// Requirement is a leaf of a Formula: a named, verse-native requirement a producer
// satisfies by writing facts. Kind names the expected producer (quorum → a vote
// surface, judgment → a judge, fact → a source, …). Only quorum is implemented in this
// first pass; the others are stubbed so the requirement/producer-binding shape is
// present from the start.
//
//tony:schemagen=verse-requirement,notag,codec=custom
type Requirement struct {
	Kind   RequirementKind `tony:"field=kind"`
	Facts  Match           `tony:"field=facts"`           // the store facts this requirement reads (a point-in-time Match)
	N      int             `tony:"field=n,omitzero"`      // quorum threshold: satisfied when ≥ N ballots match
	Choice string          `tony:"field=choice,omitzero"` // which ballot a quorum counts (default "approve"; "reject" = a deny-quorum)
	Within string          `tony:"field=within,omitzero"` // deadline duration (e.g. "4h") — a time derivation, not a store fact
}

// RequirementKind is the verse-native requirement vocabulary. The concrete surface that
// satisfies a requirement (slack/page/CI/…) is a driver bound outside the charter.
type RequirementKind string

const (
	// ReqMatch is the point-in-time leaf: its Facts match is evaluated as an EDGE on a
	// change (Formula.MatchEvent — the `when` face) and, as a store predicate, as presence.
	// It is what makes a `when` and a gate one grammar with two consequences.
	ReqMatch    RequirementKind = "match"
	ReqQuorum   RequirementKind = "quorum"
	ReqFact     RequirementKind = "fact"     // presence of a matching fact
	ReqDeadline RequirementKind = "deadline" // a duration resolved against the system clock (an `until` right side)
	ReqJudgment RequirementKind = "judgment" // stub — producer not built yet
	ReqAsk      RequirementKind = "ask"      // stub
	ReqDefault  RequirementKind = "default"  // stub
)

// Eval reports whether the formula currently holds against the store, and — when it
// holds — the witness: the fact refs whose presence satisfied it, for use as a commit
// CAS precondition. Pessimistic: a formula not satisfied now is false (blocks). Only
// the atom node is evaluated in this first pass; other ops block until implemented.
func (f *Formula) Eval(r entity.Reader) (bool, []entity.Ref) {
	if f == nil {
		return false, nil
	}
	switch f.Op {
	case OpAtom:
		if f.Req == nil {
			return false, nil
		}
		return f.Req.eval(r)
	default:
		return false, nil // temporal operators arrive in later steps
	}
}

// bind returns a copy of the formula scoped to one motion instance: a `quorum` atom's
// votes become that motion's vote log (`vote:<motionID>:*`, counting approvals), so
// concurrent motions never share a tally and each keeps its own ordered ballot log.
// Other atoms (e.g. a `fact` oracle or deadline, charter-specified) are left as-is.
func (f *Formula) bind(motionID string) *Formula {
	if f == nil {
		return nil
	}
	nf := &Formula{Op: f.Op}
	if f.Req != nil {
		r := *f.Req
		if r.Kind == ReqQuorum {
			choice := r.Choice
			if choice == "" {
				choice = "approve" // default quorum counts approvals; "reject" makes a deny-quorum
			}
			r.Facts = Match{mKeySystem: "vote", mKeyKind: motionID, mKeyValue: map[string]any{"choice": choice}}
		}
		nf.Req = &r
	}
	for _, a := range f.Args {
		nf.Args = append(nf.Args, a.bind(motionID))
	}
	return nf
}

// quorumNeed returns the first quorum threshold in the formula (0 if none) — for the
// motion channel entity's `need` field.
func (f *Formula) quorumNeed() int {
	if f == nil {
		return 0
	}
	if f.Op == OpAtom && f.Req != nil && f.Req.Kind == ReqQuorum {
		return f.Req.N
	}
	for _, a := range f.Args {
		if n := a.quorumNeed(); n > 0 {
			return n
		}
	}
	return 0
}

// Subtrees returns the distinct (system, kind) fact subtrees this formula reads — what a
// Monitor watches to know when to re-evaluate. Kind may be "" (a whole-system watch).
func (f *Formula) Subtrees() []Subtree {
	seen := map[Subtree]bool{}
	var out []Subtree
	var walk func(*Formula)
	walk = func(n *Formula) {
		if n == nil {
			return
		}
		if n.Op == OpAtom {
			// a deadline is a time derivation, not a store fact — it has no subtree to watch.
			if n.Req != nil && n.Req.Kind != ReqDeadline {
				k := subtreeOf(n.Req.Facts)
				if !seen[k] {
					seen[k] = true
					out = append(out, k)
				}
			}
			return
		}
		for _, a := range n.Args {
			walk(a)
		}
	}
	walk(f)
	return out
}

// existing keeps the refs that actually stand in the store.
//
// A requirement is about what IS there — that is the whole meaning of a fact leaf, and of the
// witness a commit is guarded by. Selection alone does not answer that, because a match that
// NAMES one entity is deliberately a set of one whether or not it exists: an action's `patch`
// on a named subject is how a trigger publishes a fact nothing has written yet. That
// affordance belongs to a subject and to nothing else, so `!fact ci:status:green` — a fully
// qualified name — must be asked of the store rather than granted by its own spelling.
//
// A read that errors drops the ref: an unanswerable read is not evidence that a fact holds.
func existing(src entity.Reader, refs []entity.Ref) []entity.Ref {
	out := refs[:0]
	for _, ref := range refs {
		if _, ok, err := src.Get(ref); err == nil && ok {
			out = append(out, ref)
		}
	}
	return out
}

// eval evaluates a requirement against the store, returning satisfaction and the witness
// refs. Only quorum is implemented; other kinds are stubs that block (their producers
// are not built yet).
func (r *Requirement) eval(src entity.Reader) (bool, []entity.Ref) {
	switch r.Kind {
	case ReqQuorum:
		refs := existing(src, r.Facts.Select(src))
		return len(refs) >= r.N, refs
	case ReqFact, ReqMatch:
		// presence: the fact holds iff a matching entity exists (quorum of 1). Used for
		// oracle facts and, notably, the `deadline` leaf of an `until` — time as a fact.
		// A match leaf reads the same way when evaluated as a store predicate rather than
		// as an edge, so a `when` composes inside a temporal formula.
		refs := existing(src, r.Facts.Select(src))
		return len(refs) >= 1, refs
	default:
		return false, nil // judgment/ask/default: producers not built yet — they block
	}
}

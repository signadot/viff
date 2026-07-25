package trigger

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/signadot/verse/entity"
)

// Formula is the TEMPORAL grammar: matches composed with operators over the verse's
// evolution. A Trigger's Condition.When, a Gate's Formula, and a Goal's DoneWhen are all
// Formulas, differing only in consequence — a satisfied When *fires*, a satisfied gate
// *proceeds*, a satisfied doneWhen *latches*.
//
// Its leaves are Requirements, and a Requirement is A MATCH PLUS HOW IT IS EVALUATED
// (see RequirementKind): `match` is an edge on a change, `fact` is presence, `quorum` is a
// tally, `deadline` is the clock. There is exactly ONE atom carrier — Req — so "a formula's
// leaves are matches" holds literally; a second carrier would make an atom two things.
// Boolean composition (and/or) lives inside the leaf match, not as Formula nodes.
type Formula struct {
	Op   FormulaOp    `json:"op"`
	Req  *Requirement `json:"req,omitempty"`  // Op == OpAtom: the leaf requirement
	Args []*Formula   `json:"args,omitempty"` // operands for temporal operators
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

// UnmarshalJSON parses the charter's compact, tag-keyed surface into the AST — the single
// object key IS the tag (an operator or a requirement kind), so a gate author writes
//
//	{"quorum": 2}                                       an atom (quorum threshold 2)
//	{"quorum": {"choice":"reject","n":2}}               a reject-quorum
//	{"fact": "decision:deadline"}                       a fact atom (system:kind selector)
//	{"until": [ {"quorum":2}, {"fact":"decision:deadline"} ]}   an operator over sub-formulas
//
// rather than the internal parse node ({"op":"atom","req":{"kind":"quorum","n":2}}). The
// verbose {op,req,args} form is still accepted (compatibility). This mirrors tony's tags —
// in a tony charter it reads `!until [ !quorum 2, … ]`.
func (f *Formula) UnmarshalJSON(data []byte) error {
	// A bare string is the match atom's ref sugar: `when: "demo:pr:1"`.
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		*f = *matchFormula(refMatch(str))
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("formula: want an object, or a ref string: %w", err)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// ORDERED CLASSIFICATION. The order matters: `op` is both a match dimension and the
	// verbose node's discriminator (the one overlap, asserted in
	// TestMatchKeysDisjointFromFormulaTags), so match keys are tested FIRST. Branching on
	// `op` alone — as this once did — silently decoded a match carrying `op: "set"` as
	// Formula{Op:"set"}, which matches nothing.
	switch {
	case len(raw) == 0: // {} — an empty match: everything
		*f = *matchFormula(Match{})
		return nil

	case hasMatchKey(keys): // a bare match object
		var m Match
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		*f = *matchFormula(m)
		return nil

	case isVerboseNode(keys, raw): // {op, req, args} — the internal parse node
		type alias Formula // no UnmarshalJSON → parse the raw node; children still use ours
		var v alias
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		*f = Formula(v)
		return nil

	case len(raw) == 1:
		tag := keys[0]
		switch op := FormulaOp(tag); op {
		case OpUntil, OpEventually, OpAlways, OpSequence:
			args, err := unmarshalFormulaList(raw[tag])
			if err != nil {
				return fmt.Errorf("formula %q: %w", tag, err)
			}
			*f = Formula{Op: op, Args: args}
			return nil
		}
		if !isRequirementKind(tag) {
			return fmt.Errorf("formula: unknown tag %q (want a match object, an operator "+
				"(until/eventually/always/sequence), or a requirement (match/quorum/fact/deadline))", tag)
		}
		req, err := requirementFromValue(RequirementKind(tag), raw[tag])
		if err != nil {
			return err
		}
		*f = Formula{Op: OpAtom, Req: req}
		return nil
	}
	return fmt.Errorf("formula: want a single tag (e.g. {\"quorum\":2}), a match object, or "+
		"{op,req,args}; got keys %v", keys)
}

// hasMatchKey reports whether these object keys name a MATCH rather than a formula tag: at
// least one match dimension other than the ambiguous `op`. Every other key must then also be
// a match dimension — Match.Validate rejects the object otherwise, so a mixed
// {"system":…,"quorum":…} fails loudly instead of being resolved one way in silence.
func hasMatchKey(keys []string) bool {
	for _, k := range keys {
		if k != mKeyOp && matchKeys[k] {
			return true
		}
	}
	return false
}

// isVerboseNode reports whether these keys are the internal {op,req,args} parse node: only
// those keys, and `op` holding a known FormulaOp. Requiring a valid op is what keeps a match
// whose only dimension is `op` (e.g. {"op":"set"}) out of this branch.
func isVerboseNode(keys []string, raw map[string]json.RawMessage) bool {
	for _, k := range keys {
		if k != "op" && k != "req" && k != "args" {
			return false
		}
	}
	var op string
	if err := json.Unmarshal(raw["op"], &op); err != nil {
		return false
	}
	switch FormulaOp(op) {
	case OpAtom, OpUntil, OpEventually, OpAlways, OpSequence:
		return true
	}
	return false
}

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

// MarshalJSON emits the compact tag-keyed form, so `trigger show` and stored charters read
// the way they were authored rather than as the internal parse node.
func (f Formula) MarshalJSON() ([]byte, error) {
	if f.Op == OpAtom {
		if f.Req == nil {
			return []byte("null"), nil
		}
		// A match atom emits its match UNWRAPPED, so a `when` reads back the way it was
		// authored ({"system":"demo",…}) rather than as {"match":{…}}.
		if f.Req.Kind == ReqMatch {
			return json.Marshal(f.Req.Facts)
		}
		return json.Marshal(map[string]any{string(f.Req.Kind): f.Req.compactValue()})
	}
	return json.Marshal(map[string]any{string(f.Op): f.Args})
}

// unmarshalFormulaList parses an operator's operands: a list of sub-formulas, or a single
// sub-formula (accepted as a one-element list).
func unmarshalFormulaList(val json.RawMessage) ([]*Formula, error) {
	var args []*Formula
	if err := json.Unmarshal(val, &args); err == nil {
		return args, nil
	}
	var one Formula
	if err := json.Unmarshal(val, &one); err != nil {
		return nil, fmt.Errorf("expected sub-formula(s): %w", err)
	}
	return []*Formula{&one}, nil
}

// requirementFromValue parses the compact value after a requirement tag: a number is the
// quorum threshold ({"quorum":2}); a string is a "system:kind" fact selector
// ({"fact":"decision:deadline"}); an object carries the full fields.
func requirementFromValue(kind RequirementKind, val json.RawMessage) (*Requirement, error) {
	var n int
	if json.Unmarshal(val, &n) == nil {
		return &Requirement{Kind: kind, N: n}, nil
	}
	var s string
	if json.Unmarshal(val, &s) == nil {
		if kind == ReqDeadline {
			return &Requirement{Kind: kind, Within: s}, nil // {"deadline":"4h"}
		}
		return &Requirement{Kind: kind, Facts: factSelFromRef(s)}, nil
	}
	var r Requirement
	if err := json.Unmarshal(val, &r); err != nil {
		return nil, fmt.Errorf(`requirement %q: want a threshold, a "system:kind", or an object: %w`, kind, err)
	}
	r.Kind = kind
	return &r, nil
}

// factSelFromRef parses "system:kind" (or "system") into a fact-selecting Match.
func factSelFromRef(s string) Match {
	parts := strings.SplitN(s, ":", 2)
	fs := Match{mKeySystem: parts[0]}
	if len(parts) == 2 {
		fs[mKeyKind] = parts[1]
	}
	return fs
}

// compactValue renders a requirement as its compact form: a bare threshold when only N is
// set, a "system:kind" string when it is only a fact selector, else an object.
func (r *Requirement) compactValue() any {
	if r.Kind == ReqDeadline {
		return r.Within
	}
	if r.Kind == ReqMatch {
		return r.Facts
	}
	factsSet := r.Facts.System() != "" || r.Facts.Kind() != "" || len(r.Facts.Fields()) > 0
	switch {
	case r.Choice == "" && !factsSet:
		return r.N
	case r.N == 0 && r.Choice == "" && len(r.Facts.Fields()) == 0:
		if r.Facts.Kind() != "" {
			return r.Facts.System() + ":" + r.Facts.Kind()
		}
		return r.Facts.System()
	default:
		m := map[string]any{}
		if r.N != 0 {
			m["n"] = r.N
		}
		if r.Choice != "" {
			m["choice"] = r.Choice
		}
		if factsSet {
			m["facts"] = r.Facts
		}
		return m
	}
}

// Requirement is a leaf of a Formula: a named, verse-native requirement a producer
// satisfies by writing facts. Kind names the expected producer (quorum → a vote
// surface, judgment → a judge, fact → a source, …). Only quorum is implemented in this
// first pass; the others are stubbed so the requirement/producer-binding shape is
// present from the start.
type Requirement struct {
	Kind   RequirementKind `json:"kind"`
	Facts  Match           `json:"facts"`            // the store facts this requirement reads (a point-in-time Match)
	N      int             `json:"n,omitempty"`      // quorum threshold: satisfied when ≥ N ballots match
	Choice string          `json:"choice,omitempty"` // which ballot a quorum counts (default "approve"; "reject" = a deny-quorum)
	Within string          `json:"within,omitempty"` // deadline duration (e.g. "4h") — a time derivation, not a store fact
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
func (f *Formula) Eval(store entity.Store) (bool, []entity.Ref) {
	if f == nil {
		return false, nil
	}
	switch f.Op {
	case OpAtom:
		if f.Req == nil {
			return false, nil
		}
		return f.Req.eval(store)
	default:
		return false, nil // temporal operators arrive in later steps
	}
}

// Unmet returns the leaf requirements that do NOT currently hold — the "waiting on"
// complement of Eval's witness. A satisfied formula yields none; mirrors Eval/Subtrees.
// An atom contributes itself iff its requirement is unsatisfied; temporal operators recurse
// into their operands. A `deadline` leaf is a time derivation, not a store fact a producer
// makes true, so it is omitted (as Subtrees omits it) — you don't "wait on" the clock as
// unmet work. This is what a goal projects as its `waitingOn` status.
func (f *Formula) Unmet(store entity.Store) []*Requirement {
	if f == nil {
		return nil
	}
	switch f.Op {
	case OpAtom:
		if f.Req == nil || f.Req.Kind == ReqDeadline {
			return nil
		}
		if ok, _ := f.Req.eval(store); ok {
			return nil
		}
		return []*Requirement{f.Req}
	default:
		var out []*Requirement
		for _, a := range f.Args {
			out = append(out, a.Unmet(store)...)
		}
		return out
	}
}

// Label renders a requirement as a short, human/machine-readable tag for a goal's
// `waitingOn` list — a fact's `system:kind`, a quorum's threshold/choice, etc.
func (r *Requirement) Label() string {
	switch r.Kind {
	case ReqMatch:
		if r.Facts.Kind() != "" {
			return "match:" + r.Facts.System() + ":" + r.Facts.Kind()
		}
		if r.Facts.System() != "" {
			return "match:" + r.Facts.System()
		}
		return "match:*"
	case ReqQuorum:
		choice := r.Choice
		if choice == "" {
			choice = "approve"
		}
		return fmt.Sprintf("quorum(%d %s)", r.N, choice)
	case ReqFact:
		if r.Facts.Kind() != "" {
			return "fact:" + r.Facts.System() + ":" + r.Facts.Kind()
		}
		return "fact:" + r.Facts.System()
	default:
		return string(r.Kind)
	}
}

// bind returns a copy of the formula scoped to one decision instance: a `quorum` atom's
// votes become that decision's vote log (`vote:<decisionID>:*`, counting approvals), so
// concurrent decisions never share a tally and each keeps its own ordered ballot log.
// Other atoms (e.g. a `fact` oracle or deadline, charter-specified) are left as-is.
func (f *Formula) bind(decisionID string) *Formula {
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
			r.Facts = Match{mKeySystem: "vote", mKeyKind: decisionID, mKeyFields: map[string]any{"choice": choice}}
		}
		nf.Req = &r
	}
	for _, a := range f.Args {
		nf.Args = append(nf.Args, a.bind(decisionID))
	}
	return nf
}

// quorumNeed returns the first quorum threshold in the formula (0 if none) — for the
// decision channel entity's `need` field.
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
func (f *Formula) Subtrees() [][2]string {
	seen := map[[2]string]bool{}
	var out [][2]string
	var walk func(*Formula)
	walk = func(n *Formula) {
		if n == nil {
			return
		}
		if n.Op == OpAtom {
			// a deadline is a time derivation, not a store fact — it has no subtree to watch.
			if n.Req != nil && n.Req.Kind != ReqDeadline {
				k := [2]string{n.Req.Facts.System(), n.Req.Facts.Kind()}
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

// eval evaluates a requirement against the store, returning satisfaction and the witness
// refs. Only quorum is implemented; other kinds are stubs that block (their producers
// are not built yet).
func (r *Requirement) eval(store entity.Store) (bool, []entity.Ref) {
	switch r.Kind {
	case ReqQuorum:
		refs := r.Facts.Select(store)
		return len(refs) >= r.N, refs
	case ReqFact, ReqMatch:
		// presence: the fact holds iff a matching entity exists (quorum of 1). Used for
		// oracle facts and, notably, the `deadline` leaf of an `until` — time as a fact.
		// A match leaf reads the same way when evaluated as a store predicate rather than
		// as an edge, so a `when` composes inside a temporal formula.
		refs := r.Facts.Select(store)
		return len(refs) >= 1, refs
	default:
		return false, nil // judgment/ask/default: producers not built yet — they block
	}
}

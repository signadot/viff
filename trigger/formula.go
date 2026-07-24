package trigger

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/example/infra/verse/entity"
)

// Formula is a temporal-logic formula over store facts — a tagged-union parse node.
// A gate may carry one Formula; evaluating it against the store IS the gate's match
// (as opposed to the tool-use GateMatch on Gate.Match). Leaves are Requirements (fact
// predicates a producer satisfies by writing facts); interior nodes are temporal
// operators over the store's evolution. Boolean composition (and/or) lives inside a
// Requirement's fact selection (a tony match), not as Formula nodes.
//
// This first pass implements the atom node and evaluates it as a snapshot predicate;
// temporal operators (until, eventually, always, sequence) are added as further Op
// variants — each a new case here, per the plan's front-loaded build.
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
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if _, verbose := raw["op"]; verbose {
		type alias Formula // no UnmarshalJSON → parse the raw node; children still use ours
		var v alias
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		*f = Formula(v)
		return nil
	}
	if len(raw) != 1 {
		return fmt.Errorf(`formula: want a single tag, e.g. {"quorum":2} or {"until":[…]} (got %d keys)`, len(raw))
	}
	for tag, val := range raw {
		switch op := FormulaOp(tag); op {
		case OpUntil, OpEventually, OpAlways, OpSequence:
			args, err := unmarshalFormulaList(val)
			if err != nil {
				return fmt.Errorf("formula %q: %w", tag, err)
			}
			*f = Formula{Op: op, Args: args}
		default:
			req, err := requirementFromValue(RequirementKind(tag), val)
			if err != nil {
				return err
			}
			*f = Formula{Op: OpAtom, Req: req}
		}
	}
	return nil
}

// MarshalJSON emits the compact tag-keyed form, so `trigger show` and stored charters read
// the way they were authored rather than as the internal parse node.
func (f Formula) MarshalJSON() ([]byte, error) {
	if f.Op == OpAtom {
		if f.Req == nil {
			return []byte("null"), nil
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

// factSelFromRef parses "system:kind" (or "system") into a FactSel.
func factSelFromRef(s string) FactSel {
	parts := strings.SplitN(s, ":", 2)
	fs := FactSel{System: parts[0]}
	if len(parts) == 2 {
		fs.Kind = parts[1]
	}
	return fs
}

// compactValue renders a requirement as its compact form: a bare threshold when only N is
// set, a "system:kind" string when it is only a fact selector, else an object.
func (r *Requirement) compactValue() any {
	if r.Kind == ReqDeadline {
		return r.Within
	}
	factsSet := r.Facts.System != "" || r.Facts.Kind != "" || len(r.Facts.Where) > 0
	switch {
	case r.Choice == "" && !factsSet:
		return r.N
	case r.N == 0 && r.Choice == "" && len(r.Facts.Where) == 0:
		if r.Facts.Kind != "" {
			return r.Facts.System + ":" + r.Facts.Kind
		}
		return r.Facts.System
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
	Facts  FactSel         `json:"facts"`            // the store facts this requirement reads
	N      int             `json:"n,omitempty"`      // quorum threshold: satisfied when ≥ N ballots match
	Choice string          `json:"choice,omitempty"` // which ballot a quorum counts (default "approve"; "reject" = a deny-quorum)
	Within string          `json:"within,omitempty"` // deadline duration (e.g. "4h") — a time derivation, not a store fact
}

// RequirementKind is the verse-native requirement vocabulary. The concrete surface that
// satisfies a requirement (slack/page/CI/…) is a driver bound outside the charter.
type RequirementKind string

const (
	ReqQuorum   RequirementKind = "quorum"
	ReqFact     RequirementKind = "fact"     // presence of a matching fact
	ReqDeadline RequirementKind = "deadline" // a duration resolved against the system clock (an `until` right side)
	ReqJudgment RequirementKind = "judgment" // stub — producer not built yet
	ReqAsk      RequirementKind = "ask"      // stub
	ReqDefault  RequirementKind = "default"  // stub
)

// FactSel selects the store facts a requirement reads — the prototype's minimal atom
// (destination: a full tony match). It picks entities of (System, Kind) whose payload
// matches every Where entry.
type FactSel struct {
	System string            `json:"system"`
	Kind   string            `json:"kind,omitempty"`
	Where  map[string]string `json:"where,omitempty"`
}

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
	case ReqQuorum:
		choice := r.Choice
		if choice == "" {
			choice = "approve"
		}
		return fmt.Sprintf("quorum(%d %s)", r.N, choice)
	case ReqFact:
		if r.Facts.Kind != "" {
			return "fact:" + r.Facts.System + ":" + r.Facts.Kind
		}
		return "fact:" + r.Facts.System
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
			r.Facts = FactSel{System: "vote", Kind: decisionID, Where: map[string]string{"choice": choice}}
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
				k := [2]string{n.Req.Facts.System, n.Req.Facts.Kind}
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
		refs := r.Facts.match(store)
		return len(refs) >= r.N, refs
	case ReqFact:
		// presence: the fact holds iff a matching entity exists (quorum of 1). Used for
		// oracle facts and, notably, the `deadline` leaf of an `until` — time as a fact.
		refs := r.Facts.match(store)
		return len(refs) >= 1, refs
	default:
		return false, nil // judgment/ask/default: producers not built yet — they block
	}
}

// match returns the refs of every store fact the selector picks: entities of (System,
// Kind) whose payload contains each Where key/value. Requires a Lister store (the
// in-memory and logd-backed stores both are); a non-listing store yields none.
func (s FactSel) match(store entity.Store) []entity.Ref {
	lister, ok := store.(entity.Lister)
	if !ok {
		return nil
	}
	ents := lister.List(func(ref entity.Ref) bool {
		return ref.System == s.System && (s.Kind == "" || ref.Kind == s.Kind)
	})
	out := make([]entity.Ref, 0, len(ents))
	for _, e := range ents {
		if s.matches(e.Payload) {
			out = append(out, e.Ref)
		}
	}
	return out
}

// matches reports whether a payload satisfies every Where entry (string equality — the
// prototype's minimal predicate; a full tony match is the destination).
func (s FactSel) matches(p entity.Payload) bool {
	for k, v := range s.Where {
		if got, _ := p[k].(string); got != v {
			return false
		}
	}
	return true
}

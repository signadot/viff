package trigger

import (
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"

	"github.com/signadot/verse/codec"
)

// The charter's compact tag-keyed surface parses into the AST: the object key is the tag
// (a requirement kind or an operator), no op/req/atom scaffolding.
func TestFormulaCompactUnmarshal(t *testing.T) {
	cases := []struct {
		in    string
		check func(*Formula) bool
	}{
		{`{"quorum":2}`, func(f *Formula) bool { return f.Op == OpAtom && f.Req.Kind == ReqQuorum && f.Req.N == 2 }},
		{`{"quorum":{"choice":"reject","n":3}}`, func(f *Formula) bool {
			return f.Op == OpAtom && f.Req.Kind == ReqQuorum && f.Req.Choice == "reject" && f.Req.N == 3
		}},
		{`{"fact":"motion:deadline"}`, func(f *Formula) bool {
			return f.Op == OpAtom && f.Req.Kind == ReqFact && f.Req.Facts.System() == "motion" && f.Req.Facts.Kind() == "deadline"
		}},
		{`{"until":[{"quorum":2},{"fact":"motion:deadline"}]}`, func(f *Formula) bool {
			return f.Op == OpUntil && len(f.Args) == 2 && f.Args[0].Req.Kind == ReqQuorum && f.Args[1].Req.Kind == ReqFact
		}},
		{`{"op":"atom","req":{"kind":"quorum","n":2}}`, func(f *Formula) bool { // verbose still accepted
			return f.Op == OpAtom && f.Req.Kind == ReqQuorum && f.Req.N == 2
		}},
	}
	for _, c := range cases {
		var f Formula
		if err := codec.Unmarshal([]byte(c.in), &f, codec.JSON); err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if !c.check(&f) {
			t.Fatalf("%s → %+v (unexpected)", c.in, f)
		}
	}
}

// A formula marshals back to the compact form and re-parses to the same AST — authored in
// the JSON key form, rendered as tags.
func TestFormulaCompactRoundTrip(t *testing.T) {
	const in = `{"until":[{"quorum":2},{"quorum":{"choice":"reject","n":2}}]}`
	var f Formula
	if err := codec.Unmarshal([]byte(in), &f, codec.JSON); err != nil {
		t.Fatal(err)
	}
	out, err := codec.Marshal(&f, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	var g Formula
	if err := codec.Unmarshal(out, &g, codec.Tony); err != nil {
		t.Fatalf("re-parse of %s: %v", out, err)
	}
	if g.Op != OpUntil || len(g.Args) != 2 || g.Args[0].Req.N != 2 || g.Args[1].Req.Choice != "reject" {
		t.Fatalf("round-trip lost info: %s → %+v", out, g)
	}
}

// A quorum atom is satisfied once ≥ N matching facts exist, and reports them as the
// witness (the refs whose presence justified satisfaction — the future CAS precondition).
func TestFormulaEvalQuorum(t *testing.T) {
	st := entitytest.Store(t)
	quorum := &Formula{Op: OpAtom, Req: &Requirement{
		Kind:  ReqQuorum,
		Facts: Match{"system": "motion", "kind": "vote"},
		N:     2,
	}}

	// Below threshold: one vote → not satisfied.
	entitytest.Set(t, st, entity.Ref{System: "motion", Kind: "vote", ID: "alice"}, entity.Obj{"choice": "approve"}.Payload())
	if ok, _ := quorum.Eval(st); ok {
		t.Fatalf("quorum(2) satisfied with 1 vote")
	}

	// At threshold: two votes → satisfied, witness names both.
	entitytest.Set(t, st, entity.Ref{System: "motion", Kind: "vote", ID: "bob"}, entity.Obj{"choice": "approve"}.Payload())
	ok, witness := quorum.Eval(st)
	if !ok {
		t.Fatalf("quorum(2) not satisfied with 2 votes")
	}
	if len(witness) != 2 {
		t.Fatalf("witness = %d refs, want 2 (%v)", len(witness), witness)
	}

	// Revoke below threshold: delete a vote → un-satisfied (safety: it can flip back).
	entitytest.Remove(t, st, entity.Ref{System: "motion", Kind: "vote", ID: "bob"})
	if ok, _ := quorum.Eval(st); ok {
		t.Fatalf("quorum(2) still satisfied after a vote was retracted")
	}
}

// A Where selector narrows which facts count.
func TestFormulaEvalQuorumWhere(t *testing.T) {
	st := entitytest.Store(t)
	approvals := &Formula{Op: OpAtom, Req: &Requirement{
		Kind:  ReqQuorum,
		Facts: Match{"system": "motion", "kind": "vote", "value": map[string]any{"choice": "approve"}},
		N:     2,
	}}
	entitytest.Set(t, st, entity.Ref{System: "motion", Kind: "vote", ID: "a"}, entity.Obj{"choice": "approve"}.Payload())
	entitytest.Set(t, st, entity.Ref{System: "motion", Kind: "vote", ID: "b"}, entity.Obj{"choice": "reject"}.Payload())
	entitytest.Set(t, st, entity.Ref{System: "motion", Kind: "vote", ID: "c"}, entity.Obj{"choice": "approve"}.Payload())
	ok, witness := approvals.Eval(st)
	if !ok || len(witness) != 2 {
		t.Fatalf("approve-quorum(2) = (%v, %d refs), want (true, 2) — 'reject' must not count", ok, len(witness))
	}
}

// A stubbed requirement kind (no producer yet) blocks — it never satisfies.
func TestFormulaEvalStubbedKindBlocks(t *testing.T) {
	st := entitytest.Store(t)
	judged := &Formula{Op: OpAtom, Req: &Requirement{Kind: ReqJudgment, Facts: Match{"system": "motion", "kind": "judgment"}}}
	entitytest.Set(t, st, entity.Ref{System: "motion", Kind: "judgment", ID: "x"}, entity.Obj{"verdict": "ship"}.Payload())
	if ok, _ := judged.Eval(st); ok {
		t.Fatalf("stubbed requirement %q should block, not satisfy", ReqJudgment)
	}
}

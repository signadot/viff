package trigger

import (
	"reflect"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// A formula is a match: every spelling a match takes reads as one, and renders back as one.
func TestFormulaIsAMatch(t *testing.T) {
	for _, in := range []string{`demo.pr.*`, `{path: "demo.pr.*"}`, `{path: "demo.pr.*", value: {stage: open}}`, `!match {path: "demo.*.*"}`} {
		var f Formula
		if err := codec.Unmarshal([]byte(in), &f, codec.Tony); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if !f.IsMatchAtom() || f.Req.Facts.Segs()[0] != "demo" {
			t.Fatalf("%s → %+v, want a match on demo", in, f)
		}
		out, err := codec.Marshal(&f, codec.Tony)
		if err != nil {
			t.Fatalf("marshal %s: %v", in, err)
		}
		var g Formula
		if err := codec.Unmarshal(out, &g, codec.Tony); err != nil {
			t.Fatalf("re-parse of %s: %v", out, err)
		}
		if !reflect.DeepEqual(g.Req.Facts.Segs(), f.Req.Facts.Segs()) {
			t.Fatalf("round-trip lost the match: %s → %s → %+v", in, out, g)
		}
	}
}

// The temporal grammar is refused by name, and the refusal says what replaced it. A charter
// still carrying `!quorum 2` must not be read as a match on a field named quorum.
func TestTheTemporalGrammarIsRefusedByName(t *testing.T) {
	cases := []struct {
		in  string
		fmt codec.Format
	}{
		{`!quorum 2`, codec.Tony},
		{`!until [!quorum 2, !deadline "4h"]`, codec.Tony},
		{`!fact ci.status.green`, codec.Tony},
		{`!deadline "4h"`, codec.Tony},
	}
	for _, c := range cases {
		var f Formula
		err := codec.Unmarshal([]byte(c.in), &f, c.fmt)
		if err == nil {
			t.Fatalf("%s parsed as %+v; want it refused", c.in, f)
		}
		for _, want := range []string{"need:", "deadline:"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not name %q: %v", c.in, want, err)
			}
		}
	}
}

// Eval is presence: a match that names an entity holds only when that entity stands in the
// store, and the refs that stood are reported.
func TestFormulaEvalIsPresence(t *testing.T) {
	st := entitytest.Store(t)
	green := matchFormula(mm("ci.status.green"))
	if ok, _ := green.Eval(st); ok {
		t.Fatal("a named entity that does not exist held")
	}
	entitytest.Set(t, st, entity.RefAt("ci", "status", "green"), entity.Obj{"at": "now"}.Payload())
	ok, refs := green.Eval(st)
	if !ok || len(refs) != 1 || refs[0] != entity.RefAt("ci", "status", "green") {
		t.Fatalf("Eval = (%v, %v), want held with the one ref", ok, refs)
	}
	if st := green.Subtrees(); len(st) != 1 || st[0].Path()[0] != "ci" {
		t.Fatalf("subtrees = %+v, want the one the match reads", st)
	}
}

package api

import (
	"errors"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/codec"
)

// A FILTER KEEPS WHAT MATCHES AND WRITES WHAT IT TRIMS TO, an array by its elements and anything
// else as one item; what keeps nothing is an empty list, and a pattern that does not apply is a
// misuse of if, never an item that did not match.
func TestAFilterSaysAPatternThatDoesNotApply(t *testing.T) {
	parse := func(src string) *ir.Node {
		t.Helper()
		n, err := codec.ParseNode([]byte(src), codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	rows := parse("[{name: a, stage: open}, {name: b, stage: done}]")
	out, err := Filter{Keep: parse("{stage: open}"), Trim: parse("{name: null}")}.Apply(rows)
	if err != nil || out.Type != ir.ArrayType || len(out.Values) != 1 {
		t.Fatalf("kept %v, %v; want one row", out, err)
	}
	if row := out.Values[0]; len(row.Fields) != 1 || row.Fields[0].String != "name" || row.Values[0].String != "a" {
		t.Errorf("the row kept is %v, want its name alone", row)
	}
	if out, err := (Filter{Keep: parse("{stage: gone}")}).Apply(parse("{stage: open}")); err != nil ||
		out.Type != ir.ArrayType || len(out.Values) != 0 {
		t.Errorf("an item that does not match = %v, %v; want an empty list", out, err)
	}
	_, err = Filter{Keep: parse("{name: !glob [1]}")}.Apply(rows)
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != "if" || !fe.Misuse {
		t.Errorf("a pattern that does not apply = %v, want a misuse of if", err)
	}
}

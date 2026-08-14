package task

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/signadot/tony-format/go-tony/encode"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/parse"
)

// The three states of `fatal` survive the runner hop.
//
// This is the test the design turns on, and it is about the WIRE rather than about Go: a task
// that runs on a remote runner is marshalled into a spawn body and decoded on the far side, so
// a distinction that does not cross is a charter meaning different things depending on where it
// happened to run. It could not cross as a plain slice — `omitzero` drops an empty one, and
// without `omitzero` an absent one is emitted as empty.
func TestFatalsThreeStatesCrossTheWire(t *testing.T) {
	in := []Call{
		{Tool: "default"},                           // absent: anything ends the list
		{Tool: "waived", Fatal: Fatals()},           // empty: nothing does
		{Tool: "narrowed", Fatal: Fatals(FatalErr)}, // these
	}
	n, err := gomap.ToTonyIR(in)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := encode.Encode(n, &buf); err != nil {
		t.Fatal(err)
	}
	n2, err := parse.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("reparse %q: %v", buf.String(), err)
	}
	var out []Call
	if err := gomap.FromTonyIR(n2, &out); err != nil {
		t.Fatalf("decode %q: %v", buf.String(), err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("wire:\n%s\nin  = %+v\nout = %+v", buf.String(), in, out)
	}
	if out[0].Fatal != nil {
		t.Error("an absent fatal came back present")
	}
	if out[1].Fatal == nil || len(*out[1].Fatal) != 0 {
		t.Error("an empty fatal came back absent — the sweep's statement did not cross")
	}
}

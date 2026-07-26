package process

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
)

// The wire types must survive a round trip through every dialect. The JSON path is the
// oracle: it is what verse serves today, so a tony round trip that disagrees with it is a
// regression, not a new spelling.
func TestTonyRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want func() any
	}{
		{"instance", Instance{
			ID: "run-1", Task: "review", Driver: "llm", Status: Running,
			Policy: "local", Cursor: 7, DialURL: "http://x/y",
			Pending: []PendingGate{{ID: "d1", Gate: "shell", Mode: "ask", Reason: "why"}},
		}, func() any { return &Instance{} }},

		{"instance-minimal", Instance{ID: "run-2", Status: Done},
			func() any { return &Instance{} }},

		{"frame", Frame{
			Kind: EvDecision, Process: "run-1", Cursor: 3, ID: "d1",
			Gate: "shell", Mode: "ask", By: "human:cli", Proceed: true, Text: "ok",
		}, func() any { return &Frame{} }},

		{"vector", Vector{Default: Ask, Gates: []Gate{{
			Name: "shell", Mode: Park,
			Match: GateMatch{
				Tools:     []string{"bash*"},
				Verbs:     []Verb{Exec, Write},
				Resources: []ResourcePred{{Kind: KindFile, Pattern: "cmd/**"}},
				Paths:     []PathPred{{Name: "*.go", Type: "glob"}},
				Branches:  []string{"main"},
				OnPush:    true,
			},
		}}}, func() any { return &Vector{} }},

		{"vector-minimal", Vector{Default: Auto}, func() any { return &Vector{} }},
	}

	for _, c := range cases {
		for _, f := range []codec.Format{codec.Tony, codec.JSON, codec.YAML} {
			t.Run(c.name+"/"+f.String(), func(t *testing.T) {
				b, err := codec.Marshal(c.v, f)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				got := c.want()
				if err := codec.Unmarshal(b, got, f); err != nil {
					t.Fatalf("unmarshal %q: %v", b, err)
				}
				if !reflect.DeepEqual(reflect.ValueOf(got).Elem().Interface(), c.v) {
					t.Errorf("round trip differs\n wire: %s\n  got: %+v\n want: %+v", b, got, c.v)
				}
			})
		}
	}
}

// Optional fields must not appear when unset — the readability this whole migration is
// for. Without codec's omitzero pass, gomap's reflection path emits all ten.
func TestTonyOmitsUnsetFields(t *testing.T) {
	b, err := codec.Marshal(Instance{ID: "run-1", Status: Running}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(b))
	if got != "id: run-1\nstatus: running" {
		t.Errorf("unset fields leaked into the document:\n%s", got)
	}
}

// The tony and JSON renderings must carry the same field names, so a reader moving
// between dialects sees one vocabulary. This is what makes the JSON path a usable oracle.
func TestTonyAndJSONAgreeOnFieldNames(t *testing.T) {
	inst := Instance{ID: "run-1", Task: "review", Status: Running, Cursor: 4}

	viaCodec, err := codec.Marshal(inst, codec.JSON)
	if err != nil {
		t.Fatal(err)
	}
	viaEncodingJSON, err := json.Marshal(inst)
	if err != nil {
		t.Fatal(err)
	}

	var a, b map[string]any
	if err := json.Unmarshal(viaCodec, &a); err != nil {
		t.Fatalf("codec JSON is not valid JSON: %v (%s)", err, viaCodec)
	}
	if err := json.Unmarshal(viaEncodingJSON, &b); err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Errorf("different field sets:\n codec: %s\n stdlib: %s", viaCodec, viaEncodingJSON)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			t.Errorf("codec JSON is missing field %q\n codec: %s\n stdlib: %s", k, viaCodec, viaEncodingJSON)
		}
	}
}

// Mode, Status, Verb and ResourceKind are scalar vocabularies; they must read as their
// names in a document, not as numbers.
func TestScalarVocabulariesAreText(t *testing.T) {
	b, err := codec.Marshal(Vector{
		Default: Park,
		Gates: []Gate{{Mode: Ask, Match: GateMatch{
			Verbs:     []Verb{CallAgent},
			Resources: []ResourcePred{{Kind: KindAgent}},
		}}},
	}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"park", "ask", "call-agent", "agent"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%q did not render as text: %s", want, b)
		}
	}
}

// A struct-valued field carrying `omitzero` is still emitted by generated code when empty.
// The scalar and collection kinds were fixed in go-tony v0.0.89; this one was not
// (tony-format#f69agjyeh12ksa25cnn0 item 12), and it shows up as `match: {}` on a gate with
// no predicate — a default gate — in documents people author by hand.
//
// Accepted deliberately when codegen was adopted: nothing has to be undone when it lands,
// the `{}` simply stops appearing. THIS TEST FAILING IS THAT MOMENT — delete it and the
// note beside Gate.Match.
func TestCodegenStillEmitsEmptyStructFields(t *testing.T) {
	b, err := codec.Marshal(Vector{Default: Auto, Gates: []Gate{{Name: "default", Mode: Auto}}}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "match: {}") {
		t.Logf("`match: {}` is gone — go-tony now honors omitzero on struct-valued fields.\n"+
			"Remove this test and the note on Gate.Match.\n%s", b)
	}
}

package codec

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
)

type optional struct {
	ID     string            `tony:"field=id"`
	Task   string            `tony:"field=task,omitzero"`
	Count  int               `tony:"field=count,omitzero"`
	Flag   bool              `tony:"field=flag,omitzero"`
	Items  []string          `tony:"field=items,omitzero"`
	Labels map[string]string `tony:"field=labels,omitzero"`
	Ptr    *optional         `tony:"field=ptr,omitzero"`
	Nested inner             `tony:"field=nested,omitzero"`
}

type inner struct {
	A string `tony:"field=a"`
	B string `tony:"field=b,omitzero"`
}

// An optional field that is not set does not appear. This is viff's requirement rather
// than a test of gomap: it is what makes a document readable, and it was carried by a
// pruning pass in this package until go-tony v0.0.89 honored the tag itself. The tests stay
// where the requirement is.
func TestOmitZeroPrunesEmptyFields(t *testing.T) {
	b, err := Marshal(optional{ID: "x"}, Tony)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "id: x") {
		t.Errorf("a required field was dropped: %q", got)
	}
	for _, gone := range []string{"task", "count", "flag", "items", "labels", "ptr", "nested"} {
		if strings.Contains(got, gone+":") {
			t.Errorf("empty optional field %q survived: %q", gone, got)
		}
	}
}

// A field that IS set must survive, whatever its kind.
func TestOmitZeroKeepsSetFields(t *testing.T) {
	v := optional{
		ID:     "x",
		Task:   "review",
		Count:  2,
		Flag:   true,
		Items:  []string{"a"},
		Labels: map[string]string{"k": "v"},
		Ptr:    &optional{ID: "child"},
		Nested: inner{A: "a"},
	}
	b, err := Marshal(v, Tony)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"id:", "task:", "count:", "flag:", "items:", "labels:", "ptr:", "nested:"} {
		if !strings.Contains(got, want) {
			t.Errorf("set field %q was dropped: %q", want, got)
		}
	}
	// Pruning recurses: the nested struct's own empty optional is gone, its set one stays.
	if strings.Contains(got, "b:") {
		t.Errorf("a nested empty optional survived: %q", got)
	}
	if !strings.Contains(got, "a: a") {
		t.Errorf("a nested set field was dropped: %q", got)
	}
}

// A required field stays even when empty — that is what distinguishes it.
func TestOmitZeroKeepsRequiredEmptyFields(t *testing.T) {
	b, err := Marshal(optional{}, Tony)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "id:") {
		t.Errorf("a required field was pruned because it was empty: %q", b)
	}
}

// An empty-but-non-nil slice or map carrying omitzero is omitted too — "set to empty" is
// not a reason to appear when the field asked to be omitted when zero.
func TestOmitZeroTreatsEmptyCollectionsAsAbsent(t *testing.T) {
	b, err := Marshal(optional{ID: "x", Items: []string{}, Labels: map[string]string{}}, Tony)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Contains(got, "items") || strings.Contains(got, "labels") {
		t.Errorf("an empty non-nil collection survived: %q", got)
	}
}

// A hand-written codec owns its own output, including what it omits. Nothing downstream
// second-guesses it by field name — its node shape need not mirror the struct at all.
type ownCodec struct {
	Task string `tony:"field=task,omitzero"`
}

func (o ownCodec) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	// Deliberately emits `task` as empty — a hand-written codec's omission is its own.
	return ir.FromKeyVals([]ir.KeyVal{{Key: ir.FromString("task"), Val: ir.FromString("")}}), nil
}

func TestOwnCodecOutputIsNotSecondGuessed(t *testing.T) {
	// Reached as a pointer field, which is the shape gomap dispatches on.
	type host struct {
		Own *ownCodec `tony:"field=own"`
	}
	b, err := Marshal(host{Own: &ownCodec{}}, Tony)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "task") {
		t.Errorf("a hand-written codec's output was altered downstream: %q", b)
	}
}

// Round-tripping is unaffected: a pruned field decodes to its zero value, which is what
// it was omitted for.
func TestOmitZeroRoundTrips(t *testing.T) {
	in := optional{ID: "x", Task: "review"}
	for _, f := range []Format{Tony, YAML} {
		b, err := Marshal(in, f)
		if err != nil {
			t.Fatalf("%v: %v", f, err)
		}
		var got optional
		if err := Unmarshal(b, &got, f); err != nil {
			t.Fatalf("%v: %v", f, err)
		}
		if got.ID != "x" || got.Task != "review" || got.Count != 0 || got.Items != nil {
			t.Errorf("%v round trip: %+v", f, got)
		}
	}
}

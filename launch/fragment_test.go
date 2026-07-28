package launch

import (
	"reflect"
	"strings"
	"testing"
)

// The overlay table. Every field is exercised absent/empty/set on BOTH sides, because
// "absent" and "empty" are different values on both and the pairs do not follow one rule:
// a string field inherits when the doer is silent, while a tool list intersects — and an
// empty tool list means ALL tools, so the two silences point in opposite directions.
func TestOverlayPerField(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec Spec
		frag Fragment
		want Spec
	}{{
		name: "an empty fragment changes nothing",
		spec: Spec{Driver: "llm", Frame: "F", Grant: "read-only", Tools: []string{"read_file"}},
		want: Spec{Driver: "llm", Frame: "F", Grant: "read-only", Tools: []string{"read_file"}},
	}, {
		name: "set fields replace",
		spec: Spec{Driver: "llm", Frame: "spawn frame", Grant: "no-delegate"},
		frag: Fragment{Driver: "scripted", Frame: "doer frame", Grant: "read-only"},
		want: Spec{Driver: "scripted", Frame: "doer frame", Grant: "read-only"},
	}, {
		// The case the design doc calls out: a doer that narrows tools must not thereby
		// lose the envelope the charter set for it.
		name: "a doer that sets tools and not grant keeps the spawn's grant",
		spec: Spec{Grant: "no-delegate"},
		frag: Fragment{Tools: []string{"read_file"}},
		want: Spec{Grant: "no-delegate", Tools: []string{"read_file"}},
	}, {
		name: "the task is never the doer's to write",
		spec: Spec{Task: "review $subject", Driver: "llm"},
		frag: Fragment{Driver: "scripted"},
		want: Spec{Task: "review $subject", Driver: "scripted"},
	}, {
		name: "spawn offers all, doer narrows",
		spec: Spec{},
		frag: Fragment{Tools: []string{"read_file", "list_dir"}},
		want: Spec{Tools: []string{"read_file", "list_dir"}},
	}, {
		name: "doer is silent, spawn's list stands",
		spec: Spec{Tools: []string{"read_file"}},
		frag: Fragment{},
		want: Spec{Tools: []string{"read_file"}},
	}, {
		// The widening the intersection exists to refuse: replace would have handed this
		// spawn run_bash, which it never offered.
		name: "a doer cannot add a tool the spawn withheld",
		spec: Spec{Tools: []string{"read_file"}},
		frag: Fragment{Tools: []string{"read_file", "run_bash"}},
		want: Spec{Tools: []string{"read_file"}},
	}, {
		name: "intersection keeps the spawn's order",
		spec: Spec{Tools: []string{"a", "b", "c"}},
		frag: Fragment{Tools: []string{"c", "a"}},
		want: Spec{Tools: []string{"a", "c"}},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.spec.Overlay(tc.frag)
			if err != nil {
				t.Fatalf("Overlay: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Overlay\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// Disjoint tool lists are the one case the data cannot express, so Overlay refuses rather
// than resolving it. "No tools" would have to be written as an empty list, and an empty list
// means ALL of them — so the silent answer to "these two agree on nothing" would be "have
// everything". The engine turns this into ineligibility before a ballot ever offers the doer.
func TestOverlayRefusesDisjointTools(t *testing.T) {
	_, err := Spec{Tools: []string{"read_file"}}.Overlay(Fragment{Tools: []string{"run_bash"}})
	if err == nil {
		t.Fatal("disjoint tool lists overlaid without error; an empty result would mean ALL tools")
	}
	if !strings.Contains(err.Error(), "share no tool") {
		t.Errorf("error should say what is wrong with the pair: %v", err)
	}
}

// Overlay is a value operation: it neither mutates the spec it is called on nor aliases the
// slices it was handed, so a charter's Spec can be overlaid once per firing.
func TestOverlayDoesNotMutateOrAlias(t *testing.T) {
	spawnTools := []string{"read_file", "list_dir"}
	spec := Spec{Driver: "llm", Tools: spawnTools}
	frag := Fragment{Driver: "scripted", Tools: []string{"read_file"}}

	got, err := spec.Overlay(frag)
	if err != nil {
		t.Fatalf("Overlay: %v", err)
	}
	if spec.Driver != "llm" || !reflect.DeepEqual(spec.Tools, []string{"read_file", "list_dir"}) {
		t.Fatalf("Overlay mutated the receiver: %+v", spec)
	}
	got.Tools[0] = "clobbered"
	if spawnTools[0] != "read_file" {
		t.Fatal("the overlaid spec aliases the spawn's tool slice")
	}
}

// A fragment carrying only attributes contributes nothing — the shape every provider entity
// written before this feature has, and the reason IsZero exists rather than a nil check.
func TestFragmentIsZero(t *testing.T) {
	if !(Fragment{}).IsZero() {
		t.Error("the zero fragment is not zero")
	}
	for _, f := range []Fragment{{Driver: "llm"}, {Frame: "F"}, {Grant: "none"}, {Tools: []string{"read_file"}}} {
		if f.IsZero() {
			t.Errorf("%+v reported as contributing nothing", f)
		}
	}
}

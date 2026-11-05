package entity

import (
	"testing"
	"time"
)

// The opt-in is PRECISE: a predicate asking about the lifecycle marker sees entities on their
// way out, and one that merely mentions the reserved root — to match a rule's progress under
// `charter`, say — does not. The two halves of the reservation are separate questions.
func TestVanishingOptIn(t *testing.T) {
	s := openStore(t)
	live := RefAt("demo", "pr", "live")
	going := RefAt("demo", "pr", "going")
	set(t, s, live, Obj{"stage": "open"}.Payload())
	set(t, s, going, Obj{"stage": "open"}.Payload())
	mustWrite(t, s, Write{At: going.At(), Value: VanishPatch(time.Now())})

	if e, _, _ := s.Get(going); !Vanishing(e.Payload) {
		t.Fatal("the marker did not land")
	}
	if e, _, _ := s.Get(live); Vanishing(e.Payload) {
		t.Fatal("an untouched entity reads as vanishing")
	}

	cases := map[string]struct {
		pred Payload
		want []Ref
	}{
		"no predicate": {nil, []Ref{live}},
		"an ordinary field": {
			Obj{"stage": "open"}.Payload(), []Ref{live},
		},
		"mentions the reserved root, but not lifecycle": {
			Obj{ReservedRoot: Obj{CharterKey: Obj{"review": Obj{"done": true}}.Payload()}.Payload()}.Payload(),
			nil,
		},
		"asks about the marker": {
			Obj{ReservedRoot: Obj{MetaKey: Obj{VanishingField: nil}.Payload()}.Payload()}.Payload(),
			[]Ref{going},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			pat := patternAt("demo", "pr", "*")
			pat.Predicate = c.pred
			got := pat.Select(s)
			if len(got) != len(c.want) {
				t.Fatalf("selected %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("selected %v, want %v", got, c.want)
				}
			}
		})
	}

	// The raw listing still shows it: making the intent VISIBLE is the point, so an operator
	// asking what is there sees an entity on its way out. Only the rule-facing selection hides it.
	seen := false
	for _, e := range s.List(func(r Ref) bool { return r == going }) {
		seen = e.Ref == going
	}
	if !seen {
		t.Fatal("List hid a vanishing entity from an operator")
	}
}

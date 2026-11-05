package entity

import "testing"

// A SEGMENT MAY HOLD A DOT, and the store has to write and read it as one segment. That is not
// a corner: a question id is `verse.trigger.<rule>.<gate>~<hash>`, and it lands as the last
// segment of `verse.ask.<id>` — so an id that reads as a path when it is a name means
// a docket that publishes a question nobody can look up.
func TestAStoredSegmentMayHoldADot(t *testing.T) {
	s := openStore(t)
	for _, ref := range []Ref{
		VerseRef("gate", "question", "verse.trigger.review.review~1"),
		RefAt("demo", "pr", "a.b"),
		RefAt("a.b", "pr", "1"), // …in the first segment too
	} {
		set(t, s, ref, Obj{"stage": "open"}.Payload())
		got, ok, err := s.Get(ref)
		if err != nil || !ok {
			t.Errorf("%s: written and not found (ok=%v err=%v)", ref, ok, err)
			continue
		}
		if Str(got.Payload, "stage") != "open" {
			t.Errorf("%s: read back %+v", ref, got.Payload)
		}
	}
}

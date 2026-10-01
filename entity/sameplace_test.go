package entity

import "testing"

// EVERY PRECONDITION ABOUT ONE PLACE HOLDS, whatever order they come in. Folded into one pattern,
// a second about the same place replaced the first, and one about a field under a place another
// asked about replaced that one, so a write was admitted on the last precondition alone
// (git-issue 874azhtjh12krn1xqsn0).
func TestEveryPreconditionAboutOnePlaceHolds(t *testing.T) {
	s := openStore(t)
	ref := VerseRef("ask", "q1")
	set(t, s, ref, Obj{"status": "answered", "verdict": "proceed", "review": map[string]any{"by": "ann"}}.Payload())
	not := func(status string) Require { return Require{Pattern: Obj{"status": status}.Payload().WithTag("!not")} }
	is := Require{Pattern: Obj{"status": "answered"}.Payload()}
	reviewedBy := func(who string) Require { return Require{At: ref.At("review"), Pattern: Obj{"by": who}.Payload()} }
	for name, c := range map[string]struct {
		when  []Require
		holds bool
	}{
		"the refusing one alone":          {[]Require{not("answered")}, false},
		"the refusing one first":          {[]Require{not("answered"), not("orphaned")}, false},
		"the refusing one last":           {[]Require{not("orphaned"), not("answered")}, false},
		"a field under it refusing last":  {[]Require{is, reviewedBy("bob")}, false},
		"a field under it refusing first": {[]Require{reviewedBy("bob"), is}, false},
		"the place refusing, field first": {[]Require{reviewedBy("ann"), not("answered")}, false},
		"all of them holding":             {[]Require{not("orphaned"), not("pending"), is, reviewedBy("ann")}, true},
	} {
		ok, _, err := s.Write(Write{At: ref.At(), Value: Obj{"touched": name}.Payload(), When: c.when})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ok != c.holds {
			t.Errorf("%s: admitted %v, want %v", name, ok, c.holds)
		}
	}
}

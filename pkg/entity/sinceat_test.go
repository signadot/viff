package entity

import (
	"fmt"
	"testing"
)

// A CATCH-UP ASKS ABOUT ONE SLICE, and gets that slice — the same commits the whole-verse
// replay would have carried for it, levelled the same way.
//
// It matters for what it COSTS rather than for what it returns: the replay reads the document
// as it stood at its starting commit and logd reads every patch in the range that touches the
// watched path, so a watch on one slice that replayed the root paid for the whole store on
// every request. That is what evicted staging's substrate (git-issue 3qf2z59dh12ksdazg9n0).
func TestSinceAtOneSlice(t *testing.T) {
	s := openStore(t)
	start := s.Rev()
	pr := RefAt("demo", "pr", "1")
	set(t, s, pr, Obj{"stage": "open"}.Payload())
	set(t, s, RefAt("other", "thing", "9"), Obj{"stage": "elsewhere"}.Payload())
	set(t, s, pr, Obj{"stage": "reviewing"}.Payload())

	var narrow, wide []Event
	eventually(t, "both replays to catch up", func() bool {
		nc, err := s.Since(start, PrefixPattern([]string{"demo", "pr"}, 3))
		if err != nil {
			return false
		}
		wc, err := s.Since(start, PrefixPattern(nil, 3))
		if err != nil {
			return false
		}
		narrow, wide = changes(nc), changes(wc)
		return len(wide) >= 3
	})

	if len(narrow) != 2 {
		t.Fatalf("the narrowed replay carried %d events, want the 2 writes to demo:pr:1: %+v",
			len(narrow), narrow)
	}
	for _, e := range narrow {
		if e.Ref != pr {
			t.Errorf("a replay asked about demo.pr carried %s", e.Ref)
		}
	}
	// LEVELS, not deltas: the second write set one field, and the event owes its consumer the
	// whole entity as of that commit — which is what the base document is read for, and the
	// half that has to keep working when the base is read at the path rather than at the root.
	if got := Str(narrow[1].To, "stage"); got != "reviewing" {
		t.Errorf("the last narrowed event says stage=%q, want the entity as it stood (reviewing)", got)
	}
	// The same commits the wide replay carried for that slice, and the wide one carried more.
	if len(wide) <= len(narrow) {
		t.Errorf("the whole-verse replay carried %d events and the slice %d; the other slice's "+
			"write is missing from the wide one", len(wide), len(narrow))
	}
}

// A LONG CATCH-UP COMPLETES, rather than being killed partway as a consumer of its own replay.
//
// The watch has a bounded buffer and logd fails a consumer that does not keep up. Levelling
// each delta inside the receive — a patch applied to the whole document, then the entity walk
// over it — is the most expensive work in the verse, and putting it between one event and the
// next filled that buffer: this replay was cut off after about a thousand commits, and the
// caller was handed a truncated catch-up reported as a failure it could do nothing about
// (git-issue 3qf2z59dh12ksdazg9n0). 1500 is comfortably past where that happened.
func TestALongCatchUpCompletes(t *testing.T) {
	s := openStore(t)
	start := s.Rev()
	const n = 1500
	for i := range n {
		set(t, s, RefAt("demo", "pr", fmt.Sprintf("%d", i)), Obj{"stage": "open"}.Payload())
	}
	commits, err := s.Since(start, ordinary()...)
	if err != nil {
		t.Fatalf("a %d-commit catch-up failed after %d commits: %v", n, len(commits), err)
	}
	if len(commits) < n {
		t.Fatalf("the catch-up carried %d commits of %d, and said it had caught up", len(commits), n)
	}
	// The last one is levelled, not just delivered: the base document is read once for the
	// whole replay and advanced per commit, which is the property draining must not lose.
	last := commits[len(commits)-1].Changes
	if len(last) != 1 || Str(last[0].To, "stage") != "open" {
		t.Fatalf("the last replayed commit does not carry the entity as it stood: %+v", last)
	}
}

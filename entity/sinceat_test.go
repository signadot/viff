package entity

import (
	"errors"
	"strings"
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
		nc, err := s.Since(start, "demo", "pr")
		if err != nil {
			return false
		}
		wc, err := s.Since(start)
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

// A MOUNTED SLICE HAS NO HISTORY, and says so rather than replaying an empty catch-up
// successfully off the log-backed base underneath it.
func TestSinceAtAMountedSliceIsRefused(t *testing.T) {
	s := openStore(t)
	_, err := s.Since(0, mountedKinds[0].System, mountedKinds[0].KindPrefix+"x")
	if err == nil {
		t.Fatal("a replay of a mounted slice succeeded; a controller-backed slice has no commits")
	}
	if errors.Is(err, ErrReplayTooOld) {
		t.Errorf("a mounted slice was reported as an expired cursor, which would send a "+
			"consumer resyncing over a thing that has no history at all: %v", err)
	}
	if !strings.Contains(err.Error(), "no commits to replay") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

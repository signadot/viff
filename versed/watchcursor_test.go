package versed

import (
	"strings"
	"testing"
)

// A NEGATIVE CURSOR COUNTS BACK FROM NOW. `--since -20` is the last twenty commits, which is
// the cursor somebody actually has — a revision number means reading `storeRev` off status and
// subtracting, which is a round trip and a race against the head moving.
//
// It is N REVISIONS back, not N events: revisions are sparse (one commit can carry many entity
// changes, and a narrowed watch sees only some of them), so the span usually holds fewer. What
// the arithmetic promises is only that `-3` at head 6 is the same cursor as `3`.
func TestWatchCursorCountsBackFromTheHead(t *testing.T) {
	head := func() int64 { return 6 }
	for _, c := range []struct {
		raw     string
		since   int64
		catchUp bool
	}{
		{"", -1, false},  // absent: stream from now, which is not `since=0`
		{"0", 0, true},   // from the beginning, for a caller that means it
		{"3", 3, true},   // an absolute revision is untouched
		{"-3", 3, true},  // …and the last three is the same replay at head 6
		{"-1", 5, true},  // the last one
		{"-6", 0, true},  // the whole of it, exactly
		{"-99", 0, true}, // below the beginning IS the beginning
		{"9", 9, true},   // ahead of the head is the store's problem, not the cursor's
	} {
		since, catchUp, err := watchCursor(c.raw, head)
		if err != nil {
			t.Errorf("since=%q: %v", c.raw, err)
			continue
		}
		if since != c.since || catchUp != c.catchUp {
			t.Errorf("since=%q → (%d, %v), want (%d, %v)", c.raw, since, catchUp, c.since, c.catchUp)
		}
	}
}

// THE HEAD IS READ WHEN THE CURSOR IS NEEDED, not before: it is a func so the revision this
// subtracts from is the one the watch is about to start at.
func TestWatchCursorReadsTheHeadOnlyWhenItIsRelative(t *testing.T) {
	asked := 0
	head := func() int64 { asked++; return 10 }
	for _, raw := range []string{"", "0", "4"} {
		if _, _, err := watchCursor(raw, head); err != nil {
			t.Fatal(err)
		}
	}
	if asked != 0 {
		t.Errorf("an absolute cursor asked the store for its head %d time(s)", asked)
	}
	if _, _, err := watchCursor("-4", head); err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Errorf("a relative cursor asked for the head %d times, want once", asked)
	}
}

// A cursor that is not a number is a refusal that says what an absent one does, since that is
// the thing the caller probably wanted.
func TestWatchCursorRefusesWhatIsNotARevision(t *testing.T) {
	_, _, err := watchCursor("yesterday", func() int64 { return 1 })
	if err == nil {
		t.Fatal("`yesterday` was accepted as a cursor")
	}
	if !strings.Contains(err.Error(), "absent one streams from now") {
		t.Errorf("the refusal does not say what absent means: %v", err)
	}
}

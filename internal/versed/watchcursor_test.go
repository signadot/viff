package versed

import (
	"strings"
	"testing"
)

// A CURSOR IS PASSED THROUGH, negative included. `since=-20` — `--ago 20` at the CLI, which is
// where a leading dash is ambiguous — is the last twenty commits, and the STORE resolves it — against its watermark at the moment the watch is established, clamped
// to its retained floor. Verse could subtract from a head it had just read, but it could not
// clamp to a floor it cannot see, so a deep `-N` on a compacted store would come back refused
// where the store would have answered from what it kept.
func TestWatchCursorPassesTheCursorThrough(t *testing.T) {
	for _, c := range []struct {
		raw     string
		since   int64
		catchUp bool
	}{
		{"", 0, false},   // absent: stream from now, and `catchUp` is what says so
		{"0", 0, true},   // …which is NOT `since=0`, the whole retained log
		{"3", 3, true},   // an absolute revision, untouched
		{"-3", -3, true}, // …and a relative one, equally untouched
		{"-999", -999, true},
		{"9", 9, true}, // ahead of the head is the store's problem, not the cursor's
	} {
		since, catchUp, err := watchCursor(c.raw)
		if err != nil {
			t.Errorf("since=%q: %v", c.raw, err)
			continue
		}
		if since != c.since || catchUp != c.catchUp {
			t.Errorf("since=%q → (%d, %v), want (%d, %v)", c.raw, since, catchUp, c.since, c.catchUp)
		}
	}
}

// ABSENT IS SAID BY catchUp, and it has to be: the sentinel for "no cursor" used to be
// `since = -1`, which is a real cursor now — the last commit.
func TestAbsentIsNotACursor(t *testing.T) {
	_, catchUp, err := watchCursor("")
	if err != nil {
		t.Fatal(err)
	}
	if catchUp {
		t.Error("an absent cursor asked for a replay")
	}
	if _, relative, _ := watchCursor("-1"); !relative {
		t.Error("`-1` was read as absence rather than as the cursor it now is")
	}
}

// A cursor that is not a number is a refusal that says what an absent one does, since that is
// the thing the caller probably wanted.
func TestWatchCursorRefusesWhatIsNotARevision(t *testing.T) {
	_, _, err := watchCursor("yesterday")
	if err == nil {
		t.Fatal("`yesterday` was accepted as a cursor")
	}
	if !strings.Contains(err.Error(), "absent one streams from now") {
		t.Errorf("the refusal does not say what absent means: %v", err)
	}
}

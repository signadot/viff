package entity

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// The property the whole snapshot exists for: a read of it answers as of its revision, and a
// commit landing afterwards is invisible to it — however many times, and in whatever order,
// it is read. Two reads of one snapshot are answers about ONE world.
func TestSnapshotDoesNotSeeLaterWrites(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	set(t, s, ref, Obj{"stage": "open"}.Payload())

	at := s.Rev()
	snap, err := s.AsOf(at)
	if err != nil {
		t.Fatalf("AsOf(%d): %v", at, err)
	}

	// The world moves on, twice, in ways a live read would see.
	set(t, s, ref, Obj{"stage": "reviewed"}.Payload())
	set(t, s, RefAt("demo", "pr", "2"), Obj{"stage": "open"}.Payload())

	e, ok, err := snap.Get(ref)
	if err != nil || !ok {
		t.Fatalf("snapshot Get: ok=%v err=%v", ok, err)
	}
	if got := ToObj(e.Payload)["stage"]; got != "open" {
		t.Fatalf("snapshot Get stage = %v, want open — it read past its own revision", got)
	}
	if n := len(snap.List(nil)); n != 1 {
		t.Fatalf("snapshot List = %d entities, want 1 — a later create was visible", n)
	}
	if snap.Rev() != at {
		t.Fatalf("snapshot Rev = %d, want %d", snap.Rev(), at)
	}

	// And the live store is not pinned by anyone holding a snapshot.
	if got := readObj(t, s, ref)["stage"]; got != "reviewed" {
		t.Fatalf("live read stage = %v, want reviewed", got)
	}
}

// A snapshot at the current revision agrees with the live store, entity for entity. This is
// what makes AsOf usable as a drop-in for a live read: taking one must not change any answer,
// only fix when it was true.
func TestSnapshotAtHeadAgreesWithLiveStore(t *testing.T) {
	s := openStore(t)
	set(t, s, RefAt("demo", "pr", "1"), Obj{"stage": "open"}.Payload())
	set(t, s, RefAt("demo", "task", "a"), Obj{"n": 3}.Payload())
	set(t, s, RefAt("other", "thing", "z"), ir.FromString("scalar"))

	snap, err := s.AsOf(s.Rev())
	if err != nil {
		t.Fatalf("AsOf: %v", err)
	}
	live := s.List(nil)
	got := snap.List(nil)
	if len(got) != len(live) {
		t.Fatalf("snapshot List = %d entities, live = %d", len(got), len(live))
	}
	byRef := map[Ref]Payload{}
	for _, e := range got {
		byRef[e.Ref] = e.Payload
	}
	for _, e := range live {
		p, ok := byRef[e.Ref]
		if !ok {
			t.Fatalf("snapshot is missing %s", e.Ref)
		}
		// Compare as read back, not by node identity: a payload arrives through a different
		// read path here (one root document) than there (one Match per call).
		if a, b := ToObj(p), ToObj(e.Payload); len(a) != len(b) {
			t.Fatalf("%s: snapshot payload %v, live %v", e.Ref, a, b)
		}
	}
	// The pred is applied the same way, so a snapshot can stand in for the store in a select.
	demo := snap.List(func(r Ref) bool { return r.Under("demo") })
	if len(demo) != 2 {
		t.Fatalf("snapshot List(demo) = %d, want 2", len(demo))
	}
}

// A removed entity is absence in a snapshot exactly as it is in the store: it has no key to
// walk, so List does not report it, and Get says not-there with a nil error — the read that
// could have failed already happened, when the snapshot was taken.
func TestSnapshotTombstoneIsAbsence(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "gone")
	set(t, s, ref, Obj{"stage": "open"}.Payload())
	remove(t, s, ref)

	snap, err := s.AsOf(s.Rev())
	if err != nil {
		t.Fatalf("AsOf: %v", err)
	}
	if _, ok, err := snap.Get(ref); ok || err != nil {
		t.Fatalf("Get of a tombstone: ok=%v err=%v, want false/nil", ok, err)
	}
	for _, e := range snap.List(nil) {
		if e.Ref == ref {
			t.Fatalf("List returned a tombstoned entity %s", ref)
		}
	}
	// An entity that was never written at all reads the same way.
	if _, ok, err := snap.Get(RefAt("demo", "pr", "never")); ok || err != nil {
		t.Fatalf("Get of a never-written ref: ok=%v err=%v, want false/nil", ok, err)
	}
}

// A revision the store cannot answer at is an ERROR, not a quietly-current read. This is the
// whole reason AsOf returns one: a snapshot that silently fell back to head would be
// indistinguishable from one that worked, at the moment being at the right commit is the
// entire requirement.
func TestSnapshotRefusesAnUnreachableRevision(t *testing.T) {
	s := openStore(t)
	set(t, s, RefAt("demo", "pr", "1"), Obj{"stage": "open"}.Payload())

	if _, err := s.AsOf(s.Rev() + 10_000); err == nil {
		t.Fatal("AsOf a future revision returned a snapshot; want an error")
	}
}

// Reading BACK past a change recovers the state that stood then — the store@(C-1) half of an
// edge, which is what asks "did this predicate only just become true".
func TestSnapshotReadsBackPastAChange(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	set(t, s, ref, Obj{"stage": "open"}.Payload())
	before := s.Rev()
	set(t, s, ref, Obj{"stage": "reviewed"}.Payload())
	after := s.Rev()
	if before == after {
		t.Fatal("the write did not advance the revision; the test cannot say anything")
	}

	was, err := s.AsOf(before)
	if err != nil {
		t.Fatalf("AsOf(before): %v", err)
	}
	is, err := s.AsOf(after)
	if err != nil {
		t.Fatalf("AsOf(after): %v", err)
	}
	e, _, _ := was.Get(ref)
	if got := ToObj(e.Payload)["stage"]; got != "open" {
		t.Fatalf("at rev %d stage = %v, want open", before, got)
	}
	e, _, _ = is.Get(ref)
	if got := ToObj(e.Payload)["stage"]; got != "reviewed" {
		t.Fatalf("at rev %d stage = %v, want reviewed", after, got)
	}
}

// A scoped store snapshots its own view: the scope's writes overlaid on the baseline, and
// never the other way round. A snapshot is a view fixed in time, not a different viff.
func TestSnapshotOfAScopeSeesTheOverlay(t *testing.T) {
	s := openStore(t)
	base := RefAt("demo", "pr", "base")
	set(t, s, base, Obj{"stage": "open"}.Payload())

	sc := s.Scope("iso")
	only := RefAt("demo", "pr", "scoped")
	set(t, sc, only, Obj{"stage": "scoped"}.Payload())

	snap, err := sc.AsOf(sc.Rev())
	if err != nil {
		t.Fatalf("scoped AsOf: %v", err)
	}
	if _, ok, _ := snap.Get(only); !ok {
		t.Fatalf("the scope's snapshot cannot see the scope's own write %s", only)
	}
	if _, ok, _ := snap.Get(base); !ok {
		t.Fatalf("the scope's snapshot cannot read through to the baseline %s", base)
	}

	// ...and the baseline's snapshot does not see the scope.
	bsnap, err := s.AsOf(s.Rev())
	if err != nil {
		t.Fatalf("baseline AsOf: %v", err)
	}
	if _, ok, _ := bsnap.Get(only); ok {
		t.Fatalf("a scoped write leaked into the baseline snapshot: %s", only)
	}
}

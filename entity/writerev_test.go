package entity

import "testing"

// A write reports the revision it landed at, and it is THE SAME NUMBER the change stream
// carries for that write.
//
// That correspondence is the whole point of returning it, and it is the one thing here that
// depends on the substrate rather than on this package: logd answers a patch with the commit
// the transaction took, and the root watch stamps its deltas from the same sequence. Nothing
// in verse can enforce that, so it is pinned here — a store that reported a revision from a
// different sequence would let a writer correlate its own write with somebody else's commit,
// which is worse than reporting nothing.
//
// See Store.Write. The engine's cycle throttle keys a causal chain on exactly this equality
// (docs/working/cycle-throttle.md): a firing recognizes the commits it caused by the revisions
// its writes reported.
func TestWriteReportsTheRevisionTheStreamCarries(t *testing.T) {
	s := openStore(t)
	ch, unsub := s.Observe()
	defer unsub()

	ref := RefAt("demo", "pr", "1")
	var last int64
	for i, w := range []Write{
		{At: ref.At(), Value: Obj{"stage": "open"}.Payload()},
		{At: ref.At("stage"), Value: Val("reviewed")},
		{At: ref.At()}, // the removal
	} {
		committed, rev, err := s.Write(w)
		if err != nil || !committed {
			t.Fatalf("write %d: committed=%v err=%v", i, committed, err)
		}
		if rev == 0 {
			t.Fatalf("write %d committed but reported no revision", i)
		}
		if rev <= last {
			t.Fatalf("write %d reported rev %d, not after the previous write's %d", i, rev, last)
		}
		last = rev
		if ev := oneChange(t, ch, "the observed change"); ev.Rev != rev {
			t.Fatalf("write %d landed at rev %d but the stream carries it at %d", i, rev, ev.Rev)
		}
	}
}

// A conditional write that COMMITS reports its revision too. Write takes a different path
// through the substrate when there are preconditions (PatchIf rather than Patch), so the two
// have to be pinned separately or the conditional one can quietly report zero — and every
// write the engine makes under a claim or a CAS is the conditional one.
func TestConditionalWriteReportsItsRevision(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")

	committed, rev, err := s.Write(Write{
		At: ref.At(), Value: Obj{"stage": "open"}.Payload(), When: []Require{Created()},
	})
	if err != nil || !committed {
		t.Fatalf("create: committed=%v err=%v", committed, err)
	}
	if rev == 0 {
		t.Fatal("a committed conditional write reported no revision")
	}
}

// A REFUSED write has no revision, and must say so rather than handing back the revision of
// whatever it collided with. The world saying no is not an error (committed=false, err=nil),
// so the revision is the only field left to get wrong, and a caller that treated a refusal's
// number as its own commit would attribute somebody else's write to itself.
func TestRefusedWriteReportsNoRevision(t *testing.T) {
	s := openStore(t)
	claim := RefAt("run", "claim", "review@demo.pr.1")

	if committed, rev, err := s.Write(Write{
		At: claim.At(), Value: Obj{"by": "first"}.Payload(), When: []Require{Created()},
	}); err != nil || !committed || rev == 0 {
		t.Fatalf("first claim: committed=%v rev=%d err=%v", committed, rev, err)
	}
	committed, rev, err := s.Write(Write{
		At: claim.At(), Value: Obj{"by": "second"}.Payload(), When: []Require{Created()},
	})
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if committed {
		t.Fatal("the second claim won an address already taken")
	}
	if rev != 0 {
		t.Errorf("a refused write reported rev %d; a write that did not happen has no revision", rev)
	}
}

// The revision a write reports survives being read at: AsOf(rev) sees the write, and
// AsOf(rev-1) does not. This is the property the `on` edge is built from (C-1 against C, see
// trigger.evaluate), asked of a revision the writer supplied rather than one it observed.
func TestReportedRevisionIsReadableAsOf(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	mustWrite(t, s, Write{At: ref.At(), Value: Obj{"stage": "open"}.Payload()})

	committed, rev, err := s.Write(Write{At: ref.At("stage"), Value: Val("reviewed")})
	if err != nil || !committed {
		t.Fatalf("write: committed=%v err=%v", committed, err)
	}

	// No wait, and no synchronization with the store's own cursor: AsOf reads SERVER-SIDE at a
	// revision (matchAt → sess.MatchAt), and the revision in hand is the one the server assigned
	// to this write. s.Rev() is a different quantity — the highest commit the root watch has
	// delivered, over every writer — and nothing orders the two. It can stand ahead of this
	// write (another writer committed and was observed first) or behind it (the delta has not
	// arrived yet), so a test that waited on it would be waiting on a condition that neither
	// implies nor is implied by the read succeeding.
	at, err := s.AsOf(rev)
	if err != nil {
		t.Fatalf("read at the revision the write reported: %v", err)
	}
	now, _, _ := at.Get(ref)
	if got := Str(now.Payload, "stage"); got != "reviewed" {
		t.Errorf("stage at rev %d = %q, want the value written there", rev, got)
	}
	at1, err := s.AsOf(rev - 1)
	if err != nil {
		t.Fatalf("read at rev-1: %v", err)
	}
	before, _, _ := at1.Get(ref)
	if got := Str(before.Payload, "stage"); got != "open" {
		t.Errorf("stage at rev %d = %q, want the value from before the write", rev-1, got)
	}
}

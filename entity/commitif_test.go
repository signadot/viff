package entity

import "testing"

// A conditional write is the airtight substrate for a revocable, terminal-once motion
// commit — docd's cross-path PatchIf, checked here through the same call the engine makes.

func TestConditionalCommit(t *testing.T) { conditionalCommit(t, openStore(t)) }

func TestConditionalCommitRevoke(t *testing.T) { conditionalCommitRevoke(t, openStore(t)) }

// conditionalCommit: the write lands when the witness ballots hold AND the channel is still
// open, and terminal-once holds — a second commit fails once status flips to closed.
func conditionalCommit(t *testing.T, s Store) {
	t.Helper()
	ch := RefAt("motion", "channel", "d1")
	set(t, s, ch, Obj{"status": "open", "gate": "approvals"}.Payload())
	set(t, s, RefAt("vote", "d1", "alice"), Obj{"choice": "approve"}.Payload())
	set(t, s, RefAt("vote", "d1", "bob"), Obj{"choice": "approve"}.Payload())

	stillOpen := Require{Pattern: Obj{"status": "open"}.Payload()} // about the write's own path
	witness := Require{At: Path{Ref: RefAt("vote", "d1")}, Pattern: Obj{
		"alice": map[string]any{"choice": "approve"},
		"bob":   map[string]any{"choice": "approve"},
	}.Payload()}

	committed, _, err := s.Write(Write{
		At:    ch.At(),
		Value: Obj{"status": "closed", "outcome": "proceed", "gate": "approvals"}.Payload(),
		When:  []Require{stillOpen, witness},
	})
	if err != nil || !committed {
		t.Fatalf("commit should succeed: committed=%v err=%v", committed, err)
	}
	got, _, _ := s.Get(ch)
	if ToObj(got.Payload)["outcome"] != "proceed" || ToObj(got.Payload)["gate"] != "approvals" {
		t.Fatalf("committed payload = %+v, want outcome=proceed gate=approvals (open fields preserved)", got.Payload)
	}

	// terminal-once: status is now "closed", so the same precondition fails.
	committed2, _, err := s.Write(Write{
		At:    ch.At(),
		Value: Obj{"status": "closed", "outcome": "deny"}.Payload(),
		When:  []Require{stillOpen, witness},
	})
	if err != nil {
		t.Fatalf("second commit err: %v", err)
	}
	if committed2 {
		t.Fatal("terminal-once violated: a second commit landed after the motion closed")
	}
	if got, _, _ := s.Get(ch); ToObj(got.Payload)["outcome"] != "proceed" {
		t.Fatalf("outcome changed after the latch: %+v", got.Payload)
	}
}

// conditionalCommitRevoke: a witness naming a ballot that isn't present must NOT commit —
// the cross-entity precondition (vote subtree) is what makes the commit revocable.
func conditionalCommitRevoke(t *testing.T, s Store) {
	t.Helper()
	ch := RefAt("motion", "channel", "d2")
	set(t, s, ch, Obj{"status": "open"}.Payload())
	set(t, s, RefAt("vote", "d2", "alice"), Obj{"choice": "approve"}.Payload())

	stillOpen := Require{Pattern: Obj{"status": "open"}.Payload()}
	witness := Require{At: Path{Ref: RefAt("vote", "d2")}, Pattern: Obj{
		"alice": map[string]any{"choice": "approve"},
		"bob":   map[string]any{"choice": "approve"}, // bob never voted → precondition fails
	}.Payload()}

	committed, _, err := s.Write(Write{
		At:    ch.At(),
		Value: Obj{"status": "closed", "outcome": "proceed"}.Payload(),
		When:  []Require{stillOpen, witness},
	})
	if err != nil {
		t.Fatalf("commit err: %v", err)
	}
	if committed {
		t.Fatal("commit should have been refused: bob's witness ballot is absent")
	}
	if got, _, _ := s.Get(ch); ToObj(got.Payload)["status"] != "open" {
		t.Fatalf("channel must stay open on a refused commit: %+v", got.Payload)
	}
}

// A precondition is a tony match, not an object shape — which is what makes "nothing is
// there" sayable. That question is the one a deterministic id needs: claiming an address
// atomically turns a race between two firings into a race the STORE settles, with exactly one
// winner, rather than one settled by an in-memory check that no restart survives.
func TestClaimIsCreateOnly(t *testing.T) { claimCreateOnly(t, openStore(t)) }

func claimCreateOnly(t *testing.T, store Store) {
	t.Helper()
	claim := RefAt("run", "claim", "review@demo:pr:1")

	won, _, err := store.Write(Write{At: claim.At(), Value: Obj{"by": "first"}.Payload(), When: []Require{Created()}})
	if err != nil || !won {
		t.Fatalf("first claim: committed=%v err=%v; want the create to win", won, err)
	}
	// the echo: same address, same deterministic id, and it must lose
	won, _, err = store.Write(Write{At: claim.At(), Value: Obj{"by": "second"}.Payload(), When: []Require{Created()}})
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if won {
		t.Fatal("a second claim on a taken address won; the id is not a claim")
	}
	// and the first writer's value stands — a lost claim writes nothing at all
	e, ok, err := store.Get(claim)
	if err != nil || !ok {
		t.Fatalf("get claim: ok=%v err=%v", ok, err)
	}
	if got := Str(e.Payload, "by"); got != "first" {
		t.Errorf("claim holder = %q; want the first writer", got)
	}
}

// A RELEASED claim is takeable again. Absence has ONE shape — no key at the path, whether the
// ref was never written or was deleted, since a removal takes the key with it — and this is
// what pins that a released claim reads as absent to a create-only precondition. It is not a
// lost edge case: releaseRun deletes the claim at the end of every sequence run, so getting it
// wrong lets a sequence run exactly once per subject, forever.
//
// (This used to say absence had two shapes, a no-key one and a key holding a tombstone, and
// absentPath ORed them. The second never existed; see absentPath.)
func TestClaimIsRetakeableAfterRelease(t *testing.T) {
	store := openStore(t)
	claim := RefAt("run", "claim", "review@demo:pr:1")
	take := func() bool {
		t.Helper()
		won, _, err := store.Write(Write{At: claim.At(), Value: Obj{"by": "runner"}.Payload(), When: []Require{Created()}})
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		return won
	}

	if !take() {
		t.Fatal("first claim lost on an untouched address")
	}
	remove(t, store, claim) // the run ends: release
	if !take() {
		t.Fatal("a released claim could not be retaken — the next run would never start")
	}
}

// Absent's complement, so the two questions are pinned together rather than one being
// inferred from the other: an object pattern still means "it looks like this", and it must
// NOT be satisfied by an entity that is not there.
func TestPresencePatternDoesNotMatchAbsent(t *testing.T) {
	store := openStore(t)
	missing := RefAt("run", "claim", "nothing-here")
	ok, _, err := store.Write(Write{
		At: RefAt("x", "y", "z").At(), Value: Obj{"a": 1}.Payload(),
		When: []Require{{At: missing.At(), Pattern: Obj{"status": "open"}.Payload()}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if ok {
		t.Error("an object precondition held against an ABSENT entity")
	}
}

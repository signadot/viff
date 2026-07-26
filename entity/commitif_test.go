package entity

import "testing"

// CommitIf is the airtight substrate for a revocable, terminal-once motion commit. These
// exercise both the in-memory store (single-lock atomicity) and the logd-backed store
// (docd cross-path PatchIf) through the same capability, so the two agree.

func TestMemStore_CommitIf(t *testing.T)  { conditionalCommit(t, NewMemStore()) }
func TestLogdStore_CommitIf(t *testing.T) { conditionalCommit(t, openStore(t)) }

func TestMemStore_CommitIfRevoke(t *testing.T)  { conditionalCommitRevoke(t, NewMemStore()) }
func TestLogdStore_CommitIfRevoke(t *testing.T) { conditionalCommitRevoke(t, openStore(t)) }

// conditionalCommit: the write lands when the witness ballots hold AND the channel is still
// open, and terminal-once holds — a second commit fails once status flips to closed.
func conditionalCommit(t *testing.T, s Store) {
	t.Helper()
	cc, ok := s.(ConditionalCommitter)
	if !ok {
		t.Fatal("store is not a ConditionalCommitter")
	}
	ch := Ref{System: "motion", Kind: "channel", ID: "d1"}
	s.Put(ch, Obj{"status": "open", "gate": "approvals"}.Payload())
	s.Put(Ref{System: "vote", Kind: "d1", ID: "alice"}, Obj{"choice": "approve"}.Payload())
	s.Put(Ref{System: "vote", Kind: "d1", ID: "bob"}, Obj{"choice": "approve"}.Payload())

	openPre := Precondition{Ref: ch, Pattern: Obj{"status": "open"}.Payload()}
	witnessPre := Precondition{Ref: Ref{System: "vote", Kind: "d1"}, Pattern: Obj{
		"alice": map[string]any{"choice": "approve"},
		"bob":   map[string]any{"choice": "approve"},
	}.Payload()}

	committed, err := cc.CommitIf(ch, Obj{"status": "closed", "outcome": "proceed", "gate": "approvals"}.Payload(), openPre, witnessPre)
	if err != nil || !committed {
		t.Fatalf("commit should succeed: committed=%v err=%v", committed, err)
	}
	got, _, _ := s.Get(ch)
	if ToObj(got.Payload)["outcome"] != "proceed" || ToObj(got.Payload)["gate"] != "approvals" {
		t.Fatalf("committed payload = %+v, want outcome=proceed gate=approvals (open fields preserved)", got.Payload)
	}

	// terminal-once: status is now "closed", so the same precondition fails.
	committed2, err := cc.CommitIf(ch, Obj{"status": "closed", "outcome": "deny"}.Payload(), openPre, witnessPre)
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
	cc := s.(ConditionalCommitter)
	ch := Ref{System: "motion", Kind: "channel", ID: "d2"}
	s.Put(ch, Obj{"status": "open"}.Payload())
	s.Put(Ref{System: "vote", Kind: "d2", ID: "alice"}, Obj{"choice": "approve"}.Payload())

	openPre := Precondition{Ref: ch, Pattern: Obj{"status": "open"}.Payload()}
	witnessPre := Precondition{Ref: Ref{System: "vote", Kind: "d2"}, Pattern: Obj{
		"alice": map[string]any{"choice": "approve"},
		"bob":   map[string]any{"choice": "approve"}, // bob never voted → precondition fails
	}.Payload()}

	committed, err := cc.CommitIf(ch, Obj{"status": "closed", "outcome": "proceed"}.Payload(), openPre, witnessPre)
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
func TestMemStore_ClaimIsCreateOnly(t *testing.T)  { claimCreateOnly(t, NewMemStore()) }
func TestLogdStore_ClaimIsCreateOnly(t *testing.T) { claimCreateOnly(t, openStore(t)) }

func claimCreateOnly(t *testing.T, store Store) {
	t.Helper()
	cc, ok := store.(ConditionalCommitter)
	if !ok {
		t.Skip("store is not a ConditionalCommitter")
	}
	claim := Ref{System: "run", Kind: "claim", ID: "review@demo:pr:1"}

	won, err := cc.CommitIf(claim, Obj{"by": "first"}.Payload(), Precondition{Ref: claim, Pattern: Absent()})
	if err != nil || !won {
		t.Fatalf("first claim: committed=%v err=%v; want the create to win", won, err)
	}
	// the echo: same address, same deterministic id, and it must lose
	won, err = cc.CommitIf(claim, Obj{"by": "second"}.Payload(), Precondition{Ref: claim, Pattern: Absent()})
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

// Absent's complement, so the two questions are pinned together rather than one being
// inferred from the other: an object pattern still means "it looks like this", and it must
// NOT be satisfied by an entity that is not there.
func TestPresencePatternDoesNotMatchAbsent(t *testing.T) {
	store := NewMemStore()
	missing := Ref{System: "run", Kind: "claim", ID: "nothing-here"}
	ok, err := store.CommitIf(Ref{System: "x", Kind: "y", ID: "z"}, Obj{"a": 1}.Payload(),
		Precondition{Ref: missing, Pattern: Obj{"status": "open"}.Payload()})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if ok {
		t.Error("an object precondition held against an ABSENT entity")
	}
}

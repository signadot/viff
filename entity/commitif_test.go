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
	s.Put(ch, Payload{"status": "open", "gate": "approvals"})
	s.Put(Ref{System: "vote", Kind: "d1", ID: "alice"}, Payload{"choice": "approve"})
	s.Put(Ref{System: "vote", Kind: "d1", ID: "bob"}, Payload{"choice": "approve"})

	openPre := Precondition{Ref: ch, Pattern: map[string]any{"status": "open"}}
	witnessPre := Precondition{Ref: Ref{System: "vote", Kind: "d1"}, Pattern: map[string]any{
		"alice": map[string]any{"choice": "approve"},
		"bob":   map[string]any{"choice": "approve"},
	}}

	committed, err := cc.CommitIf(ch, Payload{"status": "closed", "outcome": "proceed", "gate": "approvals"}, openPre, witnessPre)
	if err != nil || !committed {
		t.Fatalf("commit should succeed: committed=%v err=%v", committed, err)
	}
	got, _ := s.Get(ch)
	if got.Payload["outcome"] != "proceed" || got.Payload["gate"] != "approvals" {
		t.Fatalf("committed payload = %+v, want outcome=proceed gate=approvals (open fields preserved)", got.Payload)
	}

	// terminal-once: status is now "closed", so the same precondition fails.
	committed2, err := cc.CommitIf(ch, Payload{"status": "closed", "outcome": "deny"}, openPre, witnessPre)
	if err != nil {
		t.Fatalf("second commit err: %v", err)
	}
	if committed2 {
		t.Fatal("terminal-once violated: a second commit landed after the motion closed")
	}
	if got, _ := s.Get(ch); got.Payload["outcome"] != "proceed" {
		t.Fatalf("outcome changed after the latch: %+v", got.Payload)
	}
}

// conditionalCommitRevoke: a witness naming a ballot that isn't present must NOT commit —
// the cross-entity precondition (vote subtree) is what makes the commit revocable.
func conditionalCommitRevoke(t *testing.T, s Store) {
	t.Helper()
	cc := s.(ConditionalCommitter)
	ch := Ref{System: "motion", Kind: "channel", ID: "d2"}
	s.Put(ch, Payload{"status": "open"})
	s.Put(Ref{System: "vote", Kind: "d2", ID: "alice"}, Payload{"choice": "approve"})

	openPre := Precondition{Ref: ch, Pattern: map[string]any{"status": "open"}}
	witnessPre := Precondition{Ref: Ref{System: "vote", Kind: "d2"}, Pattern: map[string]any{
		"alice": map[string]any{"choice": "approve"},
		"bob":   map[string]any{"choice": "approve"}, // bob never voted → precondition fails
	}}

	committed, err := cc.CommitIf(ch, Payload{"status": "closed", "outcome": "proceed"}, openPre, witnessPre)
	if err != nil {
		t.Fatalf("commit err: %v", err)
	}
	if committed {
		t.Fatal("commit should have been refused: bob's witness ballot is absent")
	}
	if got, _ := s.Get(ch); got.Payload["status"] != "open" {
		t.Fatalf("channel must stay open on a refused commit: %+v", got.Payload)
	}
}

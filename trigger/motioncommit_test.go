package trigger

import (
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// The commit machinery a who-select still runs on: terminal-once, the revoke guard, and a
// deadline stamped once. It goes with the select axis when that becomes a question too.

// commitMotion latches once and freezes: a second commit (even a contradicting one)
// is a no-op, and the recorded witness is kept.
func TestCommitMotionTerminalOnce(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	ref := entity.VerseRef("motion", "commit", "t1")
	witness := []entity.Ref{entity.VerseRef("motion", "vote", "a"), entity.VerseRef("motion", "vote", "b")}

	if eff, revoked := eng.commitMotion(ref, nil, "proceed", witness); revoked || eff != "proceed" {
		t.Fatalf("first commit: eff=%q revoked=%v, want proceed/false", eff, revoked)
	}
	got, ok, _ := store.Get(ref)
	if !ok || entity.ToObj(got.Payload)["outcome"] != "proceed" {
		t.Fatalf("motion not committed: %+v", got.Payload)
	}
	if w := entity.Strs(got.Payload, "witness"); len(w) != 2 {
		t.Fatalf("witness not recorded: %+v", entity.ToObj(got.Payload)["witness"])
	}
	// a later, contradicting commit is frozen out.
	if eff, _ := eng.commitMotion(ref, nil, "deny", nil); eff != "proceed" {
		t.Fatalf("terminal-once violated: second commit eff=%q, want proceed", eff)
	}
	if got, _, _ := store.Get(ref); entity.ToObj(got.Payload)["outcome"] != "proceed" {
		t.Fatal("committed outcome changed after the latch")
	}
}

// A commit predicated on a formula that no longer holds is revoked — it does not write.
func TestCommitMotionRevokeGuard(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	ref := entity.VerseRef("motion", "commit", "t2")
	quorum := &Formula{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, Facts: Match{"system": "motion", "kind": "vote"}, N: 2}}

	// The witness was captured, but the facts have since dropped (0 votes now) → revoked.
	if _, revoked := eng.commitMotion(ref, quorum, "proceed", nil); !revoked {
		t.Fatal("commit should be revoked when the recheck formula no longer holds")
	}
	if _, ok, _ := store.Get(ref); ok {
		t.Fatal("a revoked commit must not write the motion entity")
	}
}

// The atomic commit path is revocable at the instant of commit: a witness ballot retracted
// before commitMotion runs makes the CAS precondition fail, so the motion is NOT committed —
// no stale proceed. With the witness whole, it commits.
func TestCommitMotionAtomicWitnessRevoke(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	const did = "dtest"
	channel := entity.VerseRef("motion", "channel", did)
	quorum := &Formula{Op: OpAtom, Req: &Requirement{
		Kind:  ReqQuorum,
		Facts: Match{"system": "vote", "kind": did, "value": map[string]any{"choice": "approve"}},
		N:     2,
	}}
	alice := entity.RefAt("vote", did, "alice")
	bob := entity.RefAt("vote", did, "bob")
	entitytest.Set(t, store, channel, entity.Obj{"status": "open", "gate": "approvals"}.Payload())
	entitytest.Set(t, store, alice, entity.Obj{"choice": "approve"}.Payload())
	entitytest.Set(t, store, bob, entity.Obj{"choice": "approve"}.Payload())

	if eff, revoked := eng.commitMotion(channel, quorum, "proceed", []entity.Ref{alice, bob}); revoked || eff != "proceed" {
		t.Fatalf("whole witness: eff=%q revoked=%v, want proceed/false", eff, revoked)
	}
	if got, _, _ := store.Get(channel); entity.ToObj(got.Payload)["outcome"] != "proceed" || entity.ToObj(got.Payload)["gate"] != "approvals" {
		t.Fatalf("committed payload = %+v, want outcome=proceed with open fields preserved", got.Payload)
	}

	const did2 = "dtest2"
	ch2 := entity.VerseRef("motion", "channel", did2)
	q2 := &Formula{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, Facts: Match{"system": "vote", "kind": did2, "value": map[string]any{"choice": "approve"}}, N: 2}}
	a2 := entity.RefAt("vote", did2, "alice")
	b2 := entity.RefAt("vote", did2, "bob")
	entitytest.Set(t, store, ch2, entity.Obj{"status": "open"}.Payload())
	entitytest.Set(t, store, a2, entity.Obj{"choice": "approve"}.Payload())
	entitytest.Set(t, store, b2, entity.Obj{"choice": "approve"}.Payload())
	entitytest.Remove(t, store, b2) // bob retracts — the captured witness no longer holds
	if eff, revoked := eng.commitMotion(ch2, q2, "proceed", []entity.Ref{a2, b2}); !revoked || eff != "" {
		t.Fatalf("retracted witness: eff=%q revoked=%v, want \"\"/true", eff, revoked)
	}
	if got, _, _ := store.Get(ch2); entity.ToObj(got.Payload)["outcome"] != nil {
		t.Fatalf("committed despite a retracted witness ballot: %+v", got.Payload)
	}
}

// deadlineAt stamps an absolute close time on first open and reuses it (never slides it
// forward) on a re-fire — proven deterministically with an injected clock.
func TestDeadlineAtStampAndReuse(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	base := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	eng.SetClock(func() time.Time { return base })
	ref := entity.VerseRef("motion", "channel", "z")

	at1 := eng.deadlineAt(ref, time.Hour)
	if !at1.Equal(base.Add(time.Hour)) {
		t.Fatalf("deadline_at = %v, want base+1h", at1)
	}
	eng.SetClock(func() time.Time { return base.Add(30 * time.Minute) }) // clock moves on
	if at2 := eng.deadlineAt(ref, time.Hour); !at2.Equal(at1) {
		t.Fatalf("deadline slid forward on re-fire: %v != %v", at2, at1)
	}
}

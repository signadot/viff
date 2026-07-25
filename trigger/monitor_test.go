package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
)

// A trigger whose gate is a temporal formula fires only once the formula holds — here a
// quorum of 2 votes — monitored reactively on the store's watch stream. The bound
// resolver (deny) is never consulted: a formula gate is monitored, not posed.
func TestTemporalGateFiresOnQuorum(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{}) // must NOT be consulted for a formula gate
	merged := entity.Ref{System: "result", Kind: "merged", ID: "42"}

	eng.AddTrigger(Trigger{
		Name:    "merge",
		Matches: []entity.Pattern{{System: "change"}},
		Gates: []Gate{{
			Name: "approvals",
			Mode: "ask",
			Formula: &Formula{Op: OpAtom, Req: &Requirement{
				Kind:  ReqQuorum,
				Facts: Match{"system": "decision", "kind": "vote"},
				N:     2,
			}},
		}},
		Action: Action{SetRef: merged.String(), Set: map[string]any{"ok": "yes"}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// Fire the trigger; the formula gate now blocks, monitoring votes.
	store.Put(entity.Ref{System: "change", Kind: "pr", ID: "42"}, entity.Payload{})

	// No votes → must not fire.
	if existsWithin(store, merged, 100*time.Millisecond) {
		t.Fatal("merged before any votes")
	}
	// One vote → still short of quorum(2). Votes go to THIS decision's log (vote:<did>:*).
	pr := entity.Ref{System: "change", Kind: "pr", ID: "42"}
	castVote(store, "merge", "approvals", pr, "alice")
	if existsWithin(store, merged, 80*time.Millisecond) {
		t.Fatal("merged at 1 vote (quorum is 2)")
	}
	// Quorum reached → fires.
	castVote(store, "merge", "approvals", pr, "bob")
	if !existsWithin(store, merged, 2*time.Second) {
		t.Fatal("did not fire after quorum reached")
	}
}

// castBallot writes a voter's ballot into a decision's vote log (vote:<did>:<voter>),
// where did is the deterministic decision id for (trigger, gate, subject).
func castBallot(s entity.Store, tr, gate string, subject entity.Ref, voter, choice string) {
	did := decisionChannelID(tr + "/" + gate + "/" + subject.String())
	s.Put(entity.Ref{System: "vote", Kind: did, ID: voter}, entity.Payload{"choice": choice, "by": voter})
}

func castVote(s entity.Store, tr, gate string, subject entity.Ref, voter string) {
	castBallot(s, tr, gate, subject, voter, "approve")
}

// `quorum until deadline`: proceeds when quorum is reached before the deadline fact.
func TestTemporalUntilProceedsOnQuorum(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	merged := entity.Ref{System: "result", Kind: "merged", ID: "a"}
	eng.AddTrigger(Trigger{
		Name:    "merge",
		Matches: []entity.Pattern{{System: "change"}},
		Gates:   []Gate{{Name: "approvals", Mode: "ask", Formula: quorumUntilDeadline(2)}},
		Action:  Action{SetRef: merged.String(), Set: map[string]any{"ok": "yes"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	pr := entity.Ref{System: "change", Kind: "pr", ID: "a"}
	store.Put(pr, entity.Payload{})
	castVote(store, "merge", "approvals", pr, "x")
	castVote(store, "merge", "approvals", pr, "y")
	if !existsWithin(store, merged, 2*time.Second) {
		t.Fatal("did not proceed on quorum-before-deadline")
	}
}

// `quorum until deadline`: defaults (deny — action does NOT fire) if the deadline fact
// appears before quorum.
func TestTemporalUntilDefaultsOnDeadline(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	merged := entity.Ref{System: "result", Kind: "merged", ID: "b"}
	eng.AddTrigger(Trigger{
		Name:    "merge",
		Matches: []entity.Pattern{{System: "change"}},
		Gates:   []Gate{{Name: "approvals", Mode: "ask", Formula: quorumUntilDeadline(2)}},
		Action:  Action{SetRef: merged.String(), Set: map[string]any{"ok": "yes"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	store.Put(entity.Ref{System: "change", Kind: "pr", ID: "b"}, entity.Payload{})
	// one vote (short of quorum), then the deadline fires first → default deny.
	store.Put(entity.Ref{System: "decision", Kind: "vote", ID: "x"}, entity.Payload{"choice": "approve"})
	store.Put(entity.Ref{System: "decision", Kind: "deadline", ID: "b"}, entity.Payload{"at": "now"})
	if existsWithin(store, merged, 300*time.Millisecond) {
		t.Fatal("fired despite deadline reached before quorum")
	}
}

// A deny-quorum: `approve-quorum(2) until reject-quorum(2)` — enough rejects deny the
// decision (the action does NOT fire), making "no" a first-class outcome. The channel
// closes as denied; separately, reaching the approve-quorum first would proceed.
func TestTemporalDenyQuorum(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	merged := entity.Ref{System: "result", Kind: "merged", ID: "d"}
	eng.AddTrigger(Trigger{
		Name:    "merge",
		Matches: []entity.Pattern{{System: "change"}},
		Gates: []Gate{{Name: "approvals", Mode: "ask", Formula: &Formula{Op: OpUntil, Args: []*Formula{
			{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, N: 2}},                   // approve-quorum → proceed
			{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, Choice: "reject", N: 2}}, // reject-quorum → deny
		}}}},
		Action: Action{SetRef: merged.String(), Set: map[string]any{"ok": "yes"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	pr := entity.Ref{System: "change", Kind: "pr", ID: "d"}
	store.Put(pr, entity.Payload{})
	// One reject — short of the deny-quorum, and no approvals: stays open, must not fire.
	castBallot(store, "merge", "approvals", pr, "alice", "reject")
	if existsWithin(store, merged, 120*time.Millisecond) {
		t.Fatal("fired at 1 reject and 0 approvals")
	}
	// Second reject reaches the deny-quorum → denied; the action never fires.
	castBallot(store, "merge", "approvals", pr, "bob", "reject")
	if existsWithin(store, merged, 500*time.Millisecond) {
		t.Fatal("fired despite a reject-quorum — 'no' should deny")
	}
}

// quorumUntilDeadline builds `quorum(n over decision/vote) until (deadline fact exists)`.
func quorumUntilDeadline(n int) *Formula {
	return &Formula{Op: OpUntil, Args: []*Formula{
		{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, Facts: Match{"system": "decision", "kind": "vote"}, N: n}},
		{Op: OpAtom, Req: &Requirement{Kind: ReqFact, Facts: Match{"system": "decision", "kind": "deadline"}}},
	}}
}

// commitDecision latches once and freezes: a second commit (even a contradicting one)
// is a no-op, and the recorded witness is kept.
func TestCommitDecisionTerminalOnce(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	ref := entity.Ref{System: "decision", Kind: "commit", ID: "t1"}
	witness := []entity.Ref{{System: "decision", Kind: "vote", ID: "a"}, {System: "decision", Kind: "vote", ID: "b"}}

	if eff, revoked := eng.commitDecision(ref, nil, "proceed", witness); revoked || eff != "proceed" {
		t.Fatalf("first commit: eff=%q revoked=%v, want proceed/false", eff, revoked)
	}
	got, ok := store.Get(ref)
	if !ok || got.Payload["outcome"] != "proceed" {
		t.Fatalf("decision not committed: %+v", got.Payload)
	}
	if w, _ := got.Payload["witness"].([]string); len(w) != 2 {
		t.Fatalf("witness not recorded: %+v", got.Payload["witness"])
	}
	// a later, contradicting commit is frozen out.
	if eff, _ := eng.commitDecision(ref, nil, "deny", nil); eff != "proceed" {
		t.Fatalf("terminal-once violated: second commit eff=%q, want proceed", eff)
	}
	if got, _ := store.Get(ref); got.Payload["outcome"] != "proceed" {
		t.Fatal("committed outcome changed after the latch")
	}
}

// A commit predicated on a formula that no longer holds is revoked — it does not write.
func TestCommitDecisionRevokeGuard(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	ref := entity.Ref{System: "decision", Kind: "commit", ID: "t2"}
	quorum := &Formula{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, Facts: Match{"system": "decision", "kind": "vote"}, N: 2}}

	// The witness was captured, but the facts have since dropped (0 votes now) → revoked.
	if _, revoked := eng.commitDecision(ref, quorum, "proceed", nil); !revoked {
		t.Fatal("commit should be revoked when the recheck formula no longer holds")
	}
	if _, ok := store.Get(ref); ok {
		t.Fatal("a revoked commit must not write the decision entity")
	}
}

// The atomic commit path (ConditionalCommitter, e.g. MemStore) is revocable at the instant
// of commit: a witness ballot retracted before commitDecision runs makes the CAS precondition
// fail, so the decision is NOT committed (revoked) — no stale proceed. With the witness whole,
// it commits. This exercises the airtight guard, distinct from the empty-witness fallback in
// TestCommitDecisionRevokeGuard.
func TestCommitDecisionAtomicWitnessRevoke(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	const did = "dtest"
	channel := entity.Ref{System: "decision", Kind: "channel", ID: did}
	quorum := &Formula{Op: OpAtom, Req: &Requirement{
		Kind:  ReqQuorum,
		Facts: Match{"system": "vote", "kind": did, "fields": map[string]any{"choice": "approve"}},
		N:     2,
	}}
	alice := entity.Ref{System: "vote", Kind: did, ID: "alice"}
	bob := entity.Ref{System: "vote", Kind: did, ID: "bob"}
	seed := func() {
		store.Put(channel, entity.Payload{"status": "open", "gate": "approvals"})
		store.Put(alice, entity.Payload{"choice": "approve"})
		store.Put(bob, entity.Payload{"choice": "approve"})
	}

	// Witness whole → commits proceed.
	seed()
	if eff, revoked := eng.commitDecision(channel, quorum, "proceed", []entity.Ref{alice, bob}); revoked || eff != "proceed" {
		t.Fatalf("whole witness: eff=%q revoked=%v, want proceed/false", eff, revoked)
	}
	if got, _ := store.Get(channel); got.Payload["outcome"] != "proceed" || got.Payload["gate"] != "approvals" {
		t.Fatalf("committed payload = %+v, want outcome=proceed with open fields preserved", got.Payload)
	}

	// Fresh decision, a witness ballot retracted before commit → revoked, nothing written.
	const did2 = "dtest2"
	ch2 := entity.Ref{System: "decision", Kind: "channel", ID: did2}
	q2 := &Formula{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, Facts: Match{"system": "vote", "kind": did2, "fields": map[string]any{"choice": "approve"}}, N: 2}}
	a2 := entity.Ref{System: "vote", Kind: did2, ID: "alice"}
	b2 := entity.Ref{System: "vote", Kind: did2, ID: "bob"}
	store.Put(ch2, entity.Payload{"status": "open"})
	store.Put(a2, entity.Payload{"choice": "approve"})
	store.Put(b2, entity.Payload{"choice": "approve"})
	store.Delete(b2) // bob retracts — the captured witness no longer holds
	if eff, revoked := eng.commitDecision(ch2, q2, "proceed", []entity.Ref{a2, b2}); !revoked || eff != "" {
		t.Fatalf("retracted witness: eff=%q revoked=%v, want \"\"/true", eff, revoked)
	}
	if got, _ := store.Get(ch2); got.Payload["outcome"] != nil {
		t.Fatalf("committed despite a retracted witness ballot: %+v", got.Payload)
	}
}

// The Monitor is revocable: it emits true → false → true as quorum is reached, a vote is
// retracted, and it is reached again (safety — the answer stays live until commit).
func TestMonitorRevocable(t *testing.T) {
	store := entity.NewMemStore()
	quorum := &Formula{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, Facts: Match{"system": "decision", "kind": "vote"}, N: 2}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	states := NewMonitor(quorum, store).Watch(ctx)

	if st := recvState(t, states); st.True {
		t.Fatal("initial state should be false (no votes)")
	}
	store.Put(entity.Ref{System: "decision", Kind: "vote", ID: "a"}, entity.Payload{"choice": "approve"})
	store.Put(entity.Ref{System: "decision", Kind: "vote", ID: "b"}, entity.Payload{"choice": "approve"})
	if st := recvState(t, states); !st.True || len(st.Witness) != 2 {
		t.Fatalf("expected true with witness 2 after quorum, got %+v", st)
	}
	store.Delete(entity.Ref{System: "decision", Kind: "vote", ID: "b"})
	if st := recvState(t, states); st.True {
		t.Fatal("expected false after a vote was retracted (revocable)")
	}
	store.Put(entity.Ref{System: "decision", Kind: "vote", ID: "b"}, entity.Payload{"choice": "approve"})
	if st := recvState(t, states); !st.True {
		t.Fatal("expected true again after quorum re-reached")
	}
}

// recvState reads the next monitor state within a short deadline.
func recvState(t *testing.T, ch <-chan State) State {
	t.Helper()
	select {
	case st := <-ch:
		return st
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a monitor state")
		return State{}
	}
}

// deadlineAt stamps an absolute close time on first open and reuses it (never slides it
// forward) on a re-fire — proven deterministically with an injected clock.
func TestDeadlineAtStampAndReuse(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	base := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	eng.SetClock(func() time.Time { return base })
	ref := entity.Ref{System: "decision", Kind: "channel", ID: "z"}

	at1 := eng.deadlineAt(ref, time.Hour)
	if !at1.Equal(base.Add(time.Hour)) {
		t.Fatalf("deadline_at = %v, want base+1h", at1)
	}
	eng.SetClock(func() time.Time { return base.Add(30 * time.Minute) }) // clock moves on
	if at2 := eng.deadlineAt(ref, time.Hour); !at2.Equal(at1) {
		t.Fatalf("deadline slid forward on re-fire: %v != %v", at2, at1)
	}
}

// `quorum until deadline` defaults (deny — no action) when the clock passes the deadline
// before quorum. A short real duration keeps the test quick.
func TestUntilDeadlineDefaults(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyResolver{})
	merged := entity.Ref{System: "result", Kind: "merged", ID: "dl"}
	eng.AddTrigger(Trigger{
		Name:    "merge",
		Matches: []entity.Pattern{{System: "change"}},
		Gates: []Gate{{Name: "g", Mode: "ask", Formula: &Formula{Op: OpUntil, Args: []*Formula{
			{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, N: 2}},
			{Op: OpAtom, Req: &Requirement{Kind: ReqDeadline, Within: "40ms"}},
		}}}},
		Action: Action{SetRef: merged.String(), Set: map[string]any{"ok": "y"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	store.Put(entity.Ref{System: "change", Kind: "pr", ID: "dl"}, entity.Payload{})
	if existsWithin(store, merged, 250*time.Millisecond) {
		t.Fatal("fired despite no quorum and a passed deadline")
	}
	// the decision should have closed by deadline default (deny).
	did := decisionChannelID("merge/g/" + entity.Ref{System: "change", Kind: "pr", ID: "dl"}.String())
	ch, _ := store.Get(entity.Ref{System: "decision", Kind: "channel", ID: did})
	if ch.Payload["outcome"] != "deny" {
		t.Fatalf("decision outcome = %v, want deny (deadline default)", ch.Payload["outcome"])
	}
}

// existsWithin polls until ref exists (true) or the deadline passes (false).
func existsWithin(s entity.Store, ref entity.Ref, d time.Duration) bool {
	deadline := time.After(d)
	for {
		if _, ok := s.Get(ref); ok {
			return true
		}
		select {
		case <-deadline:
			return false
		case <-time.After(5 * time.Millisecond):
		}
	}
}

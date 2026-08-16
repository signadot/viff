package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// A trigger whose gate is a temporal formula fires only once the formula holds — here a
// quorum of 2 votes — monitored reactively on the store's watch stream. The bound
// resolver (deny) is never consulted: a formula gate is monitored, not posed.
func TestTemporalGateFiresOnQuorum(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{}) // must NOT be consulted for a formula gate
	merged := entity.RefAt("result", "merged", "42")

	eng.AddTrigger(Trigger{
		Name:      "merge",
		Condition: Condition{Subject: mm(merged.String()), When: MatchWhen(Match{"system": "change"})},
		Gates: []Gate{{
			Name: "approvals",
			Mode: "ask",
			Formula: &Formula{Op: OpAtom, Req: &Requirement{
				Kind:  ReqQuorum,
				Facts: Match{"system": "motion", "kind": "vote"},
				N:     2,
			}},
		}},
		Action: Action{Patch: entity.Obj{"ok": "yes"}.Payload()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// Fire the trigger; the formula gate now blocks, monitoring votes.
	entitytest.Set(t, store, entity.RefAt("change", "pr", "42"), entity.Obj{}.Payload())

	// No votes → must not fire.
	if existsWithin(store, merged, 100*time.Millisecond) {
		t.Fatal("merged before any votes")
	}
	// One vote → still short of quorum(2). Votes go to THIS motion's log (vote:<did>:*),
	// which is keyed by the SUBJECT the firing is about, not by the change that dispatched it.
	castVote(t, store, "merge", "approvals", merged, "alice")
	if existsWithin(store, merged, 80*time.Millisecond) {
		t.Fatal("merged at 1 vote (quorum is 2)")
	}
	// Quorum reached → fires.
	castVote(t, store, "merge", "approvals", merged, "bob")
	if !existsWithin(store, merged, 2*time.Second) {
		t.Fatal("did not fire after quorum reached")
	}
}

// castBallot writes a voter's ballot into a motion's vote log (vote:<did>:<voter>), where did
// is the deterministic motion id for (trigger, gate, SUBJECTS) — what the firing is about, not
// the entity whose change dispatched it. These used to be handed the cause, which is the
// mis-keying the subject-keyed channel fixed: a completion trigger's motion belonged to the
// task that finished rather than to the PR being decided about.
func castBallot(t *testing.T, s entity.Store, tr, gate string, subject entity.Ref, voter, choice string) {
	t.Helper()
	did := motionChannelID(gateID(tr, gate, []entity.Ref{subject}))
	entitytest.Set(t, s, entity.RefAt("vote", did, voter), entity.Obj{"choice": choice, "by": voter}.Payload())
}

func castVote(t *testing.T, s entity.Store, tr, gate string, subject entity.Ref, voter string) {
	t.Helper()
	castBallot(t, s, tr, gate, subject, voter, "approve")
}

// `quorum until deadline`: proceeds when quorum is reached before the deadline fact.
func TestTemporalUntilProceedsOnQuorum(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	merged := entity.RefAt("result", "merged", "a")
	eng.AddTrigger(Trigger{
		Name:      "merge",
		Condition: Condition{Subject: mm(merged.String()), When: MatchWhen(Match{"system": "change"})},
		Gates:     []Gate{{Name: "approvals", Mode: "ask", Formula: quorumUntilDeadline(2)}},
		Action:    Action{Patch: entity.Obj{"ok": "yes"}.Payload()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	pr := entity.RefAt("change", "pr", "a")
	entitytest.Set(t, store, pr, entity.Obj{}.Payload())
	castVote(t, store, "merge", "approvals", merged, "x")
	castVote(t, store, "merge", "approvals", merged, "y")
	if !existsWithin(store, merged, 2*time.Second) {
		t.Fatal("did not proceed on quorum-before-deadline")
	}
}

// `quorum until deadline`: defaults (deny — action does NOT fire) if the deadline fact
// appears before quorum.
func TestTemporalUntilDefaultsOnDeadline(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	merged := entity.RefAt("result", "merged", "b")
	eng.AddTrigger(Trigger{
		Name:      "merge",
		Condition: Condition{Subject: mm(merged.String()), When: MatchWhen(Match{"system": "change"})},
		Gates:     []Gate{{Name: "approvals", Mode: "ask", Formula: quorumUntilDeadline(2)}},
		Action:    Action{Patch: entity.Obj{"ok": "yes"}.Payload()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, entity.RefAt("change", "pr", "b"), entity.Obj{}.Payload())
	// one vote (short of quorum), then the deadline fires first → default deny.
	entitytest.Set(t, store, entity.VerseRef("motion", "vote", "x"), entity.Obj{"choice": "approve"}.Payload())
	entitytest.Set(t, store, entity.VerseRef("motion", "deadline", "b"), entity.Obj{"at": "now"}.Payload())
	if existsWithin(store, merged, 300*time.Millisecond) {
		t.Fatal("fired despite deadline reached before quorum")
	}
}

// A deny-quorum: `approve-quorum(2) until reject-quorum(2)` — enough rejects deny the
// motion (the action does NOT fire), making "no" a first-class outcome. The channel
// closes as denied; separately, reaching the approve-quorum first would proceed.
func TestTemporalDenyQuorum(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	merged := entity.RefAt("result", "merged", "d")
	eng.AddTrigger(Trigger{
		Name:      "merge",
		Condition: Condition{Subject: mm(merged.String()), When: MatchWhen(Match{"system": "change"})},
		Gates: []Gate{{Name: "approvals", Mode: "ask", Formula: &Formula{Op: OpUntil, Args: []*Formula{
			{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, N: 2}},                   // approve-quorum → proceed
			{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, Choice: "reject", N: 2}}, // reject-quorum → deny
		}}}},
		Action: Action{Patch: entity.Obj{"ok": "yes"}.Payload()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	pr := entity.RefAt("change", "pr", "d")
	entitytest.Set(t, store, pr, entity.Obj{}.Payload())
	// One reject — short of the deny-quorum, and no approvals: stays open, must not fire.
	castBallot(t, store, "merge", "approvals", merged, "alice", "reject")
	if existsWithin(store, merged, 120*time.Millisecond) {
		t.Fatal("fired at 1 reject and 0 approvals")
	}
	// Second reject reaches the deny-quorum → denied; the action never fires.
	castBallot(t, store, "merge", "approvals", merged, "bob", "reject")
	if existsWithin(store, merged, 500*time.Millisecond) {
		t.Fatal("fired despite a reject-quorum — 'no' should deny")
	}
}

// The open channel names the FIRING's subject — what the room is deciding about — and not
// the entity whose change dispatched it. The two come apart here exactly as they do in a
// charter (`subject` acts on one entity, `when` reads another), and they came apart worse on
// a REPLAY: a level-triggered firing has no dispatching event at all, so `verse docket` named
// the subject `::` for every motion a restart or a fresh install raised.
func TestMotionChannelNamesTheSubject(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	merged := entity.RefAt("result", "merged", "s")
	eng.AddTrigger(Trigger{
		Name:      "merge",
		Condition: Condition{Subject: mm(merged.String()), When: MatchWhen(Match{"system": "change"})},
		Gates: []Gate{{Name: "approvals", Mode: "ask",
			Formula: &Formula{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, N: 2}}}},
		Action: Action{Patch: entity.Obj{"ok": "yes"}.Payload()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, entity.RefAt("change", "pr", "s"), entity.Obj{}.Payload())

	// The channel is keyed by (trigger, gate, subjects) — the same id the ballots are cast
	// into — so finding it at all is half the assertion; what it SAYS is the other half.
	channel := entity.VerseRef("motion", "channel",
		motionChannelID(gateID("merge", "approvals", []entity.Ref{merged})))

	if !existsWithin(store, channel, 2*time.Second) {
		t.Fatal("no channel opened for the formula gate")
	}
	ent, ok, err := store.Get(channel)
	if err != nil || !ok {
		t.Fatalf("get channel: ok=%v err=%v", ok, err)
	}
	if got := entity.Str(ent.Payload, "subject"); got != merged.String() {
		t.Errorf("channel subject = %v, want %q (the firing's subject, not its cause)", got, merged)
	}
}

// quorumUntilDeadline builds `quorum(n over motion/vote) until (deadline fact exists)`.
func quorumUntilDeadline(n int) *Formula {
	return &Formula{Op: OpUntil, Args: []*Formula{
		{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, Facts: Match{"system": "motion", "kind": "vote"}, N: n}},
		{Op: OpAtom, Req: &Requirement{Kind: ReqFact, Facts: Match{"system": "motion", "kind": "deadline"}}},
	}}
}

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

// The atomic commit path (ConditionalCommitter) is revocable at the instant
// of commit: a witness ballot retracted before commitMotion runs makes the CAS precondition
// fail, so the motion is NOT committed (revoked) — no stale proceed. With the witness whole,
// it commits. This exercises the airtight guard, distinct from the empty-witness fallback in
// TestCommitMotionRevokeGuard.
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
	seed := func() {
		entitytest.Set(t, store, channel, entity.Obj{"status": "open", "gate": "approvals"}.Payload())
		entitytest.Set(t, store, alice, entity.Obj{"choice": "approve"}.Payload())
		entitytest.Set(t, store, bob, entity.Obj{"choice": "approve"}.Payload())
	}

	// Witness whole → commits proceed.
	seed()
	if eff, revoked := eng.commitMotion(channel, quorum, "proceed", []entity.Ref{alice, bob}); revoked || eff != "proceed" {
		t.Fatalf("whole witness: eff=%q revoked=%v, want proceed/false", eff, revoked)
	}
	if got, _, _ := store.Get(channel); entity.ToObj(got.Payload)["outcome"] != "proceed" || entity.ToObj(got.Payload)["gate"] != "approvals" {
		t.Fatalf("committed payload = %+v, want outcome=proceed with open fields preserved", got.Payload)
	}

	// Fresh motion, a witness ballot retracted before commit → revoked, nothing written.
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

// `quorum until deadline` defaults (deny — no action) when the clock passes the deadline
// before quorum. A short real duration keeps the test quick.
func TestUntilDeadlineDefaults(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	merged := entity.RefAt("result", "merged", "dl")
	eng.AddTrigger(Trigger{
		Name:      "merge",
		Condition: Condition{Subject: mm(merged.String()), When: MatchWhen(Match{"system": "change"})},
		Gates: []Gate{{Name: "g", Mode: "ask", Formula: &Formula{Op: OpUntil, Args: []*Formula{
			{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, N: 2}},
			{Op: OpAtom, Req: &Requirement{Kind: ReqDeadline, Within: "40ms"}},
		}}}},
		Action: Action{Patch: entity.Obj{"ok": "y"}.Payload()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, entity.RefAt("change", "pr", "dl"), entity.Obj{}.Payload())
	if existsWithin(store, merged, 250*time.Millisecond) {
		t.Fatal("fired despite no quorum and a passed deadline")
	}
	// the motion should have closed by deadline default (deny).
	did := motionChannelID(gateID("merge", "g", []entity.Ref{merged}))
	ch, _, _ := store.Get(entity.VerseRef("motion", "channel", did))
	if entity.ToObj(ch.Payload)["outcome"] != "deny" {
		t.Fatalf("motion outcome = %v, want deny (deadline default)", entity.ToObj(ch.Payload)["outcome"])
	}
}

// existsWithin polls until ref exists (true) or the deadline passes (false).
func existsWithin(s entity.Store, ref entity.Ref, d time.Duration) bool {
	deadline := time.After(d)
	for {
		if _, ok, _ := s.Get(ref); ok {
			return true
		}
		select {
		case <-deadline:
			return false
		case <-time.After(5 * time.Millisecond):
		}
	}
}

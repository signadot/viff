package trigger

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/process"
)

// A NO IS A DECISION, AND A DECISION HAS A CONSEQUENCE.
//
// Before onDenied a denied firing wrote nothing at all, so the subject was left in the exact
// state that asked the question. Any later commit that touched it re-selected it, the rule
// fired again, and the gate was re-posed under its deterministic id — to somebody who had
// already answered. No rule shape could author its way out of it: the latch a well-formed rule
// carries lives in the ACTION, and on a deny the action never runs.
func TestOnDeniedLatchesTheSubject(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{})
	pr := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	if err := eng.AddTrigger(Trigger{
		Name:      "guard",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Gating: &Gating{Gates: []Gate{{
			Name: "approval", Mode: "ask",
			OnDenied: &Action{Patch: entity.Obj{"stage": "rejected"}.Payload()},
		}}},
		Action: Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, pr, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, pr, "rejected")

	// And it is a latch, not just a write: the subject no longer matches, so nothing touching
	// it asks again. This is the whole point — the question stays answered.
	entitytest.Set(t, store, pr, entity.Obj{"stage": "rejected", "note": "ping"}.Payload())
	time.Sleep(200 * time.Millisecond)
	if got := stageOf(t, store, pr); got != "rejected" {
		t.Fatalf("stage = %q after a later commit, want it to stay rejected", got)
	}
}

// The same on the OTHER arm. A gate carrying a formula is resolved by a motion rather than
// posed as a question — one construct, two implementations — and a refusal there is the same
// refusal: enough rejects deny, and the charter says what that means.
func TestOnDeniedOnAFormulaGate(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyResolver{}) // never consulted: a formula gate is monitored, not posed
	merged := entity.Ref{System: "result", Kind: "merged", ID: "f"}
	if err := eng.AddTrigger(Trigger{
		Name:      "merge",
		Condition: Condition{Subject: mm(merged.String()), When: MatchWhen(Match{"system": "change"})},
		Gating: &Gating{Gates: []Gate{{
			Name: "approvals", Mode: "ask",
			Formula: &Formula{Op: OpUntil, Args: []*Formula{
				{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, N: 2}},
				{Op: OpAtom, Req: &Requirement{Kind: ReqQuorum, Choice: "reject", N: 2}},
			}},
			OnDenied: &Action{Patch: entity.Obj{"outcome": "rejected"}.Payload()},
		}}},
		Action: Action{Patch: entity.Obj{"outcome": "merged"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, entity.Ref{System: "change", Kind: "pr", ID: "f"}, entity.Obj{}.Payload())
	castBallot(t, store, "merge", "approvals", merged, "alice", "reject")
	castBallot(t, store, "merge", "approvals", merged, "bob", "reject")

	for range 100 {
		if e, ok, _ := store.Get(merged); ok {
			if got := entity.Str(e.Payload, "outcome"); got != "" {
				if got != "rejected" {
					t.Fatalf("outcome = %q, want rejected — the action fired past a deny", got)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a rejected motion wrote nothing: onDenied never ran on the formula arm")
}

// A monitor that ENDS is not an answer. Only a committed refusal carries a consequence — a
// resolver that errored, or a run that was cancelled, said nothing at all, and writing a
// rejection on its behalf would put a decision in the store that nobody made.
func TestNoDecisionWritesNothing(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, errResolver{})
	pr := entity.Ref{System: "demo", Kind: "pr", ID: "2"}
	if err := eng.AddTrigger(Trigger{
		Name:      "guard",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Gating: &Gating{Gates: []Gate{{
			Name: "approval", Mode: "ask",
			OnDenied: &Action{Patch: entity.Obj{"stage": "rejected"}.Payload()},
		}}},
		Action: Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, pr, entity.Obj{"stage": "open"}.Payload())
	time.Sleep(250 * time.Millisecond)
	if got := stageOf(t, store, pr); got != "open" {
		t.Fatalf("stage = %q, want open — a resolver ERROR is not a refusal", got)
	}
}

// The note an author gets for the half of the rule they did not write. A warning rather than a
// refusal because re-asking is a legitimate policy: "ask again next time somebody touches it"
// is a real thing to want, and a rule that means it is indistinguishable from one that forgot.
func TestAdviseNamesAnUnlatchedDeny(t *testing.T) {
	gated := func(g Gate) Trigger {
		return Trigger{
			Name:      "guard",
			Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
			Gating:    &Gating{Gates: []Gate{g}},
			Action:    Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
		}
	}
	notes := advise(gated(Gate{Name: "approval", Mode: "ask"}))
	if len(notes) != 1 || !strings.Contains(notes[0], "onDenied") {
		t.Fatalf("a gate with no onDenied earned no usable note: %v", notes)
	}
	if !strings.Contains(notes[0], "stage") {
		t.Fatalf("the note does not name the field to advance: %v", notes[0])
	}
	// Latched: silent.
	if notes := advise(gated(Gate{Name: "approval", Mode: "ask",
		OnDenied: &Action{Patch: entity.Obj{"stage": "rejected"}.Payload()}})); len(notes) != 0 {
		t.Fatalf("a latched deny still earned a note: %v", notes)
	}
	// An onDenied that writes the WRONG field does not latch, and is named.
	if notes := advise(gated(Gate{Name: "approval", Mode: "ask",
		OnDenied: &Action{Patch: entity.Obj{"seen": true}.Payload()}})); len(notes) != 1 {
		t.Fatalf("an onDenied writing an unread field passed as a latch: %v", notes)
	}
	// auto asks nobody, so it refuses nobody.
	if notes := advise(gated(Gate{Name: "approval", Mode: "auto"})); len(notes) != 0 {
		t.Fatalf("an auto gate earned a deny note: %v", notes)
	}
}

// errResolver stands for every way an answer fails to arrive that is not a refusal.
type errResolver struct{}

func (errResolver) Resolve(context.Context, process.GateQuestion, process.ToolUse) (process.Resolution, error) {
	return process.Resolution{}, errors.New("the resolver could not be reached")
}

func stageOf(t *testing.T, s entity.Store, ref entity.Ref) string {
	t.Helper()
	e, ok, err := s.Get(ref)
	if err != nil || !ok {
		return ""
	}
	return entity.Str(e.Payload, "stage")
}

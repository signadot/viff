package trigger

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/task"
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
	pr := entity.RefAt("demo", "pr", "1")
	if err := eng.AddTrigger(Trigger{
		Name:      "guard",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
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

// The same for a gate that takes more than one voice. The tally is the answerer's; what
// reaches the engine is one refusal, and the charter says what that means.
func TestOnDeniedOnAThresholdGate(t *testing.T) {
	store := entitytest.Store(t)
	reg := task.NewRegistry()
	eng := New(store, reg)
	merged := entity.RefAt("result", "merged", "f")
	if err := eng.AddTrigger(Trigger{
		Name:      "merge",
		Condition: Condition{Subject: mm(merged.String()), When: MatchWhen(mm("change.*.*"))},
		Gating: &Gating{Gates: []Gate{{
			Name: "approvals", Need: 2,
			OnDenied: &Action{Patch: entity.Obj{"outcome": "rejected"}.Payload()},
		}}},
		Action: Action{Patch: entity.Obj{"outcome": "merged"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, entity.RefAt("change", "pr", "f"), entity.Obj{}.Payload())
	id := waitQuestionID(t, reg)
	if !reg.Answer(id, task.Choose("deny", "human:alice")) {
		t.Fatalf("Answer(%q) found nothing", id)
	}

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
	t.Fatal("a refused threshold gate wrote nothing: onDenied never ran")
}

// A monitor that ENDS is not an answer. Only a committed refusal carries a consequence — a
// resolver that errored, or a run that was cancelled, said nothing at all, and writing a
// rejection on its behalf would put a decision in the store that nobody made.
func TestNoDecisionWritesNothing(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, errResolver{})
	pr := entity.RefAt("demo", "pr", "2")
	if err := eng.AddTrigger(Trigger{
		Name:      "guard",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
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
			Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
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
	// An onDenied that writes the same value back does NOT latch, and the note says so — the
	// key intersection called this one latched, and re-posed the question forever.
	if notes := advise(gated(Gate{Name: "approval", Mode: "ask",
		OnDenied: &Action{Patch: entity.Obj{"stage": "open"}.Payload()}})); len(notes) != 1 {
		t.Fatalf("an onDenied writing the subject's own state back passed as a latch: %v", notes)
	}
}

// A COMPOUND subject predicate earned advice it had not earned: `predicateKeys` reported a root
// operator's tag name as the field it read, so no onDenied could ever intersect it and every
// gated rule with one was told to write a field called `and`. It is a warning rather than a
// refusal, so nothing broke — which made it worse, since what it said was simply wrong.
func TestAdviseReadsACompoundPredicate(t *testing.T) {
	compound := ir.FromSlice([]*ir.Node{
		entity.Obj{"stage": "open"}.Payload(),
		entity.Obj{"urgent": true}.Payload(),
	}).WithTag("!and")
	gated := func(denied entity.Payload) Trigger {
		return Trigger{
			Name:      "guard",
			Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": compound}},
			Gating: &Gating{Gates: []Gate{{Name: "approval", Mode: "ask",
				OnDenied: &Action{Patch: denied}}}},
			Action: Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
		}
	}
	if notes := advise(gated(entity.Obj{"stage": "rejected"}.Payload())); len(notes) != 0 {
		t.Fatalf("a deny that advances one operand of an `!and` was still advised: %v", notes)
	}
	notes := advise(gated(entity.Obj{"seen": true}.Payload()))
	if len(notes) != 1 || !strings.Contains(notes[0], "stage") || !strings.Contains(notes[0], "urgent") {
		t.Fatalf("the note does not name what a compound predicate reads: %v", notes)
	}
	t.Log(notes[0])
}

// errResolver stands for every way an answer fails to arrive that is not a refusal.
type errResolver struct{}

func (errResolver) Resolve(context.Context, task.GateQuestion, task.ToolUse) (task.Resolution, error) {
	return task.Resolution{}, errors.New("the resolver could not be reached")
}

func stageOf(t *testing.T, s entity.Store, ref entity.Ref) string {
	t.Helper()
	e, ok, err := s.Get(ref)
	if err != nil || !ok {
		return ""
	}
	return entity.Str(e.Payload, "stage")
}

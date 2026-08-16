package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// A gate id is a function of (trigger, gate, subjects) and nothing else — so the SAME
// decision, re-reached, is the same question. It used to come off a counter, which made
// every firing a new docket entry.
func TestGateIDIsDeterministicInTheSubjects(t *testing.T) {
	a := []entity.Ref{entity.RefAt("demo", "pr", "1")}
	b := []entity.Ref{entity.RefAt("demo", "pr", "2")}
	both := []entity.Ref{a[0], b[0]}

	if gateID("ship", "ok", a) != gateID("ship", "ok", a) {
		t.Fatal("the same firing produced two ids")
	}
	for _, pair := range [][2]string{
		{gateID("ship", "ok", a), gateID("ship", "ok", b)},    // subject
		{gateID("ship", "ok", a), gateID("hold", "ok", a)},    // trigger
		{gateID("ship", "ok", a), gateID("ship", "sec", a)},   // gate
		{gateID("ship", "ok", a), gateID("ship", "ok", both)}, // one subject vs two
	} {
		if pair[0] == pair[1] {
			t.Fatalf("distinct firings collided on %q", pair[0])
		}
	}
	// The id still splits back to its owner on GateSep, exactly once — that is how an answer
	// finds what posed it (task.GateSep).
	id := gateID("ship", "ok", both)
	owner, rest, ok := strings.Cut(id, task.GateSep)
	if !ok || owner == "" || strings.Contains(rest, task.GateSep) {
		t.Fatalf("gate id %q does not split cleanly on %q", id, task.GateSep)
	}
}

// A trigger evaluated again and again about one subject raises ONE question, and answering it
// releases the work. Two mechanisms hold that up and it is worth knowing both: a firing holds
// its subject while it runs (Engine.claimFlight), so a re-evaluation about a subject already
// firing is not a second firing; and a question's id is a function of (trigger, gate, subject),
// so one posed anyway JOINS the standing question rather than displacing it.
//
// Before either, each firing posed a fresh counter-derived id — filling the docket with copies
// of one decision, and, keyed by id, each new post displacing the last and leaving that firing
// blocked on a question nothing could answer.
func TestRepeatedFiringsShareOneGateQuestion(t *testing.T) {
	store := entitytest.Store(t)
	reg := task.NewRegistry()
	eng := New(store, reg)

	ref := entity.RefAt("demo", "release", "1")
	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		// It latches on `stage`, so once shipped it stops selecting. Until then every write
		// re-evaluates it and asks the same question again.
		Condition: Condition{Subject: Match{"system": "demo", "kind": "release",
			"value": map[string]any{"stage": "open"}}},
		Gates:  []Gate{{Name: "approve", Mode: "ask"}},
		Action: Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	for i := range 4 {
		entitytest.Set(t, store, ref, entity.Obj{"stage": "open", "n": i}.Payload())
	}

	// However many firings, one question stands.
	deadline := time.After(3 * time.Second)
	for {
		p := reg.Pending()
		if len(p) == 1 {
			break
		}
		if len(p) > 1 {
			t.Fatalf("%d questions on the docket for one decision: %+v", len(p), p)
		}
		select {
		case <-deadline:
			t.Fatal("no gate question appeared")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Give any straggler firings a moment to pose their own, if they were going to.
	time.Sleep(200 * time.Millisecond)
	if p := reg.Pending(); len(p) != 1 {
		t.Fatalf("docket grew to %d for one decision: %+v", len(p), p)
	}

	// Answering it once releases the action.
	id := reg.Pending()[0].Question.ID
	if !reg.Answer(id, task.Resolution{Proceed: true, By: "alice"}) {
		t.Fatalf("Answer(%q) found nothing", id)
	}
	waitStage(t, store, ref, "shipped")
	if p := reg.Pending(); len(p) != 0 {
		t.Fatalf("questions still pending after the answer: %+v", p)
	}
}

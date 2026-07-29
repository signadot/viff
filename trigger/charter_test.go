package trigger

import (
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/mergeop"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/process"
)

// restart stands a second engine up over the same substrate, the way `verse up` does after
// `verse down`: a fresh store client, a fresh engine, the charter read back from the verse.
// The old engine's memory is gone; only what the store holds crosses the gap.
func restart(t *testing.T, addr string) (*Engine, entity.Store) {
	t.Helper()
	store := entitytest.StoreAt(t, addr)
	eng := New(store, process.AutoResolver{})
	eng.ObserveScoped(t.Context()) // versed's order: observe, then restore into it
	if err := eng.LoadCharter(); err != nil {
		t.Fatalf("LoadCharter: %v", err)
	}
	return eng, store
}

// A trigger installed before a restart still fires after it. This is the whole point: a
// verse whose processes, sequence positions and claims all survive but whose CHARTER does
// not comes back up with its work in progress intact and nothing left to react to it.
func TestCharterSurvivesARestart(t *testing.T) {
	addr := entitytest.Addr(t)
	first := entitytest.StoreAt(t, addr)
	eng := New(first, process.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	_, store := restart(t, addr)

	ref := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, ref, "reviewed")
}

// A restored trigger acts on the backlog standing in the verse — the same level-triggered
// replay a fresh install has (docs/change-events.md). So a change that arrived while verse
// was down is not lost: the rule that comes back finds it waiting.
func TestRestoredTriggerActsOnWhatArrivedWhileDown(t *testing.T) {
	addr := entitytest.Addr(t)
	first := entitytest.StoreAt(t, addr)
	eng := New(first, process.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	// Down: nothing is observing. The PR opens anyway.
	ref := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, first, ref, entity.Obj{"stage": "open"}.Payload())

	_, store := restart(t, addr)
	waitStage(t, store, ref, "reviewed")
}

// A removal is as durable as an install. Otherwise `verse trigger remove` is a pause: the
// rule stops firing until the next start, then comes back with nobody having reinstalled it.
func TestRemovedTriggerStaysRemoved(t *testing.T) {
	addr := entitytest.Addr(t)
	first := entitytest.StoreAt(t, addr)
	eng := New(first, process.AutoResolver{})
	trig := Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}
	if err := eng.AddTrigger(trig); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	if !eng.RemoveTrigger("review") {
		t.Fatal("RemoveTrigger reported the trigger did not exist")
	}

	back, store := restart(t, addr)
	if got := back.Triggers(); len(got) != 0 {
		t.Fatalf("charter after restart = %v; a removed rule came back", got)
	}
	ref := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	time.Sleep(200 * time.Millisecond)
	if e, _, _ := store.Get(ref); entity.ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("%s stage = %v; the removed rule fires again after a restart", ref, entity.ToObj(e.Payload)["stage"])
	}
}

// The rule that comes back is the rule that went in — operators and all. A charter is full of
// tony tags (`id: !glob`, `patch: {tmp: !delete null}`), and they are what a structural
// payload could not survive until `!raw`: the store's write is a tony PATCH, so those tags
// were EXECUTED by the write rather than stored by it. Under the escape they are stored, so
// the rule is an ordinary document on its entity rather than a string holding one.
func TestCharterRoundTripsOperators(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	// Written the way a charter is, so the operators are the real thing.
	var trig Trigger
	if err := codec.Unmarshal([]byte(`{
		name: hotfix,
		condition: {
			when:    {system: demo, kind: pr, value: {stage: open}},
			subject: {system: demo, kind: pr, id: !glob "hotfix-*"},
		},
		gates:  [{name: approval, mode: ask}],
		action: {
			patch:  {stage: reviewed, tmp: !delete null},
			spawn:  {
				driver: llm,
				task:   "review $subject",
				answer: {class: !or [bug, nit], severity: !or [low, high]},
			},
			onDone: {patch: {stage: shipped}},
		},
	}`), &trig, codec.Tony); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := eng.AddTrigger(trig); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	installed, _ := eng.Trigger("hotfix") // post-desugar: what the engine actually runs

	ent, ok, err := store.Get(CharterRef("hotfix"))
	if err != nil || !ok {
		t.Fatalf("the charter entity does not read back: ok=%v err=%v", ok, err)
	}
	// The spec is STRUCTURE on the entity — a document to navigate, not a string to re-parse.
	spec := entity.Get(ent.Payload, specField)
	if spec == nil || !entity.IsObj(spec) {
		t.Fatalf("%s = %v; want the rule as an object", specField, spec)
	}
	if id := ir.Get(ir.Get(ir.Get(spec, "condition"), "subject"), "id"); id == nil || id.Tag != "!glob" {
		t.Fatalf("condition.subject.id = %v; the operator did not survive as a tag", id)
	}
	back, err := ruleOf(ent)
	if err != nil {
		t.Fatalf("ruleOf: %v", err)
	}
	want, err := installed.ToTonyIR()
	if err != nil {
		t.Fatal(err)
	}
	got, err := back.ToTonyIR()
	if err != nil {
		t.Fatal(err)
	}
	// Compared as DATA (mergeop.RawEqual: tags compared, not evaluated), because the store
	// keeps a value, not a rendering — the layout tags that decide whether an object is
	// written `{a: 1}` or over two lines are not part of what a rule says.
	if !mergeop.RawEqual(got, want) {
		gotDoc, _ := back.ToTony()
		wantDoc, _ := installed.ToTony()
		t.Fatalf("the rule came back changed:\n got: %s\nwant: %s", gotDoc, wantDoc)
	}
	doc, _ := back.ToTony()
	if !strings.Contains(string(doc), "!delete") || !strings.Contains(string(doc), "hotfix-*") {
		t.Fatalf("the round trip dropped an operator: %s", doc)
	}
	// A spawn's ANSWER CONTRACT is a pattern like any other, and it is the reason the field
	// is a document rather than the text of one: it survives the store as the operators it
	// was written as, so what the driver enforces is what the charter said.
	if _, err := answer.Parse(back.Action.Spawn.Answer); err != nil {
		t.Fatalf("the stored answer contract is not usable: %v", err)
	}
	if cls := ir.Get(ir.Get(ir.Get(ir.Get(spec, "action"), "spawn"), "answer"), "class"); cls == nil {
		t.Fatalf("action.spawn.answer.class is not on the entity: %s", doc)
	}
}

// A re-install REPLACES the stored rule rather than merging into it. The spec is written
// under entity.Raw, and that is what a record holding a document needs: merged, a field the
// new version does not have would go on standing in it — a rule firing on a condition nobody
// wrote, which nothing in the charter file would explain.
func TestReinstallReplacesTheRule(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	trig := Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr"}},
		Gates:     []Gate{{Name: "approval", Mode: "ask"}},
		Action:    setStage("reviewed"),
	}
	if err := eng.AddTrigger(trig); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	if !eng.RemoveTrigger("review") {
		t.Fatal("RemoveTrigger reported the trigger did not exist")
	}
	trig.Gates = nil // the gate is gone from the rule
	if err := eng.AddTrigger(trig); err != nil {
		t.Fatalf("AddTrigger (again): %v", err)
	}

	ent, ok, _ := store.Get(CharterRef("review"))
	if !ok {
		t.Fatal("the charter entity does not read back")
	}
	if g := ir.Get(entity.Get(ent.Payload, specField), "gates"); g != nil {
		t.Fatalf("gates = %v; the removed gate survived the re-install", g)
	}
}

// A charter entity nothing can read is reported and SKIPPED, not fatal: the rest of the
// charter is still a program worth running, and one unreadable rule must not cost it.
func TestUnreadableRuleIsReportedAndSkipped(t *testing.T) {
	addr := entitytest.Addr(t)
	first := entitytest.StoreAt(t, addr)
	eng := New(first, process.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, first, CharterRef("junk"), entity.Obj{specField: "this is not a trigger: [ {"}.Payload())

	store := entitytest.StoreAt(t, addr)
	back := New(store, process.AutoResolver{})
	err := back.LoadCharter()
	if err == nil {
		t.Fatal("LoadCharter reported success over an undecodable rule")
	}
	if !strings.Contains(err.Error(), "junk") {
		t.Fatalf("LoadCharter error does not name the rule that failed: %v", err)
	}
	if got := back.Triggers(); len(got) != 1 || got[0].Name != "review" {
		t.Fatalf("charter = %v; one bad rule cost the good one", got)
	}
}

// A rule whose document disagrees with its address is refused rather than reconciled:
// installing under the name and deleting under the address would leave a rule that
// `verse trigger remove` cannot get rid of.
func TestRuleNameMustMatchItsAddress(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	trig := Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr"}},
		Action:    setStage("reviewed"),
	}
	doc, err := trig.ToTonyIR()
	if err != nil {
		t.Fatal(err)
	}
	entitytest.Set(t, store, CharterRef("elsewhere"), entity.Obj{specField: entity.Raw(doc)}.Payload())

	if err := eng.LoadCharter(); err == nil {
		t.Fatal("a rule named 'review' installed from trigger:rule:elsewhere")
	}
	if got := eng.Triggers(); len(got) != 0 {
		t.Fatalf("charter = %v; the misaddressed rule installed anyway", got)
	}
}

// ── goals ────────────────────────────────────────────────────────────────────

// A goal is still being pursued after a restart: its record carries the definition it was
// installed from, so the doing (its owned triggers) and the watching (its monitor) both come
// back. A goal that survived as a status line but stopped being pursued would be the worst
// of both — `verse goals` showing "active" with nothing acting on it.
func TestGoalSurvivesARestart(t *testing.T) {
	addr := entitytest.Addr(t)
	first := entitytest.StoreAt(t, addr)
	eng := New(first, process.AutoResolver{})
	eng.ObserveScoped(t.Context())
	if err := eng.AddGoal(shipGoal()); err != nil {
		t.Fatalf("AddGoal: %v", err)
	}

	back, store := restartWithGoals(t, addr)
	if got := back.Goals(); len(got) != 1 || got[0].Name != "ship" {
		t.Fatalf("goals after restart = %v; want the goal that was still being pursued", got)
	}
	// The doing came back with it.
	pr := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, store, pr, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, pr, "reviewing")
	// And so did the watching: the witness appears, the goal latches, the charter is reaped.
	entitytest.Set(t, store, entity.Ref{System: "ci", Kind: "status", ID: "build"},
		entity.Obj{"state": "green"}.Payload())
	waitTerminal(t, store, "ship", "done")
}

// A goal REACHED while verse was down latches on the way back up. Restoring is an install,
// and an install evaluates doneWhen before it waits — so the witness standing in the verse
// settles the goal rather than waiting for one more change to notice it.
func TestGoalReachedWhileDownLatchesOnRestart(t *testing.T) {
	addr := entitytest.Addr(t)
	first := entitytest.StoreAt(t, addr)
	eng := New(first, process.AutoResolver{})
	eng.ObserveScoped(t.Context())
	if err := eng.AddGoal(shipGoal()); err != nil {
		t.Fatalf("AddGoal: %v", err)
	}
	// Down. The witness appears with nobody watching.
	entitytest.Set(t, first, entity.Ref{System: "ci", Kind: "status", ID: "build"},
		entity.Obj{"state": "green"}.Payload())

	_, store := restartWithGoals(t, addr)
	waitTerminal(t, store, "ship", "done")
}

// A settled goal is not restored: its outcome is latched and its charter was reaped, so
// reinstalling it would recreate the doing for a goal that is over. The record stands as the
// outcome — that is what a terminal being monotonic means.
func TestTerminalGoalIsNotRestored(t *testing.T) {
	addr := entitytest.Addr(t)
	first := entitytest.StoreAt(t, addr)
	eng := New(first, process.AutoResolver{})
	eng.ObserveScoped(t.Context())
	if err := eng.AddGoal(shipGoal()); err != nil {
		t.Fatalf("AddGoal: %v", err)
	}
	entitytest.Set(t, first, entity.Ref{System: "ci", Kind: "status", ID: "build"},
		entity.Obj{"state": "green"}.Payload())
	waitTerminal(t, first, "ship", "done")

	back, store := restartWithGoals(t, addr)
	if got := back.Goals(); len(got) != 0 {
		t.Fatalf("goals after restart = %v; a settled goal was pursued again", got)
	}
	if got := back.Triggers(); len(got) != 0 {
		t.Fatalf("charter after restart = %v; a settled goal's owned rules came back", got)
	}
	// The outcome record itself is untouched by the restart.
	e, ok, _ := store.Get(entity.Ref{System: GoalSystem, Kind: GoalKind, ID: "ship"})
	if !ok || entity.Str(e.Payload, "outcome") != "done" {
		t.Fatalf("the settled goal's record = %v; want its done outcome standing", e.Payload)
	}
}

// A removal is as durable as an install here too: a removed goal must not be pursued again
// by the next start, or `verse goal remove` is a pause rather than a removal.
func TestRemovedGoalStaysRemoved(t *testing.T) {
	addr := entitytest.Addr(t)
	first := entitytest.StoreAt(t, addr)
	eng := New(first, process.AutoResolver{})
	eng.ObserveScoped(t.Context())
	if err := eng.AddGoal(shipGoal()); err != nil {
		t.Fatalf("AddGoal: %v", err)
	}
	if !eng.RemoveGoal("ship") {
		t.Fatal("RemoveGoal reported the goal did not exist")
	}

	back, store := restartWithGoals(t, addr)
	if got := back.Goals(); len(got) != 0 {
		t.Fatalf("goals after restart = %v; a removed goal came back", got)
	}
	if got := back.Triggers(); len(got) != 0 {
		t.Fatalf("charter after restart = %v; a removed goal's owned rules came back", got)
	}
	pr := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, store, pr, entity.Obj{"stage": "open"}.Payload())
	time.Sleep(200 * time.Millisecond)
	if e, _, _ := store.Get(pr); entity.Str(e.Payload, "stage") != "open" {
		t.Fatalf("%s stage = %q; a removed goal's rule fires again after a restart",
			pr, entity.Str(e.Payload, "stage"))
	}
}

// A goal's owned rules are restored BY THE GOAL, not as standalone charter. Written out
// separately they would outlive the reap that ends the goal and keep firing for something
// nobody is pursuing.
func TestGoalOwnedTriggersAreNotStandaloneCharter(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	if err := eng.AddGoal(shipGoal()); err != nil {
		t.Fatalf("AddGoal: %v", err)
	}
	if _, ok, _ := store.Get(CharterRef("ship-review")); ok {
		t.Fatal("a goal's owned rule was written as standalone charter; it would outlive its goal")
	}
	// It is in the goal's own record, which is what brings it back.
	e, ok, _ := store.Get(entity.Ref{System: GoalSystem, Kind: GoalKind, ID: "ship"})
	if !ok {
		t.Fatal("the goal has no record")
	}
	owned := ir.Get(entity.Get(e.Payload, specField), "triggers")
	if owned == nil || len(owned.Values) != 1 || ir.Get(owned.Values[0], "name").String != "ship-review" {
		t.Fatalf("the goal's record does not carry its charter: %v", e.Payload)
	}
}

// shipGoal is one goal with one owned rule (the doing) and a fact witness (the watching).
func shipGoal() GoalDef {
	return GoalDef{
		Name:    "ship",
		Subject: "demo:pr:1",
		DoneWhen: &Formula{Op: OpAtom, Req: &Requirement{
			Kind:  ReqFact,
			Facts: Match{"system": "ci", "kind": "status", "value": map[string]any{"state": "green"}},
		}},
		Triggers: []Trigger{{
			Name: "ship-review",
			Condition: Condition{Subject: Match{"system": "demo", "kind": "pr",
				"value": map[string]any{"stage": "open"}}},
			Action: setStage("reviewing"),
		}},
	}
}

// restartWithGoals is restart plus the goals half of a start — the order versed uses.
func restartWithGoals(t *testing.T, addr string) (*Engine, entity.Store) {
	t.Helper()
	store := entitytest.StoreAt(t, addr)
	eng := New(store, process.AutoResolver{})
	eng.ObserveScoped(t.Context())
	if err := eng.LoadCharter(); err != nil {
		t.Fatalf("LoadCharter: %v", err)
	}
	if err := eng.LoadGoals(); err != nil {
		t.Fatalf("LoadGoals: %v", err)
	}
	return eng, store
}

func waitTerminal(t *testing.T, s entity.Store, name, outcome string) {
	t.Helper()
	ref := entity.Ref{System: GoalSystem, Kind: GoalKind, ID: name}
	deadline := time.After(3 * time.Second)
	for {
		if e, ok, _ := s.Get(ref); ok && entity.Str(e.Payload, "outcome") == outcome {
			return
		}
		select {
		case <-deadline:
			e, _, _ := s.Get(ref)
			t.Fatalf("goal %s did not reach %q; have %v", name, outcome, e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

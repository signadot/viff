package trigger

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"

	"github.com/signadot/verse/codec"
)

// A `when` over task:instance SWEEPS its subject: the firing acts on every entity the
// subject selects, not on the one the completed task happened to name.
//
// That `flow` redirect is gone. It read one field of the changed payload and let it decide
// whether a firing was about one entity or a whole set — a charter could not say which it
// wanted, and a reader of one could not tell. What it did well, advancing exactly the subject
// a finished task was about, is what completion HOOKS do exactly: the engine scopes a hook
// to that spawn's task, a correlation the store cannot fake because the engine spawned it.
// See TestOnDoneAdvancesOnlyItsOwnSubject, and docs/trigger-model.md §Bindings.
func TestCompletionWhenSweepsItsSubject(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	one := entity.RefAt("demo", "pr", "1")
	two := entity.RefAt("demo", "pr", "2")
	entitytest.Set(t, store, one, entity.Obj{"stage": "reviewing"}.Payload())
	entitytest.Set(t, store, two, entity.Obj{"stage": "reviewing"}.Payload())

	if err := eng.AddTrigger(Trigger{
		Name: "reviewed",
		Condition: Condition{
			When:    MatchWhen(Match{"root": "verse", "system": "task", "kind": "instance", "value": map[string]any{"status": "done"}}),
			Subject: mm("demo:pr:*"),
		},
		Action: Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// One completed task, carrying pr:1 as its flow — exactly what mirror.Reflect writes.
	entitytest.Set(t, store, mirror.Ref("rev-1"), entity.Obj{"status": "done", "flow": one.String()}.Payload())

	// BOTH advance. The `flow` field names pr:1 and no longer narrows anything.
	waitStage(t, store, one, "reviewed")
	waitStage(t, store, two, "reviewed")

	// The task entity itself is still not advanced — the firing is about the subject the
	// charter named, which is what the Subject/When split has always been for.
	if e, _, _ := store.Get(mirror.Ref("rev-1")); entity.ToObj(e.Payload)["stage"] != nil {
		t.Fatalf("the action was applied to the matched entity, not the subject: %#v", e.Payload)
	}
}

// $subject is the RESOLVED subject, so a spawn's task template names what the work is about
// rather than whatever entity happened to fire the trigger.
func TestSubjectBindingIsTheResolvedSubject(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	specs := make(chan launch.Spec, 4)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		specs <- spec
		return nil
	})

	// Seeded, because the subject SELECTS from the store: with the `flow` redirect gone, a
	// pattern subject acts on what is really there.
	pr := entity.RefAt("demo", "pr", "7")
	entitytest.Set(t, store, pr, entity.Obj{"stage": "reviewing"}.Payload())

	if err := eng.AddTrigger(Trigger{
		Name: "on-complete",
		Condition: Condition{
			When: MatchWhen(Match{"root": "verse", "system": "task", "kind": "instance", "value": map[string]any{"status": "done"}}),
			// A predicate the action advances past, so the rule has an end state.
			Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "reviewing"}},
		},
		Action: Action{
			Spawn:  &launch.Spec{Prompt: "follow up on $subject"},
			OnDone: &Action{Patch: entity.Obj{"stage": "followed"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, mirror.Ref("p1"), entity.Obj{"status": "done", "flow": pr.String()}.Payload())

	spec := <-specs
	if spec.Prompt != "follow up on "+pr.String() {
		t.Fatalf("$subject did not bind to the resolved subject: %q", spec.Prompt)
	}
	// And the spawned task is tagged with that subject, so its flow is the PR's.
	if spec.Subject != pr.String() {
		t.Fatalf("spawn.Subject = %q, want the resolved subject %q", spec.Subject, pr.String())
	}
}

// $subject is reserved and must win over a same-named payload field. This is what let a
// goal:done fact (which carries `subject`) silently redirect $subject before the binding
// order was fixed; goal latches now also publish `flow` for a trigger to read.
func TestSubjectBindingNotShadowedByPayloadField(t *testing.T) {
	ev := entity.Event{
		Ref: entity.RefAt("goal", "done", "ship"),
		Op:  entity.Set,
		To:  entity.Obj{"subject": "change:pr:42", "flow": "change:pr:42"}.Payload(),
	}
	b := bindingFor(ev.To, ev.Ref)
	if got := b["subject"]; got != ev.Ref.String() {
		t.Fatalf("$subject = %q; a payload field named \"subject\" shadowed the reserved binding "+
			"(want %q)", got, ev.Ref)
	}
	if got := b["flow"]; got != "change:pr:42" {
		t.Fatalf("$flow = %q, want the payload field", got)
	}
}

// A Subject that cannot resolve makes the firing a no-op — and, because it is resolved before
// any gate, it never poses one either.
func TestUnresolvableSubjectDoesNotFire(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, denyIfAsked{t})
	if err := eng.AddTrigger(Trigger{
		Name: "needs-flow",
		Condition: Condition{
			When:    MatchWhen(Match{"root": "verse", "system": "task", "kind": "instance"}),
			Subject: mm("demo:pr:*"),
		},
		Gates:  []Gate{{Mode: "ask"}},
		Action: Action{Patch: entity.Obj{"stage": "x"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// Nothing in the store matches demo:pr:*, so the subject selects nothing and nothing fires
	// — and, because the subject resolves before any gate, no gate is posed either.
	entitytest.Set(t, store, mirror.Ref("p1"), entity.Obj{"status": "done"}.Payload())
	time.Sleep(200 * time.Millisecond)
	if f := eng.Fired(); f != 0 {
		t.Fatalf("fired %d times with an unresolvable subject", f)
	}
}

// denyIfAsked fails the test if a gate is ever posed — proving a firing with no subject does
// not trouble an approver.
type denyIfAsked struct{ t *testing.T }

func (d denyIfAsked) Resolve(context.Context, task.GateQuestion, task.ToolUse) (task.Resolution, error) {
	d.t.Error("a gate was posed for a firing whose subject could not resolve")
	return task.Resolution{Proceed: false}, nil
}

// A charter that cannot mean what it says must fail at INSTALL, not silently match nothing.
func TestAddTriggerRejectsBadCondition(t *testing.T) {
	cases := map[string]struct {
		cond Condition
		want string
	}{
		// The required half. A trigger with no subject says what happened and never what it is
		// about, and nothing else in it can answer that: the action has no target of its own.
		"missing subject": {
			Condition{}, "condition.subject required",
		},
		"empty subject": {
			Condition{Subject: Match{}}, "condition.subject required",
		},
		"temporal when": {
			Condition{Subject: mm("demo:pr:*"), When: &Formula{Op: OpUntil, Args: []*Formula{
				matchFormula(Match{"system": "demo"}),
				{Op: OpAtom, Req: &Requirement{Kind: ReqDeadline, Within: "4h"}},
			}}},
			"temporal",
		},
		"bad when match": {
			Condition{Subject: mm("demo:pr:*"), When: MatchWhen(Match{"sytem": "demo"})}, "condition.when",
		},
		// A subject that has to SELECT — one whose firing is some other entity's change — must
		// name a kind, because that is what the engine selects and spawns for. The same subject
		// with no `when` beside it is fine (TestSubjectOnlyNeedsNoKind): nothing selects then.
		"selecting subject without kind": {
			Condition{When: MatchWhen(Match{"system": "ci"}), Subject: Match{"system": "demo"}},
			"must be an address",
		},
		"bad subject key": {
			Condition{When: MatchWhen(Match{"system": "ci"}), Subject: Match{"sytem": "demo"}},
			"condition.subject",
		},
		// Only `id` globs. A "*" inside system or kind is a typo for the whole segment, and it
		// would match nothing at all — the failure mode Validate exists to turn into an error.
		"globbed kind": {
			Condition{Subject: Match{"system": "demo", "kind": "p*"}},
			"cannot glob",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			eng := New(entitytest.Store(t), task.AutoResolver{})
			err := eng.AddTrigger(Trigger{Name: "t", Condition: c.cond})
			if err == nil {
				t.Fatalf("installed an invalid condition (%+v)", c.cond)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not explain the problem (want mention of %q)", err, c.want)
			}
		})
	}
}

// A subject with no `when` is the whole condition: it dispatches on changes to what it names,
// so it is never asked to select a set out of the store and needs no definite kind. This is the
// case the "must name a kind" rule must NOT catch — the ordinary rule that reacts to a system.
func TestSubjectOnlyNeedsNoKind(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name:      "any-demo-change",
		Condition: Condition{Subject: Match{"system": "demo"}},
		Action:    Action{Patch: entity.Obj{"seen": true}.Payload()},
	}); err != nil {
		t.Fatalf("a subject-only condition over a whole system must install: %v", err)
	}
}

// A subject is a SELECTION, always: the trigger acts on every entity its pattern names at the
// commit, not only on the one that changed. That is the reconciler reading — a rule is a
// statement about the world, so "which entities is it about" cannot depend on who happened to
// be written last.
//
// This test asserted the opposite for as long as the subject was also the cause. The
// difference it was drawing — "when a PR opens, review it" versus "review all of them" — is
// real but is not a difference in dispatch: both settle in the same place, because the action
// advances each PR out of the selection. What changed is how many firings it takes to get
// there, not where it ends up.
func TestSubjectIsASelectionNotTheChangedEntity(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	one := entity.RefAt("demo", "pr", "1")
	two := entity.RefAt("demo", "pr", "2")
	entitytest.Set(t, store, two, entity.Obj{"stage": "open"}.Payload())

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action:    Action{Patch: entity.Obj{"stage": "reviewing"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// Writing pr:1 is the commit that gets the trigger evaluated; the firing is about every
	// open PR at that commit, which is both of them.
	entitytest.Set(t, store, one, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, one, "reviewing")
	waitStage(t, store, two, "reviewing")
}

// An empty `when` matches everything — parity with the old empty pattern list, so the
// ignition-guard/budget behaviour that relied on it is unchanged.
func TestEmptyWhenMatchesEverything(t *testing.T) {
	var f Formula
	if err := codec.Unmarshal([]byte(`{}`), &f, codec.JSON); err != nil {
		t.Fatalf("unmarshal {}: %v", err)
	}
	if !f.IsMatchAtom() {
		t.Fatal("{} should parse as a match atom")
	}
	if !f.MatchEvent(entity.Event{Ref: entity.RefAt("anything", "x", "1"), Op: entity.Set}) {
		t.Fatal("{} should match every change")
	}
}

// `op` is no longer a match dimension, and a charter carrying one is refused with an
// explanation rather than an "unknown key". It classified a CHANGE (set/delete); a match is
// now asked only of STATE, at a revision, where nothing has one.
//
// The name is kept in a removed-keys table for exactly this: the charters that used it were
// written when it meant something, and their authors deserve to be told where the meaning
// went — a create is the rising edge of "this exists", which is what `on` says.
func TestOpIsNoLongerAMatchDimension(t *testing.T) {
	const in = `{"system":"local","kind":"dir-status-abc","op":"set"}`
	var f Formula
	if err := codec.Unmarshal([]byte(in), &f, codec.JSON); err == nil {
		if err := f.Req.Facts.Validate(); err == nil {
			t.Fatalf("a match carrying `op` was accepted: %+v", f)
		} else if !strings.Contains(err.Error(), "on:") {
			t.Errorf("the diagnostic does not say where `op` went: %v", err)
		} else {
			t.Log(err)
		}
	} else if !strings.Contains(err.Error(), "op") {
		t.Fatalf("unmarshal failed for the wrong reason: %v", err)
	} else {
		t.Log(err)
	}
}

// The verbose {op,req,args} node stays accepted (compatibility), and is NOT mistaken for a
// match just because it carries an `op` key.
func TestVerboseFormulaNodeStillParses(t *testing.T) {
	var f Formula
	if err := codec.Unmarshal([]byte(`{"op":"atom","req":{"kind":"quorum","n":2}}`), &f, codec.JSON); err != nil {
		t.Fatalf("verbose node: %v", err)
	}
	if f.Op != OpAtom || f.Req == nil || f.Req.Kind != ReqQuorum || f.Req.N != 2 {
		t.Fatalf("verbose node parsed wrong: %+v", f)
	}
}

// A mixed object is ambiguous and must be rejected outright, never resolved one way in
// silence.
func TestMixedMatchAndTagObjectRejected(t *testing.T) {
	for _, in := range []string{
		`{"system":"demo","quorum":2}`,
		`{"kind":"pr","until":[{"quorum":2}]}`,
	} {
		var f Formula
		if err := codec.Unmarshal([]byte(in), &f, codec.JSON); err == nil {
			t.Fatalf("accepted the ambiguous object %s (as %+v)", in, f)
		}
	}
}

// An unknown single tag must error rather than be accepted as some requirement kind.
func TestUnknownFormulaTagRejected(t *testing.T) {
	var f Formula
	if err := codec.Unmarshal([]byte(`{"whenever":2}`), &f, codec.JSON); err == nil {
		t.Fatalf("accepted an unknown tag as %+v", f)
	}
}

// A `when` round-trips as authored, so `trigger show` reads like the installed charter
// rather than exposing the internal parse node; the subject round-trips as its dimensions.
func TestConditionJSONRoundTrip(t *testing.T) {
	const in = `{"name":"reviewed","condition":{"when":{"system":"task","kind":"instance",` +
		`"value":{"status":"done"}},"subject":"demo:pr:*"},"action":{"patch":{"stage":"reviewed"}}}`
	var tr Trigger
	if err := codec.Unmarshal([]byte(in), &tr, codec.JSON); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := tr.Condition.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// The subject parsed to a demo:pr selector — the address dimensions, not a leftover ref.
	if !tr.Condition.Subject.Selects() {
		t.Fatalf("subject did not parse as a selector: %#v", tr.Condition.Subject)
	}
	out, err := codec.Marshal(&tr, codec.Tony)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// `when` must emit the bare match object — no internal parse node ({op: …, req: …}) leaks.
	var got map[string]any
	if err := codec.Unmarshal(out, &got, codec.Tony); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	cond := got["condition"].(map[string]any)
	if _, leaked := cond["when"].(map[string]any)["op"]; leaked {
		t.Errorf("`when` leaked the internal parse node: %s", out)
	}
	// Re-reading the output yields the same condition.
	var tr2 Trigger
	if err := codec.Unmarshal(out, &tr2, codec.JSON); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if !reflect.DeepEqual(tr.Condition, tr2.Condition) {
		t.Fatalf("condition drifted on round-trip:\n in: %#v\nout: %#v", tr.Condition, tr2.Condition)
	}
}

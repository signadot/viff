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
	"github.com/signadot/verse/process"

	"github.com/signadot/verse/codec"
)

// The COMPLETION PATTERN, which is what the Condition restructure exists to make readable:
// "when a process completes, about the PR it carried, set reviewed". The subject is the
// process's flow, not the process — expressed as a Subject, where it used to need an
// action-level setRef compensating for $subject being wired to the matched entity.
func TestCompletionPatternAdvancesTheCarriedSubject(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})

	pr := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, store, pr, entity.Obj{"stage": "reviewing"}.Payload())

	if err := eng.AddTrigger(Trigger{
		Name: "reviewed",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "process", "kind": "instance", "fields": map[string]any{"status": "done"}}),
			Subject: mm("demo:pr:*"),
		},
		Action: Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// A completed process carrying the PR as its flow — exactly what mirror.Reflect writes.
	entitytest.Set(t, store, mirror.Ref("rev-1"), entity.Obj{"status": "done", "flow": pr.String()}.Payload())

	waitStage(t, store, pr, "reviewed")

	// The process entity itself must NOT have been advanced — the firing was about the PR.
	if e, _, _ := store.Get(mirror.Ref("rev-1")); entity.ToObj(e.Payload)["stage"] != nil {
		t.Fatalf("the action was applied to the matched entity, not the subject: %#v", e.Payload)
	}
}

// $subject is the RESOLVED subject, so a spawn's task template names what the work is about
// rather than whatever entity happened to fire the trigger.
func TestSubjectBindingIsTheResolvedSubject(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})

	specs := make(chan launch.Spec, 4)
	eng.SetSpawner(func(id string, spec launch.Spec) process.Handle {
		specs <- spec
		return nil
	})

	pr := entity.Ref{System: "demo", Kind: "pr", ID: "7"}
	if err := eng.AddTrigger(Trigger{
		Name: "on-complete",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "process", "kind": "instance", "fields": map[string]any{"status": "done"}}),
			Subject: mm("demo:pr:*"),
		},
		Action: Action{Spawn: &launch.Spec{Task: "follow up on $subject"}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, mirror.Ref("p1"), entity.Obj{"status": "done", "flow": pr.String()}.Payload())

	spec := <-specs
	if spec.Task != "follow up on "+pr.String() {
		t.Fatalf("$subject did not bind to the resolved subject: %q", spec.Task)
	}
	// And the spawned process is tagged with that subject, so its flow is the PR's.
	if spec.Subject != pr.String() {
		t.Fatalf("spawn.Subject = %q, want the resolved subject %q", spec.Subject, pr.String())
	}
}

// $subject is reserved and must win over a same-named payload field. This is what let a
// goal:done fact (which carries `subject`) silently redirect $subject before the binding
// order was fixed; goal latches now also publish `flow` for a trigger to read.
func TestSubjectBindingNotShadowedByPayloadField(t *testing.T) {
	ev := entity.Event{
		Ref: entity.Ref{System: "goal", Kind: "done", ID: "ship"},
		Op:  entity.Set,
		To:  entity.Obj{"subject": "change:pr:42", "flow": "change:pr:42"}.Payload(),
	}
	b := bindingFor(ev, ev.Ref)
	if got := b["subject"]; got != ev.Ref.String() {
		t.Fatalf("$subject = %q; a payload field named \"subject\" shadowed the reserved binding "+
			"(want %q)", got, ev.Ref)
	}
	if got := b["flow"]; got != "change:pr:42" {
		t.Fatalf("$flow = %q, want the payload field", got)
	}
}

// A goal latch publishes `flow` alongside `subject`, so a trigger reacting to it names the
// goal's subject the same way a process completion does: a `subject` pattern matched against `flow`.
func TestGoalLatchPublishesFlowForBinding(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, process.AutoResolver{})
	if err := eng.AddGoal(GoalDef{
		Name:     "ship",
		Subject:  "change:pr:42",
		DoneWhen: &Formula{Op: OpAtom, Req: &Requirement{Kind: ReqFact, Facts: Match{"system": "ci", "kind": "status"}}},
	}); err != nil {
		t.Fatalf("AddGoal: %v", err)
	}
	entitytest.Set(t, store, entity.Ref{System: "ci", Kind: "status", ID: "build"}, entity.Obj{"state": "green"}.Payload())

	var latch entity.Payload
	deadline := time.After(2 * time.Second)
	for latch == nil {
		if e, ok, _ := store.Get(goalDoneRef("ship")); ok && entity.ToObj(e.Payload)["goal"] == "ship" {
			latch = e.Payload
			break
		}
		select {
		case <-deadline:
			t.Fatal("goal:done never latched")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if entity.Str(latch, "flow") != "change:pr:42" {
		t.Fatalf("goal:done must carry `flow` for a reacting trigger's subject: %#v", latch)
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
			When:    MatchWhen(Match{"system": "process", "kind": "instance"}),
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

	// No `flow` field: the demo:pr:* subject has nothing to match, so nothing fires (and, because
	// the subject resolves before any gate, no gate is posed either).
	entitytest.Set(t, store, mirror.Ref("p1"), entity.Obj{"status": "done"}.Payload())
	time.Sleep(200 * time.Millisecond)
	if f := eng.Fired(); f != 0 {
		t.Fatalf("fired %d times with an unresolvable subject", f)
	}
}

// denyIfAsked fails the test if a gate is ever posed — proving a firing with no subject does
// not trouble an approver.
type denyIfAsked struct{ t *testing.T }

func (d denyIfAsked) Resolve(context.Context, process.GateQuestion, process.ToolUse) (process.Resolution, error) {
	d.t.Error("a gate was posed for a firing whose subject could not resolve")
	return process.Resolution{Proceed: false}, nil
}

// A charter that cannot mean what it says must fail at INSTALL, not silently match nothing.
func TestAddTriggerRejectsBadCondition(t *testing.T) {
	cases := map[string]struct {
		cond Condition
		want string
	}{
		"missing when": {
			Condition{}, "condition.when required",
		},
		"temporal when": {
			Condition{When: &Formula{Op: OpUntil, Args: []*Formula{
				matchFormula(Match{"system": "demo"}),
				{Op: OpAtom, Req: &Requirement{Kind: ReqDeadline, Within: "4h"}},
			}}},
			"temporal",
		},
		"bad when match": {
			Condition{When: MatchWhen(Match{"sytem": "demo"})}, "condition.when",
		},
		"subject without kind": {
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
			Condition{When: MatchWhen(Match{"system": "demo", "kind": "p*"})},
			"cannot glob",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			eng := New(entitytest.Store(t), process.AutoResolver{})
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
	if !f.MatchEvent(entity.Event{Ref: entity.Ref{System: "anything", Kind: "x", ID: "1"}, Op: entity.Set}) {
		t.Fatal("{} should match every change")
	}
}

// REGRESSION: `op` is both a match dimension and the verbose formula node's discriminator.
// Branching on its presence — as the parser once did — decoded a match carrying `op: "set"`
// as Formula{Op:"set"}, which matches nothing. That is exactly the shape verse connect's
// per-file validator uses, so it silently never fired.
func TestWhenWithOpDimensionIsAMatchNotAVerboseNode(t *testing.T) {
	const in = `{"system":"local","kind":"dir-status-abc","op":"set"}`
	var f Formula
	if err := codec.Unmarshal([]byte(in), &f, codec.JSON); err != nil {
		t.Fatalf("unmarshal %s: %v", in, err)
	}
	if f.Op != OpAtom || !f.IsMatchAtom() {
		t.Fatalf("parsed as %+v; a match carrying `op` must stay a match atom "+
			"(it used to decode as Formula{Op:\"set\"} and match nothing)", f)
	}
	set := entity.Event{Ref: entity.Ref{System: "local", Kind: "dir-status-abc", ID: "a.go"}, Op: entity.Set}
	del := entity.Event{Ref: set.Ref, Op: entity.Delete}
	if !f.MatchEvent(set) {
		t.Fatal("did not match the Set event it selects")
	}
	if f.MatchEvent(del) {
		t.Fatal("matched a Delete event despite op=set")
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
	const in = `{"name":"reviewed","condition":{"when":{"system":"process","kind":"instance",` +
		`"fields":{"status":"done"}},"subject":"demo:pr:*"},"action":{"patch":{"stage":"reviewed"}}}`
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

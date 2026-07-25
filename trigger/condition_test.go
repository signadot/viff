package trigger

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
)

// The COMPLETION PATTERN, which is what the Condition restructure exists to make readable:
// "when a process completes, about the PR it carried, set reviewed". The subject is the
// process's flow, not the process — expressed as a Subject, where it used to need an
// action-level setRef compensating for $subject being wired to the matched entity.
func TestCompletionPatternAdvancesTheCarriedSubject(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})

	pr := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	store.Put(pr, entity.Payload{"stage": "reviewing"})

	if err := eng.AddTrigger(Trigger{
		Name: "reviewed",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "process", "kind": "instance", "fields": map[string]any{"status": "completed"}}),
			Subject: refMatch("$flow"),
		},
		Action: Action{Set: map[string]any{"stage": "reviewed"}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// A completed process carrying the PR as its flow — exactly what mirror.Reflect writes.
	store.Put(mirror.Ref("rev-1"), entity.Payload{"status": "completed", "flow": pr.String()})

	waitStage(t, store, pr, "reviewed")

	// The process entity itself must NOT have been advanced — the firing was about the PR.
	if e, _ := store.Get(mirror.Ref("rev-1")); e.Payload["stage"] != nil {
		t.Fatalf("the action was applied to the matched entity, not the subject: %#v", e.Payload)
	}
}

// $subject is the RESOLVED subject, so a spawn's task template names what the work is about
// rather than whatever entity happened to fire the trigger.
func TestSubjectBindingIsTheResolvedSubject(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})

	specs := make(chan launch.Spec, 4)
	eng.SetSpawner(func(id string, spec launch.Spec) process.ProcHandle {
		specs <- spec
		return nil
	})

	pr := entity.Ref{System: "demo", Kind: "pr", ID: "7"}
	if err := eng.AddTrigger(Trigger{
		Name: "on-complete",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "process", "kind": "instance", "fields": map[string]any{"status": "completed"}}),
			Subject: refMatch("$flow"),
		},
		Action: Action{Spawn: &launch.Spec{Task: "follow up on $subject"}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	store.Put(mirror.Ref("p1"), entity.Payload{"status": "completed", "flow": pr.String()})

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
		To:  entity.Payload{"subject": "change:pr:42", "flow": "change:pr:42"},
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

// A goal latch publishes `flow` alongside `subject`, so a trigger reacting to it carries the
// goal's subject onward with the same idiom as a process completion ("subject": "$flow").
func TestGoalLatchPublishesFlowForBinding(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, process.AutoResolver{})
	if err := eng.AddGoal(GoalDef{
		Name:     "ship",
		Subject:  "change:pr:42",
		DoneWhen: &Formula{Op: OpAtom, Req: &Requirement{Kind: ReqFact, Facts: Match{"system": "ci", "kind": "status"}}},
	}); err != nil {
		t.Fatalf("AddGoal: %v", err)
	}
	store.Put(entity.Ref{System: "ci", Kind: "status", ID: "build"}, entity.Payload{"state": "green"})

	var latch entity.Payload
	deadline := time.After(2 * time.Second)
	for latch == nil {
		if e, ok := store.Get(goalDoneRef("ship")); ok && e.Payload["goal"] == "ship" {
			latch = e.Payload
			break
		}
		select {
		case <-deadline:
			t.Fatal("goal:done never latched")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if latch["flow"] != "change:pr:42" {
		t.Fatalf("goal:done must carry `flow` for a reacting trigger's subject: %#v", latch)
	}
}

// A Subject that cannot resolve makes the firing a no-op — and, because it is resolved before
// any gate, it never poses one either.
func TestUnresolvableSubjectDoesNotFire(t *testing.T) {
	store := entity.NewMemStore()
	eng := New(store, denyIfAsked{t})
	if err := eng.AddTrigger(Trigger{
		Name: "needs-flow",
		Condition: Condition{
			When:    MatchWhen(Match{"system": "process", "kind": "instance"}),
			Subject: refMatch("$flow"),
		},
		Gates:  []Gate{{Mode: "ask"}},
		Action: Action{Set: map[string]any{"stage": "x"}},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// No `flow` field: "$flow" cannot resolve, so nothing fires and no gate is posed.
	store.Put(mirror.Ref("p1"), entity.Payload{"status": "completed"})
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
		"set-valued subject (step 3)": {
			Condition{When: MatchWhen(Match{"system": "ci"}), Subject: Match{"system": "demo", "kind": "pr"}},
			"fan-out is not implemented yet",
		},
		"subject without kind": {
			Condition{When: MatchWhen(Match{"system": "ci"}), Subject: Match{"system": "demo"}},
			"must be a ref",
		},
		"bad subject key": {
			Condition{When: MatchWhen(Match{"system": "ci"}), Subject: Match{"sytem": "demo"}},
			"condition.subject",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			eng := New(entity.NewMemStore(), process.AutoResolver{})
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
	if err := json.Unmarshal([]byte(`{}`), &f); err != nil {
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
	if err := json.Unmarshal([]byte(in), &f); err != nil {
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
	if err := json.Unmarshal([]byte(`{"op":"atom","req":{"kind":"quorum","n":2}}`), &f); err != nil {
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
		if err := json.Unmarshal([]byte(in), &f); err == nil {
			t.Fatalf("accepted the ambiguous object %s (as %+v)", in, f)
		}
	}
}

// An unknown single tag must error rather than be accepted as some requirement kind.
func TestUnknownFormulaTagRejected(t *testing.T) {
	var f Formula
	if err := json.Unmarshal([]byte(`{"whenever":2}`), &f); err == nil {
		t.Fatalf("accepted an unknown tag as %+v", f)
	}
}

// A `when` round-trips as authored, so `trigger show` reads like the installed charter
// rather than exposing the internal parse node.
func TestConditionJSONRoundTrip(t *testing.T) {
	const in = `{"name":"reviewed","condition":{"when":{"system":"process","kind":"instance",` +
		`"fields":{"status":"completed"}},"subject":"$flow"},"action":{"set":{"stage":"reviewed"}}}`
	var tr Trigger
	if err := json.Unmarshal([]byte(in), &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := tr.Condition.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if ref, ok := tr.Condition.Subject.Ref(); !ok || ref != "$flow" {
		t.Fatalf("subject did not parse as the ref sugar: %#v", tr.Condition.Subject)
	}
	out, err := json.Marshal(&tr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Compare structurally, not byte-wise: a Match is a map, so its keys come back sorted.
	// What must hold is the SHAPE — `when` is the bare match object and `subject` is the
	// string sugar, with no internal parse node ({"op":…,"req":…} or {"ref":…}) leaking out.
	var got, want map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if err := json.Unmarshal([]byte(in), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip drifted:\n in: %s\nout: %s", in, out)
	}
	cond := got["condition"].(map[string]any)
	if _, leaked := cond["when"].(map[string]any)["op"]; leaked {
		t.Errorf("`when` leaked the internal parse node: %s", out)
	}
	if _, isString := cond["subject"].(string); !isString {
		t.Errorf("`subject` did not emit its string sugar: %#v", cond["subject"])
	}
}

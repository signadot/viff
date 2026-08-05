package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

func rule(t *testing.T, doc string) Trigger {
	t.Helper()
	var trig Trigger
	if err := codec.Unmarshal([]byte(doc), &trig, codec.Tony); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return trig
}

// A rule may name who answers its OWN gates, which a spawn has been able to do all along.
//
// The named door was open on one side of the design and shut on the other: a spawn says
// `resolver:` for the run it starts, and a rule had no counterpart for the gates it raises
// itself — so a firing's question could only ever reach the standing layer or the docket.
func TestARuleNamesWhoAnswersItsOwnGates(t *testing.T) {
	trig := rule(t, `{
		name: validate,
		condition: {subject: {system: change, kind: pr, value: {stage: proposed}}},
		gating: {
			resolver: "resolvers:security:security-review",
			gates:    [{name: ship, mode: ask}],
		},
		action: {patch: {stage: validated}},
	}`)
	if got := trig.GateResolver(); got != "resolvers:security:security-review" {
		t.Fatalf("GateResolver = %q", got)
	}
	if g := trig.AllGates(); len(g) != 1 || g[0].Name != "ship" {
		t.Fatalf("AllGates = %+v, want the one under gating", g)
	}
}

// `gates:` at the top level still means what it always did.
func TestTopLevelGatesStillMeanTheSame(t *testing.T) {
	trig := rule(t, `{
		name: validate,
		condition: {subject: {system: change, kind: pr, value: {stage: proposed}}},
		gates:  [{name: ship, mode: ask}],
		action: {patch: {stage: validated}},
	}`)
	if g := trig.AllGates(); len(g) != 1 || g[0].Name != "ship" {
		t.Fatalf("AllGates = %+v, want the top-level one", g)
	}
	if got := trig.GateResolver(); got != "" {
		t.Fatalf("GateResolver = %q for a rule that named none", got)
	}
}

// Writing them both is refused rather than merged: a rule that says which gates it has twice
// has said it twice, and picking one silently is how a charter comes to gate on something its
// author removed.
func TestGatesWrittenTwiceAreRefused(t *testing.T) {
	trig := rule(t, `{
		name: validate,
		condition: {subject: {system: change, kind: pr, value: {stage: proposed}}},
		gates:  [{name: old, mode: ask}],
		gating: {gates: [{name: new, mode: ask}]},
		action: {patch: {stage: validated}},
	}`)
	eng := New(entitytest.Store(t), task.AutoResolver{})
	err := eng.AddTrigger(trig)
	if err == nil || !strings.Contains(err.Error(), "written twice") {
		t.Fatalf("AddTrigger = %v, want a refusal naming both spellings", err)
	}
}

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

// NOTHING NAMES WHO ANSWERS A RULE'S GATES (9ce3bva0, stage 4): a gate names its answerers, and
// whoever is asked grants the resolver answering for them. A rule or a spawn still naming one is
// refused at install, saying so, rather than decoding to nothing.
func TestAResolverNamedForGatesIsRetired(t *testing.T) {
	for what, doc := range map[string]string{
		"gating.resolver": `{name: v, condition: {subject: {path: "change.pr.*"}},
			gating: {resolver: "verse.resolvers.r", gates: [{name: ship, mode: ask}]}, action: {patch: {stage: x}}}`,
		"spawn.resolver": `{name: v, condition: {subject: {path: "change.pr.*"}},
			action: {spawn: {prompt: p, resolver: "verse.resolvers.r"}}}`,
	} {
		node, err := codec.ParseNode([]byte(doc), codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckFields(node); err == nil || !strings.Contains(err.Error(), what+": retired") {
			t.Errorf("%s: %v, want it refused as retired", what, err)
		}
	}
}

// `gates:` at the top level still means what it always did.
func TestTopLevelGatesStillMeanTheSame(t *testing.T) {
	trig := rule(t, `{
		name: validate,
		condition: {subject: {path: "change.pr.*", value: {stage: proposed}}},
		gates:  [{name: ship, mode: ask}],
		action: {patch: {stage: validated}},
	}`)
	if g := trig.AllGates(); len(g) != 1 || g[0].Name != "ship" {
		t.Fatalf("AllGates = %+v, want the top-level one", g)
	}
}

// Writing them both is refused rather than merged: a rule that says which gates it has twice
// has said it twice, and picking one silently is how a charter comes to gate on something its
// author removed.
func TestGatesWrittenTwiceAreRefused(t *testing.T) {
	trig := rule(t, `{
		name: validate,
		condition: {subject: {path: "change.pr.*", value: {stage: proposed}}},
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

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

// A SPAWN'S KEYS ARE READ STRICTLY: one the spawn does not have, the `doer` and `policy` it
// had before the environment, is refused at install rather than dropped, which would install a
// task without what its charter says; a value bound at the firing is still the firing's.
func TestASpawnsUnknownKeysAreRefused(t *testing.T) {
	for doc, want := range map[string]string{
		`{name: v, condition: {subject: {path: "a.*"}}, action: {spawn: {prompt: p, doer: {agent: verse}}}}`:                                                                               "spawn: unknown field \"doer\"",
		`{name: v, condition: {subject: {path: "a.*"}}, action: {seq: [{spawn: {prompt: p, policy: []}}]}}`:                                                                                "seq[0]: spawn: unknown field \"policy\"",
		`{name: v, condition: {subject: {path: "a.*"}}, action: {spawn: {prompt: p}, onDone: {spawn: {script: s, with: {}}}}}`:                                                             "onDone: spawn: unknown field \"with\"",
		`{name: v, condition: {subject: {path: "a.*"}}, action: {spawn: {prompt: p, environment: {vars: {A: $[subject.ref]}, systems: {git: {repo: $[subject.value.repo], fetch: {}}}}}}}`: "",
	} {
		node, err := codec.ParseNode([]byte(doc), codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		err = CheckFields(node)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", doc, err, want)
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

// A GATE'S MODE IS AUTO OR ASK, and anything else is refused at install, naming the two: `park`
// and `hold` among them, which would otherwise be read as a question by another name.
func TestAGatesModeIsAutoOrAsk(t *testing.T) {
	for mode, ok := range map[string]bool{"auto": true, "ask": true, "park": false, "hold": false} {
		trig := rule(t, `{name: m, condition: {subject: {path: "change.pr.*"}},
			gates: [{name: ship, mode: `+mode+`}], action: {patch: {stage: x}}}`)
		err := New(entitytest.Store(t), task.AutoResolver{}).AddTrigger(trig)
		if ok && err != nil {
			t.Errorf("mode %s: %v, want it installed", mode, err)
		}
		if !ok && (err == nil || !strings.Contains(err.Error(), "want auto|ask")) {
			t.Errorf("mode %s: %v, want it refused, naming auto|ask", mode, err)
		}
	}
}

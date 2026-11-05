package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/entity"

	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/task"
)

// A RULE THAT DOES NOTHING IS REFUSED.
//
// It used to install — "installed 1 trigger(s)" — and render back with `action: {}`, then watch
// its subtree for the life of the verse without ever writing anything. It is the failure every
// refusal around it exists to prevent, and a worse instance than the ones already caught: there
// is no half-written field to notice, the whole action is missing (git-issue b2g9hsnfh12ks5vqg9n0).
func TestARuleWithNoActionIsRefused(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	err := eng.AddTrigger(mustParseCharter(t, `
- name: noact
  condition: {subject: demo.pr.1}
`))
	if err == nil {
		t.Fatal("installed a rule that watches its subtree and writes nothing")
	}
	for _, want := range []string{"does nothing", "patch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not carry %q: %v", want, err)
		}
	}
}

// A GATE DOES NOT RESCUE IT. A gate with no action poses a question whose answer nothing reads,
// which is the same rule with a person's time spent on it.
func TestAGatedRuleWithNoActionIsRefused(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: gated
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  gates:
  - name: ask-someone
    mode: ask
`)); err == nil {
		t.Fatal("installed a gated rule with no action — the answer would be read by nothing")
	}
}

// A DERIVE COUNTS TOO, asked of the action directly: a rule that derives must also declare a
// stratum (a derive lands strictly above), which is a separate refusal and not this one.
func TestADeriveIsAnAction(t *testing.T) {
	a := Action{Derive: entity.Obj{"other": entity.Obj{"note": entity.Obj{"saw": "yes"}}}.Payload()}
	if !a.does() {
		t.Fatal("a derive is a write, and a rule carrying one does something")
	}
	if (&Action{}).does() {
		t.Fatal("an empty action does something")
	}
	if (&Action{OnDone: &Action{Patch: entity.Obj{"x": 1}.Payload()}}).does() {
		t.Fatal("hooks alone are not an action — there is nothing here to finish")
	}
}

// EACH OF THE THREE IS ENOUGH on its own: writing the subject, writing another entity, starting
// work, or a sequence of it. Hooks are not among them, and are refused for their own reason —
// a completion hook needs something that finishes.
func TestAnyOneActionIsEnough(t *testing.T) {
	for _, tc := range []struct{ name, action string }{
		{"patch", `patch: {stage: seen}`},
		// A spawn latches through its completion hook, which is the ordinary shape and
		// what the latch check asks for — the point here is only that a spawn is an action.
		{"spawn", "spawn: {script: \"echo hi\", autonomy: [{then: allow}]}\n    onDone: {patch: {stage: done}}"},
		{"seq", "seq: [{spawn: {script: \"echo hi\", autonomy: [{then: allow}]}}]\n    onDone: {patch: {stage: done}}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := New(entitytest.Store(t), task.AutoResolver{})
			if err := eng.AddTrigger(mustParseCharter(t, `
- name: r
  condition: {subject: {path: "demo.pr.*", value: {stage: open}}}
  action:
    `+tc.action+`
`)); err != nil {
				t.Fatalf("refused a rule whose action is a %s: %v", tc.name, err)
			}
		})
	}
}

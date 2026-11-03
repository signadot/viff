package trigger

import (
	"context"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// A gate's `pose` is the question in words, and the only thing on a gate written for a person.
// It is expanded HERE, at the firing, against the subject it settled on, so what it says is what
// the rule saw and a presenter needs no access to the subject to show it.

// asked drives one firing of a gate carrying pose and answers the question's words.
func asked(t *testing.T, pose string, subject entity.Obj) string {
	t.Helper()
	store := entitytest.Store(t)
	res := &captureResolver{}
	eng := gated(t, store, res, Gate{Name: "approval", Mode: "ask", Pose: pose})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"), subject.Payload())
	return firstQuestion(t, res).Pose
}

// The subject's own fields, in the sentence, as go-tony expands them.
func TestAGatePosesItsQuestionInWords(t *testing.T) {
	said := asked(t, "Deploy *$[subject.value.title]* ($[subject.value.changes] changes) to staging?",
		entity.Obj{"stage": "open", "title": "the migration", "changes": 3})
	if want := "Deploy *the migration* (3 changes) to staging?"; said != want {
		t.Errorf("posed %q, want %q", said, want)
	}
}

// The subject's address is in the environment too, for a question about a thing whose payload
// says nothing worth reading.
func TestAPoseMayNameTheSubjectItself(t *testing.T) {
	if said := asked(t, "Approve $[subject.ref]?", entity.Obj{"stage": "open"}); !strings.Contains(said, "demo.pr") {
		t.Errorf("posed %q, want the subject's address in it", said)
	}
}

// A sentence with no expression is a sentence.
func TestAPoseWithNoExpressionIsItsOwnText(t *testing.T) {
	if said := asked(t, "Approve this?", entity.Obj{"stage": "open"}); said != "Approve this?" {
		t.Errorf("posed %q, want it verbatim", said)
	}
}

// A question that will not expand is still asked: the words are dropped, and `reason` is what a
// presenter falls back to. A gate nobody is asked because its prose broke is worse than a bare one.
func TestAPoseThatWillNotExpandStillAsks(t *testing.T) {
	store := entitytest.Store(t)
	res := &captureResolver{}
	eng := gated(t, store, res, Gate{Name: "approval", Mode: "ask", Pose: "Deploy $[nosuchname.x]?"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())

	q := firstQuestion(t, res)
	if q.Pose != "" {
		t.Errorf("posed %q, want nothing rather than half a sentence", q.Pose)
	}
	if !strings.Contains(q.Reason, "gate approval") {
		t.Errorf("reason %q, want what it always said", q.Reason)
	}
}

// `pose` is words for somebody to read, so a gate that asks nobody may not carry one: written on
// an auto gate it would be a sentence nothing ever shows.
func TestAnAutoGateMayNotCarryAPose(t *testing.T) {
	err := Gate{Name: "approval", Mode: "auto", Pose: "Approve this?"}.validate()
	if err == nil || !strings.Contains(err.Error(), "mode: ask") {
		t.Errorf("validate = %v, want it refused with the spelling that works", err)
	}
}

// And a gate that says nothing but its words is asked: `pose` implies `ask`, as a threshold,
// options and a deadline already do.
func TestAPoseImpliesAsk(t *testing.T) {
	m, err := Gate{Name: "approval", Pose: "Approve this?"}.mode()
	if err != nil || !m.Blocks() {
		t.Errorf("mode = %v (%v), want ask implied by the words", m, err)
	}
}

// And a charter carrying one installs: an unknown field is refused by name, so a grammar the
// struct knows and the field check does not is a rule that silently does less than it says.
func TestACharterMayCarryAPose(t *testing.T) {
	doc, err := codec.ParseNode([]byte(`
- name: deploy
  condition: {subject: {path: "demo.deploy.*", value: {stage: open}}}
  gates:
  - {name: approval, pose: "Deploy $[subject.value.title]?"}
  action: {patch: {stage: approved}}
`), codec.Tony)
	if err != nil {
		t.Fatalf("parsing the charter: %v", err)
	}
	for _, rule := range doc.Values {
		if err := CheckFields(rule); err != nil {
			t.Fatalf("a gate with a pose is refused: %v", err)
		}
	}
}

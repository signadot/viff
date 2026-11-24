package entity

import (
	"testing"

	"github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir/kpath"
)

// The change walk over a commit's delta: what a pattern reaches, before the commit and after it.
// The delta is what the store received, so the operations a patch is spelled with — a removal, a
// comment stating a node's comments — are read the way the store executes them and never as
// levels of the namespace.

// changesOf applies a delta to a document and answers the changes a pattern reaches, keyed by
// address — so a test says what it expects to be reported and what it expects not to be.
func changesOf(t *testing.T, before, delta, pattern string) map[Ref]Event {
	t.Helper()
	b := doc(t, before)
	d := doc(t, delta)
	a, err := tony.Patch(b, d)
	if err != nil {
		t.Fatalf("patching %s with %s: %v", before, delta, err)
	}
	kp, err := kpath.Parse(pattern)
	if err != nil {
		t.Fatalf("pattern %q: %v", pattern, err)
	}
	out := map[Ref]Event{}
	for _, ev := range changesAt([]*kpath.KPath{kp}, b, a, d, 7) {
		out[ev.Ref] = ev
	}
	return out
}

// A REMOVAL IS AN OPERATION, NOT A STRING. A tag composes, and what a patch executes is the
// first registered label in the chain — so a delete carrying anything alongside it is still a
// delete, and the walk has to ask the same question the patch asks (opIn).
func TestAComposedDeleteIsStillARemoval(t *testing.T) {
	changes := changesOf(t, `demo: pr: "1": {stage: open}`,
		`demo: pr: "1": !delete.logd-patch-root null`, `demo.pr.*`)
	ev, ok := changes[RefAt("demo", "pr", "1")]
	if !ok {
		t.Fatalf("the removal was not reported at all; got %v", changes)
	}
	if ev.Op != Delete {
		t.Errorf("reported as %s of %+v, want a removal — `!delete` wearing a second label "+
			"is the operation the store executed", ev.Op, ev.To)
	}
}

// A REMOVAL TAKES WHAT STOOD BENEATH IT: `viff data --rm a b` commits `a: b: !delete null`, and
// a pattern reaching the nodes under `a.b` reports each of them gone. The pattern decides where
// a change is reported, so the same commit is one removal at `a.b` and two beneath it.
func TestARemovalIsReportedWhereverThePatternReaches(t *testing.T) {
	const before = `a: b: {x: 1, y: 2}`
	if changes := changesOf(t, before, `a: b: !delete null`, `a.b`); len(changes) != 1 ||
		changes[RefAt("a", "b")].Op != Delete {
		t.Fatalf("at a.b: %v, want a.b removed", changes)
	}
	changes := changesOf(t, before, `a: b: !delete null`, `a.b.*`)
	if len(changes) != 2 {
		t.Fatalf("at a.b.*: %v, want both nodes beneath it removed", changes)
	}
	for _, ref := range []Ref{RefAt("a", "b", "x"), RefAt("a", "b", "y")} {
		if changes[ref].Op != Delete {
			t.Errorf("%s = %+v, want a removal", ref, changes[ref])
		}
	}
	// A null is a VALUE being written, not a removal, and it is reported as what now stands.
	if changes := changesOf(t, before, `a: b: null`, `a.b`); changes[RefAt("a", "b")].Op != Set {
		t.Fatalf("a null wrote %v, want a set of null", changes)
	}
}

// A `!comment` ABOVE A NODE IS NOT A LEVEL OF THE NAMESPACE. It is an ordinary tagged object
// whose fields name the parts of a statement — head, line, value — so a walk that descends it
// reads those parts as addresses and reports nodes that do not exist.
func TestACommentOnAContainerReportsNothingBeneathIt(t *testing.T) {
	changes := changesOf(t, `demo: pr: {}`, `demo: pr: !comment {head: ["# the prs"]}`, `demo.pr.*`)
	if len(changes) != 0 {
		t.Fatalf("reported %v, want nothing — head: is not an address", changes)
	}
}

// The same node WITH a value states both at once: what the comments are, and what the value is.
// What is under the value is what changed.
func TestACommentCarryingAValueReportsWhatIsUnderIt(t *testing.T) {
	changes := changesOf(t, `demo: pr: {}`,
		`demo: pr: !comment {head: ["# the prs"], value: {"1": {stage: "open"}}}`, `demo.pr.*`)
	ev, ok := changes[RefAt("demo", "pr", "1")]
	if !ok {
		t.Fatalf("the node under value: was not reported; got %v", changes)
	}
	if got := Str(ev.To, "stage"); got != "open" {
		t.Errorf("to = %+v (stage %q), want the value the operand states", ev.To, got)
	}
	if len(changes) != 1 {
		t.Errorf("reported %d changes, want the one node — head: is not an address", len(changes))
	}
}

// Touches walks the SAME delta to answer whether a commit changed anything a path reaches, and
// it reads a statement's parts as a statement there too.
func TestTouchesDoesNotReadACommentOperandAsAddresses(t *testing.T) {
	c := Commit{}.WithDelta(doc(t, `demo: pr: !comment {head: ["# the prs"]}`))
	if !c.Touches(at(t, "demo.pr")) {
		t.Error("the kind's own comments changed and Touches(demo.pr) said no")
	}
	if c.Touches(at(t, `demo.pr."1".stage`)) {
		t.Error("head: was walked as an address: Touches said a node under the kind changed")
	}
	c = Commit{}.WithDelta(doc(t,
		`demo: pr: !comment {head: ["# the prs"], value: {"1": {stage: "open"}}}`))
	if !c.Touches(at(t, `demo.pr."1"`)) {
		t.Error("the value the comment states was not walked: Touches said the entity did not change")
	}
}

// Touches is the question "can what this path reads have changed", so it says yes to a write
// beneath the path and to one that replaced or removed a place above it, and no to a commit
// somewhere else — which is what lets a rule skip a commit it cannot be woken by.
func TestTouchesAnswersAboveBeneathAndElsewhere(t *testing.T) {
	for _, c := range []struct {
		delta, path string
		want        bool
	}{
		{`demo: pr: "1": stage: merged`, `demo.pr.*`, true},         // beneath
		{`demo: pr: "1": stage: merged`, `demo.pr."1".stage`, true}, // at
		{`demo: !delete null`, `demo.pr.*`, true},                   // removed above
		{`demo: 5`, `demo.pr.*`, true},                              // replaced above
		{`demo: issue: "1": {}`, `demo.pr.*`, false},                // a sibling
		{`other: pr: "1": {}`, `demo.pr.*`, false},                  // somewhere else
		{`viff: task: instance: x: {}`, `task.instance.*`, false},   // the other half
	} {
		got := Commit{}.WithDelta(doc(t, c.delta)).Touches(at(t, c.path))
		if got != c.want {
			t.Errorf("delta %s, path %s: Touches = %v, want %v", c.delta, c.path, got, c.want)
		}
	}
}

// A node whose value the commit did not change is not a change, however the patch was spelled:
// the walk compares what stands before with what stands after.
func TestAWriteOfTheSameValueIsNoChange(t *testing.T) {
	if changes := changesOf(t, `demo: pr: "1": {stage: open}`,
		`demo: pr: "1": stage: open`, `demo.pr.*`); len(changes) != 0 {
		t.Fatalf("reported %v, want nothing — the node holds what it held", changes)
	}
}

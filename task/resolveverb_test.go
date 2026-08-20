package task

import "testing"

// resolution is what a resolver's answer looks like before it is derived: a self-reported
// action naming the question it answers.
func resolution(questionID string) ToolUse {
	tu := ToolUse{Name: "resolve", Input: map[string]any{"id": questionID}}
	BasicDeriver{}.Derive(&tu)
	return tu
}

// Answering has to be sayable on the hard axis, or an envelope can only speak about
// everything a resolver does at once. A grant naming the verb and the question admits that
// resolution and nothing else.
func TestGrantCanPermitAnsweringOneQuestion(t *testing.T) {
	mine := "review@demo.pr.1~d3"
	yours := "review@demo.pr.2~d1"

	g := Grant{Allow: []GateMatch{{
		Verbs:     []Verb{Resolve},
		Resources: []ResourcePred{{Kind: KindGate, Pattern: mine}},
	}}}

	if !g.Permits(resolution(mine)) {
		t.Errorf("a grant naming {resolve, gate %s} refused that resolution", mine)
	}
	if g.Permits(resolution(yours)) {
		t.Errorf("a grant naming {resolve, gate %s} admitted a resolution of %s — a resolver "+
			"could answer questions it was never given", mine, yours)
	}
}

// A pattern over the id selects by origin, since a question id carries the task that
// raised it. That is what lets an envelope say "the questions of this run" without a second
// field to match on.
func TestGrantCanPermitAnsweringOneOrigin(t *testing.T) {
	g := Grant{Allow: []GateMatch{{
		Verbs:     []Verb{Resolve},
		Resources: []ResourcePred{{Kind: KindGate, Pattern: "review@demo.pr.1~*"}},
	}}}

	if !g.Permits(resolution("review@demo.pr.1~d3")) {
		t.Error("a glob over one origin's questions refused one of them")
	}
	if g.Permits(resolution("review@demo.pr.2~d3")) {
		t.Error("a glob over one origin's questions admitted another origin's")
	}
}

// read-only is reads and nothing else, so it must refuse answering. Resolve being its own
// verb is what makes that true without anybody writing a rule about it: were it a Write, a
// grant meant to permit editing a file would silently confer answering as well.
func TestReadOnlyRefusesAnswering(t *testing.T) {
	g, ok, err := NamedGrant("read-only")
	if err != nil || !ok {
		t.Fatalf("NamedGrant(read-only) = %v, %v", ok, err)
	}
	if g.Permits(resolution("review@demo.pr.1~d3")) {
		t.Error("read-only admitted a resolution")
	}

	w := Grant{Allow: []GateMatch{{Verbs: []Verb{Write}}}}
	if w.Permits(resolution("review@demo.pr.1~d3")) {
		t.Error("a write grant admitted a resolution — answering is not a write, and a verb " +
			"that collapsed the two would confer it by accident")
	}
}

// The derivation is what the envelope reads, so it has to produce the effect at all — and
// nothing else in the vocabulary should claim a resolution.
func TestResolutionDerivesOneGateEffect(t *testing.T) {
	tu := resolution("review@demo.pr.1~d3")
	if len(tu.Effects) != 1 {
		t.Fatalf("effects = %v, want exactly one", tu.Effects)
	}
	got := tu.Effects[0]
	if got.Verb != Resolve || got.Resource.Kind != KindGate || got.Resource.Ref != "review@demo.pr.1~d3" {
		t.Errorf("effect = %+v, want {resolve, gate, the question id}", got)
	}
}

// An answer that names no question derives nothing, so a non-wildcard grant refuses it.
// Fail-closed is the existing rule for an action nothing could classify, and a resolution
// with no subject is exactly that.
func TestResolutionWithNoQuestionDerivesNothing(t *testing.T) {
	tu := ToolUse{Name: "resolve", Input: map[string]any{}}
	BasicDeriver{}.Derive(&tu)
	if len(tu.Effects) != 0 {
		t.Fatalf("effects = %v, want none", tu.Effects)
	}
	g := Grant{Allow: []GateMatch{{Verbs: []Verb{Resolve}}}}
	if g.Permits(tu) {
		t.Error("a resolution naming no question was admitted; it should fail closed")
	}
}

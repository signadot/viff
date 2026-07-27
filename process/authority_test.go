package process

import (
	"context"
	"testing"
)

// countResolver proceeds like AutoResolver but records how many questions reached it —
// the difference between "refused" and "asked and approved", which is the whole point of
// putting the envelope ahead of the vector.
type countResolver struct{ calls int }

func (c *countResolver) Resolve(_ context.Context, q GateQuestion, _ ToolUse) (Resolution, error) {
	c.calls++
	return Resolution{Proceed: true, By: "auto", Question: q}, nil
}

func execTool() ToolUse {
	return ToolUse{Name: "run_bash", Input: map[string]any{"command": "rm -rf /"}}
}

func readTool() ToolUse {
	return ToolUse{Name: "read", Input: map[string]any{"path": "notes.md"}}
}

// derived classifies an action the way the loop does before checking it — Permits reads
// effects and never produces them, so a caller testing it directly must derive first.
func derived(tu ToolUse) ToolUse {
	BasicDeriver{}.Derive(&tu)
	return tu
}

// The complaint that produced this change: a trigger installed with `grant: read-only`
// spawned an agent that still raised an exec gate. Two things had to be true for that —
// the field reached nothing, and nothing would have checked it if it had. This covers the
// second: with a read-only envelope installed, an exec action is refused outright.
//
// The resolver assertion is the load-bearing one. A grant that merely made the gate ask
// would look identical in the ledger and be a different mechanism entirely: an envelope
// the caller narrowed is not a decision the person at the console may reverse.
func TestGrantRefusesOutsideEnvelopeWithoutAsking(t *testing.T) {
	res := &countResolver{}
	e := &recExec{}
	d := &scriptDriver{steps: []ToolUse{execTool()}}
	p := NewProcess("t", NewSupervisor(autoVec(), res), d, e)
	p.SetScope(Grant{Allow: []GateMatch{{Verbs: []Verb{Read}}}})

	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(e.order) != 0 {
		t.Errorf("the executor ran %v; an action outside the grant must not execute", e.order)
	}
	if res.calls != 0 {
		t.Errorf("the resolver was asked %d times; an action outside the grant is refused, not asked about", res.calls)
	}
}

// The same envelope must still admit what it names, or "read-only" would just be "none"
// with a friendlier spelling.
func TestGrantAdmitsWhatItNames(t *testing.T) {
	e := &recExec{}
	d := &scriptDriver{steps: []ToolUse{readTool()}}
	p := NewProcess("t", NewSupervisor(autoVec(), AutoResolver{}), d, e)
	p.SetScope(Grant{Allow: []GateMatch{{Verbs: []Verb{Read}}}})

	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(e.order) != 1 || e.order[0] != "read" {
		t.Errorf("executed %v, want the read to proceed", e.order)
	}
}

// The trap this design has to avoid: Grant's zero value permits NOTHING, so if "no grant
// declared" were represented as the zero Grant, every process that never mentioned one
// would refuse every action. A process built without a scope keeps the wildcard, and
// nothing this change adds narrows it.
func TestUndeclaredGrantLeavesTheProcessUnbounded(t *testing.T) {
	e := &recExec{}
	d := &scriptDriver{steps: []ToolUse{execTool(), readTool()}}
	p := NewProcess("t", NewSupervisor(autoVec(), AutoResolver{}), d, e)

	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(e.order) != 2 {
		t.Errorf("executed %v, want both actions: a process that declared no grant is unbounded", e.order)
	}
}

// NamedGrant must keep "absent" and "empty" apart, because they are the two ends of the
// range: absent means unbounded, empty means nothing is permitted. A single-return API
// would have to pick one of them to represent with the zero value, and either choice is
// wrong for the other.
func TestNamedGrantSeparatesAbsentFromEmpty(t *testing.T) {
	if _, ok, err := NamedGrant(""); ok || err != nil {
		t.Errorf(`NamedGrant("") = ok %v, err %v; want a declared-nothing signal and no error`, ok, err)
	}
	g, ok, err := NamedGrant("none")
	if !ok || err != nil {
		t.Fatalf(`NamedGrant("none") = ok %v, err %v`, ok, err)
	}
	if g.Permits(derived(readTool())) {
		t.Error(`"none" must permit nothing, including reads`)
	}
	ro, _, err := NamedGrant("read-only")
	if err != nil {
		t.Fatal(err)
	}
	if !ro.Permits(derived(readTool())) || ro.Permits(derived(execTool())) {
		t.Error(`"read-only" must admit a read and refuse an exec`)
	}
}

// A name the vocabulary does not have is an error, never a silent wildcard. Defaulting an
// unrecognised envelope to "unbounded" is the failure this change exists to remove, and it
// would be a worse version of it: the charter says read-only, the log says nothing, the
// agent runs shell commands.
func TestNamedGrantRejectsUnknownRatherThanWidening(t *testing.T) {
	if _, ok, err := NamedGrant("reed-only"); err == nil || ok {
		t.Fatalf("a misspelt grant resolved to ok=%v with err=%v; it must fail", ok, err)
	}
}

// An action the deriver cannot classify carries no effects and so matches no verb
// predicate: it is refused under any narrowed envelope. That is fail-closed, which is the
// direction authority.md asks for — the action nobody can classify is exactly the one an
// envelope should not admit.
func TestUnclassifiedActionIsRefusedUnderANarrowedGrant(t *testing.T) {
	g := Grant{Allow: []GateMatch{{Verbs: []Verb{Read}}}}
	if g.Permits(ToolUse{Name: "something_new"}) {
		t.Error("an action with no derived effects must not pass a narrowed grant")
	}
}

// A named vector is the three modes and nothing else, matching what `verse process vector`
// accepts — and an unstated one is not a vector at all, so the runtime's own default stands
// rather than being silently replaced by a permissive one.
func TestNamedVector(t *testing.T) {
	if v, err := NamedVector(""); v != nil || err != nil {
		t.Errorf("an unstated vector must resolve to nil with no error, got %v / %v", v, err)
	}
	v, err := NamedVector("auto")
	if err != nil || v == nil || v.Default != Auto {
		t.Fatalf(`NamedVector("auto") = %v, %v`, v, err)
	}
	if len(v.Gates) != 0 {
		t.Error("a named vector carries no gates: naming one is how a firing says it already answered them")
	}
	if _, err := NamedVector("sometimes"); err == nil {
		t.Error("an unknown mode must be an error, not a default")
	}
}

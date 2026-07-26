package process

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

// scriptDriver replays a fixed list of actions — a stand-in for an owned LLM
// loop, so the machinery can be tested without a model.
type scriptDriver struct {
	steps []ToolUse
	i     int
	// optional: cancel the process when Next is called with i == cancelAt
	proc     *Process
	cancelAt int
}

func (d *scriptDriver) Next(_ context.Context, _ StepResult) (ToolUse, bool, error) {
	if d.proc != nil && d.i == d.cancelAt {
		d.proc.Cancel("test")
	}
	if d.i >= len(d.steps) {
		return ToolUse{}, false, nil
	}
	tu := d.steps[d.i]
	d.i++
	return tu, true, nil
}

// recExec records the order actions (including unwound defers) execute, and
// returns per-name deferred actions to register.
type recExec struct {
	order  []string
	defers map[string][]ToolUse
	fail   map[string]bool
}

func (e *recExec) Execute(_ context.Context, tu ToolUse) (StepResult, []ToolUse, error) {
	e.order = append(e.order, tu.Name)
	if e.fail[tu.Name] {
		return StepResult{}, nil, fmt.Errorf("boom")
	}
	return StepResult{Use: tu, Output: "ok"}, e.defers[tu.Name], nil
}

func autoVec() *Vector { return &Vector{Default: Auto} }

func tool(name string) ToolUse { return ToolUse{Name: name} }

// Defers registered at the root frame unwind LIFO at normal completion.
func TestDefersUnwindLIFOAtRoot(t *testing.T) {
	e := &recExec{defers: map[string][]ToolUse{
		"setupA": {tool("teardownA")},
		"setupB": {tool("teardownB")},
	}}
	d := &scriptDriver{steps: []ToolUse{tool("setupA"), tool("setupB")}}
	p := NewProcess("t", NewSupervisor(autoVec(), AutoResolver{}), d, e)

	out, err := p.Run(context.Background())
	if err != nil || out.Reason != "done" {
		t.Fatalf("run: reason=%q err=%v", out.Reason, err)
	}
	want := []string{"setupA", "setupB", "teardownB", "teardownA"}
	if !reflect.DeepEqual(e.order, want) {
		t.Fatalf("order = %v, want %v", e.order, want)
	}
}

// A CallAgent's defers are scoped to the call and unwind at the call's return —
// before subsequent root actions — while root defers unwind at the end.
// Cancel unwinds the open frames — best-effort teardown on the way out — and
// leaves already-applied effects untouched (no reversal).
func TestCancelUnwinds(t *testing.T) {
	e := &recExec{defers: map[string][]ToolUse{"action1": {tool("teardown1")}}}
	d := &scriptDriver{steps: []ToolUse{tool("action1"), tool("action2")}, cancelAt: 0}
	p := NewProcess("t", NewSupervisor(autoVec(), AutoResolver{}), d, e)
	d.proc = p

	out, err := p.Run(context.Background())
	if err == nil {
		t.Fatalf("want cancellation error, got nil")
	}
	if out.Reason != "cancelled" {
		t.Fatalf("reason = %q, want cancelled", out.Reason)
	}
	// action1 ran (cancel latched, honored at next boundary), then its teardown
	// unwound; action2 never ran.
	want := []string{"action1", "teardown1"}
	if !reflect.DeepEqual(e.order, want) {
		t.Fatalf("order = %v, want %v", e.order, want)
	}
}

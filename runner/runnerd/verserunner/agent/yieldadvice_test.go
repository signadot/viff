package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/task"
)

// HOW A MODEL WAITS, pinned twice: that it is TOLD, and that the telling names something real.
//
// task.Task joins children where a turn ENDS — the driver has nothing to propose, children are
// outstanding, and the loop blocks in Go until one reports. That is the correct wait and a free
// one. The model cannot see it, so what it is told is the whole of what it knows, and the
// telling has now been wrong twice in opposite directions:
//
//   - Told only that a result "comes back when it finishes", it reached for the wait it could
//     see: `run_bash {command: "sleep 120"}`, a round trip per poll. Eighteen minutes and
//     twenty-five boundaries mostly asleep (git-issue 91r674qzh12ks35vfnn0).
//   - Told "END YOUR TURN: say what you are waiting for and stop, without calling a tool", it
//     did the saying and not the stopping — narrating "Still waiting for server_go_reviewer and
//     test_go_reviewer" at every boundary while spawning `noop`, `noop2` … `noop10` to fill
//     them. A turn carries content AND tool calls, so that instruction asks for a restraint the
//     model has no way to commit to.
//
// So the advice now points at a VERB, and this pins that the verb it points at is on the menu.
// A description naming a tool the request does not carry is the same failure as no advice at
// all, and it is the one that would look correct in review.
func TestTheWaitAdviceNamesAToolThatIsActuallyOffered(t *testing.T) {
	tds, err := selectTools(nil, nil)
	if err != nil {
		t.Fatalf("selectTools: %v", err)
	}
	byName := map[string]string{}
	for _, td := range tds {
		byName[td.Function.Name] = td.Function.Description
	}
	desc, ok := byName["call_agent"]
	if !ok {
		t.Fatal("call_agent is not declared")
	}
	if _, ok := byName[yieldTool]; !ok {
		t.Fatalf("call_agent is offered and %q is not: the only way to wait is missing", yieldTool)
	}
	if !strings.Contains(desc, yieldTool) {
		t.Errorf("call_agent does not name %q, so a model that wants to wait must invent a wait "+
			"again:\n%s", yieldTool, desc)
	}
	// And it must not tell the model to do the thing that did not work. "End your turn" is a
	// restraint; the model has a verb now.
	if strings.Contains(desc, "END YOUR TURN") {
		t.Error("call_agent still asks the model to end its turn, which is the advice the drive " +
			"watched fail: a turn carries content and tool calls, so stopping is not an act")
	}
}

// yield is offered ALWAYS, and is the one tool that never was optional.
//
// It carries the only structural way to say "I could not", which everything downstream depends
// on — a task that merely stops reads as success — and now also the only way to wait. A spawn
// that withheld it would turn every failure into a completed task with an excuse, and every
// wait into whatever the model could improvise.
func TestYieldIsNotOptional(t *testing.T) {
	for _, only := range [][]string{nil, {"read_file"}, {"read_file", "call_agent"}} {
		tds, err := selectTools(only, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !hasTool(tds, yieldTool) {
			t.Errorf("tools %v were offered without %q", only, yieldTool)
		}
	}
}

func hasTool(tds []toolDef, name string) bool {
	for _, td := range tds {
		if td.Function.Name == name {
			return true
		}
	}
	return false
}

// And the mechanism the advice now describes: `wait` ends the turn, reaches no executor, and
// the loop calls the model again exactly once per child that reports.
//
// This is the claim the polling was approximating at a round trip per attempt — and at two,
// since each `noop` was a whole sub-task with its own model calls. One turn per RESULT is the
// floor, and this asserts the floor is what it costs.
func TestWaitEndsTheTurnAndCostsOneModelCallPerResult(t *testing.T) {
	fake := &fakeCompleter{turns: []scriptedTurn{
		// Turn 1: fan out over two files.
		{msg: chatMessage{Role: "assistant", ToolCalls: []toolCall{
			mkTC("c1", "call_agent"), mkTC("c2", "call_agent")}}, finish: "tool_calls"},
		// Turn 2: nothing to do until they report.
		{msg: chatMessage{Role: "assistant", ToolCalls: []toolCall{mkTC("c3", yieldTool)}}, finish: "tool_calls"},
		// Turn 3: one has reported; still waiting on the other.
		{msg: chatMessage{Role: "assistant", ToolCalls: []toolCall{mkTC("c4", yieldTool)}}, finish: "tool_calls"},
		// Turn 4: both in, answer.
		{msg: chatMessage{Role: "assistant", Content: "both summarised"}, finish: "stop"},
	}}
	d := &llmDriver{client: fake, prompt: "summarise each file", log: io.Discard}

	ex := &recExec{}
	p := task.NewTask("p", task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{}), d, ex)
	p.SetLog(io.Discard)
	var kids int
	p.SetSpawn(func(parent *task.Task, mode task.ChildMode, req *task.CallAgentRequest) (*task.Task, error) {
		kids++
		// Staggered, so they report SEPARATELY. Simultaneous results fold together into one
		// call — the loop drains everything waiting at a boundary — which is the loop being
		// efficient and would make this assertion pass for the wrong reason.
		return parent.NewChild(fmt.Sprintf("kid%d", kids), mode, req,
			&slowChild{after: time.Duration(kids) * 60 * time.Millisecond}, &recExec{})
	})

	out, err := p.Run(context.Background())
	if err != nil || out.Reason != string(task.Done) {
		t.Fatalf("run: reason=%q err=%v", out.Reason, err)
	}
	if kids != 2 {
		t.Errorf("%d children spawned, want 2 — anything more is filler work", kids)
	}
	// `wait` is not an action. It must never reach an executor, which would both do nothing
	// and, being an action in flight, be the one moment a child's result CANNOT be folded in.
	for _, ran := range ex.ran {
		if ran == yieldTool {
			t.Fatalf("%q was dispatched to the executor: it is a turn ending, not a thing to do", yieldTool)
		}
	}
	if len(ex.ran) != 0 {
		t.Errorf("the executor ran %v; this task only delegates", ex.ran)
	}
	// One model call to fan out, one to wait, one per result that arrives. Four, and no polling
	// in between — against the twenty-nine boundaries the drive spent doing this by hand.
	if fake.i != 4 {
		t.Errorf("%d model calls, want 4 (fan out, wait, and one per result)", fake.i)
	}
	// The wire stays valid: a tool_call with no tool response is a malformed conversation, and
	// unlike `fail` there IS a next turn to send it in.
	sent := fake.seen[len(fake.seen)-1]
	var answered bool
	for _, m := range sent {
		if m.Role == "tool" && m.Name == yieldTool {
			answered = true
		}
	}
	if !answered {
		t.Errorf("no tool result was recorded for %q — the next turn ships a tool_call nobody "+
			"answered, which the API rejects", yieldTool)
	}
}

// slowChild finishes after a delay, so the parent has to wait for it.
type slowChild struct {
	after time.Duration
	fired bool
}

func (d *slowChild) Next(context.Context, task.StepResult) (task.ToolUse, bool, error) {
	if !d.fired {
		d.fired = true
		time.Sleep(d.after)
	}
	return task.ToolUse{}, false, nil
}

// recExec records what actually reached an executor.
type recExec struct{ ran []string }

func (e *recExec) Execute(_ context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	e.ran = append(e.ran, tu.Name)
	return task.StepResult{Use: tu, Output: "ok"}, nil, nil
}

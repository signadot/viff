package verse

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/task"
)

// yieldTurn is one model turn that hands control back with the given raw arguments.
func yieldTurn(args string) scriptedTurn {
	tc := mkTC("c1", yieldTool)
	tc.Function.Arguments = args
	return scriptedTurn{msg: chatMessage{Role: "assistant", ToolCalls: []toolCall{tc}}, finish: "tool_calls"}
}

func yieldOnce(t *testing.T, args string) (task.ToolUse, bool, error) {
	t.Helper()
	d := &llmDriver{
		client: &fakeCompleter{turns: []scriptedTurn{yieldTurn(args), stop("anything")}},
		prompt: "go", log: io.Discard,
	}
	return d.Next(context.Background(), task.StepResult{})
}

// `cannot` is TERMINAL: it ends the loop with the model's reason rather than being dispatched
// to an executor. Without a structural way to say "I could not", both endings exit through "no
// more tool calls" — which is why a task that could not open the file it was asked to review
// completed as `done` with prose explaining itself.
func TestYieldCannotTerminatesWithTheReason(t *testing.T) {
	tu, more, err := yieldOnce(t, `{"reason":"cannot","note":"could not open review.md"}`)
	if err == nil {
		t.Fatal("a yield of cannot must end the loop with an error")
	}
	if more {
		t.Error("it must not ask for another turn")
	}
	if tu.Name != "" {
		t.Errorf("it must not be dispatched to the executor, got tool %q", tu.Name)
	}
	if !strings.Contains(err.Error(), "could not open review.md") {
		t.Errorf("the model's reason must survive: %v", err)
	}
}

// A `cannot` with nothing usable in it is still a failure. Swallowing it into a successful
// completion because the JSON was malformed is the exact outcome this exists to stop.
func TestYieldCannotWithoutANoteStillFails(t *testing.T) {
	for _, args := range []string{`{"reason":"cannot"}`, `{"reason":"cannot","note":"  "}`} {
		if _, more, err := yieldOnce(t, args); err == nil || more {
			t.Errorf("args %q: want a terminal error, got more=%v err=%v", args, more, err)
		}
	}
}

// And the other reasons END THE TURN without ending the task. What happens next is the loop's
// to decide — park if a child is outstanding, quiesce if not — which is the whole point of the
// machine deciding rather than the driver.
func TestYieldWaitingAndDoneEndTheTurnAndNotTheTask(t *testing.T) {
	for _, args := range []string{`{"reason":"waiting"}`, `{"reason":"done"}`} {
		tu, more, err := yieldOnce(t, args)
		if err != nil || more {
			t.Errorf("args %q: want a clean end of turn, got more=%v err=%v", args, more, err)
		}
		if tu.Name != "" {
			t.Errorf("args %q: dispatched %q to the executor", args, tu.Name)
		}
	}
}

// AN UNRECOGNISED REASON YIELDS, and says so to the model rather than guessing.
//
// The two ways to get this wrong are opposite and both have been paid for. Treat it as failure
// and a model experimenting discards finished work — the run that ended on "placeholder - not
// actually failing, retracting", with a review whose children had both reported clean. Treat a
// genuine failure as an ordinary stop and the task reports `done` with an excuse in its result.
// So: the enum is in the schema, `cannot` is the only failing value, and anything else hands
// back with the misunderstanding written into the tool result, where the model gets another
// turn to correct it.
func TestAnUnrecognisedYieldReasonDoesNotFailTheRun(t *testing.T) {
	for _, args := range []string{"", "{}", "not json", `{"reason":"retracting"}`} {
		d := &llmDriver{
			client: &fakeCompleter{turns: []scriptedTurn{yieldTurn(args), stop("the answer")}},
			prompt: "go", log: io.Discard,
		}
		_, more, err := d.Next(context.Background(), task.StepResult{})
		if err != nil || more {
			t.Errorf("args %q: an unrecognised reason must yield, not fail: more=%v err=%v",
				args, more, err)
			continue
		}
		// It is told, in the one place it will read: the result of the call it just made.
		last := d.messages[len(d.messages)-1]
		if last.Role != "tool" || !strings.Contains(last.Content, yieldCannot) {
			t.Errorf("args %q: the model was not told how to say it could not continue: %+v",
				args, last)
		}
	}
}

// An ordinary tool is unaffected: it still dispatches to the executor.
func TestANonYieldToolStillDispatches(t *testing.T) {
	d := &llmDriver{
		client: &fakeCompleter{turns: []scriptedTurn{
			{msg: chatMessage{Role: "assistant", ToolCalls: []toolCall{mkTC("c1", "read_file")}}, finish: "tool_calls"},
		}},
		prompt: "go", log: io.Discard,
	}
	tu, more, err := d.Next(context.Background(), task.StepResult{})
	if err != nil || !more {
		t.Fatalf("want a dispatched tool, got more=%v err=%v", more, err)
	}
	if tu.Name != "read_file" {
		t.Errorf("tool = %q, want read_file", tu.Name)
	}
}

// The tool must be advertised, or the branch exists and is never taken — and the description
// has to say that stopping reads as success, because a model that does not know the default is
// success has no reason to reach for this.
func TestYieldIsDeclaredWithItsReasons(t *testing.T) {
	for _, td := range toolDefs() {
		if td.Function.Name != yieldTool {
			continue
		}
		if !strings.Contains(td.Function.Description, "SUCCESS") {
			t.Error("the description must warn that simply stopping reads as success")
		}
		for _, r := range []string{yieldWaiting, yieldDone, yieldCannot} {
			if !strings.Contains(td.Function.Description, r) {
				t.Errorf("the description does not mention the %q reason", r)
			}
		}
		// The reason is enumerated in the SCHEMA, not merely in the prose. A field a model can
		// waffle into is how a terminal tool became a scratchpad.
		props, _ := td.Function.Parameters["properties"].(map[string]any)
		reason, _ := props["reason"].(map[string]any)
		if _, ok := reason["enum"]; !ok {
			t.Error("reason is not enumerated in the schema, so any string reaches the driver")
		}
		return
	}
	t.Fatalf("%q is not in toolDefs", yieldTool)
}

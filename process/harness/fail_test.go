package harness

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/signadot/verse/process"
)

// failTurn is one model turn that calls the fail tool with the given raw arguments.
func failTurn(args string) scriptedTurn {
	tc := mkTC("c1", failTool)
	tc.Function.Arguments = args
	return scriptedTurn{msg: chatMessage{Role: "assistant", ToolCalls: []toolCall{tc}}, finish: "tool_calls"}
}

// The fail tool is TERMINAL: it ends the loop with an error instead of being dispatched to
// the executor. Without it the model has no structural way to distinguish "I did it" from
// "I could not" — both exit through "no more tool calls" — which is why a process that could
// not open the file it was asked to review completed as `done` with prose explaining itself.
func TestFailToolTerminatesWithReason(t *testing.T) {
	d := &llmDriver{
		client: &fakeCompleter{turns: []scriptedTurn{failTurn(`{"reason":"could not open review.md"}`)}},
		prompt: "review review.md", log: io.Discard,
	}
	tu, more, err := d.Next(context.Background(), process.StepResult{})
	if err == nil {
		t.Fatal("fail must end the loop with an error")
	}
	if more {
		t.Error("fail must not ask for another turn")
	}
	if tu.Name != "" {
		t.Errorf("fail must not be dispatched to the executor, got tool %q", tu.Name)
	}
	if !strings.Contains(err.Error(), "could not open review.md") {
		t.Errorf("the model's reason must survive: %v", err)
	}
}

// A fail whose arguments do not parse is still a fail. Swallowing it into a successful
// completion because the JSON was malformed is the exact outcome this tool exists to stop.
func TestFailToolWithoutReasonStillFails(t *testing.T) {
	for _, args := range []string{"", "{}", "not json", `{"reason":"   "}`} {
		d := &llmDriver{
			client: &fakeCompleter{turns: []scriptedTurn{failTurn(args)}},
			prompt: "go", log: io.Discard,
		}
		_, more, err := d.Next(context.Background(), process.StepResult{})
		if err == nil || more {
			t.Errorf("args %q: want a terminal error, got more=%v err=%v", args, more, err)
		}
	}
}

// An ordinary tool is unaffected: it still dispatches to the executor.
func TestNonFailToolStillDispatches(t *testing.T) {
	d := &llmDriver{
		client: &fakeCompleter{turns: []scriptedTurn{
			{msg: chatMessage{Role: "assistant", ToolCalls: []toolCall{mkTC("c1", "read_file")}}, finish: "tool_calls"},
		}},
		prompt: "go", log: io.Discard,
	}
	tu, more, err := d.Next(context.Background(), process.StepResult{})
	if err != nil || !more {
		t.Fatalf("want a dispatched tool, got more=%v err=%v", more, err)
	}
	if tu.Name != "read_file" {
		t.Errorf("tool = %q, want read_file", tu.Name)
	}
}

// The tool must be advertised to the model, or the branch exists and is never taken. The
// description has to say that stopping reads as success — a model that does not know the
// default is success has no reason to reach for this.
func TestFailToolIsDeclared(t *testing.T) {
	for _, td := range toolDefs() {
		if td.Function.Name == failTool {
			if !strings.Contains(td.Function.Description, "SUCCESS") {
				t.Error("the description must warn that simply stopping reads as success")
			}
			return
		}
	}
	t.Fatalf("%q is not in toolDefs", failTool)
}

package task

import (
	"context"
	"strings"
	"testing"
)

// callThenDone emits one CallAgent, then reports done; it records folded messages
// (steers and child completions) so a test can assert the join.
type callThenDone struct {
	called bool
	folded []string
}

func (d *callThenDone) Next(_ context.Context, _ StepResult) (ToolUse, bool, error) {
	if !d.called {
		d.called = true
		return ToolUse{Name: "delegate", CallAgent: &CallAgentRequest{Agent: "child"}}, true, nil
	}
	return ToolUse{}, false, nil
}

func (d *callThenDone) Fold(msgs []string) { d.folded = append(d.folded, msgs...) }

// A CallAgent starts a detached child; the parent structurally joins it and folds
// its completion back into the conversation before finishing.
func TestCallAgentAsyncDelegateAndJoin(t *testing.T) {
	d := &callThenDone{}
	p := NewTask("p", toolSup(allowAll(), AutoResolver{}), d, &recExec{})
	log := &syncLog{}
	p.SetLog(log)
	p.SetDelegate(func(parent *Task, req *CallAgentRequest) (*Task, error) {
		return parent.NewChild("child-1", req, &scriptDriver{}, &recExec{}), nil
	})

	out, err := p.Run(context.Background())
	if err != nil || out.Reason != "done" {
		t.Fatalf("run: reason=%q err=%v", out.Reason, err)
	}
	joined := false
	for _, m := range d.folded {
		if strings.Contains(m, "child-1 completed") {
			joined = true
		}
	}
	if !joined {
		t.Fatalf("child completion was not folded into the parent: %v", d.folded)
	}
	if !strings.Contains(log.String(), "⑃ child-1 ended (done)") {
		t.Fatalf("no child-completion line in the log:\n%s", log.String())
	}
}

package task

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// withdrawDriver proposes its steps for somebody who may stop waiting: Withdrawn is that
// somebody's going, and what the driver read after each step is kept.
type withdrawDriver struct {
	steps []ToolUse
	gone  chan struct{}
	read  []StepResult
	i     int
}

func (d *withdrawDriver) Next(_ context.Context, prev StepResult) (ToolUse, bool, error) {
	if d.i > 0 {
		d.read = append(d.read, prev)
	}
	if d.i == len(d.steps) {
		return ToolUse{}, false, nil
	}
	d.i++
	return d.steps[d.i-1], true, nil
}

func (d *withdrawDriver) Withdrawn(ToolUse) <-chan struct{} { return d.gone }

// A DRIVER'S WITHDRAWAL ends a standing question the way a say does: the question leaves the
// docket saying nobody is waiting, the action is not taken, and the driver reads a refusal it
// goes on from.
func TestADriverWithdrawsTheQuestionAboutItsAction(t *testing.T) {
	d := &withdrawDriver{steps: []ToolUse{tool("A")}, gone: make(chan struct{})}
	ex := &recExec{}
	reg := NewRegistry()
	dropped := make(chan string, 4)
	reg.OnDrop(func(_ GateQuestion, why string) { dropped <- why })
	reg.OnPost(func(Pending) { close(d.gone) }) // whoever asked goes as the question is posed

	p := NewTask("t", toolSup(askOn("A"), reg), d, ex)
	p.SetLog(io.Discard)
	// Bounded: a withdrawal not honoured is a question that stands forever.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := p.Run(ctx)
	if err != nil || out.Reason != string(Done) {
		t.Fatalf("reason=%q err=%v, want done", out.Reason, err)
	}
	select {
	case why := <-dropped:
		if !strings.Contains(why, "no longer waiting") {
			t.Errorf("the docket was told %q, which does not say nobody is waiting", why)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the question never left the docket")
	}
	if len(ex.order) != 0 {
		t.Fatalf("the withdrawn action ran: %v", ex.order)
	}
	if len(d.read) != 1 || !d.read[0].Blocked || !strings.Contains(d.read[0].Note, "no longer waiting") {
		t.Fatalf("the driver read %+v, want a refusal saying nobody is waiting", d.read)
	}
}

// A withdrawal after the answer is about a decision already made: the approved action runs,
// and whoever goes while it runs withdraws nothing.
func TestAWithdrawalAfterTheAnswerChangesNothing(t *testing.T) {
	d := &withdrawDriver{steps: []ToolUse{tool("A")}, gone: make(chan struct{})}
	ex := &goneExec{gone: d.gone}
	reg := NewRegistry()
	reg.OnPost(func(q Pending) {
		go reg.Answer(q.Question.ID, Resolution{Proceed: true, By: "human:test"})
	})
	p := NewTask("t", toolSup(askOn("A"), reg), d, ex)
	p.SetLog(io.Discard)
	out, err := p.Run(context.Background())
	if err != nil || out.Reason != string(Done) {
		t.Fatalf("reason=%q err=%v, want done", out.Reason, err)
	}
	if !ex.ran || len(d.read) != 1 || d.read[0].Blocked {
		t.Fatalf("ran=%v, read %+v: want the approved action run", ex.ran, d.read)
	}
}

// goneExec runs an action by closing gone: whoever asked for it goes while it runs.
type goneExec struct {
	gone chan struct{}
	ran  bool
}

func (e *goneExec) Execute(_ context.Context, tu ToolUse) (StepResult, []ToolUse, error) {
	e.ran = true
	close(e.gone)
	time.Sleep(20 * time.Millisecond) // a late withdrawal has time to arrive, and must change nothing
	return StepResult{Use: tu}, nil, nil
}

package task

import (
	"context"
	"errors"
	"io"
	"testing"
)

// hearDriver proposes its steps and keeps what it hears, with who said it.
type hearDriver struct {
	steps []ToolUse
	i     int
	heard []Said
}

func (d *hearDriver) Next(context.Context, StepResult) (ToolUse, bool, error) {
	if d.i == len(d.steps) {
		return ToolUse{}, false, nil
	}
	d.i++
	return d.steps[d.i-1], true, nil
}

func (d *hearDriver) Hear(said []Said) { d.heard = append(d.heard, said...) }

// A HEARER IS TOLD WHO SPOKE: what a gate's answerer said with an approval reaches it as the
// gate's, at the boundary after the action it is about.
func TestAHearerIsToldTheGatesWordsAsTheGates(t *testing.T) {
	d := &hearDriver{steps: []ToolUse{tool("A")}}
	reg := NewRegistry()
	reg.OnPost(func(q Pending) {
		go reg.Answer(q.Question.ID, Resolution{Proceed: true, By: "human:ana", Steer: []string{"next time, a draft"}})
	})
	p := NewTask("t", toolSup(askOn("A"), reg), d, &recExec{})
	p.SetLog(io.Discard)
	if out, err := p.Run(context.Background()); err != nil || out.Reason != string(Done) {
		t.Fatalf("reason=%q err=%v", out.Reason, err)
	}
	if len(d.heard) != 1 || d.heard[0].Kind != SaidByGate || d.heard[0].By != "human:ana" ||
		d.heard[0].Text == "" {
		t.Fatalf("heard %+v, want the gate's words, as the gate's", d.heard)
	}
}

// unsteerable is a driver with no turn for an operator's words.
type unsteerable struct{ hearDriver }

func (unsteerable) Unsteerable() string { return "it runs once" }

// AN UNSTEERABLE DRIVER'S TASK REFUSES A SAY WHEN IT IS SAID, with why, rather than taking words
// nobody will hear.
func TestASayToAnUnsteerableDriverIsRefused(t *testing.T) {
	d := &unsteerable{hearDriver{steps: []ToolUse{tool("A")}}}
	reg := NewRegistry()
	p := NewTask("t", toolSup(askOn("A"), reg), d, &recExec{})
	p.SetLog(io.Discard)
	said := make(chan error, 1)
	reg.OnPost(func(q Pending) {
		said <- p.Steer("do B instead", "human:bo", "")
		go reg.Answer(q.Question.ID, Resolution{Proceed: true, By: "human:ana"})
	})
	if out, err := p.Run(context.Background()); err != nil || out.Reason != string(Done) {
		t.Fatalf("reason=%q err=%v: the refused say must not have withdrawn the question", out.Reason, err)
	}
	if err := <-said; !errors.Is(err, ErrUnsteerable) {
		t.Fatalf("the say = %v, want ErrUnsteerable", err)
	}
	for _, s := range d.heard {
		if s.Kind == SaidByOperator {
			t.Fatalf("the refused say reached the driver: %+v", d.heard)
		}
	}
}

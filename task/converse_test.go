package task

import (
	"context"
	"strings"
	"testing"
	"time"
)

// turnOf waits for the task's turn to reach the docket.
func turnOf(t *testing.T, reg *Registry) Pending {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range reg.Pending() {
			if p.Question.Gate == "turn" {
				return p
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the person was never handed the turn")
	return Pending{}
}

// A CONVERSATION HANDS THE PERSON THE TURN when the driver goes quiet, folds their words as a
// say, and ends — contract, finisher, done — only when they end it. The finisher runs once,
// after the end, not after each turn.
func TestAConversationEndsWhenThePersonEndsIt(t *testing.T) {
	reg := NewRegistry()
	d := &scripted{acts: []string{"a"}, result: "the answer"}
	f := &finisher{action: "deliver"}
	p := NewTask("t", NewSupervisor(&Vector{Default: Auto}, reg), d, &recorder{})
	p.SetFinisher(f)
	p.SetConverse(reg)
	done := make(chan Outcome, 1)
	go func() { oc, _ := p.Run(context.Background()); done <- oc }()

	turn := turnOf(t, reg)
	if !strings.HasPrefix(turn.Question.ID, "t"+GateSep) || turn.Question.Origin != "t" {
		t.Fatalf("the turn's question is %+v", turn.Question)
	}
	if ids := OptionIDs(turn.Question.Options); strings.Join(ids, ",") != Reply+","+End {
		t.Fatalf("the turn offers %v, want reply and end", ids)
	}
	if f.ran != 0 {
		t.Fatal("the finisher ran before the conversation ended")
	}
	reg.Answer(turn.Question.ID, Resolution{Choice: Reply, By: "user:t", Steer: []string{"and then?"}})

	second := turnOf(t, reg)
	if second.Question.ID == turn.Question.ID {
		t.Fatal("the same turn was posed twice")
	}
	if strings.Join(d.steers, "|") != "and then?" {
		t.Fatalf("the person's words did not reach the driver: %v", d.steers)
	}
	reg.Answer(second.Question.ID, Resolution{Choice: End, By: "user:t"})

	select {
	case oc := <-done:
		if oc.Reason != string(Done) {
			t.Fatalf("ended %s", oc.Reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not end after the person ended it")
	}
	if f.ran != 1 {
		t.Fatalf("the finisher ran %d time(s), want once after the end", f.ran)
	}
	if len(reg.Pending()) != 0 {
		t.Fatalf("the docket still holds %+v", reg.Pending())
	}
}

// A SAY IS THE TURN TAKEN: the standing question is withdrawn rather than left for an answer
// already given, and the words reach the driver.
func TestASayWithdrawsTheStandingTurn(t *testing.T) {
	reg := NewRegistry()
	dropped := make(chan string, 4)
	reg.OnDrop(func(q GateQuestion, _ string) { dropped <- q.ID })
	d := &scripted{result: "hi"}
	p := NewTask("t", NewSupervisor(&Vector{Default: Auto}, reg), d, &recorder{})
	p.SetConverse(reg)
	done := make(chan Outcome, 1)
	go func() { oc, _ := p.Run(context.Background()); done <- oc }()

	turn := turnOf(t, reg)
	if err := p.Steer("carry on", "user:t"); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-dropped:
		if id != turn.Question.ID {
			t.Fatalf("dropped %q, want the turn %q", id, turn.Question.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn was not withdrawn by the say")
	}
	next := turnOf(t, reg) // the driver spoke again; the person gets the turn again
	// A say is folded as steering, with its preamble; the words are at the end of it.
	if len(d.steers) != 1 || !strings.HasSuffix(d.steers[0], "carry on") {
		t.Fatalf("the say did not reach the driver: %v", d.steers)
	}
	reg.Answer(next.Question.ID, Resolution{Choice: End, By: "user:t"})
	<-done
}

// A CANCEL UNDER THE TURN ends the run cancelled and takes the question with it.
func TestACancelUnderTheTurnWithdrawsIt(t *testing.T) {
	reg := NewRegistry()
	d := &scripted{result: "hi"}
	p := NewTask("t", NewSupervisor(&Vector{Default: Auto}, reg), d, &recorder{})
	p.SetConverse(reg)
	done := make(chan Outcome, 1)
	go func() { oc, _ := p.Run(context.Background()); done <- oc }()
	turnOf(t, reg)
	p.Cancel("enough")
	select {
	case oc := <-done:
		if oc.Reason != string(Cancelled) {
			t.Fatalf("ended %s, want cancelled", oc.Reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not end on cancel")
	}
	if len(reg.Pending()) != 0 {
		t.Fatalf("the docket still holds the turn: %+v", reg.Pending())
	}
}

// Waiting names the turn: property 5 is met by the question, not by an exception.
func TestWaitingNamesTheTurn(t *testing.T) {
	s := state{ph: phTurn}
	if w := s.Waiting(); !strings.Contains(w, "turn") {
		t.Fatalf("Waiting() = %q", w)
	}
}

// A driver that holds no conversation cannot converse: the turn would be words nobody hears,
// and the run would stand at a question for ever. The run ends when the driver stops.
func TestConverseIsRefusedForADriverWithNoConversation(t *testing.T) {
	reg := NewRegistry()
	d := &scriptDriver{} // proposes nothing and folds nothing
	p := NewTask("t", NewSupervisor(&Vector{Default: Auto}, reg), d, &recExec{})
	p.SetConverse(reg)
	oc, err := p.Run(context.Background())
	if err != nil || oc.Reason != string(Done) {
		t.Fatalf("run: %v %+v", err, oc)
	}
	if len(reg.Pending()) != 0 {
		t.Fatalf("a turn was posed to a driver that cannot hear the answer: %+v", reg.Pending())
	}
}

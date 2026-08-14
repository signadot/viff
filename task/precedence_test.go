package task

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

// THE MERGE POLICY, exercised. A task's boundary IS a sequencer — one goroutine merging its
// inputs — so who goes first is a choice rather than a derivation, and these are the choices.
// The point of testing them is that they used to be inferrable only from the order of nine
// statements in Run, which is not a place anybody can disagree with a policy.

// askOn returns a vector that stops on one tool by name.
func askOn(tool string) *Vector {
	return &Vector{Default: Auto, Gates: []Gate{
		{Name: "g", Mode: Ask, Match: GateMatch{Tools: []string{tool}}},
	}}
}

// An INTERVENTION preempts a standing question: the question is abandoned, the action is not
// taken, and the driver is told so as a refusal it can re-plan around.
//
// The justification is that a say is about THIS RUN — it may redirect the task into work the
// question no longer bears on — and leaving somebody staring at a question about an action the
// task will now never take is worse than withdrawing it. The withdrawal is not silent: the
// question leaves the docket through the registry's own drop path, which is what tells
// mirror.DropQuestion.
func TestASayWithdrawsAStandingQuestion(t *testing.T) {
	d := &steerDriver{steps: []ToolUse{tool("A"), tool("B")}}
	ex := &recExec{}
	reg := NewRegistry()

	dropped := make(chan string, 4)
	reg.OnDrop(func(q GateQuestion) { dropped <- q.ID })

	p := NewTask("t", NewSupervisor(askOn("A"), reg), d, ex)
	p.SetLog(io.Discard)
	// Nobody ever answers. The say is the discharge — which is the whole point: a wait whose
	// only exit is a person choosing to look is a wait that fails the contract.
	reg.OnPost(func(Pending) { p.Steer("stop that, do B instead", "human:test") })

	out, err := p.Run(context.Background())
	if err != nil || out.Reason != string(Done) {
		t.Fatalf("reason=%q err=%v, want done", out.Reason, err)
	}
	// The registry announces the drop from the goroutine that was blocked on the question, so
	// it can land just after Run returns.
	select {
	case <-dropped:
	case <-time.After(2 * time.Second):
		t.Fatal("the question never left the docket: a withdrawn question the docket was not " +
			"told about is one nobody can see is gone")
	}
	if len(dropped) != 0 {
		t.Fatalf("%d extra question(s) left the docket", len(dropped))
	}
	// A never ran: the question was about it and the question was abandoned.
	for _, r := range ex.order {
		if r == "A" {
			t.Fatal("the action ran even though its question was withdrawn")
		}
	}
	// And the driver both heard the operator and was told its action did not happen.
	var heard bool
	for _, e := range d.events {
		if e == "steer:stop that, do B instead" {
			heard = true
		}
	}
	if !heard {
		t.Fatalf("the say never reached the conversation: %v", d.events)
	}
}

// A PAUSE withdraws one too, and a RESUME does not — which is the one place this loop does not
// follow the rule as written.
//
// The design lists all four interventions (cancel, pause, resume, say) as preempting a standing
// question, on the ground that all four are about this run. Three of them are: a cancel ends
// it, a pause suspends it, a say may redirect it. A resume is not, in the only situation where
// it could meet a standing question — a question can only be pending if the task was NOT held,
// so there is no hold to lift, and the resume changes nothing about what the task is doing. It
// would destroy a question for nothing, which an operator can do by clicking resume twice.
//
// So the rule implemented is the justification rather than the enumeration: an intervention
// withdraws a standing question when it changes what the task does next.
func TestAPauseWithdrawsAStandingQuestionAndAResumeDoesNot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		act      func(*Task)
		withdraw bool
	}{
		{"pause", func(p *Task) { p.Pause() }, true},
		{"resume", func(p *Task) { p.Resume() }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &scriptDriver{steps: []ToolUse{tool("A")}}
			reg := NewRegistry()
			dropped := make(chan string, 4)
			reg.OnDrop(func(q GateQuestion) { dropped <- q.ID })

			p := NewTask("t", NewSupervisor(askOn("A"), reg), d, &recExec{})
			p.SetLog(io.Discard)
			var once sync.Once
			reg.OnPost(func(pd Pending) {
				once.Do(func() {
					go func() {
						tc.act(p)
						time.Sleep(50 * time.Millisecond)
						// Whatever happened to the question, this run has to be able to end.
						reg.Answer(pd.Question.ID, Resolution{Proceed: false, By: "human:test"})
						p.Resume()
						p.Cancel("done here")
					}()
				})
			})
			p.Run(context.Background())

			select {
			case <-dropped:
				if !tc.withdraw {
					t.Fatal("a resume dissolved a question it had no hold to lift")
				}
			case <-time.After(300 * time.Millisecond):
				if tc.withdraw {
					t.Fatal("the question outlived the intervention that was about to suspend it")
				}
			}
		})
	}
}

// A POLICY CHANGE does not. Sliding a running task to AUTO says "stop asking me about actions
// like this", not "approve the one already in front of me" — and dissolving the pending
// question would turn a policy change into approval of a specific action nobody looked at. The
// operator never saw what was being asked, and an envelope that widens is not consent to what
// was already refused entry.
//
// So the standing question stands and is answered on its own terms; the new vector governs the
// next evaluation.
func TestAPolicyChangeLeavesAStandingQuestionStanding(t *testing.T) {
	d := &steerDriver{steps: []ToolUse{tool("A")}}
	reg := NewRegistry()

	var mu sync.Mutex
	var dropped int
	reg.OnDrop(func(GateQuestion) { mu.Lock(); dropped++; mu.Unlock() })

	p := NewTask("t", NewSupervisor(askOn("A"), reg), d, &recExec{})
	p.SetLog(io.Discard)
	var once sync.Once
	reg.OnPost(func(pd Pending) {
		once.Do(func() {
			go func() {
				// The slide arrives while the question is standing.
				p.SetVector(&Vector{Default: Auto}, "human:test")
				// Long enough for a machine that DID dissolve it to have done so, and for
				// the drop to be recorded.
				time.Sleep(50 * time.Millisecond)
				reg.Answer(pd.Question.ID, Resolution{Proceed: false, By: "human:test"})
			}()
		})
	})

	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if dropped != 0 {
		t.Fatal("the autonomy slide dissolved a question that was already standing, which " +
			"turns a policy change into approval of an action nobody looked at")
	}
}

// A CHILD'S RESULT is not an operator input and does not preempt: it folds at the hinge, like
// any other work the task produced for itself, and it never overtakes an action already
// proposed. Preemption is for people.
func TestAChildsResultDoesNotOvertakeAProposedAction(t *testing.T) {
	// The driver delegates, then proposes a slow action, then ends its turn. The child
	// completes WHILE that action is running.
	d := &delegateThenAct{}
	ran := make(chan struct{})
	ex := &blockingExec{on: "slow", entered: ran, release: make(chan struct{})}

	p := NewTask("p", NewSupervisor(autoVec(), AutoResolver{}), d, ex)
	p.SetLog(io.Discard)
	childRelease := make(chan struct{})
	p.SetSpawn(func(pt *Task, mode ChildMode, req *CallAgentRequest) (*Task, error) {
		return pt.NewChild("kid", mode, req, &waitThenDone{release: childRelease}, &recExec{})
	})

	done := make(chan Outcome, 1)
	go func() { oc, _ := p.Run(context.Background()); done <- oc }()

	<-ran               // the slow action is in flight
	close(childRelease) // the child finishes now, mid-action
	time.Sleep(30 * time.Millisecond)
	// It has NOT been folded: the action is still what the task is doing.
	if got := d.foldedBefore(); got != 0 {
		t.Fatalf("%d message(s) folded while an action was in flight — a child's result "+
			"preempted an action already proposed", got)
	}
	close(ex.release)
	<-done
	if !d.sawChild() {
		t.Fatal("the child's result never reached the conversation at all")
	}
}

// delegateThenAct: delegate, then one slow action, then end the turn.
type delegateThenAct struct {
	mu     sync.Mutex
	i      int
	folded []string
	acting bool
}

func (d *delegateThenAct) Next(context.Context, StepResult) (ToolUse, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.i++
	switch d.i {
	case 1:
		return ToolUse{Name: "delegate", CallAgent: &CallAgentRequest{Agent: "kid",
			Grant: Grant{Allow: []GateMatch{{}}}}}, true, nil
	case 2:
		d.acting = true
		return ToolUse{Name: "slow"}, true, nil
	}
	return ToolUse{}, false, nil
}

func (d *delegateThenAct) Steer(msgs []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.folded = append(d.folded, msgs...)
}

// foldedBefore is how much reached the conversation while the action was still in flight.
func (d *delegateThenAct) foldedBefore() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.folded)
}

func (d *delegateThenAct) sawChild() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.folded) > 0
}

// blockingExec holds one named action until released, announcing that it has started.
type blockingExec struct {
	on      string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *blockingExec) Execute(ctx context.Context, tu ToolUse) (StepResult, []ToolUse, error) {
	if tu.Name != e.on {
		return StepResult{Use: tu, Output: "ok"}, nil, nil
	}
	e.once.Do(func() { close(e.entered) })
	select {
	case <-e.release:
	case <-ctx.Done():
		return StepResult{}, nil, ctx.Err()
	}
	return StepResult{Use: tu, Output: "ok"}, nil, nil
}

// waitThenDone finishes only once released.
type waitThenDone struct {
	release chan struct{}
	fired   bool
}

func (d *waitThenDone) Next(ctx context.Context, _ StepResult) (ToolUse, bool, error) {
	if !d.fired {
		d.fired = true
		select {
		case <-d.release:
		case <-ctx.Done():
		}
	}
	return ToolUse{}, false, nil
}

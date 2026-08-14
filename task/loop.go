package task

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"sync"
)

// Driver produces the next action the task wants to take, given the result of
// the previous one. It abstracts WHERE actions come from — an owned LLM loop, a
// compiled plan, or a human at a REPL — so the loop is source-of-action agnostic.
// Returning more=false says the driver has nothing further to propose. It does NOT say the
// task is over: with a child outstanding it means "I am waiting", and whether the task is
// over is the machine's question, answered where the whole pending list is visible.
type Driver interface {
	Next(ctx context.Context, prev StepResult) (tu ToolUse, more bool, err error)
}

// Conversation is an optional Driver capability: a driver that HOLDS one, into which the loop
// can fold a message. It delivers everything waiting at the boundary, before Next — so messages
// land cooperatively, honored at the next model turn, never interrupting an in-flight action.
// Drivers that don't implement it simply ignore them.
//
// It was called `Steerable.Steer`, and the rename is the point rather than tidying. `Steer`
// named two things one level apart: `Handle.Steer` is an OPERATOR saying something to a running
// task, and this is the loop delivering ANY message into a driver's conversation — an
// operator's say, yes, but also a child's result returning, a resolver's context attached to an
// answer, and the runtime asking for an answer to be repaired. Three of those four are not
// steering and nobody sent them. Calling the mechanism by the name of one of its callers is how
// "a child's result is a steer" became sayable, and it is not true.
//
// So: one MECHANISM, four speakers, and who was speaking is carried on the read plane rather
// than by having four channels — which the loop tried, with a mutex-guarded slice and a
// buffered channel that could not be ordered against each other, and that is why the precedence
// rule was unwritable. `Fold` is the codebase's own verb for this: a message folds in at the
// hinge.
type Conversation interface {
	Fold(messages []string)
}

// Resulter is an optional Driver capability: report a completed run's result
// (e.g. the agent's final message) so a parent CallAgent folds in the child's
// output rather than just a note that it completed.
type Resulter interface {
	Result() string
}

// Executor performs a proceed-approved action and returns its result plus any
// deferred actions to register on the current frame — teardowns paired with
// setups, à la Go's defer. It is the leaf where real side effects live (and, for
// a CallAgent, where a foreign one-shot or a recursive Task is invoked).
//
// Ownership rule: return a teardown as a defer to have it unwind at the current
// frame's close; to let a resource OUTLIVE the frame, do not defer it — return
// it in the result and let a later action own it (return, don't defer).
//
// OBLIGATION: Execute must honour ctx. The loop is one goroutine that pauses between actions,
// so an action that ignores cancellation is a wait this loop cannot see into or interrupt —
// `run_bash {command: "sleep 120"}` is a task nobody can steer for two minutes. That is a
// defect of the executor, not the fate of the loop, and it is stated here because a contract
// that says "every wait names its discharge" is otherwise quietly false at this seam. See
// bounds_test.go, which pins the obligation rather than the behaviour of any one executor.
type Executor interface {
	Execute(ctx context.Context, tu ToolUse) (StepResult, []ToolUse, error)
}

// AnswerCheck judges the answer a task is about to return against what its spawn declared it
// must be. It is a PREDICATE ON A VALUE, consulted by the loop at the moment the input would
// go quiet — which is the difference between this and the version that lived inside the llm
// driver, where it was a verdict about a MOMENT: the first time the model stopped talking,
// possibly while a child was still editing the tree it was describing.
//
// Check returns the repair to feed back (the run continues, and the answer is checked again
// at the next quiescence), or an error (the run fails), or neither (the answer stands). The
// repair budget belongs to the implementation, because how many times it is worth asking is a
// property of the contract rather than of the loop.
type AnswerCheck interface {
	Check(answer string) (repair string, err error)
}

// SpawnFunc builds a child Task for a CallAgent — the domain-specific step
// the core can't do itself (it must construct the child's Driver/Executor,
// typically an LLM driver seeded from req.Prompt). It should call
// parent.NewChild(...); the loop owns spawning, tracking, folding, and joining
// the returned child. CallAgent is uniformly async: spawn returns immediately,
// the child runs concurrently, and its result folds in at a later boundary.
type SpawnFunc func(parent *Task, mode ChildMode, req *CallAgentRequest) (*Task, error)

// childHandle tracks a running child so the parent can join it or cancel it
// individually — per-child cancel brings down one subtree without touching the
// parent or the siblings.
type childHandle struct {
	task   *Task
	cancel context.CancelFunc
}

// StepResult is what an executed action produced, fed back to the Driver as the
// basis for its next decision. Blocked carries a gate/scope refusal so the driver
// can re-plan rather than treating it as an error.
type StepResult struct {
	Use     ToolUse
	Output  string
	Err     error
	Blocked bool
	Note    string
}

// frame is a call-scoped defer stack — a stack activation record. Frames open at
// the root loop and at each CallAgent; deferred actions registered while a
// frame is current unwind LIFO when it closes.
type frame struct {
	label  string
	defers []ToolUse
}

// Outcome summarizes how a run ended and where the cursor stopped.
type Outcome struct {
	Task     string
	Reason   string // completed | cancelled | failed (a task.Status)
	Boundary int    // cursor position at exit
}

// Task is a first-class, addressable governed task — the handle every
// autonomy mode shares (invariant #1). It drives a cursor between actions: it
// only ever advances or holds; it never reverses. Control (pause/cancel) is
// latched and honored at the next boundary.
type Task struct {
	ID     string
	sup    *Supervisor
	driver Driver
	exec   Executor
	fin    Finisher    // the runtime's closing act, consulted when the input goes quiet
	ansck  AnswerCheck // what this spawn declared its answer must be; nil = no contract
	scope  Grant       // this task's own authority — checked against a CallAgent's Grant
	repo   func() *RepoContext
	log    io.Writer

	// touched only by the Run goroutine:
	idc        int
	seq        int                     // the boundary the machine last reached (for attribution)
	gateCancel context.CancelCauseFunc // withdraws the standing question, saying why
	cancelSeen bool                    // ctx.Done has already been turned into an input
	// written on the Run goroutine, read via Journal() from others — under p.mu:
	journal []string

	// test hook, called at each boundary before the cancellation check:
	onEnterBoundary func(seq int)

	// async children (spawn set before Run; children map touched only by Run):
	spawn    SpawnFunc
	children map[string]*childHandle
	failFast bool // cancel siblings when a child errors

	// shared with control callers:
	mu     sync.Mutex
	cancel context.CancelCauseFunc
	// inbox is ONE queue for everything that arrives from outside the Run goroutine — an
	// operator's say, a pause, a policy change, a child reporting. One queue because the
	// merge policy has to be stated somewhere, and it cannot be stated over three mechanisms
	// (a mutex-guarded slice, a buffered channel, a registry callback) that cannot be ordered
	// against each other. It is a slice rather than a channel so nothing ever blocks a
	// control caller and nothing is ever dropped for want of buffer — the loop's own bound
	// used to be 65 simultaneous child completions.
	inbox     []input
	wake      chan struct{} // cap 1: pings the Run goroutine that the inbox is non-empty
	pauseGate *pauseGate
	err       string // why this task terminated (failed/cancelled); see Err

	// read plane (event bus; Subscribe from any goroutine, emit from Run):
	busMu  sync.Mutex
	subs   map[int]*subscriber
	subID  int
	cursor int64   // per-task monotonic event cursor (assigned in emit)
	ring   []Frame // bounded retained window, for SubscribeSince(?since) replay
	// retain is this task's read-plane window in events; 0 = the package default. Set from
	// the Launch at spawn, because how much of the past is worth keeping is a property of
	// the work rather than of the runtime.
	retain int
	// open is the gates standing right now, keyed by decision id: an EvPending opens one and
	// its EvDecision closes it. The RING is not a substitute — it forgets, and what it
	// forgets first is the oldest, which for a long-standing gate is the gate itself.
	open map[string]Frame
	// ended latches on the terminal frame. After it, the read plane is a finite record: no
	// event will ever follow, so subscribers are closed rather than left listening to a
	// task that has stopped speaking.
	ended bool
}

func NewTask(id string, sup *Supervisor, d Driver, e Executor) *Task {
	sup.SetOrigin(id) // decisions from this task carry its id
	return &Task{
		ID:        id,
		sup:       sup,
		driver:    d,
		exec:      e,
		scope:     Grant{Allow: []GateMatch{{}}}, // default: wildcard (allowsAll)
		log:       io.Discard,
		children:  map[string]*childHandle{},
		wake:      make(chan struct{}, 1),
		pauseGate: &pauseGate{}, // root gate; children link into it via NewChild
	}
}

// SetSpawn sets the child factory used to build a CallAgent's sub-task.
func (p *Task) SetSpawn(f SpawnFunc) { p.spawn = f }

// SetFinisher installs the runtime's closing act. It is consulted by the LOOP when the input
// would otherwise go quiet, and its action runs through the same authority → gate → execute
// path as anything else — which is every property the driver-wrapping version bought, without
// the latch it needed to keep. See Finisher.
func (p *Task) SetFinisher(f Finisher) { p.fin = f }

// SetAnswerCheck installs this spawn's answer contract. nil (the default) means the run ends
// wherever the driver stops.
func (p *Task) SetAnswerCheck(a AnswerCheck) { p.ansck = a }

// SetBoundTo names the resolver binding that owns this task's questions, from the launch.
// Applied at spawn, before the loop runs.
func (p *Task) SetBoundTo(ref string) { p.sup.SetBoundTo(ref) }

// SetVector swaps the gate vector on the running supervisor — the autonomy slide, applied at
// the next EVALUATION with no restart. It emits an EvVector frame so the slide is observable
// on the read plane (the new default mode; the operator who slid it rides By).
//
// It is a POLICY change and not an intervention, which is why it does not disturb a question
// already standing: sliding a running task to AUTO says "stop asking me about actions like
// this", not "approve the one already in front of me". See machine.go's inVector.
func (p *Task) SetVector(v *Vector, by string) error {
	p.sup.SetVector(v)
	p.emit(Frame{Kind: EvVector, Mode: v.Default.String(), By: by})
	p.post(inVector{})
	return nil // the local handle: the task is right here, so delivery is not in doubt
}

// SetFailFast makes a child's error cancel its still-running siblings
// (errgroup-style). Off by default: siblings run to their own completion.
func (p *Task) SetFailFast(v bool) { p.failFast = v }

func (p *Task) SetScope(g Grant)              { p.scope = g }
func (p *Task) SetLog(w io.Writer)            { p.log = w }
func (p *Task) SetRepo(f func() *RepoContext) { p.repo = f }

// Journal returns a copy of the run's journal, safe to read from any goroutine.
func (p *Task) Journal() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.journal...)
}

// appendJournal records a line under p.mu — the journal is written on the Run
// goroutine but read via Journal() from others, so both go through the lock.
func (p *Task) appendJournal(line string) {
	p.mu.Lock()
	p.journal = append(p.journal, line)
	p.mu.Unlock()
}

// post puts an input on the queue and wakes the Run goroutine. Safe from any goroutine, and
// it never blocks: a control caller must not be held up by a loop that is busy, and a child
// reporting must not be held up by a parent that is not yet listening.
func (p *Task) post(in input) {
	p.mu.Lock()
	p.inbox = append(p.inbox, in)
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// take pulls the next queued input, if any.
func (p *Task) take() (input, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.inbox) == 0 {
		return nil, false
	}
	in := p.inbox[0]
	p.inbox = p.inbox[1:]
	return in, true
}

// Steer injects an instruction (re-prompt) into the running task. It is latched
// and honored at the next boundary, folded into the conversation before the next
// model turn — cooperative: it never interrupts an in-flight action, and it DOES
// preempt a question standing at a gate, because a run that has been told something new may
// be redirected into work that question no longer bears on.
// Requires the Driver to hold a Conversation; otherwise the say is ignored.
func (p *Task) Steer(instruction, by string) error {
	p.post(inSay{msgs: []convMsg{{text: instruction, by: by, kind: convSay}}})
	return nil
}

// Cancel is a subtree operation: it cancels this task's context, which the
// whole subtree derives from (top-down signal). The loop then joins its children
// bottom-up — children unwind before the parent's frame — and returns "cancelled"
// at the next boundary. A steer or another cancel arriving mid-teardown does not
// interrupt it.
func (p *Task) Cancel(reason string) error {
	p.mu.Lock()
	c := p.cancel
	p.mu.Unlock()
	if c != nil {
		c(fmt.Errorf("cancelled: %s", reason))
	}
	return nil
}

// Run drives the machine. It is a SHELL: read an input, call step, perform the effects. Every
// decision about what the task does next is in machine.go, over a value; everything here is
// the world — goroutines, contexts, channels, the executor.
//
// The shape matters for one reason: the machine emits at most one BLOCKING effect at a time,
// each with exactly one reply, so "which wait am I in" is a single field rather than a set of
// flags. A reply that arrives for a phase the machine has moved past is discarded, which is
// what lets an intervention preempt a standing gate without racing it.
func (p *Task) Run(ctx context.Context) (oc Outcome, err error) {
	ctx, cancel := context.WithCancelCause(ctx)
	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()
	defer cancel(nil)

	// A panic anywhere in this goroutine becomes this task's FAILURE, not the process's death.
	//
	// The seams are guarded where they run (see seam) and a child is guarded by its parent (see
	// runChild), which leaves this: the effects performed inline on the Run goroutine — the
	// answer contract, the finisher, the spawn factory, emit, and the executor calls that unwind
	// the frames at teardown. A panic in any of those used to leave the loop with no terminal
	// frame at all, which is worse than a crash and quieter: every subscriber stays open on a
	// task that has stopped speaking, and the durable entity says `running` forever, which is
	// exactly the leak the terminal-frame handling exists to prevent.
	//
	// So it ends the way any other infrastructure failure ends. fail.go already makes this
	// argument for a spawn that cannot be honoured — "that spawn errors while the engine keeps
	// running, rather than a nil that vanishes or a panic that stops the loop" — and a hosted
	// runtime carrying somebody else's driver has the same interest in one task's bug not taking
	// the fleet with it.
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		p.logf("↯ the loop panicked: %v\n%s", r, debug.Stack())
		err = fmt.Errorf("the task loop panicked: %v", r)
		p.setErr(err)
		// Only if nothing terminal has been said yet: property 1 is that a task reaches a
		// terminal state at most once and nothing follows it, and a panic during teardown can
		// arrive after the frame has already gone out.
		p.emitTerminal(Failed)
		oc = Outcome{Task: p.ID, Reason: string(Failed), Boundary: p.seq}
	}()

	s := state{
		frames:         []*frame{{label: "root"}},
		scope:          p.scope,
		failFast:       p.failFast,
		hasFinisher:    p.fin != nil,
		hasAnswerCheck: p.ansck != nil,
	}
	replies := make(chan input, 16)

	var in input = inStart{}
	for {
		if c, ok := in.(inChild); ok {
			delete(p.children, c.id) // the handle's job is done; the machine tracks the id
		}
		var effs []effect
		s, effs = step(s, in)
		out, err, done := p.perform(ctx, s, effs, replies)
		if done {
			return out, err
		}
		var ok bool
		if in, ok = p.next(ctx, s, replies); !ok {
			// Property 5, made mechanical: a wait with no discharge is a defect rather than
			// patience, and failing loudly is the difference between a bug that is found and
			// one that is waited out.
			err := fmt.Errorf("the loop has nothing to wait for and nothing to do (phase %s): "+
				"this is a defect in the machine, not a state a task can be in", s.ph)
			p.setErr(err)
			p.emit(Frame{Kind: EvStatus, Text: string(Failed)})
			return Outcome{Task: p.ID, Reason: string(Failed), Boundary: s.seq}, err
		}
	}
}

// perform executes one batch of effects. It returns the run's outcome once the teardown has
// run — the one effect that ends the loop rather than feeding it.
func (p *Task) perform(ctx context.Context, s state, effs []effect, replies chan<- input) (Outcome, error, bool) {
	for _, e := range effs {
		switch v := e.(type) {
		case effEmit:
			if v.f.Kind == EvBoundary {
				p.seq = v.f.Seq
				if p.onEnterBoundary != nil {
					p.onEnterBoundary(v.f.Seq)
				}
			}
			p.emit(v.f)

		case effJournal:
			p.appendJournal(v.line)

		case effLog:
			p.logf("%s", v.line)

		case effHold:
			// The pause CHAIN is the shell's, not the machine's: a pause is a subtree
			// property held by an ancestor's gate, and a per-task flag could not see it.
			// Cancel outranks it — hold returns ctx.Err().
			//
			// Almost every boundary meets no pause at all, and this is the one effect that
			// fires at EVERY boundary, so the unheld case is answered on the spot rather than
			// through a goroutine that would exist only to return immediately. The check
			// racing a pause that arrives just after it is not a defect: a pause is honoured
			// at the NEXT boundary either way, which is what hold's own check-then-return
			// does too.
			tok := v.tok
			if ctx.Err() != nil {
				replies <- inHeld{err: ctx.Err(), tok: tok}
			} else if p.pauseGate.heldOn() == nil {
				replies <- inHeld{tok: tok}
			} else {
				p.seam("the pause gate", replies,
					func(err error) input { return inHeld{err: err, tok: tok} },
					func() input { return inHeld{err: p.pauseGate.hold(ctx), tok: tok} })
			}

		case effAsk:
			prev, tok := v.prev, v.tok
			p.seam("the driver", replies,
				func(err error) input { return inDriver{err: err, tok: tok} },
				func() input {
					tu, more, err := p.driver.Next(ctx, prev)
					if err == nil && more {
						if p.repo != nil {
							tu.Repo = p.repo()
						}
						tu.ID = p.nextID()
						p.sup.Derive(&tu) // classify before the machine checks the envelope
					}
					return inDriver{tu: tu, more: more, err: err, answer: p.Result(), tok: tok}
				})

		case effGate:
			// The question gets its own context so an intervention can WITHDRAW it. Cancelling
			// that context is what tells the docket (Registry.OnDrop → mirror.DropQuestion)
			// rather than leaving somebody staring at a question about an action this task
			// will now never take.
			// WithCancelCause, so a withdrawal can say WHY. The registry reads the cause and
			// hands it to whoever publishes the docket, which is the difference between a
			// question that reads as "the task died" and one that reads as "an operator
			// redirected this run" — the same settling, and not the same news.
			gctx, gcancel := context.WithCancelCause(ctx)
			p.gateCancel = gcancel
			tu, tok := v.tu, v.tok
			p.seam("the gate", replies,
				func(err error) input { return inGate{err: err, tok: tok} },
				func() input {
					res, err := p.sup.Check(gctx, tu)
					return inGate{res: res, err: err, tok: tok}
				})

		case effDropGate:
			if p.gateCancel != nil {
				p.logf("↯ withdrawing the standing question: %s", v.why)
				p.gateCancel(errors.New(v.why))
				p.gateCancel = nil
			}

		case effExecute:
			tu, closing, tok := v.tu, v.closing, v.tok
			p.seam("the executor", replies,
				func(err error) input { return inExecuted{out: StepResult{Use: tu}, err: err, tok: tok} },
				func() input {
					if closing {
						out, err := p.fin.Do(ctx, tu)
						return inExecuted{out: StepResult{Use: tu, Output: out}, err: err, tok: tok}
					}
					out, defers, err := p.exec.Execute(ctx, tu)
					return inExecuted{out: out, defers: defers, err: err, tok: tok}
				})

		case effSpawn:
			// Performed inline: it touches the children map, which is the Run goroutine's.
			id, res := p.spawnChild(ctx, v.tu, v.mode)
			replies <- inSpawned{id: id, res: res, tok: v.tok}

		case effConverse:
			p.converse(v.msgs)

		case effCheck:
			repair, err := p.ansck.Check(v.answer)
			if repair != "" {
				p.logf("📐 the answer did not satisfy its contract; asking for a repair")
			}
			replies <- inChecked{repair: repair, err: err, tok: v.tok}

		case effFinish:
			tu, ok := p.fin.Finish(v.answer)
			if ok {
				// The repo context, exactly as a driver's action gets it. Leaving it off was
				// the one thing that made the CLOSING action less gateable than an ordinary
				// one: `GateMatch.OnPush` compares an effect's ref against RepoContext's push
				// target and `Branches` globs RepoContext's branch, so with no context both
				// fell through — and the closing action is the delivery push, which is the one
				// action those two fields were written for. A charter saying `onPush: true`
				// matched nothing.
				if p.repo != nil {
					tu.Repo = p.repo()
				}
				tu.ID = p.nextID()
				p.sup.Derive(&tu)
			}
			replies <- inFinish{tu: tu, ok: ok, tok: v.tok}

		case effCancelChildren:
			for _, id := range v.ids {
				if h, ok := p.children[id]; ok {
					h.cancel() // idempotent if already cancelled
				}
			}

		case effTeardown:
			out, err := p.teardown(ctx, v)
			return out, err, true
		}
	}
	return Outcome{}, nil, false
}

// seam runs one blocking call off the Run goroutine and delivers its reply EXACTLY ONCE.
//
// The defer is what makes "exactly once" true rather than usual. A seam is somebody else's
// code — a model client, an executor, a resolver — and the machine is waiting on a reply that
// only this call can produce, so a panic in it is not just a crash: it is a wait that can
// never be discharged, which is property 5 lost at the one place the loop cannot see into.
// Turning the panic into that seam's failure keeps the machine's account of itself true, and
// the task fails with the reason rather than the process dying with a stack.
func (p *Task) seam(what string, replies chan<- input, fail func(error) input, body func() input) {
	go func() {
		var out input
		defer func() {
			if r := recover(); r != nil {
				p.logf("↯ %s panicked: %v\n%s", what, r, debug.Stack())
				out = fail(fmt.Errorf("%s panicked: %v", what, r))
			}
			replies <- out
		}()
		out = body()
	}()
}

// next produces the input to fold. Arrivals go first — an operator's intervention preempts —
// and a cancellation is turned into an input exactly once, so the loop does not spin on a
// context that stays done. It returns false when the machine is blocked on nothing, which
// property 5 says cannot happen and which is therefore worth reporting rather than waiting on.
func (p *Task) next(ctx context.Context, s state, replies <-chan input) (input, bool) {
	for {
		// Cancel outranks everything, so it is read first — and exactly once, so the loop does
		// not spin on a context that stays done. Reading it first is not the same as acting on
		// it first: the machine LATCHES it and exits at the next boundary, which is what keeps
		// an action already proposed from being cut off mid-thought.
		if !p.cancelSeen && ctx.Err() != nil {
			p.cancelSeen = true
			return inCancel{cause: context.Cause(ctx)}, true
		}
		if in, ok := p.take(); ok {
			return in, true
		}
		if s.Waiting() == "" {
			return nil, false
		}
		if p.cancelSeen {
			select {
			case in := <-replies:
				return in, true
			case <-p.wake:
			}
			continue
		}
		select {
		case in := <-replies:
			return in, true
		case <-p.wake:
		case <-ctx.Done():
		}
	}
}

// converse delivers messages into the driver's conversation and narrates who was speaking.
// One mechanism, four speakers: the read plane is where they stay distinguishable, because
// that is where the distinction is actually read.
// fold is how somebody else's words reach the driver, and it says WHO is speaking.
//
// The loop knows and used to throw it away: a convMsg carries `by` and a kind, and this handed
// the driver bare text. So an operator's decision arrived in the model's conversation looking
// exactly like a tool result or its own earlier reasoning — one more thing to weigh — and a run
// told "stop verifying and produce your answer" read it, weighed it, and ran the same command
// again. That was watched happening on staging.
//
// What is added is provenance and STANDING, not emphasis. A model has no way to know that the
// thing which just appeared came from a person who can see the run from outside — why it was
// started, what else is happening, what it looks like from where they sit — and that is exactly
// what makes an instruction a decision rather than a suggestion. Saying so is the difference
// between context it has and context it cannot have.
//
// It says nothing about AUTHORITY, deliberately. An operator's word outranks the plan the model
// is following; it does not widen the grant or answer a gate, and a frame that implied otherwise
// would be promising something the loop then refuses.
//
// The runtime's own repair speaks for itself and is left alone: it already names what is wrong
// with the answer, and framing it as somebody talking would be a second voice for one machine.
func fold(m convMsg) string {
	switch m.kind {
	case convSay:
		return who(m.by, "An operator") + " is steering this run. They can see everything you " +
			"can and more — the run from outside, why it was started, and what else is " +
			"happening — so this is a decision about what you do NEXT rather than a suggestion " +
			"to weigh: act on it now, not after you finish what you were doing.\n\n" + m.text
	case convGate:
		return who(m.by, "The person who answered a gate") + " said this about the decision, " +
			"and they are looking at more of this run than you are:\n\n" + m.text
	}
	return m.text
}

// who names the speaker when there is one, and falls back to what they are when there is not.
// `by` is an identity like `human:scott`, which is worth showing: a run steered by a person and
// a run steered by a service are different things to be told.
func who(by, fallback string) string {
	if by == "" {
		return fallback
	}
	return by
}

func (p *Task) converse(msgs []convMsg) {
	d, ok := p.driver.(Conversation)
	if !ok {
		for _, m := range msgs {
			if m.kind == convSay {
				p.logf("⚠ say ignored: this driver holds no conversation")
			}
		}
		return
	}
	texts := make([]string, len(msgs))
	var says int
	for i, m := range msgs {
		texts[i] = fold(m)
		if m.kind == convSay {
			says++
		}
	}
	d.Fold(texts)
	if says > 0 {
		p.appendJournal(fmt.Sprintf("[say] %d said-to at boundary %d", says, p.seq))
		p.logf("↩ said-to: %d instruction(s)", says)
	}
	for _, m := range msgs {
		switch m.kind {
		case convChild:
			// Already on the plane as EvChild, where it belongs: it is the task's own work
			// returning, not somebody talking to it.
		case convRepair:
			p.emit(Frame{Kind: EvSay, By: "runtime:answer-contract", Seq: p.seq, Text: m.text})
		default:
			p.emit(Frame{Kind: EvSay, By: m.by, Seq: p.seq, Text: m.text})
		}
	}
}

// teardown is the last thing a run does: unwind the frames bottom-up and emit the terminal.
func (p *Task) teardown(ctx context.Context, td effTeardown) (Outcome, error) {
	// Record the reason BEFORE the terminal frame: the mirror projects err off that frame, and
	// setting it afterwards would leave the durable entity — the only thing a charter can
	// match or bind — carrying a failed task with no account of why.
	p.setErr(td.cause)
	if len(td.frames) > 0 {
		p.logf("— %s: unwinding %d frame(s)", td.status, len(td.frames))
	}
	for i := len(td.frames) - 1; i >= 0; i-- {
		p.unwind(ctx, td.frames[i])
	}
	p.emit(Frame{Kind: EvStatus, Text: string(td.status)})
	out := Outcome{Task: p.ID, Reason: string(td.status), Boundary: td.seq}
	if td.status == Done {
		return out, nil
	}
	return out, td.cause
}

// Result returns the task's final output — the driver's last message, when the
// driver reports one (an LLM's answer). Empty for drivers that produce no result
// (e.g. the scripted driver) or before the run has produced one. This is the same
// value a parent CallAgent folds in from a completed child, exposed for the top
// level so a hosted task's answer is observable over the wire.
func (p *Task) Result() string {
	if r, ok := p.driver.(Resulter); ok {
		return r.Result()
	}
	return ""
}

// spawnChild launches a CallAgent as a detached child: build it via the spawn
// factory, run it concurrently, and hand the driver an immediate "started"
// handle. The child's completion folds in later, at a boundary. The child
// manages its own frames/defers inside its own Run.
func (p *Task) spawnChild(ctx context.Context, tu ToolUse, mode ChildMode) (string, StepResult) {
	req := tu.CallAgent
	if p.spawn == nil {
		return "", StepResult{Use: tu, Err: fmt.Errorf("no spawn factory configured")}
	}
	if !p.scope.Subset(req.Grant) { // monotonicity (also enforced in NewChild)
		p.logf("⛔ call %s denied: child scope exceeds parent", req.Agent)
		return "", StepResult{Use: tu, Blocked: true, Note: "scope exceeds parent"}
	}
	child, err := p.spawn(p, mode, req)
	if err != nil {
		return "", StepResult{Use: tu, Err: err}
	}
	childCtx, childCancel := context.WithCancel(ctx)
	p.children[child.ID] = &childHandle{task: child, cancel: childCancel}
	p.logf("⑂ spawned child %s (agent=%s)", child.ID, req.Agent)
	p.emit(Frame{Kind: EvChild, Tool: req.Agent, Text: "spawned " + child.ID})
	go p.runChild(childCtx, child)
	return child.ID, StepResult{Use: tu,
		Output: fmt.Sprintf("started %s (handle=%s); result arrives when it completes", req.Agent, child.ID)}
}

// runChild is the child's goroutine, and the `defer` is the whole of property 3.
//
// A child is an in-process goroutine the parent owns, so its completion is not something to
// hope for — it is a defer away. Without this, a panicking child sent nothing and its parent
// waited on `p.completions` forever, which is not a hypothetical: it is how a task on staging
// hung with a subtree that had already stopped existing. `recover()` turns that into an
// OUTCOME, and then the parent's wait is discharged by the Go runtime rather than by the
// child behaving.
func (p *Task) runChild(ctx context.Context, child *Task) {
	done := inChild{
		id:      child.ID,
		reason:  string(Failed),
		failed:  true,
		summary: fmt.Sprintf("child %s ended without reporting", child.ID),
	}
	defer func() {
		if r := recover(); r != nil {
			// The stack goes to the log rather than into the conversation: the parent's agent
			// needs to know its subtask died, not how. The log is where somebody debugging it
			// looks.
			p.logf("↯ child %s panicked: %v\n%s", child.ID, r, debug.Stack())
			done = inChild{
				id:      child.ID,
				reason:  string(Failed),
				failed:  true,
				summary: fmt.Sprintf("child %s ended with error: it panicked: %v", child.ID, r),
			}
		}
		p.post(done)
	}()
	oc, err := child.Run(ctx)
	done = summarize(child, oc, err)
}

// summarize is what the parent's conversation is told about a finished child.
func summarize(child *Task, oc Outcome, err error) inChild {
	in := inChild{id: child.ID, reason: oc.Reason, failed: err != nil}
	switch {
	case err != nil:
		in.summary = fmt.Sprintf("child %s ended with error: %v", child.ID, err)
	default:
		in.summary = fmt.Sprintf("child %s completed (%s)", child.ID, oc.Reason)
		if out := child.Result(); out != "" {
			in.summary += ": " + out
		}
	}
	return in
}

// setErr records why this task is terminating. Read by Err() and, through it, projected
// onto the durable entity by the mirror.
func (p *Task) setErr(err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == "" { // first cause wins — a later unwind must not overwrite the reason
		p.err = err.Error()
	}
}

// Err reports why a terminal task failed or was cancelled, empty otherwise.
//
// It is the counterpart to Result, and the two are INDEPENDENT rather than exclusive: a
// driver can say something and then fail, and both halves are worth keeping. What Err does
// NOT cover is a task that fails at its task and reports so in prose — for an llm driver
// that is an ordinary completion, because the model's only structural exits are "keep going"
// and "stop" (see harness/llm.go, which is why the fail tool exists). Err is infrastructure
// failure; semantic failure is the charter's to check by naming what it expects.
func (p *Task) Err() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// unwind runs a frame's defers LIFO, best-effort. It uses a context detached
// from cancellation so cancel-time teardown can still make progress, and it does
// NOT re-gate the teardowns (they are author-declared, paired with already-
// approved setups, and must run even when the resolver is gone). Failures are
// recorded, never fatal — no atomicity is claimed.
func (p *Task) unwind(ctx context.Context, f *frame) {
	tctx := context.WithoutCancel(ctx)
	for i := len(f.defers) - 1; i >= 0; i-- {
		d := f.defers[i]
		d.ID = p.nextID()
		if _, _, err := p.exec.Execute(tctx, d); err != nil {
			p.logf("⚠ defer %s failed (%s): %v", d.Name, f.label, err)
		} else {
			p.logf("↩ defer %s (%s)", d.Name, f.label)
		}
	}
}

func (p *Task) nextID() string {
	p.idc++
	return fmt.Sprintf("%s%s%d", p.ID, GateSep, p.idc)
}

func (p *Task) logf(format string, a ...any) {
	fmt.Fprintf(p.log, format+"\n", a...)
}

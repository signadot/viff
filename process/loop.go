package process

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// Driver produces the next action the process wants to take, given the result of
// the previous one. It abstracts WHERE actions come from — an owned LLM loop, a
// compiled plan, or a human at a REPL — so the loop is source-of-action agnostic.
// Returning more=false ends the run (normal completion).
type Driver interface {
	Next(ctx context.Context, prev StepResult) (tu ToolUse, more bool, err error)
}

// Steerable is an optional Driver capability: fold injected instructions
// (re-prompt) into the running conversation. The loop delivers drained steers at
// the boundary, before Next — so they land cooperatively, honored at the next
// model turn, never interrupting an in-flight action. Drivers that don't
// implement it simply ignore steers.
type Steerable interface {
	Steer(instructions []string)
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
// a CallAgent, where a foreign one-shot or a recursive Process is invoked).
//
// Ownership rule: return a teardown as a defer to have it unwind at the current
// frame's close; to let a resource OUTLIVE the frame, do not defer it — return
// it in the result and let a later action own it (return, don't defer).
type Executor interface {
	Execute(ctx context.Context, tu ToolUse) (StepResult, []ToolUse, error)
}

// SpawnFunc builds a child Process for a CallAgent — the domain-specific step
// the core can't do itself (it must construct the child's Driver/Executor,
// typically an LLM driver seeded from req.Prompt). It should call
// parent.NewChild(...); the loop owns spawning, tracking, folding, and joining
// the returned child. CallAgent is uniformly async: spawn returns immediately,
// the child runs concurrently, and its result folds in at a later boundary.
type SpawnFunc func(parent *Process, mode ChildMode, req *CallAgentRequest) (*Process, error)

// childDone reports a finished child back to the parent's Run goroutine.
type childDone struct {
	id      string
	outcome Outcome
	err     error
}

// childHandle tracks a running child so the parent can join it or cancel it
// individually — per-child cancel brings down one subtree without touching the
// parent or the siblings.
type childHandle struct {
	proc   *Process
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
	Process  string
	Reason   string // completed | cancelled | failed (a process.Status)
	Boundary int    // cursor position at exit
}

// Process is a first-class, addressable governed process — the handle every
// autonomy mode shares (invariant #1). It drives a cursor between actions: it
// only ever advances or holds; it never reverses. Control (pause/cancel) is
// latched and honored at the next boundary.
type Process struct {
	ID     string
	sup    *Supervisor
	driver Driver
	exec   Executor
	scope  Grant // this process's own authority — checked against a CallAgent's Grant
	repo   func() *RepoContext
	log    io.Writer

	// touched only by the Run goroutine:
	frames []*frame
	seq    int
	idc    int
	// written on the Run goroutine, read via Journal() from others — under p.mu:
	journal []string

	// test hook, called at each boundary before the cancellation check:
	onEnterBoundary func(seq int)

	// async children (spawn set before Run; children map touched only by Run):
	spawn       SpawnFunc
	children    map[string]*childHandle
	completions chan childDone
	steerSig    chan struct{} // wakes an awaiting loop when a steer arrives
	failFast    bool          // cancel siblings when a child errors
	failing     bool          // fail-fast already fired (Run goroutine)

	// shared with control callers:
	mu        sync.Mutex
	cancel    context.CancelCauseFunc
	steer     []sayMsg // injected say-to instructions, drained at the boundary
	pauseGate *pauseGate
	err       string // why this process terminated (failed/cancelled); see Err

	// read plane (event bus; Subscribe from any goroutine, emit from Run):
	busMu  sync.Mutex
	subs   map[int]*subscriber
	subID  int
	cursor int64   // per-process monotonic event cursor (assigned in emit)
	ring   []Frame // bounded retained window, for SubscribeSince(?since) replay
}

func NewProcess(id string, sup *Supervisor, d Driver, e Executor) *Process {
	sup.SetOrigin(id) // decisions from this process carry its id
	return &Process{
		ID:          id,
		sup:         sup,
		driver:      d,
		exec:        e,
		scope:       Grant{Allow: []GateMatch{{}}}, // default: wildcard (allowsAll)
		log:         io.Discard,
		children:    map[string]*childHandle{},
		completions: make(chan childDone, 64),
		steerSig:    make(chan struct{}, 1),
		pauseGate:   &pauseGate{}, // root gate; children link into it via NewChild
	}
}

// SetSpawn sets the child factory used to build a CallAgent's sub-process.
func (p *Process) SetSpawn(f SpawnFunc) { p.spawn = f }

// SetVector swaps the gate vector on the running supervisor — the autonomy slide,
// applied at the next boundary with no restart. It emits an EvVector frame so the slide is
// observable on the read plane (the new default mode; the operator who slid it rides By).
func (p *Process) SetVector(v *Vector, by string) {
	p.sup.SetVector(v)
	p.emit(Frame{Kind: EvVector, Mode: v.Default.String(), By: by})
}

// SetFailFast makes a child's error cancel its still-running siblings
// (errgroup-style). Off by default: siblings run to their own completion.
func (p *Process) SetFailFast(v bool) { p.failFast = v }

func (p *Process) SetScope(g Grant)              { p.scope = g }
func (p *Process) SetLog(w io.Writer)            { p.log = w }
func (p *Process) SetRepo(f func() *RepoContext) { p.repo = f }

// Journal returns a copy of the run's journal, safe to read from any goroutine.
func (p *Process) Journal() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.journal...)
}

// appendJournal records a line under p.mu — the journal is written on the Run
// goroutine but read via Journal() from others, so both go through the lock.
func (p *Process) appendJournal(line string) {
	p.mu.Lock()
	p.journal = append(p.journal, line)
	p.mu.Unlock()
}

// Steer injects an instruction (re-prompt) into the running process. It is latched
// and honored at the next boundary, folded into the conversation before the next
// model turn — cooperative: it never interrupts an in-flight model call or action.
// Requires the Driver to implement Steerable; otherwise the steer is ignored.
func (p *Process) Steer(instruction, by string) {
	p.mu.Lock()
	p.steer = append(p.steer, sayMsg{text: instruction, by: by})
	p.mu.Unlock()
	select { // wake an awaiting loop, if any
	case p.steerSig <- struct{}{}:
	default:
	}
}

// sayMsg is a say-to instruction latched for the next boundary, with its speaker (by)
// so the folded-in utterance is emitted as an attributed `say` event.
type sayMsg struct{ text, by string }

func (p *Process) drainSteer() []sayMsg {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.steer
	p.steer = nil
	return s
}

// Cancel is a subtree operation: it cancels this process's context, which the
// whole subtree derives from (top-down signal). The loop then joins its children
// bottom-up — children unwind before the parent's frame — and returns "cancelled"
// at the next boundary. A steer or another cancel arriving mid-teardown does not
// interrupt it.
func (p *Process) Cancel(reason string) {
	p.mu.Lock()
	c := p.cancel
	p.mu.Unlock()
	if c != nil {
		c(fmt.Errorf("cancelled: %s", reason))
	}
}

// Run drives the cursor until the driver is done, or a cancel unwinds it. It is
// the one place gates fire, steering merges, and teardowns unwind — the reified
// boundary is the top of each iteration.
func (p *Process) Run(ctx context.Context) (Outcome, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()
	defer cancel(nil)

	p.pushFrame("root")
	var prev StepResult

	for {
		// ── BOUNDARY: the hinge ──────────────────────────────────
		p.seq++
		if p.onEnterBoundary != nil {
			p.onEnterBoundary(p.seq)
		}
		if ctx.Err() != nil { // subtree cancel exits here
			return p.cancelExit(ctx, context.Cause(ctx))
		}
		p.logf("· boundary %d", p.seq)
		p.emit(Frame{Kind: EvBoundary, Seq: p.seq})
		if err := p.pauseGate.hold(ctx); err != nil { // subtree pause; cancel outranks it
			return p.cancelExit(ctx, context.Cause(ctx))
		}

		// steering and child completions merge here — both fold into the driver's
		// conversation (as messages) before it decides the next action.
		p.foldSteer()
		p.foldCompletions()

		// 1. what does the process want to do next?
		tu, more, err := p.driver.Next(ctx, prev)
		if err != nil {
			// Record the reason BEFORE teardown: teardown emits the terminal EvStatus, and the
			// mirror projects err off that frame. Set it afterwards and the durable entity —
			// which is the only thing a charter can match or bind — would carry a failed
			// process with no account of why.
			p.setErr(err)
			p.joinChildren() // bottom-up: children unwind before the parent
			p.teardown(ctx, Failed)
			return Outcome{Process: p.ID, Reason: string(Failed), Boundary: p.seq}, err
		}
		if !more {
			// structured join: never complete while children run (no orphans).
			if len(p.children) > 0 {
				if err := p.awaitChild(ctx); err != nil {
					return p.cancelExit(ctx, err)
				}
				prev = StepResult{} // a completion (or steer) was folded; let the driver react
				continue
			}
			return p.teardown(ctx, Done), nil // normal return → unwind
		}
		if p.repo != nil {
			tu.Repo = p.repo()
		}
		tu.ID = p.nextID()

		// 2. authority: does the hard envelope admit this action at all?
		//
		// This runs BEFORE the vector, and it refuses instead of asking. The two layers
		// answer different questions — the grant says what the caller authorized, the
		// vector says which of that must be confirmed — so an action outside the envelope
		// is not a decision anyone present may make. Asking would offer the resolver an
		// authority the caller never delegated to them.
		//
		// Until now nothing consulted a process's own scope; Grant.Subset at the
		// call_agent boundary was its only reader, so an envelope bounded delegation and
		// nothing else. A default root scope is the wildcard, so a process that declared
		// no grant is unaffected — this refuses only what a caller explicitly narrowed.
		p.sup.Derive(&tu)
		if !p.scope.Permits(tu) {
			p.logf("⛔ %s refused: outside the process's grant", tu.Name)
			p.emit(Frame{Kind: EvExec, Tool: tu.Name, Err: "outside grant"})
			p.record(tu, Resolution{Question: GateQuestion{Gate: "grant", Reason: "outside the process's grant"}})
			prev = StepResult{Use: tu, Blocked: true,
				Note: "outside the grant this process was launched with: not permitted, and not something a gate can approve"}
			continue
		}

		// 3. gate: derive → evaluate → resolve (present human on ASK)
		res, err := p.sup.Check(ctx, tu)
		p.record(tu, res)
		if err != nil { // e.g. context cancelled while blocked at the gate
			return p.cancelExit(ctx, err)
		}
		for _, s := range res.Steer { // a gate that informs: fold context in at the next turn
			p.Steer(s, res.By)
		}
		if !res.Proceed {
			p.logf("⛔ %s held by gate %q (%s)", tu.Name, res.Question.Gate, res.Question.Reason)
			prev = StepResult{Use: tu, Blocked: true, Note: res.Question.Reason}
			continue // PARK/deny: cursor holds here; driver re-plans
		}

		// 4. execute
		if tu.CallAgent != nil { // uniformly async: spawn detached, join later
			prev = p.spawnChild(ctx, tu, res.ChildMode)
			continue
		}

		out, defers, err := p.exec.Execute(ctx, tu)
		if err != nil {
			if ctx.Err() != nil {
				return p.cancelExit(ctx, ctx.Err())
			}
			p.logf("✗ %s failed: %v", tu.Name, err)
			p.emit(Frame{Kind: EvExec, Tool: tu.Name, Err: err.Error()})
			prev = StepResult{Use: tu, Err: err} // no defers registered on failure
			continue
		}
		p.registerDefers(defers) // only on success — defer iff setup succeeded
		p.emit(Frame{Kind: EvExec, Tool: tu.Name, Text: evTrunc(out.Output)})
		prev = out
	}
}

// Result returns the process's final output — the driver's last message, when the
// driver reports one (an LLM's answer). Empty for drivers that produce no result
// (e.g. the scripted driver) or before the run has produced one. This is the same
// value a parent CallAgent folds in from a completed child, exposed for the top
// level so a hosted process's answer is observable over the wire.
func (p *Process) Result() string {
	if r, ok := p.driver.(Resulter); ok {
		return r.Result()
	}
	return ""
}

// spawnChild launches a CallAgent as a detached child: build it via the spawn
// factory, run it concurrently, and hand the driver an immediate "started"
// handle. The child's completion folds in later, at a boundary. The child
// manages its own frames/defers inside its own Run.
func (p *Process) spawnChild(ctx context.Context, tu ToolUse, mode ChildMode) StepResult {
	req := tu.CallAgent
	if p.spawn == nil {
		return StepResult{Use: tu, Err: fmt.Errorf("no spawn factory configured")}
	}
	if !p.scope.Subset(req.Grant) { // monotonicity (also enforced in NewChild)
		p.logf("⛔ call %s denied: child scope exceeds parent", req.Agent)
		return StepResult{Use: tu, Blocked: true, Note: "scope exceeds parent"}
	}
	child, err := p.spawn(p, mode, req)
	if err != nil {
		return StepResult{Use: tu, Err: err}
	}
	childCtx, childCancel := context.WithCancel(ctx)
	p.children[child.ID] = &childHandle{proc: child, cancel: childCancel}
	p.logf("⑂ spawned child %s (agent=%s)", child.ID, req.Agent)
	p.emit(Frame{Kind: EvChild, Tool: req.Agent, Text: "spawned " + child.ID})
	go func() {
		oc, cerr := child.Run(childCtx)
		p.completions <- childDone{id: child.ID, outcome: oc, err: cerr}
	}()
	return StepResult{Use: tu, Output: fmt.Sprintf("started %s (handle=%s); result arrives when it completes", req.Agent, child.ID)}
}

// foldSteer drains latched steer instructions into the driver.
func (p *Process) foldSteer() {
	msgs := p.drainSteer()
	if len(msgs) == 0 {
		return
	}
	s, ok := p.driver.(Steerable)
	if !ok {
		p.logf("⚠ say ignored: driver is not Steerable")
		return
	}
	texts := make([]string, len(msgs))
	for i, m := range msgs {
		texts[i] = m.text
	}
	s.Steer(texts)
	p.appendJournal(fmt.Sprintf("[say] %d said-to at boundary %d", len(msgs), p.seq))
	p.logf("↩ said-to: %d instruction(s)", len(msgs))
	for _, m := range msgs { // each folded-in utterance is an attributed `say` event
		p.emit(Frame{Kind: EvSay, By: m.by, Seq: p.seq, Text: m.text})
	}
}

// foldCompletions folds any children that finished since the last boundary
// (non-blocking).
func (p *Process) foldCompletions() {
	for {
		select {
		case cd := <-p.completions:
			p.absorbCompletion(cd)
		default:
			return
		}
	}
}

// awaitChild blocks for the next event while children are outstanding — a child
// completion (folded), a steer (re-loop to fold and act), or cancellation.
func (p *Process) awaitChild(ctx context.Context) error {
	if ctx.Err() != nil { // cancellation takes priority over a ready completion
		return ctx.Err()
	}
	p.logf("… awaiting %d child(ren)", len(p.children))
	select {
	case cd := <-p.completions:
		p.absorbCompletion(cd)
		return nil
	case <-p.steerSig:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// absorbCompletion removes a finished child and folds its result into the driver.
func (p *Process) absorbCompletion(cd childDone) {
	done := p.children[cd.id]
	delete(p.children, cd.id)
	summary := fmt.Sprintf("child %s completed (%s)", cd.id, cd.outcome.Reason)
	if cd.err != nil {
		summary = fmt.Sprintf("child %s ended with error: %v", cd.id, cd.err)
	} else if done != nil {
		if r, ok := done.proc.driver.(Resulter); ok {
			if out := r.Result(); out != "" {
				summary += ": " + out
			}
		}
	}
	p.appendJournal("[child] " + summary)
	p.logf("⑃ %s", summary)
	p.emit(Frame{Kind: EvChild, Tool: cd.id, Text: summary})
	if s, ok := p.driver.(Steerable); ok {
		s.Steer([]string{summary})
	}
	if cd.err != nil && p.failFast && !p.failing && len(p.children) > 0 {
		p.failing = true
		for id, sib := range p.children {
			p.logf("↯ fail-fast: cancelling sibling %s", id)
			sib.cancel()
		}
	}
}

// joinChildren waits (bottom-up) for every outstanding child to finish before the
// parent tears down. On cancel the children are already ctx-cancelled and will
// unwind and report — this is "signal top-down, join bottom-up": children's frames
// close before the parent's. It does not select on steer, so a cancel in progress
// is not interruptible.
func (p *Process) joinChildren() {
	for _, h := range p.children {
		h.cancel() // wind children down so we can join (idempotent if already cancelled)
	}
	for len(p.children) > 0 {
		cd := <-p.completions
		delete(p.children, cd.id)
		p.appendJournal("[child] " + cd.id + " joined on exit")
		p.logf("⑃ %s joined on exit", cd.id)
	}
}

// cancelExit joins children bottom-up, then unwinds the parent's own frame.
func (p *Process) cancelExit(ctx context.Context, cause error) (Outcome, error) {
	p.setErr(cause)
	p.joinChildren()
	return p.teardown(ctx, Cancelled), cause
}

// setErr records why this process is terminating. Read by Err() and, through it, projected
// onto the durable entity by the mirror.
func (p *Process) setErr(err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == "" { // first cause wins — a later unwind must not overwrite the reason
		p.err = err.Error()
	}
}

// Err reports why a terminal process failed or was cancelled, empty otherwise.
//
// It is the counterpart to Result, and the two are INDEPENDENT rather than exclusive: a
// driver can say something and then fail, and both halves are worth keeping. What Err does
// NOT cover is a process that fails at its task and reports so in prose — for an llm driver
// that is an ordinary completion, because the model's only structural exits are "keep going"
// and "stop" (see harness/llm.go, which is why the fail tool exists). Err is infrastructure
// failure; semantic failure is the charter's to check by naming what it expects.
func (p *Process) Err() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *Process) pushFrame(label string) {
	p.frames = append(p.frames, &frame{label: label})
}

func (p *Process) registerDefers(ds []ToolUse) {
	if len(ds) == 0 {
		return
	}
	f := p.frames[len(p.frames)-1]
	f.defers = append(f.defers, ds...)
}

func (p *Process) popFrameUnwind(ctx context.Context) {
	f := p.frames[len(p.frames)-1]
	p.frames = p.frames[:len(p.frames)-1]
	p.unwind(ctx, f)
}

// unwind runs a frame's defers LIFO, best-effort. It uses a context detached
// from cancellation so cancel-time teardown can still make progress, and it does
// NOT re-gate the teardowns (they are author-declared, paired with already-
// approved setups, and must run even when the resolver is gone). Failures are
// recorded, never fatal — no atomicity is claimed.
func (p *Process) unwind(ctx context.Context, f *frame) {
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

func (p *Process) teardown(ctx context.Context, why Status) Outcome {
	if len(p.frames) > 0 {
		p.logf("— %s: unwinding %d frame(s)", why, len(p.frames))
	}
	for len(p.frames) > 0 {
		p.popFrameUnwind(ctx)
	}
	p.emit(Frame{Kind: EvStatus, Text: string(why)})
	return Outcome{Process: p.ID, Reason: string(why), Boundary: p.seq}
}

func (p *Process) record(tu ToolUse, res Resolution) {
	line := fmt.Sprintf("[%s] %s → %s by %s (%s)",
		tu.ID, tu.Name, res.Question.Mode, res.By, res.Question.Reason)
	p.appendJournal(line)
	p.emit(Frame{
		Kind: EvDecision, ID: res.Question.ID, Tool: tu.Name, Gate: res.Question.Gate,
		Mode: res.Question.Mode.String(), Proceed: res.Proceed, By: res.By, Text: res.Question.Reason,
	})
}

func (p *Process) nextID() string {
	p.idc++
	return fmt.Sprintf("%s%s%d", p.ID, GateSep, p.idc)
}

func (p *Process) logf(format string, a ...any) {
	fmt.Fprintf(p.log, format+"\n", a...)
}

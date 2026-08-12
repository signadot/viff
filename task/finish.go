package task

import "context"

// A Finisher is the RUNTIME's own closing act — something the runtime does with the work once the
// driver is done, composed onto the END of the loop rather than run after it.
//
// The case it exists for is a coding runner's push. The task edits a checkout, the runner delivers
// it, and doing that after the loop returns costs three things at once, none of which is about
// git:
//
//   - it is UNGATED. The vector never sees it, so `GateMatch.OnPush` and `RepoContext.PushTarget`
//     — fields that exist precisely to let a charter say "ask before anything touches a release
//     branch" — are decoration. The one action with an effect outside the verse was the one action
//     nobody could gate.
//   - it is UNRECORDED. The task's read plane never names the branch it produced, so `task get`
//     cannot say where the work went, and a failure to deliver is a line in a runner's log.
//   - it is NOT A CAUSE. A flow that wants to run when work lands has to chain off a ref appearing
//     in a source reflection, rather than off a boundary in the task that made it.
//
// Inside the loop all three close at once, and none of them needed a new mechanism: the vector
// gates it because it is an action, the read plane narrates it because it is an action, and it
// happens before terminal because it is an action. That is the argument for this seam over an
// `OnComplete` hook — a hook is a fourth thing to supervise, and an action is already supervised.
//
// What it is NOT is a way to make the driver do something. The driver has finished; this is the
// runtime's act, proposed on its own behalf, and the agent is never told about it.
type Finisher interface {
	// Finish returns the closing action, or false for none — an environment with nowhere to
	// deliver has nothing to propose, and proposing a no-op action would put a gate in front of
	// nothing. result is what the driver produced, so a finisher can decide by it.
	Finish(result string) (ToolUse, bool)
	// Do performs the action once it is approved, returning what to record as its output. An
	// error FAILS the task: a run whose work did not leave is not a run that succeeded, and the
	// whole point of moving this inside the loop is that the failure stops being a log line.
	Do(ctx context.Context, tu ToolUse) (string, error)
}

// WithFinisher composes f onto a driver and executor, returning the pair to hand to NewTask.
//
// The returned Driver implements exactly the optional capabilities the original did — Steerable,
// Resulter, or neither. That is fiddlier than embedding and it is the point: the loop TYPE-ASSERTS
// for both, so a wrapper that always claimed them would silently swallow steers for a driver that
// cannot take them, and one that never claimed them would break steering and make every task's
// result empty.
func WithFinisher(d Driver, x Executor, f Finisher) (Driver, Executor) {
	st := &finishState{fin: f, inner: d}
	fd := &finishDriver{inner: d, st: st}
	return withCapabilities(fd, d), &finishExec{inner: x, st: st}
}

// finishState is what the wrapped driver and the wrapped executor both need to know: whether the
// closing action has been proposed and under what name, so the executor can tell it apart from a
// tool of the driver's own. One task's loop is one goroutine, so this needs no lock.
type finishState struct {
	fin     Finisher
	inner   Driver
	name    string
	pending bool
}

type finishDriver struct {
	inner  Driver
	st     *finishState
	done   bool // the inner driver has finished
	posted bool
}

func (d *finishDriver) Next(ctx context.Context, prev StepResult) (ToolUse, bool, error) {
	if d.posted {
		// The closing action was the last thing this task does, so the run ends here whatever
		// came back — but HOW it ends is the difference between a lost push and a reported one.
		//
		// The loop hands an executor's error back as prev.Err and lets the driver decide, which
		// is right for a driver that can re-plan and wrong for the end of the task: a run that
		// swallowed it would report `done` for work that never left. So the error is returned,
		// which fails the task.
		//
		// A refusal is not that. `Blocked` means a supervisor said no, and being told not to
		// deliver is a decision the task complied with rather than a failure of it. The read
		// plane already carries the denial, and the workspace is what preserves the work.
		if prev.Err != nil {
			return ToolUse{}, false, prev.Err
		}
		return ToolUse{}, false, nil
	}
	if !d.done {
		tu, more, err := d.inner.Next(ctx, prev)
		if err != nil || more {
			return tu, more, err
		}
		d.done = true
	}
	var result string
	if r, ok := d.inner.(Resulter); ok {
		result = r.Result()
	}
	tu, ok := d.st.fin.Finish(result)
	if !ok {
		return ToolUse{}, false, nil
	}
	d.posted = true
	d.st.name, d.st.pending = tu.Name, true
	return tu, true, nil
}

type finishExec struct {
	inner Executor
	st    *finishState
}

func (x *finishExec) Execute(ctx context.Context, tu ToolUse) (StepResult, []ToolUse, error) {
	if !x.st.pending || tu.Name != x.st.name {
		return x.inner.Execute(ctx, tu)
	}
	x.st.pending = false
	out, err := x.st.fin.Do(ctx, tu)
	if err != nil {
		return StepResult{Use: tu, Output: out}, nil, err
	}
	return StepResult{Use: tu, Output: out}, nil, nil
}

// withCapabilities re-exposes the optional Driver interfaces the inner driver had. The embedded
// interface values are the INNER driver's, so a steer reaches the conversation it was meant for
// and Result stays the driver's final message rather than the finisher's output.
func withCapabilities(fd *finishDriver, inner Driver) Driver {
	steer, canSteer := inner.(Steerable)
	res, hasResult := inner.(Resulter)
	switch {
	case canSteer && hasResult:
		return struct {
			*finishDriver
			Steerable
			Resulter
		}{fd, steer, res}
	case canSteer:
		return struct {
			*finishDriver
			Steerable
		}{fd, steer}
	case hasResult:
		return struct {
			*finishDriver
			Resulter
		}{fd, res}
	}
	return fd
}

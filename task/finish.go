package task

import "context"

// A Finisher is the RUNTIME's own closing act — something the runtime does with the work once
// the driver is done, composed onto the END of the loop rather than run after it.
//
// The case it exists for is a coding runner's push. The task edits a checkout, the runner
// delivers it, and doing that after the loop returns costs three things at once, none of which
// is about git:
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
//
// HOW IT IS CONSULTED, and why that changed. This used to be a DRIVER WRAPPER: `WithFinisher`
// returned a driver that impersonated the real one, watched for `!more`, and injected the
// closing action as though the agent had asked for it. The goal was right and survives; the
// mechanism inverted the direction of knowledge. A wrapper sees one boolean, the loop holds the
// whole pending list, and `!more` with a child outstanding means "I am waiting" — so the
// wrapper delivered a half-written tree, and then needed a `posted` latch to avoid delivering
// twice, and that latch made a SECOND delivery impossible even when an operator reopened the
// run with something new to do.
//
// So the loop consults it directly, at the point where the input would go quiet, and runs what
// it proposes through the same authority → gate → execute path. Every property the wrapper
// existed to get, and no latch: being asked once per quiescence is a consequence of the state
// rather than a flag. See SetFinisher and machine.go's quiesce.
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

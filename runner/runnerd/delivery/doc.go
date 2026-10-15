// Package delivery is how a task's work leaves the sandbox: a push is a CAPABILITY the runner
// holds and the task may ask it to exercise, and the runner exercises it. The task process holds
// the Client end, with the push, pull request and fetch tools; the runner holds the Server end,
// which checks the branch against what the spawn permits, stages the committed work in its mirror,
// and pushes it from there with its own credential. Channel is the runner's side of one task, for
// any runner that prepares a task's process; Attach is the task's.
//
// The push is an ACTION of the task's loop — the agent's own tool, or a call in a charter's list
// — so the vector sees it, the read plane names the branch, and what comes after can chain off
// it. It may be called as many times as the work needs: there is no closing delivery, and what
// the task did not push does not leave.
//
// So: the spawn says which branches each repository permits, the task names one, deriving stamps
// the ref it resolves to, the vector gates THAT, and approval is what makes the runner act. The
// credential never leaves the runner, so the task's shell has neither the secret nor, under a
// restricted egress, any route to the forge.
//
//	child   the `push` tool proposes an action naming a repository and a branch
//	        the supervisor gates it — this is where a charter's `verbs: [push]` lands
//	        approved: the executor asks the runner to push
//	parent  checks the branch against the allowance and the commits for their trailers, stages
//	        the work in its mirror, pushes from there, and answers with what happened
//	child   what happened is the action's output: on the read plane, before terminal
//
// The channel is NOT the task-control contract and does not pretend to be. The task can ask, and
// asking cannot widen anything: what may be pushed is the spawn's to say, and the trailers are
// the runner's to check.
//
// Unlike the egress socket it is an INHERITED CONNECTION rather than a path, because unlike the
// egress socket nothing needs to reach it except this process. A path in the sandbox would be one
// the agent's shell could also write to, and a task that can ask for a push directly is a task
// that pushes without passing the gate.
package delivery

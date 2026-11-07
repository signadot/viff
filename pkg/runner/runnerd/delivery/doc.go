// Package delivery is how a task's work leaves the sandbox: a push is a CAPABILITY the runner
// holds and the task may ask it to exercise, and the runner exercises it. The task process holds
// the Client end; the runner holds the Server end, which checks the branch against what the
// launch permits (runner.Repo.Permit), stages the committed work in its mirror, and pushes it
// from there with its own credential. Channel is the runner's side of one task; Attach is the
// task's.
//
// A push is a STEP of the task (git.push, and github.create-pr for a proposal), asked about in the
// task's loop like any other: the agent's own tool, or a script's call through the task process.
// It may be asked for as many times as the work needs: there is no closing delivery, and what
// the task did not push does not leave.
//
//	task    a tool or a script asks to push a repository to a branch
//	        the task's autonomy asks a person, when it says to
//	        approved: the task process asks the runner over the channel
//	runner  checks the branch against the launch and the commits for their trailers, stages the
//	        work in its mirror, pushes from there, and answers with what happened
//	task    what happened is the step's output
//
// A push is one op of the channel, and the rest are the acts of the other systems the runner
// performs with a credential of its own: a pull request, an issue at a tracker, a message in
// Slack, a request to verse. EVERY OP IS IN ONE TABLE (ops), and none performs anything itself:
// it reads the request into the steps it asks about and the act that follows them, and the
// server checks every step against the task's environment, its autonomy and the runner's ceiling
// before it calls the act (Server.perform). A request is an op, what it is asked with by name,
// and a body, so a system the channel comes to carry adds its ops to the table and nothing to
// the protocol. Requests are performed in turn, in the order the task asked, except an op the
// table performs apart. An answer is one frame, but for an act that streams (a command's
// output, `secret.exec`): a frame ahead of it carries the task's ends as descriptors on the
// socket pair the channel is (Reply.Streams), and the final frame the answer.
//
// The channel is NOT the Runner API and does not pretend to be. The task can ask, and asking
// cannot widen anything: what may be pushed is the launch's to say, and the trailers are the
// runner's to check. The credential never leaves the runner, so the task's shell has neither the
// secret nor, under a network that does not allow the forge, any route to it.
//
// It is an INHERITED CONNECTION rather than a path, because nothing needs to reach it except the
// task process. A path in the sandbox would be one the agent's shell could also write to, and a
// task that can ask for a push directly is a task that pushes without passing its gates.
package delivery

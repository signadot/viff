package runner

import "github.com/signadot/verse/task"

// The bodies of the control routes: `POST /tasks/{id}/say`, `/cancel` and `/resolve`, and
// `/pause` and `/resume`, and `PUT /tasks/{id}/gates`.
//
// ONE DECLARATION, for both ends: the client that drives a runner (runner/client) and the
// runtime a runner is built on (runner/runnerd). A body written by hand at each end loses a
// field without anybody being told.

// SayRequest is the body of `POST /tasks/{id}/say`: an instruction to a running task, and who
// gave it. An empty By is an unattributed caller, said as such on the task's read plane.
//
//tony:schemagen=verse-runner-say,notag
type SayRequest struct {
	Instruction string `tony:"field=instruction"`
	By          string `tony:"field=by,omitzero"`
	// Ask is the ask these words are (9ce3bva0, stage 6), echoed on the say event the task emits
	// when it takes them, so the ask settles as heard. Empty for words nobody tracks.
	Ask string `tony:"field=ask,omitzero"`
}

// CancelRequest is the body of `POST /tasks/{id}/cancel`: why the task is being ended. The
// reason is what the task's charters see on `onCancelled`, so it crosses. A cancel with no
// body, or no reason, is a cancel all the same, and the runner says its own.
//
//tony:schemagen=verse-runner-cancel,notag
type CancelRequest struct {
	Reason string `tony:"field=reason,omitzero"`
	// By is who cancelled it; empty is an unattributed caller.
	By string `tony:"field=by,omitzero"`
}

// PauseRequest is the body of `POST /tasks/{id}/pause`: who is pausing the task. The task holds
// at its next boundary, and its `paused` event says who. A pause with no body is a pause all
// the same, unattributed.
//
//tony:schemagen=verse-runner-pause,notag
type PauseRequest struct {
	By string `tony:"field=by,omitzero"`
}

// ResumeRequest is the body of `POST /tasks/{id}/resume`: who is resuming the task.
//
//tony:schemagen=verse-runner-resume,notag
type ResumeRequest struct {
	By string `tony:"field=by,omitzero"`
}

// ResolveRequest is the body of `POST /tasks/{id}/resolve`: the answer to one of the task's open
// gates. Answered `400` without a choice, and `404` when the task holds no open gate by that id:
// an unknown id, or one already answered.
//
//tony:schemagen=verse-runner-resolve,notag
type ResolveRequest struct {
	// ID is the gate answered, as the task's pending event and snapshot name it.
	ID string `tony:"field=id"`
	// Choice is the option chosen, by id: `proceed` or `deny` on a gate that names no options of
	// its own, or one of the options it named. Required: a resolve without one is refused.
	Choice string `tony:"field=choice"`
	// By is who answered; empty is an unattributed caller.
	By string `tony:"field=by,omitzero"`
	// Say is words said to the task with the answer — "yes, but do it this way".
	Say []string `tony:"field=say,omitzero"`
}

// PolicyRequest is the body of `PUT /tasks/{id}/policy`: the running task's new policy, in the
// launch's shape, replacing the one it had, and who replaced it. It applies from the task's
// next step, its sub-agents' included; a question already asked stands as asked. The task's
// read plane says who changed it (a `policy` event). Refused with 400 as a launch's policy
// would be, and with 410 once the task has ended.
//
//tony:schemagen=verse-runner-policy,notag
type PolicyRequest struct {
	Policy task.Policy `tony:"field=policy,omitzero"`
	By     string      `tony:"field=by,omitzero"`
}

package task

import "errors"

// Every control call reports what became of it. Three outcomes, and the third is the one that
// used to have nowhere to go: applied (nil), REFUSED by the far side, or never reached it.
//
// The distinction is not pedantry. Only one of the three is worth retrying, and a caller that
// cannot tell them apart either retries what was already applied or abandons what never
// arrived. A cancel that never left the building read as a cancel.
//
// Each is the status a runner answers with, as the client reads it (runner/client).
var (
	// ErrUnreachable: the far side did not answer (no status). Nothing is known about whether
	// it acted.
	ErrUnreachable = errors.New("runner unreachable")
	// ErrNoSuchTask (`404`) / ErrNoSuchDecision (`404` on resolve): the far side answered, and
	// has never heard of it.
	ErrNoSuchTask     = errors.New("no such task")
	ErrNoSuchDecision = errors.New("no such decision")
	// ErrAlreadyAnswered (`409`): a decision that was settled before this answer arrived. Distinct
	// from ErrNoSuchDecision because the two mean opposite things to whoever asked — one is
	// "look for your typo", the other is "somebody beat you to it".
	ErrAlreadyAnswered = errors.New("already answered")
	// ErrEnded (`410`): the task is over, so nothing said to it will be heard.
	ErrEnded = errors.New("the task has ended")
	// ErrUnsupported (`501`): this runner has no such route at all.
	ErrUnsupported = errors.New("unsupported by this runner")
)

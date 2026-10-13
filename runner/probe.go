package runner

import (
	"errors"

	"github.com/signadot/verse/task"
)

// Reach is what a url said when asked, and it is the whole of what verse knows about a
// runner's health. There is no fourth state and no stored one: a probe is answered live,
// reported, and discarded, so nothing verse records can be stale in the direction that costs —
// telling an operator a runner is well because it was well when something last looked.
type Reach int

const (
	// IsRunner: it answered, and the body decoded as the task list the contract promises.
	IsRunner Reach = iota
	// NotRunner: it answered, and is not that: a url pointed at some unrelated service, or at
	// a face of a runner that is not its task list.
	NotRunner
	// Unreachable: nothing answered, or what answered said nothing about what it is. Both are
	// facts that can change on their own, which is what separates this from NotRunner.
	Unreachable
)

func (r Reach) String() string {
	switch r {
	case IsRunner:
		return "a runner"
	case NotRunner:
		return "not a runner"
	default:
		return "unreachable"
	}
}

// Probe asks a url what it is, with the cheapest question that has an answer: GET /tasks,
// which the verse-facing contract already serves and which has no effect on the runner.
//
// It needs nothing new because ListTasks now reports how it failed. Its three outcomes are
// already the three this wants — nil, ErrUnsupported, ErrUnreachable — for the reason a probe
// is a control call carrying no work.
//
// A runner answering 500 lands in Unreachable rather than NotRunner, and deliberately. The
// division that matters to the caller is not answered-vs-silent, it is whether waiting could
// change the answer: something at that url is speaking HTTP and failing, which is a runner
// having a bad day at least as often as it is the wrong url, and refusing the install on it
// would be refusing a fact that fixes itself.
func Probe(baseURL string) (Reach, error) {
	l, ok := HTTP(baseURL).(task.Lister)
	if !ok {
		return Unreachable, errors.New("runner: the HTTP client cannot list tasks")
	}
	_, err := l.ListTasks()
	switch {
	case err == nil:
		return IsRunner, nil
	case errors.Is(err, task.ErrUnsupported):
		return NotRunner, err
	default:
		return Unreachable, err
	}
}

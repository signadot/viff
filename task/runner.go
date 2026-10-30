package task

// Runner runs and governs tasks on some substrate; verse (the engine) is its
// client. The only implementation today is the in-process local supervisor (a runnerd
// wrapper); remote runners addressed by a base URL — k8s, lambda, another verse — come
// later, all behind this same seam. See docs/task-control.md.
//
// Keeping even the local case behind the interface is deliberate: there is no privileged
// in-memory path, so the contract stays complete and local ≡ remote.
//
// It starts nothing: starting a task is the runner client's (runner/client's Launcher, which
// sends a runner.Launch).
type Runner interface {
	// Attach re-attaches to an already-running task by id — the resume path on
	// restart, when the work outlived verse (a remote runner). Its handle's read plane
	// replays from the start so the mirror re-derives current state. A local (in-process)
	// runner has nothing to re-attach to after a restart and returns nil (the caller
	// orphans).
	Attach(id string) Handle

	// Ref identifies this runner on the task:instance entity, so a restart can
	// reconstruct it and decide resume-vs-orphan: "local" for the in-process supervisor,
	// else the base URL.
	Ref() string
}

// Lister is a Runner that keeps its OWN task registry and can be asked for it: a runner behind a
// URL.
//
// It is declared HERE, beside Runner, rather than inline at the call site that asserts it.
// The assertion is structural and checked with `, ok`, so widening a signature on one side
// alone does not fail to compile — it makes the assertion silently report false and the
// feature stop working. That has already happened once in this codebase, to gate resolution,
// when `by` was added to one side. One declaration, and each implementation asserts against
// it, so a signature change is a compile error where the implementation is.
//
// Both methods report what went wrong rather than folding a failure into an absence.
//
// ListTasks: nil, nil is a runner with no tasks; a runner that did not answer is
// ErrUnreachable and one with no such face is ErrUnsupported. Those are not the same fact, and
// a caller that cannot tell them apart shows an operator a verse with less work in it than it
// has.
//
// TaskSnapshot: ErrNoSuchTask is the far side answering that it has never heard of this
// id, and it is the only one of these that means "look for your typo". It returns an error
// rather than an ok for the same reason — an ok cannot distinguish that from a runner that
// never answered, so a task that exists on an unreachable runner reads as one that does not
// exist at all.
type Lister interface {
	ListTasks() ([]Snapshot, error)
	TaskSnapshot(id string) (Snapshot, error)
}

// Handle is verse's governing handle on one running task — the control plane
// (pause/resume/cancel/say/set-autonomy) plus the read plane (Subscribe). It hides whether
// the task runs in-process or behind a URL, so the engine tracks handles, not the
// concrete *Task. The local handle is *Task itself; a remote handle wraps an HTTP
// client of a runner base URL.
type Handle interface {
	Subscribe() (<-chan Event, func())
	// SubscribeSince resumes: the frames after that cursor, then live, with an EventGap first
	// if the cursor is below what is still retained. It is on the interface rather than
	// type-asserted for because a reader that cannot resume cannot recover — a stream that
	// dies mid-task would leave it with no way back to current, and no way to tell that
	// from the task having ended.
	SubscribeSince(since int64) (<-chan Event, func())
	// by, on these three, is who did it.
	Pause(by string) error
	Resume(by string) error
	Cancel(reason, by string) error
	// Steer says words to the task, taken at its next boundary; ask is the ask they are, echoed
	// on the say frame when they are taken, and empty for words nobody tracks.
	Steer(instruction, by, ask string) error
	// SetAutonomy replaces the task's autonomy from its next step; by is who did.
	SetAutonomy(p Autonomy, by string) error
	Result() string
	// Err is why a terminal task failed or was cancelled — independent of Result, not
	// exclusive with it: a driver can say something and then fail. It is on the Handle
	// rather than inferred from status because status says a task STOPPED and nothing
	// about why, and the mirror can only project what the handle exposes.
	Err() string
	// Refused is the connections the task's proxy refused, by destination, and how often
	// (Snapshot.Refused); nil where the runner serves it no proxy.
	Refused() map[string]int
	// Agent is who ran it and with what (Snapshot.Agent, Snapshot.Settings): "" where the runner
	// did not say, as for a script.
	Agent() (name string, settings map[string]string)
}

// *Task is the local Handle.
var _ Handle = (*Task)(nil)

// FailedHandle is the Handle for work that never started: a launch that could not be issued
// at all. Its read plane reports one terminal frame and closes, so a mirror records a failed
// task rather than stranding the entity as running, and Err carries the reason to
// `task get`.
//
// It is here rather than in each caller because more than one caller has the need — a launch
// whose POST did not go through, and verse's own spawns that never reached a runner — and a
// Handle that lies about a task's status is exactly the thing not to reimplement per package.
func FailedHandle(reason string) Handle { return failedHandle{reason} }

type failedHandle struct{ err string }

func (f failedHandle) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 1)
	ch <- Event{Kind: EventStatus, Text: string(Failed), Err: f.err}
	close(ch)
	return ch, func() {}
}

// Resuming work that never started is the same one-frame record from any cursor: there is
// nothing before the failure and there will be nothing after it.
func (f failedHandle) SubscribeSince(int64) (<-chan Event, func()) { return f.Subscribe() }

// Control on work that never started is refused rather than silently accepted. There is no
// task to pause, and saying so is the difference between "your cancel landed" and "there
// was never anything there".
func (failedHandle) Pause(string) error                 { return ErrNoSuchTask }
func (failedHandle) Resume(string) error                { return ErrNoSuchTask }
func (failedHandle) Cancel(string, string) error        { return ErrNoSuchTask }
func (failedHandle) Steer(string, string, string) error { return ErrNoSuchTask }
func (failedHandle) SetAutonomy(Autonomy, string) error { return ErrNoSuchTask }
func (failedHandle) Result() string                     { return "" }
func (f failedHandle) Err() string                      { return f.err }
func (failedHandle) Refused() map[string]int            { return nil }
func (failedHandle) Agent() (string, map[string]string) { return "", nil }

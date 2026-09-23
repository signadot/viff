package task

import (
	"errors"

	"github.com/signadot/tony-format/go-tony/ir"
)

// Launch is everything one spawn says about the task it asks for: which driver builds
// it, the task it is seeded with, and the FRAME that task is read in.
//
// It is a struct rather than the positional (id, task, driver) it replaces because what a
// spawn may say about its agent is the axis that grows, and every growth was widening the
// same four signatures. Every field but ID is optional; empty means "whatever the runtime
// defaults to", which is what an ad-hoc `task run` sends.
//
// It is the runner-facing half of launch.Spec: the fields a runtime needs to BUILD the
// task, without the ones (vector, grant, environment, provider) the engine resolves
// against its own registries before it ever gets here.
type Launch struct {
	ID     string
	Prompt string
	Driver string

	// Frame is the standing instructions the Prompt is read in — the agent's system
	// prompt, as against its one job.
	//
	// It is per-spawn because it is the CHARTER's to write, and a package default cannot
	// know what this spawn is for. Two things it gets wrong by construction: whether the
	// agent's last message is prose for a person or the value a `patch: {x: $result}`
	// will lift out of it, and which of the offered tools are worth reaching for under
	// the gate vector this trigger runs at — a default that recommends delegating is
	// wrong advice for an unattended firing whose delegate gate is ASK.
	//
	// Empty = the driver's own default frame.
	Frame string

	// Resolver is the binding that answers this task's gates — `verse.resolvers.<name>`.
	// Empty means nobody is bound, and its questions go to whoever reads the docket.
	Resolver string

	// Retain is how many read-plane events to keep for a resuming reader; 0 = the runtime's
	// default. It is per-spawn because the right number is a property of the work: see
	// launch.Spec.Retain.
	Retain int

	// Tools names the tools offered to the agent, narrowing the driver's full set. Empty
	// = all of them. It is the hard counterpart of Frame's advice: a frame can only ask
	// an agent not to reach for something, whereas a tool that is not offered cannot be
	// called at all. A driver always keeps whatever tools its own contract depends on
	// (the LLM harness keeps `fail`, which is how a task reports it could not do the
	// work). A name no driver knows fails the spawn rather than shrinking it in silence.
	Tools []string

	// VirtualKey is the model credential this task runs under — THE KEY ITSELF, not an address
	// and not a name. Empty means the runtime's own.
	//
	// It is the one field here that carries a secret, and it does so because the permission
	// question is answered before this exists: versed read it from the address the spawn named,
	// AS WHOEVER NAMED IT, and refused the spawn if that read was not theirs to make. What a
	// runner receives is the answer, so it has nothing to check and no list to hold.
	//
	// A Portkey virtual key is what this is for. It is bounded by its own budget and route, so
	// it can cross a wire verse already trusts with a task's prompt, its grant and its
	// provenance. A provider key is not bounded that way and does not belong on it.
	VirtualKey string

	// Answer is the ANSWER CONTRACT: the shape this task's final message must have,
	// as a tony match pattern (see package answer). nil = no contract.
	//
	// It rides the wire with Frame and Tools rather than being enforced by the engine after
	// the fact, because the check has to happen INSIDE the loop — the run is not finished
	// until the answer is good, and a driver that has already stopped cannot be asked
	// again. A runtime whose driver cannot enforce a contract must fail the spawn rather
	// than run it unchecked: a contract silently dropped is worse than none, since the
	// charter downstream is written believing it holds.
	Answer *ir.Node

	// Calls is the work itself, for a spawn that knows what it wants done: a list of actions
	// to take in order, each saying which of its outcomes end the list. Empty for every other
	// driver.
	//
	// It is the one field here that is not a reference to something the far end resolves, and
	// it is where a `driver:` becomes almost redundant — a spawn carrying calls is a call
	// list, because nothing else could run one. The engine still writes the driver name in,
	// so what ran is readable rather than inferred.
	Calls []Call

	// Vector is the gate vector to launch under; nil = the runtime's own default. Grant is
	// the hard envelope to install as the task's scope; nil = unbounded, which is what
	// every task got before a spawn could say otherwise.
	//
	// These two are RESOLVED VALUES where Frame and Prompt are text, and the asymmetry is
	// deliberate. A charter writes `vector: auto` / `grant: read-only` — names — and the
	// engine resolves them (NamedVector/NamedGrant) before the spawn crosses this seam. It
	// could as easily have sent the names and let the far end resolve them, and that would
	// be worse: the two ends can be different builds, and a runtime whose vocabulary had
	// drifted would run the task under a different envelope than the charter named,
	// silently. Sending the predicates makes the meaning travel with the request and keeps
	// the vocabulary in exactly one place.
	//
	// Grant is a pointer for the same reason NamedGrant returns an ok: the zero Grant
	// permits NOTHING, so "no envelope declared" and "the empty envelope" must not be the
	// same value. Conflating them refuses every action of every task that never
	// mentioned a grant.
	Vector *Vector
	Grant  *Grant

	// Environment names the workspace this task should run in — a repo at a base, a
	// toolchain, a containment — resolved by the RUNNER against its own config, not by
	// verse. It is the launch.Spec axis that has been parsed, `$var`-expanded and read by
	// nothing (see versed.launchOf); this is the first consumer.
	//
	// It travels as a NAME, unresolved, the way Driver does rather than the way Vector and
	// Grant do. The asymmetry is deliberate: verse holds the vocabulary for an envelope
	// because it can enforce one, and holds none for a workspace because only the runner can
	// build one. A runner handed a name it does not have refuses the launch, which is how a
	// runner declines anything.
	//
	// Empty = the runner's own default workspace.
	Environment string

	// Scope is the federation this task was spawned into — a `verse connect` scope, or
	// empty for baseline. The RUNTIME does nothing with it: a task runs the same either
	// way, and the runner never sees it.
	//
	// It rides here because a task's records have to land where the task lives, and the one
	// piece of machinery that publishes on a task's behalf is scope-blind. taskd is a single
	// global runtime shared by baseline and every scoped engine, so the gate questions it
	// posts went to whichever store the ONE OnQuestion callback closed over — baseline —
	// while the task's own `task:instance` record went to the spawning scope. One run,
	// two records, two different stores: `verse gates` showed a pending gate whose task
	// `verse ps` would not list, one dev's decision appeared on every other dev's docket,
	// and the question named `local:dir-status-<scope>:*` entities the reader could not
	// read (git-issue kwxxm8cth12ksn99fhn0).
	//
	// Carried rather than looked up, because the alternatives are worse where it matters.
	// A side map from task id to store is state that a restart loses and that leaks an
	// entry per task; resolving the scope by hunting for the `task:instance` record across
	// scopes races the very first gate, since a task can ask something before its own
	// record is written. The launch is the one thing that exists before the task does.
	Scope string

	// Occurrence is the store revision this RUN is about — the commit that woke the firing,
	// or the revision a hand-spawned run's record was created at. Zero when nothing knows.
	//
	// The runtime does nothing with it, and holds it for the reason it holds Scope: an id
	// names the WORK, so one id runs many times, and something has to be able to say WHICH
	// run a process is. Without it that answer lived only on the entity — asking whether a
	// running process is the one a record describes meant reading
	// `task:instance:<id>.occurrence` and comparing by hand, and `verse ps`, which reads the
	// runtime rather than the store, could not answer it at all.
	Occurrence int64

	// Provenance is what the runner needs to make its own effects auditable: who is
	// answerable for this launch, and what it is about.
	//
	// It crosses the wire because a runner's effects can leave the verse — a coding runner
	// pushes a branch under a credential verse does not hold — and the only externally
	// checkable link back is what the runner itself writes down (a commit trailer, a branch
	// name). A runner that was never told cannot record it.
	Provenance Provenance
}

// Provenance is the answer to "on whose account, and about what" for one launch. Every
// field is optional and an empty one means verse did not know, which is a real state and
// not a default.
//
// It is deliberately NOT authority. A runner must not read Principal and confer anything on
// the strength of it — that is the on-behalf-of model docs/roadmap/authority.md rejects by
// name, where an agent inherits what a user may do rather than what a user asked for. What
// may be done is Grant, and nothing here widens it.
type Provenance struct {
	// Principal is who is answerable for this launch — the argument half of the
	// parameter-at-install / argument-at-launch split.
	//
	// Self-asserted, and known to be: verifying that a caller is who it says is
	// authority's whole subject, and nothing local could do better than a lock with the key
	// taped to it. It is carried anyway because a chain of one asserted link and several
	// derived ones is checkable against the store, where a bare unattributed effect is not.
	//
	// Empty on a charter firing today: what is answerable there is whoever installed the
	// charter, which is the PARAMETER half and wants an author on an entity write —
	// `entity.Write` carries none. Subject and Chain below are what the firing does know.
	Principal string
	// Subject is the flow this launch belongs to (an entity ref, e.g. a PR), and Chain is
	// the kpath of the action inside its trigger ("review", "review.onDone"). Together they
	// are the precipitating cause of a charter firing — which verse has always recorded on
	// its own side and never told the runner, so a remote runner could not say why it was
	// asked for anything.
	Subject string
	Chain   string
}

// Runner runs and governs tasks on some substrate; verse (the engine) is its
// client. The only implementation today is the in-process local supervisor (a taskd
// wrapper); remote runners addressed by a base URL — k8s, lambda, a human broker — come
// later, all behind this same seam. See docs/task-control.md.
//
// Keeping even the local case behind the interface is deliberate: there is no privileged
// in-memory path, so the contract stays complete and local ≡ remote.
type Runner interface {
	// Spawn starts a task and returns a handle to govern it. The id is assigned by
	// the caller (the engine's spawnID); content routes a federated read_file.
	Spawn(l Launch, content ContentReader) Handle

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

// Lister is a Runner that keeps its OWN task registry — a broker, or a runner behind a
// URL — distinct from the local runtime. The in-process runner does NOT implement it: it
// delegates to the same taskd the union already lists, so brokered tasks are added
// without double-counting local ones.
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
	ListTasks() ([]Instance, error)
	TaskSnapshot(id string) (Instance, error)
}

// Handle is verse's governing handle on one running task — the control plane
// (pause/resume/cancel/say/set-vector) plus the read plane (Subscribe). It hides whether
// the task runs in-process or behind a URL, so the engine tracks handles, not the
// concrete *Task. The local handle is *Task itself; a remote handle wraps an HTTP
// client of a runner base URL.
// Every control call reports what became of it. Three outcomes, and the third is the one that
// used to have nowhere to go: applied (nil), REFUSED by the far side, or never reached it.
//
// The distinction is not pedantry. Only one of the three is worth retrying, and a caller that
// cannot tell them apart either retries what was already applied or abandons what never
// arrived. A cancel that never left the building read as a cancel.
var (
	// ErrUnreachable: the far side did not answer. Nothing is known about whether it acted.
	ErrUnreachable = errors.New("runner unreachable")
	// ErrNoSuchTask / ErrNoSuchDecision: the far side answered, and has never heard of it.
	ErrNoSuchTask     = errors.New("no such task")
	ErrNoSuchDecision = errors.New("no such decision")
	// ErrAlreadyAnswered: a decision that was settled before this answer arrived. Distinct
	// from ErrNoSuchDecision because the two mean opposite things to whoever asked — one is
	// "look for your typo", the other is "somebody beat you to it".
	ErrAlreadyAnswered = errors.New("already answered")
	// ErrTaskEnded: the task is over, so its open gates will never be acted on. Not
	// ErrAlreadyAnswered — nobody answered — and not ErrNoSuchDecision, because the gate was
	// real and the answerer is not looking at a typo.
	ErrTaskEnded = errors.New("the task ended with this gate unanswered")
	// ErrFenced: a stale claim. The caller's generation was displaced.
	ErrFenced = errors.New("fenced: stale claim")
	// ErrUnsupported: this runner has no such face at all.
	ErrUnsupported = errors.New("unsupported by this runner")
)

type Handle interface {
	Subscribe() (<-chan Frame, func())
	// SubscribeSince resumes: the frames after that cursor, then live, with an EvGap first
	// if the cursor is below what is still retained. It is on the interface rather than
	// type-asserted for because a reader that cannot resume cannot recover — a stream that
	// dies mid-task would leave it with no way back to current, and no way to tell that
	// from the task having ended.
	SubscribeSince(since int64) (<-chan Frame, func())
	Pause() error
	Resume() error
	Cancel(reason string) error
	Steer(instruction, by string) error
	SetVector(v *Vector, by string) error
	Result() string
	// Err is why a terminal task failed or was cancelled — independent of Result, not
	// exclusive with it: a driver can say something and then fail. It is on the Handle
	// rather than inferred from status because status says a task STOPPED and nothing
	// about why, and the mirror can only project what the handle exposes.
	Err() string
}

// *Task is the local Handle.
var _ Handle = (*Task)(nil)

// FailedHandle is the Handle for work that never started: a spawn that could not be issued
// at all. Its read plane reports one terminal frame and closes, so a mirror records a failed
// task rather than stranding the entity as running, and Err carries the reason to
// `task get`.
//
// It is here rather than in each caller because there are now two callers with the same
// need — a remote spawn whose POST did not go through (runner), and a spawn whose declared
// envelope did not resolve (versed) — and a Handle that lies about a task's status is
// exactly the thing not to reimplement per package.
func FailedHandle(reason string) Handle { return failedHandle{reason} }

type failedHandle struct{ err string }

func (f failedHandle) Subscribe() (<-chan Frame, func()) {
	ch := make(chan Frame, 1)
	ch <- Frame{Kind: EvStatus, Text: string(Failed), Err: f.err}
	close(ch)
	return ch, func() {}
}

// Resuming work that never started is the same one-frame record from any cursor: there is
// nothing before the failure and there will be nothing after it.
func (f failedHandle) SubscribeSince(int64) (<-chan Frame, func()) { return f.Subscribe() }

// Control on work that never started is refused rather than silently accepted. There is no
// task to pause, and saying so is the difference between "your cancel landed" and "there
// was never anything there".
func (failedHandle) Pause() error                    { return ErrNoSuchTask }
func (failedHandle) Resume() error                   { return ErrNoSuchTask }
func (failedHandle) Cancel(string) error             { return ErrNoSuchTask }
func (failedHandle) Steer(string, string) error      { return ErrNoSuchTask }
func (failedHandle) SetVector(*Vector, string) error { return ErrNoSuchTask }
func (failedHandle) Result() string                  { return "" }
func (f failedHandle) Err() string                   { return f.err }

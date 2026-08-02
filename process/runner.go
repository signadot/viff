package process

import (
	"errors"

	"github.com/signadot/tony-format/go-tony/ir"
)

// Launch is everything one spawn says about the process it asks for: which driver builds
// it, the task it is seeded with, and the FRAME that task is read in.
//
// It is a struct rather than the positional (id, task, driver) it replaces because what a
// spawn may say about its agent is the axis that grows, and every growth was widening the
// same four signatures. Every field but ID is optional; empty means "whatever the runtime
// defaults to", which is what an ad-hoc `process run` sends.
//
// It is the runner-facing half of launch.Spec: the fields a runtime needs to BUILD the
// process, without the ones (vector, grant, environment, provider) the engine resolves
// against its own registries before it ever gets here.
type Launch struct {
	ID     string
	Task   string
	Driver string

	// Frame is the standing instructions the Task is read in — the agent's system
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

	// Tools names the tools offered to the agent, narrowing the driver's full set. Empty
	// = all of them. It is the hard counterpart of Frame's advice: a frame can only ask
	// an agent not to reach for something, whereas a tool that is not offered cannot be
	// called at all. A driver always keeps whatever tools its own contract depends on
	// (the LLM harness keeps `fail`, which is how a process reports it could not do the
	// work). A name no driver knows fails the spawn rather than shrinking it in silence.
	Tools []string

	// Answer is the ANSWER CONTRACT: the shape this process's final message must have,
	// as a tony match pattern (see package answer). nil = no contract.
	//
	// It rides the wire with Frame and Tools rather than being enforced by the engine after
	// the fact, because the check has to happen INSIDE the loop — the run is not finished
	// until the answer is good, and a driver that has already stopped cannot be asked
	// again. A runtime whose driver cannot enforce a contract must fail the spawn rather
	// than run it unchecked: a contract silently dropped is worse than none, since the
	// charter downstream is written believing it holds.
	Answer *ir.Node

	// Vector is the gate vector to launch under; nil = the runtime's own default. Grant is
	// the hard envelope to install as the process's scope; nil = unbounded, which is what
	// every process got before a spawn could say otherwise.
	//
	// These two are RESOLVED VALUES where Frame and Task are text, and the asymmetry is
	// deliberate. A charter writes `vector: auto` / `grant: read-only` — names — and the
	// engine resolves them (NamedVector/NamedGrant) before the spawn crosses this seam. It
	// could as easily have sent the names and let the far end resolve them, and that would
	// be worse: the two ends can be different builds, and a runtime whose vocabulary had
	// drifted would run the process under a different envelope than the charter named,
	// silently. Sending the predicates makes the meaning travel with the request and keeps
	// the vocabulary in exactly one place.
	//
	// Grant is a pointer for the same reason NamedGrant returns an ok: the zero Grant
	// permits NOTHING, so "no envelope declared" and "the empty envelope" must not be the
	// same value. Conflating them refuses every action of every process that never
	// mentioned a grant.
	Vector *Vector
	Grant  *Grant
}

// Runner runs and governs processes on some substrate; verse (the engine) is its
// client. The only implementation today is the in-process local supervisor (a processd
// wrapper); remote runners addressed by a base URL — k8s, lambda, a human broker — come
// later, all behind this same seam. See docs/process-control.md.
//
// Keeping even the local case behind the interface is deliberate: there is no privileged
// in-memory path, so the contract stays complete and local ≡ remote.
type Runner interface {
	// Spawn starts a process and returns a handle to govern it. The id is assigned by
	// the caller (the engine's spawnID); content routes a federated read_file.
	Spawn(l Launch, content ContentReader) Handle

	// Attach re-attaches to an already-running process by id — the resume path on
	// restart, when the work outlived verse (a remote runner). Its handle's read plane
	// replays from the start so the mirror re-derives current state. A local (in-process)
	// runner has nothing to re-attach to after a restart and returns nil (the caller
	// orphans).
	Attach(id string) Handle

	// Ref identifies this runner on the process:instance entity, so a restart can
	// reconstruct it and decide resume-vs-orphan: "local" for the in-process supervisor,
	// else the base URL.
	Ref() string
}

// Handle is verse's governing handle on one running process — the control plane
// (pause/resume/cancel/say/set-vector) plus the read plane (Subscribe). It hides whether
// the process runs in-process or behind a URL, so the engine tracks handles, not the
// concrete *Process. The local handle is *Process itself; a remote handle wraps an HTTP
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
	// ErrNoSuchProcess / ErrNoSuchDecision: the far side answered, and has never heard of it.
	ErrNoSuchProcess  = errors.New("no such process")
	ErrNoSuchDecision = errors.New("no such decision")
	// ErrAlreadyAnswered: a decision that was settled before this answer arrived. Distinct
	// from ErrNoSuchDecision because the two mean opposite things to whoever asked — one is
	// "look for your typo", the other is "somebody beat you to it".
	ErrAlreadyAnswered = errors.New("already answered")
	// ErrFenced: a stale claim. The caller's generation was displaced.
	ErrFenced = errors.New("fenced: stale claim")
	// ErrUnsupported: this runner has no such face at all.
	ErrUnsupported = errors.New("unsupported by this runner")
)

type Handle interface {
	Subscribe() (<-chan Frame, func())
	Pause() error
	Resume() error
	Cancel(reason string) error
	Steer(instruction, by string) error
	SetVector(v *Vector, by string) error
	Result() string
	// Err is why a terminal process failed or was cancelled — independent of Result, not
	// exclusive with it: a driver can say something and then fail. It is on the Handle
	// rather than inferred from status because status says a process STOPPED and nothing
	// about why, and the mirror can only project what the handle exposes.
	Err() string
}

// *Process is the local Handle.
var _ Handle = (*Process)(nil)

// FailedHandle is the Handle for work that never started: a spawn that could not be issued
// at all. Its read plane reports one terminal frame and closes, so a mirror records a failed
// process rather than stranding the entity as running, and Err carries the reason to
// `process get`.
//
// It is here rather than in each caller because there are now two callers with the same
// need — a remote spawn whose POST did not go through (runner), and a spawn whose declared
// envelope did not resolve (versed) — and a Handle that lies about a process's status is
// exactly the thing not to reimplement per package.
func FailedHandle(reason string) Handle { return failedHandle{reason} }

type failedHandle struct{ err string }

func (f failedHandle) Subscribe() (<-chan Frame, func()) {
	ch := make(chan Frame, 1)
	ch <- Frame{Kind: EvStatus, Text: string(Failed), Err: f.err}
	close(ch)
	return ch, func() {}
}

// Control on work that never started is refused rather than silently accepted. There is no
// process to pause, and saying so is the difference between "your cancel landed" and "there
// was never anything there".
func (failedHandle) Pause() error                    { return ErrNoSuchProcess }
func (failedHandle) Resume() error                   { return ErrNoSuchProcess }
func (failedHandle) Cancel(string) error             { return ErrNoSuchProcess }
func (failedHandle) Steer(string, string) error      { return ErrNoSuchProcess }
func (failedHandle) SetVector(*Vector, string) error { return ErrNoSuchProcess }
func (failedHandle) Result() string                  { return "" }
func (f failedHandle) Err() string             { return f.err }

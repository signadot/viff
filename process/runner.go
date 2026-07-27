package process

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
	Spawn(id, task, driver string, content ContentReader) Handle

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
type Handle interface {
	Subscribe() (<-chan Frame, func())
	Pause()
	Resume()
	Cancel(reason string)
	Steer(instruction, by string)
	SetVector(v *Vector, by string)
	Result() string
}

// *Process is the local Handle.
var _ Handle = (*Process)(nil)

package task

import "github.com/signadot/tony-format/go-tony/ir"

// THE BODIES OF THE RUNNER CONTRACT, declared once.
//
// A runner is a url, and what verse sends it is these. They are declared here, beside Launch
// and Instance, because this is the package both ends already import: the client that drives a
// runner (package runner) and the runtime a runner is built on (task/taskd). A runner written
// in another language reads them from the schema, where each is published under its name.
//
// ONE DECLARATION, because a body written by hand at each end loses a field without anybody
// being told: the end that sends one the other has no name for has sent nothing, and the end
// that reads one nobody sends reads its zero. A field added to a launch is added here, and
// from here both ends have it or neither compiles.

// SpawnRequest is the body of `POST /tasks`: a launch, as it crosses to the runner.
//
// EVERY FIELD OF A LAUNCH CROSSES, save the two that are verse's own (see Launch.Wire): a
// runner handed less than the launch runs a task other than the one that was asked for, and
// says nothing.
//
// Absent is the runner's own default, for every field, which is why each is `omitzero`. It
// matters most for the grant: the zero Grant permits nothing, so an empty one written out would
// refuse every action of a task nobody meant to bound.
//
//tony:schemagen=verse-runner-spawn,notag
type SpawnRequest struct {
	// ID is the task's id, which the caller mints so that it matches the task's record. A
	// runner asked to spawn with none mints its own.
	ID     string `tony:"field=id,omitzero"`
	Prompt string `tony:"field=prompt,omitzero"`
	Driver string `tony:"field=driver,omitzero"`
	// Frame is the standing instructions the prompt is read in (Launch.Frame).
	Frame string `tony:"field=frame,omitzero"`
	// Tools names the tools offered, narrowing the driver's own set (Launch.Tools).
	Tools []string `tony:"field=tools,omitzero"`
	// Retain is how many read-plane events to keep for a reader that resumes (Launch.Retain).
	Retain int `tony:"field=retain,omitzero"`
	// VirtualKey is the model credential the task runs under: the key itself
	// (Launch.VirtualKey). It is the one field here that carries a secret.
	VirtualKey string `tony:"field=virtualKey,omitzero"`
	// Answer is the answer contract, a tony match pattern (Launch.Answer). A runner that
	// cannot enforce one refuses the spawn.
	Answer *ir.Node `tony:"field=answer,omitzero"`
	// Calls is the work itself, for the `calls` driver (Launch.Calls).
	Calls []Call `tony:"field=calls,omitzero"`
	// Vector and Grant are resolved values, not names (Launch.Vector).
	Vector *Vector `tony:"field=vector,omitzero"`
	Grant  *Grant  `tony:"field=grant,omitzero"`
	// Environment names the workspace, which the runner resolves (Launch.Environment).
	Environment string `tony:"field=environment,omitzero"`
	// Occurrence is which run this is: the store revision it is about (Launch.Occurrence).
	// The runner holds it and says it back on the task's snapshot.
	Occurrence int64 `tony:"field=occurrence,omitzero"`
	// Provenance is on whose account, and about what (Launch.Provenance). Absent when verse
	// knew none of it.
	Provenance *SpawnProvenance `tony:"field=provenance,omitzero"`
}

// SpawnProvenance is a launch's Provenance as it crosses. Each field is absent when verse did
// not know it, which is a record of not knowing and not a claim of having asked.
//
//tony:schemagen=verse-runner-provenance,notag
type SpawnProvenance struct {
	Principal string `tony:"field=principal,omitzero"`
	Subject   string `tony:"field=subject,omitzero"`
	Chain     string `tony:"field=chain,omitzero"`
}

// Spawned is what a runner answers a spawn with: the id the task runs under. `201` built it,
// `200` found it already running.
//
//tony:schemagen=verse-runner-spawned,notag
type Spawned struct {
	ID string `tony:"field=id"`
}

// SayRequest is the body of `POST /tasks/{id}/say`: an instruction to a running task, and who
// gave it. An empty By is an unattributed caller, said as such on the task's read plane.
//
//tony:schemagen=verse-runner-say,notag
type SayRequest struct {
	Instruction string `tony:"field=instruction"`
	By          string `tony:"field=by,omitzero"`
}

// CancelRequest is the body of `POST /tasks/{id}/cancel`: why the task is being ended. The
// reason is what the task's charters see on `onCancelled`, so it crosses. A cancel with no
// body, or no reason, is a cancel all the same, and the runner says its own.
//
//tony:schemagen=verse-runner-cancel,notag
type CancelRequest struct {
	Reason string `tony:"field=reason,omitzero"`
}

// VectorRequest is the body of `PUT /tasks/{id}/vector`: the vector a running task goes on
// under, from its next boundary, and who changed it.
//
//tony:schemagen=verse-runner-vector,notag
type VectorRequest struct {
	Vector Vector `tony:"field=vector"`
	By     string `tony:"field=by,omitzero"`
}

// Wire is the launch as it crosses to a runner.
//
// TWO FIELDS STAY HOME, and they are the two that are verse's own bookkeeping about the task
// and mean nothing to whoever runs it:
//
//   - Scope is the federation the task was spawned into. It says where the task's records
//     land, which is verse's to know, and the runner never sees it.
//   - Resolver is an address in this verse (`verse.resolvers.<name>`). A question a remote
//     task raises comes back on its event stream, and verse holds it out to the resolver from
//     the launch it kept.
//
// Every other field crosses, and launchwire_test.go fails for a field added to Launch that is in
// neither list.
func (l Launch) Wire() SpawnRequest {
	r := SpawnRequest{
		ID: l.ID, Prompt: l.Prompt, Driver: l.Driver, Frame: l.Frame, Tools: l.Tools,
		Retain: l.Retain, VirtualKey: l.VirtualKey, Answer: l.Answer, Calls: l.Calls,
		Vector: l.Vector, Grant: l.Grant, Environment: l.Environment,
		Occurrence: l.Occurrence,
	}
	if p := l.Provenance; p != (Provenance{}) {
		r.Provenance = &SpawnProvenance{Principal: p.Principal, Subject: p.Subject, Chain: p.Chain}
	}
	return r
}

// Launch is the launch a runner was sent. Scope and Resolver are empty: they stay with the
// verse that spawned it (Launch.Wire).
func (r SpawnRequest) Launch() Launch {
	l := Launch{
		ID: r.ID, Prompt: r.Prompt, Driver: r.Driver, Frame: r.Frame, Tools: r.Tools,
		Retain: r.Retain, VirtualKey: r.VirtualKey, Answer: r.Answer, Calls: r.Calls,
		Vector: r.Vector, Grant: r.Grant, Environment: r.Environment,
		Occurrence: r.Occurrence,
	}
	if p := r.Provenance; p != nil {
		l.Provenance = Provenance{Principal: p.Principal, Subject: p.Subject, Chain: p.Chain}
	}
	return l
}

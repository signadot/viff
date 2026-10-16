package task

import "github.com/signadot/tony-format/go-tony/ir"

// THE LAUNCH'S BODIES, as they cross to a runner. The rest of the Runner API's bodies are in
// package runner; these stay beside Launch until the launch is replaced (step 2a of the runners
// plan), because they carry the engine's own types (Vector, Grant, Call).
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
	// Tools names the tools offered (Launch.Tools). Absent, the task gets what the runner
	// makes available for it; `[]` is no tools. A pointer so the two cross the wire apart.
	Tools *[]string `tony:"field=tools,omitzero"`
	// Retain is how many read-plane events to keep for a reader that resumes (Launch.Retain).
	Retain int `tony:"field=retain,omitzero"`
	// Converse makes the run a conversation, the person handed the turn when the driver goes
	// quiet (Launch.Converse).
	Converse bool `tony:"field=converse,omitzero"`
	// VirtualKey is the model credential the task runs under: the key itself
	// (Launch.VirtualKey). It is the one field here that carries a secret.
	VirtualKey string `tony:"field=virtualKey,omitzero"`
	// Credential is the token verse minted for the task (Launch.Credential). A secret, like
	// VirtualKey: the runner hands it to the task as a file, and does not log it.
	Credential string `tony:"field=credential,omitzero"`
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
	// Repos are the repositories the task works in, and what it may push (Launch.Repos).
	Repos []Repo `tony:"field=repos,omitzero"`
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
// ONE FIELD STAYS HOME, verse's own bookkeeping about the task that means nothing to whoever runs
// it: Scope, the copy-on-write scope the task was spawned into. It says where the task's records land,
// which is verse's to know, and the runner never sees it.
//
// Every other field crosses, and launchwire_test.go fails for a field added to Launch that is in
// neither list.
func (l Launch) Wire() SpawnRequest {
	r := SpawnRequest{
		ID: l.ID, Prompt: l.Prompt, Driver: l.Driver, Frame: l.Frame, Tools: ToolsPtr(l.Tools),
		Retain: l.Retain, Converse: l.Converse, VirtualKey: l.VirtualKey, Credential: l.Credential, Answer: l.Answer, Calls: l.Calls,
		Vector: l.Vector, Grant: l.Grant, Environment: l.Environment, Repos: l.Repos,
		Occurrence: l.Occurrence,
	}
	if p := l.Provenance; p != (Provenance{}) {
		r.Provenance = &SpawnProvenance{Principal: p.Principal, Subject: p.Subject, Chain: p.Chain}
	}
	return r
}

// Launch is the launch a runner was sent. Scope is empty: it stays with the verse that spawned it
// (Launch.Wire).
func (r SpawnRequest) Launch() Launch {
	l := Launch{
		ID: r.ID, Prompt: r.Prompt, Driver: r.Driver, Frame: r.Frame, Tools: ToolsOf(r.Tools),
		Retain: r.Retain, Converse: r.Converse, VirtualKey: r.VirtualKey, Credential: r.Credential, Answer: r.Answer, Calls: r.Calls,
		Vector: r.Vector, Grant: r.Grant, Environment: r.Environment, Repos: r.Repos,
		Occurrence: r.Occurrence,
	}
	if p := r.Provenance; p != nil {
		l.Provenance = Provenance{Principal: p.Principal, Subject: p.Subject, Chain: p.Chain}
	}
	return l
}

// ToolsPtr is a tool list as the wire carries it: nil stays absent, and a list — empty
// included — is written out, so "no tools" is not read back as "unnamed".
func ToolsPtr(tools []string) *[]string {
	if tools == nil {
		return nil
	}
	out := append([]string{}, tools...)
	return &out
}

// CloneTools is a copy of a tool list as the wire carries it: nil stays nil, else one copy.
func CloneTools(p *[]string) *[]string {
	if p == nil {
		return nil
	}
	out := append([]string{}, (*p)...)
	return &out
}

// ToolsOf is a tool list as read from the wire: absent is nil, and a list — empty included —
// is a list, never nil.
func ToolsOf(p *[]string) []string {
	if p == nil {
		return nil
	}
	return append([]string{}, (*p)...)
}

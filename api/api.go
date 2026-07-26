// Package api is the wire form of verse's HTTP surface: one named type per request
// body, shared by the server that decodes it and every client that builds it.
//
// It exists because the contract used to be written twice — a `var body struct{…}`
// with tony field tags inside each handler, and a `map[string]any{"…": …}` literal at
// each call site — with no symbol in common and nothing to check one against the other.
// That is not hypothetical drift: `/gate/resolve` took `{id, approve}` while
// `/processes/{id}/resolve` took `{decisionId, proceed}` for what the CLI presents as
// the same operation, and `say` was a list on one and a string on the other. Nobody
// wrote that on purpose; there was simply nowhere for the two halves to disagree in
// front of a compiler.
//
// So: the types live here, both sides import them, and a renamed field is a build
// failure rather than a silently-ignored key and a puzzling 400. The schemagen
// directives are the second reason — they make the request bodies visible to
// tony-codegen, which is what lets the API describe itself (docs/… , `GET /schema`)
// instead of describing only the payload types that happened to be named already.
//
// Only request bodies live here today. Responses are still assembled as map literals
// at their handlers; naming those is the same exercise and the same argument, one pass
// behind.
package api

//go:generate go tool tony-codegen -dir .

import (
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/process"
)

// PutEntity writes a payload to an entity ref (`POST /entity`).
//
// Payload is a tony node, not a map: an entity payload is ANY tony value (see
// entity.Payload), so `verse entity put demo:count:1 42` has to survive the wire as the
// number it is. A map here would have re-imposed on the wire exactly the object constraint
// the store just dropped.
//
//tony:schemagen=verse-put-entity,notag
type PutEntity struct {
	Ref     string   `tony:"field=ref"`
	Payload *ir.Node `tony:"field=payload,omitzero"`
}

// Resolve answers ONE pending gate, by its id, wherever it lives — an engine gate the
// trigger registry raised, or a process gate raised inside a running agent. The routing
// is the server's problem (a process gate's id carries its owner ahead of
// process.GateSep); a caller just names the gate and says yes or no.
//
// This is the type that unified two spellings. `id`/`approve` won over
// `decisionId`/`proceed` because they are the words the CLI, `gate list`, and the docs
// already use, and because "decision id" named the same thing twice.
//
//tony:schemagen=verse-resolve,notag
type Resolve struct {
	ID      string `tony:"field=id"`
	Approve bool   `tony:"field=approve"`
	// Say rides along with the answer as steering — "yes, but do it this way". It is a
	// list because process.Resolution.Steer is: an answer may carry several instructions,
	// and collapsing them at the boundary would lose an author's paragraphing.
	Say []string `tony:"field=say,omitzero"`
}

// Spawn runs an ad-hoc process (`POST /processes`) — the `process run` path, which has
// no subject or flow, unlike a trigger's spawn.
//
//tony:schemagen=verse-spawn,notag
type Spawn struct {
	ID     string `tony:"field=id,omitzero"` // empty → the runtime mints a `run-…` id
	Task   string `tony:"field=task"`
	Driver string `tony:"field=driver,omitzero"` // empty → the runtime default
}

// Spawned is what a spawn answers with: the id to address the new process by.
//
//tony:schemagen=verse-spawned,notag
type Spawned struct {
	ID string `tony:"field=id"`
}

// Say re-prompts a running process with a new instruction
// (`POST /processes/{id}/say`).
//
//tony:schemagen=verse-say,notag
type Say struct {
	Instruction string `tony:"field=instruction"`
	By          string `tony:"field=by,omitzero"` // attribution; empty → human:cli
}

// SetVector slides a running process's gate vector — the autonomy dial — applied at its
// next boundary with no restart (`PUT /processes/{id}/vector`).
//
// Vector is a pointer because that is what process.SetVector takes and because an
// absent vector and a zero one are different requests. It also sidesteps a
// tony-codegen defect: a struct-valued field of another package makes the generator
// emit an import it never qualifies, and the generated file does not compile.
//
//tony:schemagen=verse-set-vector,notag
type SetVector struct {
	Vector *process.Vector `tony:"field=vector"`
	By     string          `tony:"field=by,omitzero"` // who slid it, for the record
}

// GoalSteer addresses one goal by name: pause, resume, cancel (name alone), or say
// (name plus text).
//
//tony:schemagen=verse-goal-steer,notag
type GoalSteer struct {
	Name string `tony:"field=name"`
	Text string `tony:"field=text,omitzero"`
	By   string `tony:"field=by,omitzero"`
}

// FlowSteer addresses a flow — every live process whose subject matches — and pauses,
// resumes, cancels, or says to all of them as a unit. Subject is an address or a
// pattern, so one call can steer a subject, a kind, or a whole system.
//
//tony:schemagen=verse-flow-steer,notag
type FlowSteer struct {
	Subject string `tony:"field=subject"`
	Text    string `tony:"field=text,omitzero"`
	By      string `tony:"field=by,omitzero"`
}

// Route is one entry of the API's own route table (`GET /api`): a method and path, and the
// signature names of what it accepts and returns.
//
// An empty Request is a route with no body (a GET, or a control POST that takes none). An
// empty Response means the response shape is NOT YET a named type — most handlers still
// assemble map literals. That absence is deliberate and load-bearing: it makes the remaining
// naming work visible in the published artifact rather than hiding it behind a claim like
// "returns an open value", which would be true of anything and useful about nothing.
//
//tony:schemagen=verse-route,notag
type Route struct {
	Method   string `tony:"field=method"`
	Path     string `tony:"field=path"`
	Request  string `tony:"field=request,omitzero"`
	Response string `tony:"field=response,omitzero"`
}

// Routes is the whole table, as served.
//
//tony:schemagen=verse-routes,notag
type Routes struct {
	Routes []Route `tony:"field=routes"`
}

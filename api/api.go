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
	"github.com/signadot/verse/entity"
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
	Ref     string         `tony:"field=ref"`
	Payload entity.Payload `tony:"field=payload,omitzero"`
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
// Vector is a pointer because that is what process.SetVector takes and because an absent
// vector and a zero one are different requests — a distinction this route needs, since
// "slide to this vector" and "slide to the zero vector" are not the same instruction.
//
// It was ALSO a workaround, and no longer is: until go-tony v0.0.94 a struct-valued field of
// another package made the generator emit an import it never qualified (item 17 of
// tony-format#f69agjyeh12ksa25cnn0). Fixed, so a value field would compile today. The
// pointer stays on the first reason alone.
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

// Steered is the result of steering a flow or a goal: pause, resume, cancel, or say.
//
// ONE type with a verb field rather than four types keyed by verb. The routes used to answer
// `{subject, paused: 2}`, `{subject, resumed: 2}`, `{subject, cancelled: 2}`, `{subject,
// said: 2}` — four shapes for one operation, where the key was doing double duty as the name
// of what happened and the count of it. A reader had to know which endpoint produced a
// document to know what its second key meant.
//
// Carrying the verb makes the answer self-describing: piped, logged, or read back later it
// says what was done without reference to the route that did it. And a consumer parses one
// shape for all eight routes instead of eight.
//
// Affected is a count for both, because a goal steer IS a flow steer — Engine.PauseGoal and
// friends delegate to the flow methods over the goal's own processes.
//
//tony:schemagen=verse-steered,notag
type Steered struct {
	// Subject and Goal are the two ways the same operation is addressed: a flow by the
	// entity address (or pattern) its processes are about, a goal by name. Exactly one is
	// set — they are kept as distinct fields rather than collapsed into a `target` because
	// "subject" is a first-class word in this model and a synonym would cost more than the
	// uniformity it buys.
	Subject string `tony:"field=subject,omitzero"`
	Goal    string `tony:"field=goal,omitzero"`
	// Verb is what was done: pause | resume | cancel | say.
	Verb string `tony:"field=verb"`
	// Affected is how many live processes it reached. Zero is a normal answer — an address
	// that names no running work is not an error, it is a flow that has finished.
	Affected int `tony:"field=affected"`
}

// InboxItem is one control instruction queued for a process's worker — the broker's
// dial-in leg. It is cursored so a reconnecting worker resumes with ?since=<cursor>.
//
// It lives here because it was spelled THREE times: the broker's own queue element, a
// map[string]any re-spelling of that element in ProcessInbox, and a partial copy in the CLI
// carrying the four fields an executor acts on. Three spellings of one message, none of
// which a compiler could compare — the same defect this package was created to remove for
// request bodies, surviving on the response side because nobody looked.
//
// The subset the CLI used to declare is not lost by naming the whole thing: a field an
// executor does not read simply arrives and is ignored.
//
//tony:schemagen=verse-inbox-item,notag
type InboxItem struct {
	Cursor int64  `tony:"field=cursor"`
	Kind   string `tony:"field=kind"` // spec | say | cancel | pause | resume | vector | resolve
	Task   string `tony:"field=task,omitzero"`
	Driver string `tony:"field=driver,omitzero"`
	Text   string `tony:"field=text,omitzero"`
	By     string `tony:"field=by,omitzero"`
	// Decision and Proceed answer a gate the worker raised: which one, and yes or no.
	Decision string `tony:"field=decision,omitzero"`
	Proceed  bool   `tony:"field=proceed,omitzero"`
	// Vector slides the worker's autonomy dial mid-run.
	Vector *process.Vector `tony:"field=vector,omitzero"`
}

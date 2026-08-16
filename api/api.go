// Package api is the wire form of verse's HTTP surface: one named type per request
// body, shared by the server that decodes it and every client that builds it.
//
// It exists because the contract used to be written twice — a `var body struct{…}`
// with tony field tags inside each handler, and a `map[string]any{"…": …}` literal at
// each call site — with no symbol in common and nothing to check one against the other.
// That is not hypothetical drift: `/gate/resolve` took `{id, approve}` while
// `/tasks/{id}/resolve` took `{decisionId, proceed}` for what the CLI presents as
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

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/task"
)

// PutEntity is one conditional write (`POST /entity`) — the store's single write, on the
// wire. Creating, updating, patching a field and removing one are all this request with
// different fields set.
//
// Ref names the entity; Field goes further, naming a place INSIDE its payload
// ({"labels","urgent"}), so a write can be a whole entity or one field of one.
//
// THE WIRE STAYS SPLIT while the CLI takes one address. `verse state put
// demo.pr.1.labels.urgent true` is one kinded path, and entity.ParsePath is the one parser
// that reads it — the wire has no reason to re-parse what the CLI has already split, and the
// structured form is what the store's writePath consumes. A list rather than a dotted string
// for the reason the CLI needs quoting: an entity id may itself contain dots (a file path is a
// perfectly good id), so a dotted field spelling could not be told apart from one.
//
// The value MERGES at that place: fields it does not mention are left alone, and an absent
// (null) Payload removes whatever stands there. A writer that means to drop a field says
// so, by writing null at it.
//
// Payload is a tony node, not a map: an entity payload is ANY tony value (see
// entity.Payload), so `verse state put demo:count:1 42` has to survive the wire as the
// number it is. A map here would have re-imposed on the wire exactly the object constraint
// the store just dropped.
//
//tony:schemagen=verse-put-entity,notag
type PutEntity struct {
	Ref     string         `tony:"field=ref"`
	Field   []string       `tony:"field=field,omitzero"`
	Payload entity.Payload `tony:"field=payload,omitzero"`
	// When are preconditions, ALL of which must hold. They are checked WITH the write, in
	// one step at the store, so there is no window between the check and the write for
	// something to change underneath. An empty When is an unconditional write.
	When []Condition `tony:"field=when,omitzero"`
}

// Condition is one precondition on a write: what must be true, and where.
//
// Ref/Field name the place asked about; both empty means the write's own place, which is
// what create ("nothing is there yet") and update ("something is") are about.
//
//tony:schemagen=verse-condition,notag
type Condition struct {
	Ref   string   `tony:"field=ref,omitzero"`
	Field []string `tony:"field=field,omitzero"`
	// Exists asks only whether anything stands there: false is a create, true is an update.
	// It is a pointer so that "did not ask" is distinguishable from "asked for absence".
	Exists *bool `tony:"field=exists,omitzero"`
	// Pattern asks what it looks like — a tony match, so it can say more than equality.
	Pattern entity.Payload `tony:"field=pattern,omitzero"`
}

// Resolve answers ONE pending gate, by its id, wherever it lives — an engine gate the
// trigger registry raised, or a task gate raised inside a running agent. The routing
// is the server's problem (a task gate's id carries its owner ahead of
// task.GateSep); a caller just names the gate and says yes or no.
//
// This is the type that unified two spellings. `id`/`approve` won over
// `decisionId`/`proceed` because they are the words the CLI, `gate list`, and the docs
// already use, and because "decision id" named the same thing twice.
//
//tony:schemagen=verse-resolve,notag
type Resolve struct {
	ID      string `tony:"field=id"`
	Approve bool   `tony:"field=approve"`
	// By is who answered. It rides the wire the same way FlowSteer.By and a say's By do,
	// and for the same reason: a resolution is an act, and an act has an actor.
	//
	// REQUIRED. The server used to default it to "human:cli", which published a name
	// belonging to nobody; now that an answer is a durable entry on the question record, an
	// unattributable one would be permanent. Same rule as a ballot's --as, and for the same
	// reason — a decision has no meaning without whoever made it.
	By string `tony:"field=by"`
	// Say rides along with the answer as steering — "yes, but do it this way". It is a
	// list because task.Resolution.Steer is: an answer may carry several instructions,
	// and collapsing them at the boundary would lose an author's paragraphing.
	Say []string `tony:"field=say,omitzero"`
	// Override answers a question an exclusive binding owns, as somebody else.
	//
	// It exists because `exclusive` is not real against whoever holds this API, and a dial
	// that pretended otherwise would be a lie told to the person relying on it. So the
	// override is a named act rather than a hole: it lands on the record naming both
	// parties — who overrode, and whom.
	Override bool `tony:"field=override,omitzero"`
}

// Spawn runs an ad-hoc task (`POST /tasks`) — the `task run` path, which has
// no subject or flow, unlike a trigger's spawn.
//
//tony:schemagen=verse-spawn,notag
type Spawn struct {
	// ID names the run. It is validated rather than taken as given: an id is the prefix of
	// every gate question the run raises (`<id>~d1`), and answering one recovers the owner by
	// cutting at the first `~` — so an id carrying one hands its questions to a task that
	// is not it. It also lands in a URL path (`/tasks/{id}`).
	//
	// Prefer Name, which is a label the runtime derives a safe id FROM.
	ID string `tony:"field=id,omitzero"` // empty → derived from Name, or a minted `run-…`
	// Name is a label for the run, from which the id is derived and returned. It exists so a
	// caller can say what a run IS without choosing its address: `--name review` becomes
	// `review-8f306077b1c2`, which is readable, unique, and cannot be malformed.
	Name   string `tony:"field=name,omitzero"`
	Prompt string `tony:"field=prompt"`
	Driver string `tony:"field=driver,omitzero"` // empty → the runtime default
	// Retain is how many read-plane events to keep for a resuming reader; 0 = the runtime's
	// default. A charter says it per spawn (launch.Spec.Retain) and this is the ad-hoc face
	// of the same field, so `verse task run` and a firing can ask for the same thing.
	Retain int `tony:"field=retain,omitzero"`
	// Subject is the flow this run belongs to — the entity it is about. A trigger spawn
	// carries one from its firing; this is the ad-hoc face of the same field, and without it
	// a `task run` is about nothing, which is a real state and not a useful default.
	Subject string `tony:"field=subject,omitzero"`
	// Vector is the autonomy dial — `auto`, `ask` or `park` — the ad-hoc face of a spawn's
	// `vector:`. Without it the runtime's own default stands, which for a scripted run is
	// `ask`: every command it proposes is a question. `verse task run --driver scripted`
	// sends `auto` on the caller's behalf, because a prompt somebody typed IS the command
	// they authorized — nothing else gets that, a charter least of all.
	Vector string `tony:"field=vector,omitzero"`
	// Calls is a call list posted directly, for `verse task run --driver calls`: the actions
	// themselves rather than a prompt for something to work them out. The CLI parses the tony
	// document a person typed (task.ParseCalls) and sends this, so a list that will not read
	// is a message on their terminal rather than a task that failed on a machine elsewhere.
	Calls []task.Call `tony:"field=calls,omitzero"`
	// Resolver names the binding that answers this run's gates — the ad-hoc face of a
	// spawn's `resolver:`, so `verse task run` and a firing can bind the same way.
	Resolver string `tony:"field=resolver,omitzero"`
	// Runner names WHERE this run runs — `runners:<kind>:<name>`, installed with `verse
	// runner add`. Empty means the verse's own default runner, which is what `local` means
	// on a task:instance.
	//
	// It is orthogonal to Resolver and to a provider: a provider picks the DOER, a resolver
	// picks who answers for it, and this picks the place. The same doer may run in either of
	// two places and the same place may host either of two doers, so folding them together
	// would make one selection answer two questions.
	Runner string `tony:"field=runner,omitzero"`
	// Environment names the workspace the runner should build for this run — the ad-hoc face
	// of a spawn's `environment:`, so `verse task run` and a firing can ask for the same
	// thing. Resolved by the runner against its own config, never by verse: see
	// task.Launch.Environment.
	Environment string `tony:"field=environment,omitzero"`
	// Answer is this run's ANSWER CONTRACT — the shape its final message must have, as a tony
	// match pattern. The ad-hoc face of a spawn's `answer:`, so `verse task run` can declare
	// what a charter can.
	//
	// It is not decoration on this path. Without a contract a driver's only definition of
	// "finished" is the model declining to call another tool, and a model that narrates a plan
	// between steps ends the task mid-investigation — observed, with the result reading "Let me
	// focus on:". A contract is what makes done CHECKABLE: the answer is validated when the
	// model stops, the complaint is fed back, and the run fails if the shape never arrives.
	Answer *ir.Node `tony:"field=answer,omitzero"`
	// Principal is who is answerable for this run. Self-asserted — the same standing `--as`
	// has everywhere else, and for the same reason: verifying a caller is authority's
	// subject, not this field's. It is carried so a runner whose effects LEAVE the verse can
	// write an externally checkable link back (docs/working/attribution.md).
	//
	// A charter firing has no equivalent yet, and deliberately no default here: an empty
	// principal means nobody said, which is a fact worth recording as itself.
	Principal string `tony:"field=principal,omitzero"`
}

// Spawned is what a spawn answers with: the id to address the new task by.
//
//tony:schemagen=verse-spawned,notag
type Spawned struct {
	ID string `tony:"field=id"`
}

// Verdict is one answer from a resolver (`POST /resolvers/instance/{id}/verdicts`).
//
// It names the question rather than being addressed to it: a resolver reads a stream and
// decides about what it sees, so the id is which of the things it has read this is about.
//
//tony:schemagen=verse-verdict,notag
type Verdict struct {
	ID string `tony:"field=id"`
	// Verdict is `proceed` or `deny` — the record's own vocabulary, so what a resolver says
	// and what the docket shows are one word rather than a mapping. `escalate` is the third
	// and arrives with the layer that can receive it.
	Verdict string `tony:"field=verdict"`
	// Why is the reasoning, for the record. Say is what to tell the task — "yes, but do
	// it this way" — and rides the same Steer a person's `--say` does.
	Why string   `tony:"field=why,omitzero"`
	Say []string `tony:"field=say,omitzero"`
}

// Position is how far a resolver has read (`POST /resolvers/instance/{id}/position`).
//
// Reported even when nothing was decided, which is the point: a hundred frames may produce no
// verdict, and the position advancing is what tells "has not decided" from "has not caught
// up". Notes are a handover for whoever picks the position up — a replacement after a
// restart, or the same resolver with a fresh context — not a memory, since a live one
// remembers by being alive.
//
//tony:schemagen=verse-position,notag
type Position struct {
	Position int64  `tony:"field=position"`
	Notes    string `tony:"field=notes,omitzero"`
}

// Say re-prompts a running task with a new instruction
// (`POST /tasks/{id}/say`).
//
//tony:schemagen=verse-say,notag
type Say struct {
	Instruction string `tony:"field=instruction"`
	By          string `tony:"field=by,omitzero"` // attribution; empty → human:cli
}

// SetVector slides a running task's gate vector — the autonomy dial — applied at its
// next boundary with no restart (`PUT /tasks/{id}/vector`).
//
// Vector is a pointer because that is what task.SetVector takes and because an absent
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
	Vector *task.Vector `tony:"field=vector"`
	By     string       `tony:"field=by,omitzero"` // who slid it, for the record
}

// FlowSteer addresses a flow — every live task whose subject matches — and pauses,
// resumes, cancels, or says to all of them as a unit. Subject is an address or a
// pattern, so one call can steer a subject, a kind, or a whole system.
//
//tony:schemagen=verse-flow-steer,notag
type FlowSteer struct {
	Subject string `tony:"field=subject"`
	Text    string `tony:"field=text,omitzero"`
	By      string `tony:"field=by,omitzero"`
}

// Route is one entry of the API's own route table (`GET /api`): a method and path, the
// signature names of what it accepts and returns, and how many of them come back.
//
// An empty Request is a route with no body (a GET, or a control POST that takes none). An
// empty Response means the response shape is NOT YET a named type — most handlers still
// assemble map literals. That absence is deliberate and load-bearing: it makes the remaining
// naming work visible in the published artifact rather than hiding it behind a claim like
// "returns an open value", which would be true of anything and useful about nothing.
//
// Response names ONE document; a name is all it can carry. Returns says how many arrive and by
// what means, because the name alone could not: `GET /triggers` published `response:
// verse-trigger` while its body is an ARRAY of them, and `GET /watch` published the same claim
// for a thing that is not a document at all but an SSE stream of them. Both read as "returns a
// verse-trigger" / "returns a verse-event", which is wrong in the way a published signature
// must never be wrong — confidently, in the artifact that exists to be trusted about the wire.
//
// Returns is also what makes a route COMPARABLE to the schema. The root (schema/root.tony)
// types each top-level path — `state: .[verse-state]` — and is written by hand,
// beholden to no route table; a route's response and returns together say the same thing in the
// same terms, which is what lets a test hold the two accounts of one surface equal. See
// TestTheRootIsTheServedSurface.
//
//tony:schemagen=verse-route,notag
type Route struct {
	Method  string `tony:"field=method"`
	Path    string `tony:"field=path"`
	Request string `tony:"field=request,omitzero"`
	// Response is a signature name, or a tony base type where that is the whole truth:
	// `GET /flows` answers with the subjects that have live work, which are addresses, so its
	// response is `string` and there is nothing further to name.
	Response string `tony:"field=response,omitzero"`
	Returns  string `tony:"field=returns,omitzero"`
}

// The values of Route.Returns: how many Response documents come back, and by what means.
//
// Empty is the fourth case and means nothing worth naming came back — a route that answers
// with no body, or one whose shape is not named yet. Every route with a Response has one of
// these, and a route without one has neither (TestEveryRouteSaysWhatItReturns).
const (
	// ReturnsOne is one document, alone, as the body.
	ReturnsOne = "one"
	// ReturnsList is an array of them.
	ReturnsList = "list"
	// ReturnsStream is a sequence of them over server-sent events — frames, not a document,
	// so the body has no tony type even though each frame's payload does.
	ReturnsStream = "stream"
)

// Routes is the whole table, as served.
//
//tony:schemagen=verse-routes,notag
type Routes struct {
	Routes []Route `tony:"field=routes"`
}

// Steered is the result of steering a flow: pause, resume, cancel, or say.
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
//tony:schemagen=verse-steered,notag
type Steered struct {
	// Subject is what was steered: the entity address (or pattern) the flow's tasks are
	// about. A `goal` field stood beside it while a goal could be steered by name — one
	// operation addressed two ways — and went with goals.
	Subject string `tony:"field=subject,omitzero"`
	// Verb is what was done: pause | resume | cancel | say.
	Verb string `tony:"field=verb"`
	// Affected is how many live tasks it reached. Zero is a normal answer — an address
	// that names no running work is not an error, it is a flow that has finished.
	Affected int `tony:"field=affected"`
}

// InboxItem is one control instruction queued for a task's worker — the broker's
// dial-in leg. It is cursored so a reconnecting worker resumes with ?since=<cursor>.
//
// It lives here because it was spelled THREE times: the broker's own queue element, a
// map[string]any re-spelling of that element in TaskInbox, and a partial copy in the CLI
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
	Prompt string `tony:"field=prompt,omitzero"`
	Driver string `tony:"field=driver,omitzero"`
	Text   string `tony:"field=text,omitzero"`
	By     string `tony:"field=by,omitzero"`
	// Decision and Proceed answer a gate the worker raised: which one, and yes or no.
	Decision string `tony:"field=decision,omitzero"`
	Proceed  bool   `tony:"field=proceed,omitzero"`
	// Vector slides the worker's autonomy dial mid-run.
	Vector *task.Vector `tony:"field=vector,omitzero"`
}

// Status is what GET /status answers: what this verse is, what it is running, and where to
// reach the pieces around it.
//
// The counters are the cheap health read; the three optional blocks are discovery. `docd` is
// how `verse connect` finds the store's mount face without guessing the ephemeral port `up`
// chose, `listen` publishes this verse's own addresses, and `llm` says whether agent creds
// are configured ON THIS SERVER — the common gotcha, since the runtime reads its own
// environment rather than the client's.
//
//tony:schemagen=verse-status,notag
type Status struct {
	StoreRev int64 `tony:"field=storeRev"`
	Triggers int   `tony:"field=triggers"`
	Flows    int   `tony:"field=flows"`
	Gates    int   `tony:"field=gates"`
	Tasks    int   `tony:"field=tasks"`
	Fired    int   `tony:"field=fired"`

	// WHAT IS INSTALLED, as against what is happening. The counts above move on their own;
	// these three are things somebody put here, and a verse that has lost them looks healthy by
	// every other line — which is exactly the state a wiped store comes back in.
	//
	// Strata are NAMES IN LEVEL ORDER, low to high: the order is the fact, and `verse charter`
	// carries the numbers. Runners are the refs a spawn may name (`runners:pool:code`), which is
	// a different question from `runner` below — that one is how THIS verse runs a task when a
	// spawn names nobody. Sources are the refs of what WRITES into this verse
	// (`sources:git:main`) — the one line here that says anything is arriving at all, since a
	// verse whose sources are gone goes on reporting a fine store rev and an idle charter.
	//
	// All three are refs rather than records: status is an orientation, and `verse charter`,
	// `verse runners` and `verse sources` are the surfaces that answer what one IS. A source's
	// health — when its last pass completed, whether it is failing — is on `verse sources`.
	Strata  []string `tony:"field=strata,omitzero"`
	Runners []string `tony:"field=runners,omitzero"`
	Sources []string `tony:"field=sources,omitzero"`

	Runner *RunnerStatus `tony:"field=runner,omitzero"`
	Docd   *Docd         `tony:"field=docd,omitzero"`
	LLM    *LLM          `tony:"field=llm,omitzero"`
	Listen *Listen       `tony:"field=listen,omitzero"`
}

// RunnerStatus is how this verse runs tasks: local (in-process), broker (a dial-in
// worker face), or remote (a runner base URL). Resume says what survives a restart,
// and Dial is the worker-facing address when there is one.
//
//tony:schemagen=verse-runner-status,notag
type RunnerStatus struct {
	Kind   string `tony:"field=kind"`
	Resume string `tony:"field=resume"`
	Dial   string `tony:"field=dial,omitzero"`
}

// Docd is the document store's two faces: the client address store clients connect to, and
// the mount address a controller task reaches.
//
//tony:schemagen=verse-docd,notag
type Docd struct {
	Client string `tony:"field=client,omitzero"`
	Mount  string `tony:"field=mount,omitzero"`
}

// LLM is a SECRETS-SAFE summary of the task runtime's model config: whether it is usable
// at all, and masked keys so an operator can tell which credential is configured without the
// credential leaving the server.
//
//tony:schemagen=verse-llm,notag
type LLM struct {
	Available  bool   `tony:"field=available"`
	APIKey     string `tony:"field=apiKey,omitzero"`
	VirtualKey string `tony:"field=virtualKey,omitzero"`
	Model      string `tony:"field=model,omitzero"`
	BaseURL    string `tony:"field=baseURL,omitzero"`
}

// Listen is this verse's own addresses: the operator API, and the broker face when served.
//
//tony:schemagen=verse-listen,notag
type Listen struct {
	API    string `tony:"field=api,omitzero"`
	Broker string `tony:"field=broker,omitzero"`
}

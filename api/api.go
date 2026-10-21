// Package api is the wire form of verse's HTTP surface: one named type per request or
// response body, shared by the server that decodes or assembles it and every client that
// builds it or reads it back.
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
// Most responses are still assembled as map literals at their handlers; naming those is
// the same exercise and the same argument, one pass behind. Task is named here because
// TaskFrom needed somewhere both verse and its caller could read the same shape from.
package api

//go:generate go tool tony-codegen -dir .

import (
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// PutEntity is one conditional write (`POST /entity`) — the store's single write, on the
// wire. Creating, updating, patching a field and removing one are all this request with
// different fields set.
//
// THE ADDRESS IS ONE KINDED PATH, at whatever depth, and it is the whole place written:
// `demo.pr.1` is an entity, `demo.pr.1.labels.urgent` is a field of one, and nothing on the
// wire says which — entity-hood is a LENS rather than a stored fact, so a write that had to
// declare where the entity ended was
// answering a question the store does not ask. It carried three spellings to avoid it: a
// three-segment `ref`, a `path` vector for anything deeper, and a `field` list for the place
// inside. They are one path, so that is what this takes — the same grammar the READ takes
// (`GET /state?at=`).
//
// A KINDED PATH RATHER THAN A VECTOR because the quoting is the grammar's own: an entity id
// may hold a dot (a file path is a perfectly good id), and `demo.pr."a.b"` says so where a
// bare dotted string could not. That was the argument for the vector, and kpath answers it
// without a second spelling.
//
// The value MERGES at that place: fields it does not mention are left alone, and an absent
// (null) Payload removes whatever stands there. A writer that means to drop a field says
// so, by writing null at it.
//
// Payload is a tony node, not a map: an entity payload is ANY tony value (see
// entity.Payload), so `verse data demo count 1 --put 42` has to survive the wire as the
// number it is. A map here would have re-imposed on the wire exactly the object constraint
// the store just dropped.
//
//tony:schemagen=verse-put-entity,notag
type PutEntity struct {
	// At is the place written, as a kinded path: `demo.pr.1`, `demo.pr.1.labels.urgent`,
	// `demo.pr."a.b"`.
	At string `tony:"field=at"`
	// Sub is the place inside the address when it is richer than names: an array access, and
	// anything under it — `votes[0]`, `votes(scott).choice`. A kinded path fragment below At.
	// See entity.Path.Access for why it rides beside the address rather than inside it.
	Sub     string         `tony:"field=sub,omitzero"`
	Payload entity.Payload `tony:"field=payload,omitzero"`
	// When are preconditions, ALL of which must hold. They are checked WITH the write, in
	// one step at the store, so there is no window between the check and the write for
	// something to change underneath. An empty When is an unconditional write.
	When []Condition `tony:"field=when,omitzero"`
	// Grants is what the node the write CREATES holds out, in the list `verse perms --grants`
	// sets on a record — `[{read: [ana@example.com]}]`, and `[]` for nothing — in place of the
	// default it would copy from the nearest `child-grants` above. The write is then a create:
	// the node lands with its record in one commit, or not at all, and a node that stands is
	// refused (409); what it holds out is changed with `verse perms`.
	Grants entity.Payload `tony:"field=grants,omitzero"`
}

// Condition is one precondition on a write: what must be true, and where.
//
// At names the place asked about, in the same grammar the write's own address is in; empty
// means the write's own place — the array element it names, where Sub names one — which is
// what create ("nothing is there yet") and update ("something is") are about.
//
//tony:schemagen=verse-condition,notag
type Condition struct {
	At string `tony:"field=at,omitzero"`
	// Exists asks only whether anything stands there: false is a create, true is an update.
	// It is a pointer so that "did not ask" is distinguishable from "asked for absence".
	Exists *bool `tony:"field=exists,omitzero"`
	// Pattern asks what it looks like — a tony match, so it can say more than equality.
	Pattern entity.Payload `tony:"field=pattern,omitzero"`
}

// GateSay says something ABOUT a question without answering it: "checking with ops", "this is the
// third time this week", "I would, but not before the release".
//
// A separate act from Resolve, and a separate route, because an answer carries a choice and this
// one carries none — `/asks/{id}/answer` refuses an answer that names no option, and rightly, since a
// comment that settled a gate by accident would be the worst possible failure of this model. What
// it leaves is a durable, attributed line on the question, where everyone who reads the question
// reads it, rather than in whatever chat window it was typed in.
//
//tony:schemagen=verse-gate-say,notag
type GateSay struct {
	// ID is the question said on. POST /asks/{id}/say takes it from its path, and a body naming
	// another is refused.
	ID  string `tony:"field=id"`
	By  string `tony:"field=by"`
	Say string `tony:"field=say"`
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
	// ID is the gate answered. POST /asks/{id}/answer takes it from its path, and a body naming
	// another is refused; POST /tasks/{id}/resolve takes it here.
	ID string `tony:"field=id"`
	// Choice is the option chosen, by id: `proceed` or `deny` on the question every gate
	// poses unless it says otherwise, or one of the record's own options where it names
	// them. A ballot naming an option the record does not carry is refused, with the
	// options listed. It was a boolean, `approve`, which could not name a third option.
	Choice string `tony:"field=choice"`
	// By is who answered. It rides the wire the same way FlowSelect.By and a say's By do,
	// and for the same reason: a resolution is an act, and an act has an actor.
	//
	// REQUIRED, and never defaulted: a default publishes a name belonging to nobody, and an
	// answer is a durable entry on the question record, so an unattributable one would be
	// permanent. Same rule as a ballot's --as, and for the same
	// reason — a decision has no meaning without whoever made it.
	By string `tony:"field=by"`
	// Say rides along with the answer as steering — "yes, but do it this way". It is a
	// list because task.Resolution.Steer is: an answer may carry several instructions,
	// and collapsing them at the boundary would lose an author's paragraphing.
	Say []string `tony:"field=say,omitzero"`
	// For is the party a resolver answers for, where the request is made as the resolver
	// (ResolverHeader): a principal, or a place's address, as GET /resolvers/{name}/asks names
	// it. It may be left out where the resolver serves the ask for one party only.
	For string `tony:"field=for,omitzero"`
	// Edit is the edit of a person's ask this answers, as the answerer was shown it. An answer
	// to an earlier edit is refused: it consented to words no longer asked. Absent is the edit
	// standing now.
	Edit int `tony:"field=edit,omitzero"`
	// Force answers as yourself an ask a resolver you granted serves for you. Without it that
	// answer is refused, naming the resolver: the party chose to have it answer.
	Force bool `tony:"field=force,omitzero"`
}

// Spawn runs a task by hand (`POST /tasks`, `verse task run -f`): a spawn as a charter writes
// one, and what is about this request rather than the work — its id, a conversation, who asks.
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
	Name string `tony:"field=name,omitzero"`
	// Spawn is the work, as a charter's spawn writes it (launch.Spec), read as strictly and
	// meaning the same: nothing is filled in that it does not say. Nothing fires for it, so it
	// binds no `$` and sets none of what a firing sets (launch.Spec.CheckByHand).
	Spawn launch.Spec `tony:"field=spawn"`
	// Converse makes the run a conversation: when the model goes quiet the person is handed
	// the turn as a question, and the run ends when they end it. `task run -attach` sends it
	// unless told `-no-converse`; it has no charter face, deliberately.
	Converse bool `tony:"field=converse,omitzero"`
	// Principal is who is answerable for this run. Self-asserted — the same standing `--as`
	// has everywhere else, and for the same reason: verifying a caller is authority's
	// subject, not this field's. It is carried so a runner whose effects LEAVE the verse can
	// write an externally checkable link back.
	//
	// A charter firing has no equivalent yet, and deliberately no default here: an empty
	// principal means nobody said, which is a fact worth recording as itself.
	Principal string `tony:"field=principal,omitzero"`
}

// Task is a task as verse reports it: the answer to verse's `GET /tasks` and `GET /tasks/{id}`
// (`verse ps`, `verse task get`). It is built from verse's record of the task and, while a
// runner still holds it, that runner's snapshot (task.Snapshot), whose fields it repeats
// under the same names; Runner and RunnerName are what only verse knows.
//
// The snapshot's fields are declared again rather than embedded because the generated codec
// nests an embedded type from another package under its own key, and the shape on the wire is
// flat.
//
//tony:schemagen=verse-instance,notag
type Task struct {
	ID         string             `tony:"field=id"`
	Prompt     string             `tony:"field=prompt,omitzero"`
	Status     task.Status        `tony:"field=status"`
	Runner     string             `tony:"field=runner,omitzero"` // where it runs or ran: local, or the runner's url
	RunnerName string             `tony:"field=runnerName,omitzero"`
	Cursor     int64              `tony:"field=cursor,omitzero"`
	Occurrence int64              `tony:"field=occurrence,omitzero"`
	Started    string             `tony:"field=started,omitzero"`
	Ended      string             `tony:"field=ended,omitzero"`
	Result     string             `tony:"field=result,omitzero"`
	Err        string             `tony:"field=err,omitzero"`
	Pending    []task.PendingGate `tony:"field=pending,omitzero"`
	Refused    map[string]int     `tony:"field=refused,omitzero"`
	// Agent is who ran it and with what, its name and settings as the runner resolved them, or
	// as the spawn asked before the runner reported; nil for a script (Script). Its annotations
	// are the runner's plumbing and never here.
	Agent  *runner.Agent `tony:"field=agent,omitzero"`
	Script bool          `tony:"field=script,omitzero"`
	// Autonomy is the task's autonomy as it stands, verse's part of its asks with it, and AutonomyBy
	// who last replaced it while it ran: the record's, so a finished task shows the autonomy it
	// ended under.
	Autonomy   task.Autonomy `tony:"field=autonomy,omitzero"`
	AutonomyBy string        `tony:"field=autonomyBy,omitzero"`
}

// TaskFrom is the task as verse reports it, from what a runner reports of it.
func TaskFrom(snap task.Snapshot) Task {
	t := Task{
		ID: snap.ID, Prompt: snap.Prompt, Status: snap.Status,
		Cursor: snap.Cursor, Occurrence: snap.Occurrence, Started: snap.Started, Ended: snap.Ended,
		Result: snap.Result, Err: snap.Err, Pending: snap.Pending, Refused: snap.Refused,
	}
	switch snap.Agent {
	case "script":
		t.Script = true
	case "":
	default:
		t.Agent = &runner.Agent{Name: snap.Agent, Settings: snap.Settings}
	}
	return t
}

// Who is who ran the task, in a word, for a listing: `script`, the agent's name, or `agent` for
// the runner's default not yet resolved.
func (t Task) Who() string {
	switch {
	case t.Script:
		return "script"
	case t.Agent != nil && t.Agent.Name != "":
		return t.Agent.Name
	}
	return "agent"
}

// Spawned is what a spawn answers with: the id to address the new task by.
//
//tony:schemagen=verse-spawned,notag
type Spawned struct {
	ID string `tony:"field=id"`
}

// Say re-prompts a running task with a new instruction
// (`POST /tasks/{id}/say`).
//
//tony:schemagen=verse-say,notag
type Say struct {
	Instruction string `tony:"field=instruction"`
	By          string `tony:"field=by,omitzero"` // attribution; empty is an unattributed caller
}

// SetAutonomy replaces a running task's autonomy, applied to its later steps and not to a
// question already waiting (`PUT /tasks/{id}/autonomy`). The runner carries the rows whole;
// verse keeps what it reads of them (what silence settles on, the labels).
//
//tony:schemagen=verse-set-autonomy,notag
type SetAutonomy struct {
	Autonomy task.Autonomy `tony:"field=autonomy,omitzero"`
	By       string        `tony:"field=by,omitzero"` // who set it, for the record
}

// FlowSelect names the work a `verse flow` verb reaches, and narrows what `GET /flows` lists (as
// query parameters there). A selection is a TASK — that run only — or a CHARTER and a SUBJECT,
// the coordinates a live task carries and a suspension names; a task combines with neither.
//
// Charter is `<address>[:<rule>]`: an installed charter's address, and one rule of it after the
// colon. Baseline's charter has no address, so its rule is `:<rule>` and all its rules `:`.
// Recursive names the charter at the address and every charter beneath it. Subject is an address
// with `*` open in any segment. Either may be left out beside the other, and is then `*`; an act
// naming neither is refused, and only the listing reads a selection of nothing as everything.
//
// Why is what a suspension is for; Text is what a say says; By is who says it.
//
//tony:schemagen=verse-flow-select,notag
type FlowSelect struct {
	Task      string `tony:"field=task,omitzero"`
	Charter   string `tony:"field=charter,omitzero"`
	Subject   string `tony:"field=subject,omitzero"`
	Recursive bool   `tony:"field=recursive,omitzero"`
	Why       string `tony:"field=why,omitzero"`
	Text      string `tony:"field=text,omitzero"`
	By        string `tony:"field=by,omitzero"`
}

// Flow row kinds.
const (
	FlowRowTask       = "task"
	FlowRowSuspension = "suspension"
)

// FlowRow is one row of `GET /flows`, and what `POST /flow/suspend` and `/flow/resume` answer: a
// live task, or a suspension. A suspension is usually holding work that is not running yet, so it
// is a row of its own rather than a field of a task.
//
// Charter, Recursive, Subject and Task are the selection that names the row, so a row printed as
// flags is the act that reaches it again.
//
//tony:schemagen=verse-flow-row,notag
type FlowRow struct {
	// Kind is task | suspension.
	Kind string `tony:"field=kind"`
	// Task is a live task's id.
	Task string `tony:"field=task,omitzero"`
	// ID is a suspension's id.
	ID string `tony:"field=id,omitzero"`
	// Charter is a task's charter and the rule that spawned it, or a suspension's; empty for a
	// task run by hand, which no rule spawned.
	Charter   string `tony:"field=charter,omitzero"`
	Recursive bool   `tony:"field=recursive,omitzero"`
	Subject   string `tony:"field=subject,omitzero"`
	// Scope is the scope a suspension was made in.
	Scope string `tony:"field=scope,omitzero"`
	// HeldBy is who holds a live task at its boundary: `operator` for `flow suspend --task`, and
	// the suspensions that paused it.
	HeldBy []string `tony:"field=heldBy,omitzero"`
	// Held is every holder of a suspension, by principal, in order.
	Held []SuspensionHolder `tony:"field=held,omitzero"`
	// Still is what else covers everything a lifted suspension did — on a resume, the suspensions
	// the lifted pair is still held by.
	Still []string `tony:"field=still,omitzero"`
	// Holding is what a suspension is holding now, across the engines of its scope.
	Holding *SuspensionHolding `tony:"field=holding,omitzero"`
	// Covers is whether a suspension names anything installed: false for one whose charter or
	// rule is gone — renamed, moved, removed — which holds nothing and fails open.
	Covers *bool `tony:"field=covers,omitzero"`
}

// SuspensionHolding is what a suspension holds now: tasks it paused, questions it withdrew, and
// launches it parked.
//
//tony:schemagen=verse-suspension-holding,notag
type SuspensionHolding struct {
	Tasks     int `tony:"field=tasks,omitzero"`
	Questions int `tony:"field=questions,omitzero"`
	Launches  int `tony:"field=launches,omitzero"`
}

// SuspensionHolder is one party's hold on a suspension.
//
//tony:schemagen=verse-suspension-holder,notag
type SuspensionHolder struct {
	By    string `tony:"field=by"`
	Why   string `tony:"field=why"`
	Since string `tony:"field=since"`
	// Occurrence is the store revision the hold was written at; Moved is how far the store's
	// head has moved since, which is how much the suspended work has not seen.
	Occurrence int64 `tony:"field=occurrence,omitzero"`
	Moved      int64 `tony:"field=moved,omitzero"`
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
	// a route that answers with plain strings says `string`, and there is nothing further to name.
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

// Steered is the result of steering live work: say, or cancel.
//
// ONE type with a verb field rather than a type per verb, so the answer describes itself: piped,
// logged, or read back later it says what was done without reference to the route that did it,
// and a key never does double duty as the name of the act and the count of it.
//
//tony:schemagen=verse-steered,notag
type Steered struct {
	// Task, Charter and Subject are the selection that was steered.
	Task    string `tony:"field=task,omitzero"`
	Charter string `tony:"field=charter,omitzero"`
	Subject string `tony:"field=subject,omitzero"`
	// Verb is what was done: say | cancel.
	Verb string `tony:"field=verb"`
	// Affected is how many live tasks it reached. Zero is a normal answer — a selection that
	// names no running work is not an error, it is a flow that has finished.
	Affected int `tony:"field=affected"`
}

// FlowCount is how much work is live: tasks, and the distinct subjects they are about.
//
//tony:schemagen=verse-flow-count,notag
type FlowCount struct {
	Tasks    int `tony:"field=tasks"`
	Subjects int `tony:"field=subjects"`
}

// SuspensionCount is what a scope's suspensions hold now: how many are in force, the live tasks
// they hold and the distinct subjects those are about, the questions they have withdrawn and the
// launches they have parked. Each is counted once however many suspensions cover it.
//
//tony:schemagen=verse-suspension-count,notag
type SuspensionCount struct {
	Count     int `tony:"field=count"`
	Tasks     int `tony:"field=tasks"`
	Subjects  int `tony:"field=subjects"`
	Questions int `tony:"field=questions"`
	Launches  int `tony:"field=launches"`
}

// Status is what GET /status answers: what this verse is, what it is running, and where to
// reach the pieces around it.
//
// The counters are the cheap health read; the three optional blocks are discovery. `docd` is
// how a store client beside this verse finds docd without guessing the ephemeral port `up`
// chose, `listen` publishes this verse's own addresses, and `llm` says whether agent creds
// are configured ON THIS SERVER — the common gotcha, since the runtime reads its own
// environment rather than the client's.
//
//tony:schemagen=verse-status,notag
type Status struct {
	StoreRev int64     `tony:"field=storeRev"`
	Flows    FlowCount `tony:"field=flows"`
	Gates    int       `tony:"field=gates"`
	// Suspensions is what is suspended in the scope asked about, absent when nothing is.
	Suspensions *SuspensionCount `tony:"field=suspensions,omitzero"`
	Tasks       int              `tony:"field=tasks"`
	Fired       int              `tony:"field=fired"`
	// Firings is what has gone wrong at the rules' firings since this process started, absent
	// when nothing has. `verse charter` says which rule, and with what.
	Firings *Firings `tony:"field=firings,omitzero"`

	// WHAT IS INSTALLED, as against what is happening. The counts above move on their own;
	// these three are things somebody put here, and a verse that has lost them looks healthy by
	// every other line — which is exactly the state a wiped store comes back in.
	//
	// Runners are the refs a spawn may name (`verse.runners.code`), which is a different question
	// from `runner` below — that one is how THIS verse runs a task when a spawn names nobody.
	// IOs are the refs of what reaches the world for this verse (`verse.iods.slack`) — the one
	// line here that says anything is arriving at all, since a verse whose iods are gone goes on
	// reporting a fine store rev and an idle charter.
	//
	// The charter used to be here too, as `rules` (a count) and `strata` (names in level
	// order). Both were BASELINE's alone while every count beside them is the whole verse's, so
	// a verse whose program is packaged reported `rules: 0` and no levels — the scoping was the
	// bug, not the zero. `charter` below carries both, per node, where the shape says whose.
	//
	// All three are refs rather than records: status is an orientation, and `verse charter`,
	// `verse runners` and `verse iod` are the surfaces that answer what one IS. An io's health —
	// when its last pass completed, whether it is failing, what its out half delivered — is on
	// `verse iod`.
	Runners []string `tony:"field=runners,omitzero"`
	IOs     []string `tony:"field=iods,omitzero"`
	// Charter is the charter as a CENSUS: how much is installed, and where.
	//
	//	charter:
	//	  rules: 2          # baseline's own, at the root — the charter with no address
	//	  strata: 1
	//	  children:
	//	    verse-dev:      # a PLACE: nothing installed here, so no counts
	//	      children:
	//	        drift: {rules: 4, strata: 2}
	//	        merge: {rules: 1}
	//
	// SAME SHAPE AS `verse charter`, counted instead of enumerated: `rules`, `strata` and
	// `children` mean what they mean there, and a node with no charter carries only `children`
	// — so `rules: 0` appears where a charter really holds none, and nowhere else. Status is a
	// census of numbers and the charter surface is what answers what a rule IS.
	//
	// `arming`, when present, names rules counted in `rules` whose watch is still being opened:
	// restored from the charter over a store that did not answer, in the charter and firing on
	// nothing until it does. Absent on a verse that is up.
	//
	// A TREE rather than a flat list of addresses, because addresses SHARE PREFIXES and a
	// status line is read at a glance: `verse-dev.drift` and `verse-dev.merge` are two charters
	// in one place, and a list said `verse-dev` twice and the shape not at all.
	Charter *ir.Node `tony:"field=charter,omitzero"`

	Runner *RunnerStatus `tony:"field=runner,omitzero"`
	Docd   *Docd         `tony:"field=docd,omitzero"`
	LLM    *LLM          `tony:"field=llm,omitzero"`
	Listen *Listen       `tony:"field=listen,omitzero"`
	// Perms is what the permission check has refused since this process started: a count
	// that climbs is a flow somebody is being refused, which is either the autonomy's intent or
	// a row the seed is missing.
	Perms *Perms `tony:"field=perms,omitzero"`
	// Restoring is the phases of the server's start still being asked again, by name: each
	// reads a slice of the store, the store did not answer, and what it would have restored —
	// the sources, the docket's deadlines, a charter — is not running yet. A read that did
	// not happen is asked again until it does; this is the sign that one is. Absent on a verse
	// that is up.
	Restoring []string `tony:"field=restoring,omitzero"`
}

// Firings counts what has gone wrong at the rules' firings, over every charter installed.
//
// A count that climbs is a rule doing less than it says. Which rule, where in it, about what
// and why are on the rule in `verse charter`, under `trouble`.
//
//tony:schemagen=verse-firings,notag
type Firings struct {
	// Failed is actions that did not do what their rule says: a reference that did not
	// resolve, a patch or a derive that failed, a spawn refused, a pose that did not expand.
	Failed int `tony:"field=failed,omitzero"`
	// Unread is commits a rule could not read, and so did not evaluate.
	Unread int `tony:"field=unread,omitzero"`
}

// Group is a named set of principals (`verse group add`): as a grantee, `group:<name>` admits its
// members. A member is a grantee in the grammar (entity.Grammar): a principal, `<kind>:*`, `*`,
// `group:<name>`, or a place acting.
//
//tony:schemagen=verse-group,notag
type Group struct {
	Name    string   `tony:"field=name"`
	Members []string `tony:"field=members,omitzero"`
}

// Ask is a person asking somebody something (`verse ask`, POST /asks): what is asked, of whom, by
// when, and what follows a yes. The ask is written at `verse.ask.<id>` as its asker's record, with
// the id derived from Name, and the verb poses it (docs/asking.md).
//
// Then is one key naming what follows a yes, on the asker's authority: `{spawn: {…}}`, a task
// written as a charter's spawn is (its prompt or script, agent, environment and autonomy) and
// meaning only what it says; `{answer: {ask, choice, say?, force?}}`, an answer to another ask as
// the asker; or `{ask: {…}}`, another ask the asker raises, whose `due` may be a duration from
// the yes. Absent, the ask is a vote and the answer is all it leaves.
//
//tony:schemagen=verse-ask,notag
type Ask struct {
	Name      string   `tony:"field=name"`
	Question  string   `tony:"field=question"`
	Answerers []string `tony:"field=answerers,omitzero"`
	Due       string   `tony:"field=due,omitzero"`
	// OnSilence is what the ask settles on when Due passes and nobody has answered: `no`, the
	// default, or `yes`, which runs its `then` as an answer would. It needs a Due. An onward ask
	// takes the served ask's.
	OnSilence string         `tony:"field=onSilence,omitzero"`
	Then      entity.Payload `tony:"field=then,omitzero"`
	// Else is what follows a no, as Then is what follows a yes: a no answered, or silence settling
	// on no at Due. It takes what Then does.
	Else  entity.Payload `tony:"field=else,omitzero"`
	About string         `tony:"field=about,omitzero"`
	// Keep is how long the ask stands once settled, as a duration (168h); absent, a week.
	Keep string `tony:"field=keep,omitzero"`
	// Labels are what the ask says about itself for whoever watches asks (`team: payments`).
	Labels map[string]string `tony:"field=labels,omitzero"`
	// Serves makes this an ONWARD ask (9ce3bva0, stage 4): one a resolver raises, made as the
	// resolver, about an ask it serves — the ask's id — for the party named in For, or the only
	// one it serves it for. Its options are the served ask's, its clock fits inside the served
	// ask's, and it takes no `then`: what follows it is the answer it carries back.
	Serves string `tony:"field=serves,omitzero"`
	For    string `tony:"field=for,omitzero"`
}

// Asked is what an ask answers with: the id it was written under, and the edit it stands at.
//
//tony:schemagen=verse-asked,notag
type Asked struct {
	ID   string `tony:"field=id"`
	Edit int    `tony:"field=edit,omitzero"`
}

// ServedAsk is one waiting ask a resolver serves (GET /resolvers/{name}/asks): what it asks, and
// the parties the resolver answers for on it — each principal it answers for, or the address of
// the place.
//
//tony:schemagen=verse-served-ask,notag
type ServedAsk struct {
	ID     string            `tony:"field=id"`
	Edit   int               `tony:"field=edit,omitzero"`
	Pose   string            `tony:"field=pose,omitzero"`
	About  string            `tony:"field=about,omitzero"`
	Labels map[string]string `tony:"field=labels,omitzero"`
	For    []string          `tony:"field=for"`
}

// AskServed is GET /asks/{id}/served: the resolver serving the ask for the caller, empty where
// none does.
//
//tony:schemagen=verse-ask-served,notag
type AskServed struct {
	Resolver string `tony:"field=resolver,omitzero"`
}

// ServedAsks is the listing of them.
//
//tony:schemagen=verse-served-asks,notag
type ServedAsks struct {
	Asks []ServedAsk `tony:"field=asks"`
}

// AskChange is a change to a waiting ask by its asker (`verse ask <id> --due …`, PATCH
// /asks/{id}). A new Due moves its clock and asks nothing again. A new Question, Answerers, Then
// or Else changes what it asks, so it is asked again, as the next edit, and the ballots cast before
// are dropped. Edit is the edit the asker changed, where they name it; a change to an ask that has
// moved on since is refused.
//
//tony:schemagen=verse-ask-change,notag
type AskChange struct {
	Edit      int            `tony:"field=edit,omitzero"`
	Due       string         `tony:"field=due,omitzero"`
	Question  string         `tony:"field=question,omitzero"`
	Answerers []string       `tony:"field=answerers,omitzero"`
	Then      entity.Payload `tony:"field=then,omitzero"`
	Else      entity.Payload `tony:"field=else,omitzero"`
}

// ScheduleRow is one schedule as `verse schedule` lists it: its record's id (the declared name,
// `<address>:<name>` for a package's), its grid, the instant the daemon is armed against, and
// the tick standing under it.
//
//tony:schemagen=verse-schedule-row,notag
type ScheduleRow struct {
	ID    string `tony:"field=id"`
	Every string `tony:"field=every"`
	From  string `tony:"field=from,omitzero"`
	Next  string `tony:"field=next,omitzero"`
	// Tick is the due instant of the standing tick, and Missed how many instants before it
	// went unticked; empty when nothing stands.
	Tick   string `tony:"field=tick,omitzero"`
	Missed int64  `tony:"field=missed,omitzero"`
	// At is the package address the schedule was declared in, empty for baseline; By its
	// installer.
	At string `tony:"field=at,omitzero"`
	By string `tony:"field=by,omitzero"`
}

// ScheduleTick is a backfill (`verse schedule tick`): the past instant to write the tick for.
//
//tony:schemagen=verse-schedule-tick,notag
type ScheduleTick struct {
	Due string `tony:"field=due"`
}

// PermsRecord is a node's perms record as its owner reads it back (`verse perms <path>`): who
// owns the node, what they hold out on it, and what a node created beneath it holds out.
//
//tony:schemagen=verse-perms-record,notag
type PermsRecord struct {
	At          string         `tony:"field=at"`
	Owner       string         `tony:"field=owner,omitzero"`
	Grants      entity.Payload `tony:"field=grants,omitzero"`
	ChildGrants entity.Payload `tony:"field=child-grants,omitzero"`
}

// PermsEdit is what an owner sets on a node's record (`PUT /perms`). A node with no record of
// its own gets one — the place is taken by its owner, holding out what the default above says
// — and then: `child-grants` sets what a node created beneath holds out (`{}` nothing, null
// inherits again), `grants` replaces what the node itself holds out (`[]` nothing), `grant`
// adds one line at the node, beneath it when `below` is set, and `revoke` takes the named
// grantees off the named levels of every line at the node, leaving the rest standing.
//
// AnswerFor is POST /resolvers/{name}/grant and /revoke: whom the resolver answers for. For is
// the place, as a kinded path; absent, it is the caller, at `verse.for.<caller>`.
//
//tony:schemagen=verse-answer-for,notag
type AnswerFor struct {
	For string `tony:"field=for,omitzero"`
}

//tony:schemagen=verse-perms-edit,notag
type PermsEdit struct {
	At          string         `tony:"field=at"`
	ChildGrants entity.Payload `tony:"field=child-grants,omitzero"`
	Grants      entity.Payload `tony:"field=grants,omitzero"`
	Grant       entity.Payload `tony:"field=grant,omitzero"`
	Below       bool           `tony:"field=below,omitzero"`
	Revoke      entity.Payload `tony:"field=revoke,omitzero"`
}

// Perms is the permission check's census: refusals by the level a write or read wanted, checks
// the mirror could not answer, what listings left out, and grantee names the grammar does not
// read.
//
//tony:schemagen=verse-perms,notag
type Perms struct {
	Refused    map[string]int `tony:"field=refused,omitzero"`
	Unreadable int            `tony:"field=unreadable,omitzero"`
	// Hidden is what listings, replays and watches left out for a reader who may not see it —
	// apart from refused, since a listing that leaves something out is answered.
	Hidden int `tony:"field=hidden,omitzero"`
	// Unparsed is how many times a check met a grantee name the grammar does not read
	// (entity.Grammar), which admitted nobody. A stored grant from before the grammar is one;
	// zero is the expected reading.
	Unparsed int `tony:"field=unparsed,omitzero"`
}

// RunnerStatus is how this verse runs tasks: local (in-process) or remote (a runner base
// URL). Resume says what survives a restart.
//
//tony:schemagen=verse-runner-status,notag
type RunnerStatus struct {
	Kind   string `tony:"field=kind"`
	Resume string `tony:"field=resume"`
}

// Docd is the document store's client face: the address store clients connect to.
//
//tony:schemagen=verse-docd,notag
type Docd struct {
	Client string `tony:"field=client,omitzero"`
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

// Listen is this verse's own addresses: the operator API, and the admin face.
//
//tony:schemagen=verse-listen,notag
type Listen struct {
	API string `tony:"field=api,omitzero"`
	// Admin is the pprof/stacks face. It is reported because its address is DERIVED from
	// the api one rather than fixed, and an operator reaching for it is mid-incident.
	Admin string `tony:"field=admin,omitzero"`
}

// CharterCheck is what `POST /charter/check` answers: whether a tree of charters would install
// at an address over what runs there, and what would change, with nothing installed. It is a
// question, beside the install and never part of it (versed/chartercheck.go).
//
//tony:schemagen=verse-charter-check,notag
type CharterCheck struct {
	// At is the address checked, which the tree would be installed at with a prune.
	At string `tony:"field=at"`
	// Verdict is `installs` or `refused`.
	Verdict string `tony:"field=verdict"`
	// Refusal is what the install would be refused with, where it would be.
	Refusal string `tony:"field=refusal,omitzero"`
	// Packages are the packages the tree holds, each with what would change in it.
	Packages []CharterCheckPackage `tony:"field=packages,omitzero"`
	// Pruned are the packages standing under At that the tree leaves out, which the install
	// would remove.
	Pruned []string `tony:"field=pruned,omitzero"`
}

// CharterCheckPackage is one package of a checked tree: its address and what would change there,
// each entry `rules.<name>` or `strata.<name>`.
//
//tony:schemagen=verse-charter-check-package,notag
type CharterCheckPackage struct {
	At      string   `tony:"field=at"`
	Added   []string `tony:"field=added,omitzero"`
	Changed []string `tony:"field=changed,omitzero"`
	Removed []string `tony:"field=removed,omitzero"`
	// Held are the rules that would install waiting for a stratum the package does not declare.
	Held []string `tony:"field=held,omitzero"`
}

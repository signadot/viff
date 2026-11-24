package api

// FlowSelect names the work a `viff flow` verb reaches, and narrows what `GET /flows` lists (as
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
//tony:schemagen=viff-flow-select,notag
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
//tony:schemagen=viff-flow-row,notag
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
//tony:schemagen=viff-suspension-holding,notag
type SuspensionHolding struct {
	Tasks     int `tony:"field=tasks,omitzero"`
	Questions int `tony:"field=questions,omitzero"`
	Launches  int `tony:"field=launches,omitzero"`
}

// SuspensionHolder is one party's hold on a suspension.
//
//tony:schemagen=viff-suspension-holder,notag
type SuspensionHolder struct {
	By    string `tony:"field=by"`
	Why   string `tony:"field=why"`
	Since string `tony:"field=since"`
	// Occurrence is the store revision the hold was written at; Moved is how far the store's
	// head has moved since, which is how much the suspended work has not seen.
	Occurrence int64 `tony:"field=occurrence,omitzero"`
	Moved      int64 `tony:"field=moved,omitzero"`
}

// Steered is the result of steering live work: say, or cancel.
//
// ONE type with a verb field rather than a type per verb, so the answer describes itself: piped,
// logged, or read back later it says what was done without reference to the route that did it,
// and a key never does double duty as the name of the act and the count of it.
//
//tony:schemagen=viff-steered,notag
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
//tony:schemagen=viff-flow-count,notag
type FlowCount struct {
	Tasks    int `tony:"field=tasks"`
	Subjects int `tony:"field=subjects"`
}

// SuspensionCount is what a scope's suspensions hold now: how many are in force, the live tasks
// they hold and the distinct subjects those are about, the questions they have withdrawn and the
// launches they have parked. Each is counted once however many suspensions cover it.
//
//tony:schemagen=viff-suspension-count,notag
type SuspensionCount struct {
	Count     int `tony:"field=count"`
	Tasks     int `tony:"field=tasks"`
	Subjects  int `tony:"field=subjects"`
	Questions int `tony:"field=questions"`
	Launches  int `tony:"field=launches"`
}

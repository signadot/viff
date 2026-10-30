package api

import (
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

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
	ID         string         `tony:"field=id"`
	Prompt     string         `tony:"field=prompt,omitzero"`
	Status     task.Status    `tony:"field=status"`
	Runner     string         `tony:"field=runner,omitzero"` // where it runs or ran: the runner's url
	RunnerName string         `tony:"field=runnerName,omitzero"`
	Occurrence int64          `tony:"field=occurrence,omitzero"`
	Started    string         `tony:"field=started,omitzero"`
	Ended      string         `tony:"field=ended,omitzero"`
	Result     string         `tony:"field=result,omitzero"`
	Err        string         `tony:"field=err,omitzero"`
	Refused    map[string]int `tony:"field=refused,omitzero"`
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

// Cancel ends a task (`POST /tasks/{id}/cancel`), wherever it runs. Reason is what the task's
// charters see on `onCancelled`; By is who cancelled it, which is whoever the request is made as,
// so a By naming anybody else is refused. A cancel with no body is a cancel all the same.
//
//tony:schemagen=verse-cancel,notag
type Cancel struct {
	Reason string `tony:"field=reason,omitzero"`
	By     string `tony:"field=by,omitzero"`
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

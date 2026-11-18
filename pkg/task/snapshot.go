package task

import "github.com/signadot/tony-format/go-tony/ir"

// Snapshot is a task as its runner reports it: the answer to `GET /tasks` (a list of them)
// and `GET /tasks/{id}`. It is the runtime's view, and only what the runner knows; verse's own
// view of a task (api.Task) adds what verse knows, and the durable record (the mirror's
// task:instance:<id>) is a separate index that adds read-plane previews.
//
//tony:schemagen=verse-task-snapshot,notag
type Snapshot struct {
	ID     string `tony:"field=id"`
	Prompt string `tony:"field=prompt,omitzero"`
	// Agent is who ran it: the agent's name as its runner resolved it (the runner's default where
	// the launch named none), or `script`.
	Agent  string `tony:"field=agent,omitzero"`
	Status Status `tony:"field=status"`
	Cursor int64  `tony:"field=cursor,omitzero"` // resume the event stream with ?since=<cursor>
	// Occurrence is which RUN this is: the store revision it is about. An id names the work
	// and every run of it reuses the id, so this is what tells a live process apart from the
	// record of a previous one — and what lets a reader of the runtime answer that without a
	// second read against the store.
	Occurrence int64 `tony:"field=occurrence,omitzero"`
	// Started and Ended are RFC3339 instants, and they are the RECORD's to say rather than a
	// runtime's: a task outlives the process that ran it, and "when did this start" is a
	// question asked most often about one that is over. Empty while unknown — a runner that
	// reports neither is not lying about a task it is genuinely hosting, it simply has not
	// been asked to keep clocks.
	Started string `tony:"field=started,omitzero"`
	Ended   string `tony:"field=ended,omitzero"`
	Result  string `tony:"field=result,omitzero"` // the driver's final answer, once completed
	Err     string `tony:"field=err,omitzero"`    // failure detail (e.g. "driver error: …")
	// Orphaned says a failed task ended because verse and its runner lost each other, and not
	// for a reason of its own.
	Orphaned bool          `tony:"field=orphaned,omitzero"`
	Pending  []PendingGate `tony:"field=pending,omitzero"` // open gates awaiting resolution
	// Refused is the connections the task's proxy refused, by destination (`host:port`), and how
	// often: the runner's account, since the proxy is the runner's and the task never sees the
	// decision, only the 403. A refusal is where a run tried to reach what nobody gave it.
	Refused map[string]int `tony:"field=refused,omitzero"`
	// Settings is what the agent ran with, by setting (`model`, `effort`): the launch's, and the
	// runner's defaults where it named none, so the record says which model ran (beside Agent).
	Settings map[string]string `tony:"field=settings,omitzero"`
}

// PendingGate is one open gate on a task, in Snapshot's `pending`: a raised GateQuestion awaiting a
// resolution, in the snapshot's shape, with the step it is about when it is about one.
//
//tony:schemagen=verse-pending-gate,notag
type PendingGate struct {
	ID     string   `tony:"field=id"`
	Origin string   `tony:"field=origin,omitzero"`
	Gate   string   `tony:"field=gate,omitzero"`
	Tool   string   `tony:"field=tool,omitzero"`
	Action string   `tony:"field=action,omitzero"` // the action in one line, see ToolUse.Describe
	Step   string   `tony:"field=step,omitzero"`
	Fields *ir.Node `tony:"field=fields,omitzero"`
	Reason string   `tony:"field=reason,omitzero"`
}

// Status is a task's lifecycle state, in Snapshot's `status` and an EventStatus's
// `text` — one vocabulary across every
// runner (local, remote) and the entity mirror, replacing the
// per-layer strings that used to disagree ("error" vs "failed" vs "driver
// error"). The completion pattern keys off exactly one value (Done), so the
// terminal states stay distinct rather than collapsing into one end state.
type Status string

const (
	// Running: an executor is working the task.
	Running Status = "running"
	// Paused: a pause is in effect (Task.Pause). The task holds at its next boundary, so an
	// action already running finishes first; nothing new starts until it is resumed. Not an end
	// state.
	Paused Status = "paused"
	// Done: finished its work successfully — the ONLY status a completion trigger
	// (matching task:instance status=done) or a spawn's onDone hook reacts to.
	Done Status = "done"
	// Cancelled: stopped by an operator (destructive), not by finishing.
	Cancelled Status = "cancelled"
	// Failed: ran but did not succeed — a driver/agent error or an executor-reported
	// failure.
	Failed Status = "failed"
)

// Terminal reports whether a status is an end state (no further work). Running and Paused are
// the live states; everything else is terminal.
func (s Status) Terminal() bool {
	switch s {
	case Done, Cancelled, Failed:
		return true
	}
	return false
}

// TerminalStatus reports whether a raw status string is a terminal end state —
// for callers holding an untyped status (e.g. read off an entity payload).
func TerminalStatus(s string) bool { return Status(s).Terminal() }

// MarshalText / UnmarshalText spell Status as its text — see Verb's, which explains
// why a named string type needs them at all.
func (s Status) MarshalText() ([]byte, error) { return []byte(s), nil }

func (s *Status) UnmarshalText(b []byte) error { *s = Status(b); return nil }

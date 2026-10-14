package task

// Snapshot is a task as its runner reports it: the answer to `GET /tasks` (a list of them)
// and `GET /tasks/{id}`. It is the runtime's view, and only what the runner knows; verse's own
// view of a task (api.Task) adds what verse knows, and the durable record (the mirror's
// task:instance:<id>) is a separate index that adds read-plane previews.
//
//tony:schemagen=verse-task-snapshot,notag
type Snapshot struct {
	ID     string `tony:"field=id"`
	Prompt string `tony:"field=prompt,omitzero"`
	Driver string `tony:"field=driver,omitzero"`
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
	Started string        `tony:"field=started,omitzero"`
	Ended   string        `tony:"field=ended,omitzero"`
	Result  string        `tony:"field=result,omitzero"`  // the driver's final answer, once completed
	Err     string        `tony:"field=err,omitzero"`     // failure detail (e.g. "driver error: …")
	Pending []PendingGate `tony:"field=pending,omitzero"` // open gates awaiting resolution
}

// PendingGate is one open gate on a task, in Snapshot's `pending`: a raised GateQuestion awaiting a
// resolution, in the snapshot's shape. The local runtime carries the full question; a
// remote runner fills what its worker reported. Origin and Mode may be empty there — a
// worker raises a gate, it does not decide who answers it, so those are versed's to fill
// upstream of the snapshot.
//
//tony:schemagen=verse-pending-gate,notag
type PendingGate struct {
	ID     string `tony:"field=id"`
	Origin string `tony:"field=origin,omitzero"`
	Gate   string `tony:"field=gate,omitzero"`
	Tool   string `tony:"field=tool,omitzero"`
	Action string `tony:"field=action,omitzero"` // the action in one line, see ToolUse.Describe
	Mode   string `tony:"field=mode,omitzero"`
	Reason string `tony:"field=reason,omitzero"`
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
	// Done: finished its work successfully — the ONLY status a completion trigger
	// (matching task:instance status=done) or a spawn's onDone hook reacts to.
	Done Status = "done"
	// Cancelled: stopped by an operator (destructive), not by finishing.
	Cancelled Status = "cancelled"
	// Failed: ran but did not succeed — a driver/agent error or an executor-reported
	// failure.
	Failed Status = "failed"
	// Orphaned: its runtime died (e.g. a monolith restart), so the last durable state
	// is retired rather than left claiming to run. Terminal, but the work did NOT
	// complete — a completion trigger deliberately does not match it.
	Orphaned Status = "orphaned"
)

// Terminal reports whether a status is an end state (no further work). Running is the
// one live state; everything else is terminal.
func (s Status) Terminal() bool {
	switch s {
	case Done, Cancelled, Failed, Orphaned:
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

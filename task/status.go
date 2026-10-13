package task

// Status is a hosted task's lifecycle state — one vocabulary across every
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

// MarshalText / UnmarshalText spell Status as its text — see Verb's, which explains why a
// named string type needs them at all.
func (s Status) MarshalText() ([]byte, error) { return []byte(s), nil }

func (s *Status) UnmarshalText(b []byte) error { *s = Status(b); return nil }

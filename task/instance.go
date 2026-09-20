package task

// Instance is the snapshot of a hosted task as the task API returns it
// (GET /tasks and /tasks/{id}) — one typed shape across the local
// runtime, a remote taskd, and a dial-in broker, so `verse task list`/`get`
// is uniform regardless of where the task runs. It is the runtime view; the
// durable entity projection (the mirror's task:instance:<id>) is a separate
// index that adds read-plane previews.
//
//tony:schemagen=verse-instance,notag
type Instance struct {
	ID     string `tony:"field=id"`
	Prompt string `tony:"field=prompt,omitzero"`
	Driver string `tony:"field=driver,omitzero"`
	Status Status `tony:"field=status"`
	Runner string `tony:"field=runner,omitzero"` // which runner hosts it (local|<ref>|broker)
	// RunnerName is the installed runner a spawn NAMED (`verse.runners.<name>`), when it
	// named one. Empty for a spawn that took the verse's default, which Runner reads as
	// `local`. It is what `verse ps --runner <name>` selects on, and it keeps answering for a
	// runner that has since been removed — which is when an operator most wants it.
	RunnerName string `tony:"field=runnerName,omitzero"`
	Cursor     int64  `tony:"field=cursor,omitzero"` // resume the event stream with ?since=<cursor>
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
	DialURL string        `tony:"field=dialURL,omitzero"` // broker: where a worker dials in to execute
	Pending []PendingGate `tony:"field=pending,omitzero"` // open gates awaiting resolution
}

// PendingGate is one open gate on a task: a raised GateQuestion awaiting a
// resolution, in the snapshot's shape. The local runtime carries the full question; a
// broker fills what the worker reported, which is id/gate plus Tool and the --reason on
// Text. Origin and Mode stay empty there — a worker raises a gate, it does not decide who
// answers it, so those are versed's to fill upstream of the snapshot.
//
//tony:schemagen=verse-pending-gate,notag
type PendingGate struct {
	ID     string `tony:"field=id"`
	Origin string `tony:"field=origin,omitzero"`
	Gate   string `tony:"field=gate,omitzero"`
	Tool   string `tony:"field=tool,omitzero"`
	Mode   string `tony:"field=mode,omitzero"`
	Reason string `tony:"field=reason,omitzero"`
}

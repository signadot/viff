package process

// Instance is the snapshot of a hosted process as the process API returns it
// (GET /processes and /processes/{id}) — one typed shape across the local
// runtime, a remote processd, and a dial-in broker, so `verse process list`/`get`
// is uniform regardless of where the process runs. It is the runtime view; the
// durable entity projection (the mirror's process:instance:<id>) is a separate
// index that adds read-plane previews.
//
//tony:schemagen=verse-instance,notag
type Instance struct {
	ID      string        `tony:"field=id"`
	Task    string        `tony:"field=task,omitzero"`
	Driver  string        `tony:"field=driver,omitzero"`
	Status  Status        `tony:"field=status"`
	Runner  string        `tony:"field=runner,omitzero"`  // which runner hosts it (local|<ref>|broker)
	Cursor  int64         `tony:"field=cursor,omitzero"`  // resume the event stream with ?since=<cursor>
	Result  string        `tony:"field=result,omitzero"`  // the driver's final answer, once completed
	Err     string        `tony:"field=err,omitzero"`     // failure detail (e.g. "driver error: …")
	DialURL string        `tony:"field=dialURL,omitzero"` // broker: where a worker dials in to execute
	Pending []PendingGate `tony:"field=pending,omitzero"` // open gates awaiting resolution
}

// PendingGate is one open gate on a process: a raised GateQuestion awaiting a
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

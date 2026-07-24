package process

// Instance is the snapshot of a hosted process as the process API returns it
// (GET /processes and /processes/{id}) — one typed shape across the local
// runtime, a remote processd, and a dial-in broker, so `verse process list`/`get`
// is uniform regardless of where the process runs. It is the runtime view; the
// durable entity projection (the mirror's process:instance:<id>) is a separate
// index that adds read-plane previews.
type Instance struct {
	ID      string        `json:"id"`
	Task    string        `json:"task,omitempty"`
	Driver  string        `json:"driver,omitempty"`
	Status  Status        `json:"status"`
	Policy  string        `json:"policy,omitempty"`  // which controlPolicy hosts it (local|<ref>|broker)
	Cursor  int64         `json:"cursor,omitempty"`  // resume the event stream with ?since=<cursor>
	Result  string        `json:"result,omitempty"`  // the driver's final answer, once completed
	Err     string        `json:"err,omitempty"`     // failure detail (e.g. "driver error: …")
	DialURL string        `json:"dialURL,omitempty"` // broker: where a worker dials in to execute
	Pending []PendingGate `json:"pending,omitempty"` // open gates awaiting resolution
}

// PendingGate is one open gate on a process: a raised GateQuestion awaiting a
// resolution, in the snapshot's shape. A broker fills only what the worker
// reported (id + gate); the local runtime carries the full question.
type PendingGate struct {
	ID     string `json:"id"`
	Origin string `json:"origin,omitempty"`
	Gate   string `json:"gate,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Reason string `json:"reason,omitempty"`
}

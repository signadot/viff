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
	ID      string        `json:"id" tony:"field=id"`
	Task    string        `json:"task,omitempty" tony:"field=task,omitzero"`
	Driver  string        `json:"driver,omitempty" tony:"field=driver,omitzero"`
	Status  Status        `json:"status" tony:"field=status"`
	Policy  string        `json:"policy,omitempty" tony:"field=policy,omitzero"`   // which controlPolicy hosts it (local|<ref>|broker)
	Cursor  int64         `json:"cursor,omitempty" tony:"field=cursor,omitzero"`   // resume the event stream with ?since=<cursor>
	Result  string        `json:"result,omitempty" tony:"field=result,omitzero"`   // the driver's final answer, once completed
	Err     string        `json:"err,omitempty" tony:"field=err,omitzero"`         // failure detail (e.g. "driver error: …")
	DialURL string        `json:"dialURL,omitempty" tony:"field=dialURL,omitzero"` // broker: where a worker dials in to execute
	Pending []PendingGate `json:"pending,omitempty" tony:"field=pending,omitzero"` // open gates awaiting resolution
}

// PendingGate is one open gate on a process: a raised GateQuestion awaiting a
// resolution, in the snapshot's shape. A broker fills only what the worker
// reported (id + gate); the local runtime carries the full question.
//
//tony:schemagen=verse-pending-gate,notag
type PendingGate struct {
	ID     string `json:"id" tony:"field=id"`
	Origin string `json:"origin,omitempty" tony:"field=origin,omitzero"`
	Gate   string `json:"gate,omitempty" tony:"field=gate,omitzero"`
	Tool   string `json:"tool,omitempty" tony:"field=tool,omitzero"`
	Mode   string `json:"mode,omitempty" tony:"field=mode,omitzero"`
	Reason string `json:"reason,omitempty" tony:"field=reason,omitzero"`
}

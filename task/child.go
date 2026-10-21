package task

// NewChild constructs (does not run) a sub-agent's Task for a delegation. It runs under the
// parent's autonomy, as it stands from moment to moment: its supervisor shares the parent's autonomy
// and resolver, so a change to the autonomy reaches it too. The caller supplies its Driver and
// Executor.
func (p *Task) NewChild(id string, req *CallAgentRequest, d Driver, e Executor) *Task {
	child := NewTask(id, p.sup.child(), d, e)
	child.pauseGate = &pauseGate{parent: p.pauseGate} // link into the parent's pause chain
	return child
}

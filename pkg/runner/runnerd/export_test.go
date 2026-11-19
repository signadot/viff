package runnerd

import "time"

// SetLingerAfterEnd shortens how long a finished task's process is kept, for a test.
func SetLingerAfterEnd(d time.Duration) (restore func()) {
	old := lingerAfterEnd
	lingerAfterEnd = d
	return func() { lingerAfterEnd = old }
}

// Count returns the number of hosted tasks (live and completed-but-unreaped).
func (d *InProcessHandler) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.tasks)
}

// SetLease gives every task launched from here on a lease of d, whatever its launch says, for a
// test: a launch may name none under runner.MinLease.
func (d *InProcessHandler) SetLease(lease time.Duration) { d.lease = lease }

// SetLease is InProcessHandler.SetLease for a SubprocessHandler.
func (h *SubprocessHandler) SetLease(lease time.Duration) { h.lease = lease }

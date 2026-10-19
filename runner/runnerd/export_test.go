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

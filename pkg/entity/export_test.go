package entity

import "time"

// SetProgressBudget shortens how long a read waits for the perms watches to answer, for a test
// that makes them never answer; the returned func puts it back.
func SetProgressBudget(d time.Duration) func() {
	was := progressBudget
	progressBudget = d
	return func() { progressBudget = was }
}

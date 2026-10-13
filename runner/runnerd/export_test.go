package runnerd

import "time"

// SetLingerAfterEnd shortens how long a finished task's process is kept, for a test.
func SetLingerAfterEnd(d time.Duration) (restore func()) {
	old := lingerAfterEnd
	lingerAfterEnd = d
	return func() { lingerAfterEnd = old }
}

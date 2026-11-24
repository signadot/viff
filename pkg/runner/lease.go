package runner

import "time"

// A TASK'S LEASE is how long it runs with nobody asking after it. Whoever launched a task asks
// for its events again and again while it runs, and each subscribe the runner accepts renews the
// lease. A task whose lease runs out has lost whoever launched it, and its runner ends it. The
// lease is the launch's (Launch.Lease).
const (
	// DefaultLease is the lease of a launch that names none.
	DefaultLease = 4 * time.Minute
	// MinLease is the shortest lease a launch may name. Under it, the time a runner takes to stop
	// a task and a few missed subscribes are no longer small beside the lease.
	MinLease = time.Minute
)

// LeaseOf is a lease as a launch or a task's record writes it, as a length of time:
// DefaultLease where it says none, or nothing that reads as one.
func LeaseOf(lease string) time.Duration {
	if d, err := time.ParseDuration(lease); err == nil && d > 0 {
		return d
	}
	return DefaultLease
}

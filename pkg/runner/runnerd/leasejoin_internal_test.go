package runnerd

import (
	"testing"
	"time"

	"github.com/signadot/verse/pkg/runner"
)

// A launch that joins a task sets its lease to the one it names, which is the one its launcher
// waits by, and a lease that has run out is not set again.
func TestAJoiningLaunchTakesTheLeaseItNames(t *testing.T) {
	l := newLease(&runner.Launch{Lease: "1m"}, 0)
	l.join(&runner.Launch{Lease: "2h"}, 0)
	if l.length != 2*time.Hour {
		t.Fatalf("lease = %s after a launch naming 2h joined, want 2h", l.length)
	}
	l.over = true
	l.join(&runner.Launch{Lease: "5m"}, 0)
	if l.length != 2*time.Hour {
		t.Fatalf("a lease that ran out was set again, to %s", l.length)
	}
}

// A joining launch that names a shorter lease shortens it from then: the lease runs out by the
// new length, and not by the one it was counting.
func TestAJoiningLaunchThatShortensTheLeaseIsKeptByIt(t *testing.T) {
	l := newLease(&runner.Launch{}, time.Hour)
	ended, expired := make(chan struct{}), make(chan struct{})
	defer close(ended)
	go l.keep(ended, func() { close(expired) })
	time.Sleep(50 * time.Millisecond) // keep is counting the hour
	l.join(&runner.Launch{}, 50*time.Millisecond)
	select {
	case <-expired:
	case <-time.After(5 * time.Second):
		t.Fatal("the lease kept counting the hour it was launched with")
	}
}

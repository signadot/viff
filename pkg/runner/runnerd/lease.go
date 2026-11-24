package runnerd

import (
	"strings"
	"sync"
	"time"

	"github.com/signadot/verse/pkg/runner"
)

// A RUNNER KEEPS EACH TASK'S LEASE (runner.DefaultLease). The lease counts from the task's
// launch, and again from each subscribe to its events the runner accepts. A task whose lease
// runs out has lost whoever launched it: nobody is reading what it does, and nobody will be
// told how it ends. The runner stops it and ends it `failed` and orphaned, with noReader for
// its error. A subscribe that comes after that renews nothing, and is answered with that end.
//
// The handler a launcher talks to keeps the lease. A task's own process (ServeTask) keeps
// none: its one reader is the runner that started it.

// lease is one task's lease, as its runner keeps it.
type lease struct {
	mu      sync.Mutex
	length  time.Duration
	renewed time.Time
	over    bool
	// changed wakes keep when a join sets the length, which may shorten it.
	changed chan struct{}
}

// newLease is the lease of a launch, counting from now. fixed, where set, is its length
// whatever the launch says: a test's.
func newLease(l *runner.Launch, fixed time.Duration) *lease {
	return &lease{length: leaseLength(l, fixed), renewed: time.Now(), changed: make(chan struct{}, 1)}
}

func leaseLength(l *runner.Launch, fixed time.Duration) time.Duration {
	if fixed > 0 {
		return fixed
	}
	return runner.LeaseOf(l.Lease)
}

// join counts a launch that joins the task as the launch it is: the lease is the joining
// launch's from now, which is the one its launcher waits by. Nothing, once the lease has run
// out.
func (l *lease) join(by *runner.Launch, fixed time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.over {
		return
	}
	l.length, l.renewed = leaseLength(by, fixed), time.Now()
	select {
	case l.changed <- struct{}{}:
	default:
	}
}

// renew counts a subscribe the runner accepted, and reports whether it renewed the lease: false
// once the lease has run out.
func (l *lease) renew() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.over {
		return false
	}
	l.renewed = time.Now()
	return true
}

// keep waits for the lease to run out and then calls expire, once. It returns without calling
// it when ended closes first.
func (l *lease) keep(ended <-chan struct{}, expire func()) {
	for {
		l.mu.Lock()
		left := time.Until(l.renewed.Add(l.length))
		if left <= 0 {
			l.over = true
		}
		l.mu.Unlock()
		if left <= 0 {
			expire()
			return
		}
		t := time.NewTimer(left)
		select {
		case <-ended:
			t.Stop()
			return
		case <-l.changed:
			t.Stop()
		case <-t.C:
		}
	}
}

// noReader is why a task whose lease ran out ended.
func (l *lease) noReader() string {
	l.mu.Lock()
	length := l.length
	l.mu.Unlock()
	// A duration as it is written: 4m, and not 4m0s.
	s := length.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return "no reader for " + s
}

// ranOut reports whether the lease has run out.
func (l *lease) ranOut() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.over
}

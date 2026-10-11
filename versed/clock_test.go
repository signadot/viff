package versed

import (
	"sort"
	"sync"
	"testing"
	"time"
)

// fakeClock is a clock a test moves. Now stands still until Advance, and a timer runs when
// Advance carries the clock past its instant, in the order of their instants, on the
// advancing goroutine; one due at once runs on its own goroutine, as time.AfterFunc's would.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c    *fakeClock
	due  time.Time
	f    func()
	done bool
}

// clockOf gives a server a clock the test moves, before it starts, and answers it.
func clockOf(t *testing.T, srvs ...*Server) *fakeClock {
	t.Helper()
	c := &fakeClock{now: time.Now().Truncate(time.Second)}
	for _, s := range srvs {
		s.clock = c
	}
	return c
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	tm := &fakeTimer{c: c, due: c.now.Add(d), f: f}
	if d <= 0 {
		tm.done = true
		c.mu.Unlock()
		go f()
		return tm
	}
	c.timers = append(c.timers, tm)
	c.mu.Unlock()
	return tm
}

func (tm *fakeTimer) Stop() bool {
	tm.c.mu.Lock()
	defer tm.c.mu.Unlock()
	if tm.done {
		return false
	}
	tm.done = true
	return true
}

// Advance moves the clock by d and runs every timer it passes, earliest first.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var due *fakeTimer
		live := c.timers[:0]
		for _, tm := range c.timers {
			if !tm.done {
				live = append(live, tm)
			}
		}
		c.timers = live
		sort.SliceStable(c.timers, func(i, j int) bool { return c.timers[i].due.Before(c.timers[j].due) })
		if len(c.timers) > 0 && !c.timers[0].due.After(c.now) {
			due = c.timers[0]
			due.done = true
		}
		c.mu.Unlock()
		if due == nil {
			return
		}
		due.f()
	}
}

package viffd

import "time"

// Clock is where viffd reads the time and sets its timers: a question's deadline, an ask's
// expiry, a schedule's next tick. The wall clock, except in a test, which moves the clock
// rather than waiting on it: a keep of two seconds is proved by advancing two seconds, not by
// sleeping them, and what the test then asserts is the action the clock took.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is what AfterFunc hands back: a way to stop the function before it runs.
type Timer interface {
	Stop() bool
}

type wallClock struct{}

func (wallClock) Now() time.Time                            { return time.Now() }
func (wallClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// now is the server's reading of the clock.
func (s *Server) now() time.Time { return s.clock.Now() }

// at runs f when the server's clock reaches t, now if it has.
func (s *Server) at(t time.Time, f func()) Timer { return s.clock.AfterFunc(t.Sub(s.now()), f) }

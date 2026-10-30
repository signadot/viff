// Package versedtest stands up a versed for a test outside the package: a server over a store, with
// a runner served beside it and registered as its local runner, as `verse up` registers its own.
// No task runs in a verse's process, so a test that runs tasks needs one.
package versedtest

import (
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/runner/runnerd/runnerdtest"
	"github.com/signadot/verse/versed"
)

// Server is a server over store, with build served as its local runner, offering everything a
// test asks of it (runnerdtest.Everything).
func Server(t testing.TB, build runnerd.Builder, store entity.Store, opts ...versed.Option) *versed.Server {
	t.Helper()
	return Offering(t, build, runnerdtest.Everything(), store, opts...)
}

// Offering is Server with a local runner that offers what desc says, as a runner of that
// configuration describes itself.
func Offering(t testing.TB, build runnerd.Builder, desc runner.Description, store entity.Store, opts ...versed.Option) *versed.Server {
	t.Helper()
	s := versed.NewWithStore(store, opts...)
	Runner(t, s, build, desc)
	return s
}

// Runner serves build as s's local runner, offering desc, and registers it.
func Runner(t testing.TB, s *versed.Server, build runnerd.Builder, desc runner.Description) *runnerdtest.Runner {
	t.Helper()
	r := runnerdtest.Serve(t, build, desc)
	if err := s.RegisterLocalRunner(r.URL, r.Subject); err != nil {
		t.Fatalf("registering the local runner: %v", err)
	}
	return r
}

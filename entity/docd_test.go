package entity

import (
	"os"
	"testing"

	docdserver "github.com/signadot/tony-format/go-tony/system/docd/server"
)

// startDocd brings up an in-process docd in front of logd with both faces listening: the
// client face (which speaks the logd session protocol, and is what a LogdStore connects
// to) and the controller face, which docd serves whether or not anything mounts on it.
func startDocd(t *testing.T, logdAddr string) *docdserver.Server {
	t.Helper()
	srv := docdserver.New(&docdserver.Spec{LogdAddr: logdAddr})
	if err := srv.StartClientTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start docd client face: %v", err)
	}
	t.Cleanup(func() { srv.StopClientTCP() })
	if err := srv.StartTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start docd controller face: %v", err)
	}
	t.Cleanup(func() { srv.StopTCP() })
	return srv
}

// storeOverDocd opens a LogdStore through a fresh logd+docd pair and starts the lazy live
// root watch (what Engine.Start does, and what makes Rev track commits).
func storeOverDocd(t *testing.T) (*LogdStore, *docdserver.Server) {
	t.Helper()
	docd := startDocd(t, startLogd(t))
	s, err := OpenLogdStore(LogdConfig{Addr: docd.ClientTCPAddr(), ClientID: "verse-test"})
	if err != nil {
		t.Fatalf("open store through docd: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	_, unsub := s.Observe(ordinary()...)
	t.Cleanup(unsub)
	return s, docd
}

// hasRef reports whether a replay carried an event for ref.
func hasRef(evs []Event, ref Ref) bool {
	for _, e := range evs {
		if e.Ref == ref {
			return true
		}
	}
	return false
}

// requireLong skips a test unless GOTEST_LONG is set to something non-empty.
//
// For tests whose runtime is the point rather than an accident — waiting out a real
// timeout to demonstrate what happens when it expires. Those cannot be made fast without
// destroying what they show, and leaving them in the default run costs more than they earn
// on an ordinary edit-test loop.
//
//	GOTEST_LONG=1 go test ./entity/
func requireLong(t *testing.T) {
	t.Helper()
	if os.Getenv("GOTEST_LONG") == "" {
		t.Skip("long: set GOTEST_LONG=1 to run (skipped by default; see the test's comment)")
	}
}

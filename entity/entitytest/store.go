// Package entitytest opens entity stores for tests.
//
// There is one entity.Store implementation — the logd-backed one — so a test that
// needs a store needs a substrate. Standing one up is cheap (in-process servers over a
// temp dir: a few milliseconds), so every test gets its own rather than sharing a
// namespace with its neighbours, and t.Cleanup tears them all down.
//
// IT IS DOCD-BACKED, because that is what a person runs: `verse up` brings up logd AND
// docd and points the store at docd's client face. A store spoken straight to logd is a
// configuration nobody deploys, and the two substrates do not accept the same writes —
// docd decomposes a patch by mount, statically, so it can only route a path made of
// field names. Tests over logd alone said an array-element write worked; against every
// real verse it was refused.
//
// entity's own tests cannot use this (it imports entity); they keep a local copy.
package entitytest

import (
	"testing"

	docdserver "github.com/signadot/tony-format/go-tony/system/docd/server"
	logdserver "github.com/signadot/tony-format/go-tony/system/logd/server"
	"github.com/signadot/tony-format/go-tony/system/logd/storage"

	"github.com/signadot/verse/entity"
)

// Addr brings up an in-process logd and, over it, a docd — both on ephemeral ports,
// backed by a temp dir — and returns DOCD's client face, which is where a store connects.
// All of it is stopped at the end of the test.
func Addr(t testing.TB) string {
	t.Helper()
	return docdOver(t, LogdAddr(t))
}

// LogdAddr brings up the logd alone, for a test that needs the substrate underneath docd
// (one mounting a controller, say, which talks to docd's other face).
func LogdAddr(t testing.TB) string {
	t.Helper()
	store, err := storage.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open logd storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	srv := logdserver.New(&logdserver.Spec{Storage: store})
	if err := srv.StartTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start logd: %v", err)
	}
	t.Cleanup(func() { srv.StopTCP() })
	return srv.TCPAddr()
}

// docdOver runs a docd against a logd and returns its client face.
func docdOver(t testing.TB, logdAddr string) string {
	t.Helper()
	srv := docdserver.New(&docdserver.Spec{LogdAddr: logdAddr})
	if err := srv.StartClientTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start docd client face: %v", err)
	}
	t.Cleanup(func() { srv.StopClientTCP() })
	return srv.ClientTCPAddr()
}

// Store returns a store over a fresh logd of its own, closed at the end of the test.
func Store(t testing.TB) *entity.LogdStore {
	t.Helper()
	return StoreAt(t, Addr(t))
}

// StoreAt returns a store over an already-running substrate — for a test that needs two
// stores (two clients, two scopes) over the same one.
func StoreAt(t testing.TB, addr string) *entity.LogdStore {
	t.Helper()
	s, err := entity.OpenLogdStore(entity.LogdConfig{Addr: addr, ClientID: "verse-test"})
	if err != nil {
		t.Fatalf("open logd store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Set writes a payload at ref, failing the test if the store could not. It is the
// arrange-a-fixture spelling: a test that is not about writing says what should stand
// where, and hears about it if that did not happen.
func Set(t testing.TB, s entity.Store, ref entity.Ref, p entity.Payload) {
	t.Helper()
	if _, _, err := s.Write(entity.Write{At: ref.At(), Value: p}); err != nil {
		t.Fatalf("write %s: %v", ref, err)
	}
}

// Remove takes away whatever stands at ref. Removing what is not there is not an error —
// the point is that nothing stands there afterwards.
func Remove(t testing.TB, s entity.Store, ref entity.Ref) {
	t.Helper()
	if _, _, err := s.Write(entity.Write{At: ref.At()}); err != nil {
		t.Fatalf("remove %s: %v", ref, err)
	}
}

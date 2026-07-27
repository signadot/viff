// Package entitytest opens entity stores for tests.
//
// There is one entity.Store implementation — the logd-backed one — so a test that
// needs a store needs a logd. Standing one up is cheap (an in-process server over a
// temp dir: tens of microseconds), so every test gets its own rather than sharing a
// namespace with its neighbours, and t.Cleanup tears both down.
//
// entity's own tests cannot use this (it imports entity); they keep a local copy.
package entitytest

import (
	"testing"

	logdserver "github.com/signadot/tony-format/go-tony/system/logd/server"
	"github.com/signadot/tony-format/go-tony/system/logd/storage"

	"github.com/signadot/verse/entity"
)

// Addr brings up an in-process logd on an ephemeral port, backed by a temp dir, and
// returns its address. Both are stopped at the end of the test.
func Addr(t testing.TB) string {
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

// Store returns a store over a fresh logd of its own, closed at the end of the test.
func Store(t testing.TB) *entity.LogdStore {
	t.Helper()
	return StoreAt(t, Addr(t))
}

// StoreAt returns a store over an already-running logd — for a test that needs two
// stores (two clients, two scopes) over the same substrate.
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
	if _, err := s.Write(entity.Write{At: ref.At(), Value: p}); err != nil {
		t.Fatalf("write %s: %v", ref, err)
	}
}

// Remove takes away whatever stands at ref. Removing what is not there is not an error —
// the point is that nothing stands there afterwards.
func Remove(t testing.TB, s entity.Store, ref entity.Ref) {
	t.Helper()
	if _, err := s.Write(entity.Write{At: ref.At()}); err != nil {
		t.Fatalf("remove %s: %v", ref, err)
	}
}

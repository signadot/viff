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
	"time"

	docdserver "github.com/signadot/tony-format/go-tony/system/docd/server"
	logdserver "github.com/signadot/tony-format/go-tony/system/logd/server"
	"github.com/signadot/tony-format/go-tony/system/logd/storage"

	"github.com/signadot/verse/pkg/entity"
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
	_, addr := startLogd(t)
	return addr
}

// startLogd is LogdAddr with the storage underneath handed back as well.
func startLogd(t testing.TB) (*storage.Storage, string) {
	t.Helper()
	store, err := storage.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open logd storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// SNAPSHOT SOONER THAN A DEPLOYMENT WOULD. A read at a path folds the patches since the
	// last snapshot there, and a test that drives one record through hundreds of firings pays
	// that fold on every read of it: the reconciler's scenario, ~2,700 commits in one story,
	// spent 47 seconds at logd's default of 64 records per MiB before a path is snapshotted,
	// 168 seconds with path snapshots off, and 29 at 8. Past 8 the curve is flat (27 at 2),
	// and a full snapshot more often than the default changed nothing. A test store is read
	// by one process for one test, so the disk the sooner snapshots cost is nobody's.
	cfg := (&logdserver.Config{
		Storage: &logdserver.StorageConfig{PathSnapshotTail: 8},
	}).WithDefaults()
	srv := logdserver.New(&logdserver.Spec{Config: cfg, Storage: store})
	if err := srv.StartTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start logd: %v", err)
	}
	t.Cleanup(func() { srv.StopTCP() })
	return store, srv.TCPAddr()
}

// CompactingStore is Store with a hand on its history: compact erases every commit the store
// holds so far, as logd's compaction does to whatever is older than its cutoff, so a read at any
// of those revisions is refused from then on (entity.ErrReplayTooOld) and the head still reads.
// It is how a test reaches what a long-running verse reaches after its cutoff, without waiting.
func CompactingStore(t testing.TB) (s *entity.LogdStore, compact func()) {
	t.Helper()
	logd, addr := startLogd(t)
	s = StoreAt(t, docdOver(t, addr))
	return s, func() {
		t.Helper()
		before := logd.ReplayFloor()
		if err := logd.SwitchDLog(); err != nil {
			t.Fatalf("switch dlog: %v", err)
		}
		cfg := storage.DefaultCompactionConfig()
		cfg.Cutoff = -time.Hour // older than every commit: the whole delta history goes
		if err := logd.Compact(cfg); err != nil {
			t.Fatalf("compact: %v", err)
		}
		if logd.ReplayFloor() <= before {
			t.Fatalf("compaction left the replay floor at %d: nothing was erased", logd.ReplayFloor())
		}
	}
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

// OpenStore is Store with the record a deployment's seed writes for an OPEN verse: everyone
// holds `owner` beneath the root, so a test writes where it likes and what it creates is its
// writer's, and everyone reads the root itself.
//
// IT IS WRITTEN RATHER THAN ASSUMED, because openness is a deployment's policy and not the
// check's: a store nobody seeded admits nobody but the system, which is what makes a verse whose
// seed failed refuse rather than stand wide open. A test that is ABOUT the policy takes Store
// and writes the record it means (versed's seed writes this one, plus verse's half and the
// deployment's admins).
func OpenStore(t testing.TB) *entity.LogdStore {
	t.Helper()
	s := Store(t)
	OpenRoot(t, s)
	return s
}

// OpenRoot writes the open verse's root record into a store that has none.
func OpenRoot(t testing.TB, s entity.Store) {
	t.Helper()
	rec := entity.Obj{
		entity.OwnerField: entity.SystemDefault,
		entity.GrantsField: entity.GrantsPayload(nil, []entity.Grant{
			{Below: true, To: map[entity.Level][]string{entity.OwnerLevel: {entity.Everyone}}},
			{To: map[entity.Level][]string{entity.Read: {entity.Everyone}}},
		}),
	}
	seed := entity.As(s, entity.SystemSeed)
	if _, _, err := seed.Write(entity.Write{
		At: entity.PermsRef(entity.RefAt()).At(), Value: rec.Payload(), Principal: entity.SystemSeed,
	}); err != nil {
		t.Fatalf("opening the root: %v", err)
	}
	// AND VERSE'S HALF IS FENCED, as the deployment's seed fences it: a grant does not reach
	// through a node carrying an owner record, and without this one the root's `owner` beneath
	// it would hand every reader verse's own half and the perms mirror with it. `grants: []`
	// out loud, since a record with an owner and no grants field reads as holding out the
	// root's default.
	if _, _, err := seed.Write(entity.Write{
		At: entity.PermsRef(entity.RefAt(entity.VerseRoot)).At(),
		Value: entity.Obj{
			entity.OwnerField:  entity.SystemDefault,
			entity.GrantsField: entity.GrantsPayload(nil, nil),
		}.Payload(),
		Principal: entity.SystemSeed,
	}); err != nil {
		t.Fatalf("fencing verse's half: %v", err)
	}
}

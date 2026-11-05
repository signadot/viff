package entity

import (
	"fmt"
	"strings"
	"testing"

	logdserver "github.com/signadot/tony-format/go-tony/system/logd/server"
	"github.com/signadot/tony-format/go-tony/system/logd/storage"
)

// A LISTING ANSWERS A CONTAINER A READ CANNOT, which is the whole point of asking the store for
// the names.
//
// A read BUILDS the node it answers, under the session's read budget, so the wider a container
// gets the more a listing of it costs -- until the read is refused outright, on exactly the
// container somebody most wants the names of. The listing builds nothing: logd lists a level
// from its own tables (tony-format 3kgxprskh12krjrmndn0, a directory of children per container),
// and verse walks levels, so neither end holds the container.
//
// The budget is set small here so the difference is visible in a test rather than in a
// production incident; the shape is the same at 64 MiB.
func TestAListingAnswersAContainerAReadCannot(t *testing.T) {
	store, err := storage.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := logdserver.DefaultConfig()
	if cfg.Storage == nil {
		cfg.Storage = &logdserver.StorageConfig{}
	}
	cfg.Storage.ReadBudget = 8 << 10 // 8 KiB: a few records, not a few thousand
	srv := logdserver.New(&logdserver.Spec{Storage: store, Config: cfg})
	if err := srv.StartTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start logd: %v", err)
	}
	t.Cleanup(func() { srv.StopTCP() })
	s := openStoreAt(t, startDocd(t, srv.TCPAddr()).ClientTCPAddr())

	const n = 200
	body := strings.Repeat("x", 128)
	for i := range n {
		ref := RefAt("demo", "pr", fmt.Sprintf("%03d", i))
		if _, _, err := s.Write(Write{At: ref.At(), Value: Obj{"stage": "open", "note": body}.Payload()}); err != nil {
			t.Fatalf("seed %s: %v", ref, err)
		}
	}

	// The read of the container is refused: it would have to hold every record at once.
	if _, _, err := s.ReadAt([]string{"demo", "pr"}); err == nil {
		t.Fatal("the read was answered; this test needs a container the budget refuses")
	} else if !strings.Contains(err.Error(), "budget") {
		t.Fatalf("the read failed for another reason: %v", err)
	}

	// The listing answers it, whole.
	out, err := s.PathsAt([]string{"demo", "pr"}, Unbounded)
	if err != nil {
		t.Fatalf("PathsAt over a container past the read budget: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range out {
		seen[p.KPath()] = true
	}
	for _, want := range []string{`demo.pr."000"`, `demo.pr."199"`, `demo.pr."000".stage`} {
		if !seen[want] {
			t.Errorf("the listing does not name %s (%d addresses)", want, len(out))
		}
	}
	// Each record is three addresses -- itself, its stage and its note -- so the listing is the
	// whole container rather than as much of it as fitted.
	if len(out) != 3*n {
		t.Errorf("the listing answered %d addresses, want %d", len(out), 3*n)
	}
	// And a record it named reads on its own, which is what the listing is for: the container
	// is past the budget, one record is not.
	if _, ok, err := s.ReadAt([]string{"demo", "pr", "000"}); err != nil || !ok {
		t.Errorf("a listed record does not read: ok=%v err=%v", ok, err)
	}
}

package entity

import (
	"fmt"
	"strings"
	"testing"

	logdserver "github.com/signadot/tony-format/go-tony/system/logd/server"
	"github.com/signadot/tony-format/go-tony/system/logd/storage"
)

// WHAT A LISTING COSTS TODAY, and the ceiling it has not escaped yet.
//
// The listing ships names rather than what they stand for -- that much is true and is what
// versed's TestStateAnswersALitingAsAddresses shows on the wire. The claim it does NOT yet
// support is the one that sounds best: that it answers a container too wide for a read.
//
// It does not, because the substrate enumerates a level by BUILDING the node above it: logd's
// setChildren reads the container to list its members, under the session read budget, so a
// listing of a container past that budget is refused with the same error a read of it gets. That
// is tony-format 3kgxprskh12krjrmndn0, filed from here.
//
// This test pins both halves so the limit is written down where somebody will meet it: within
// the budget a listing answers the whole container, and past it both are refused. When the
// enumeration stops building the container, the second half is what changes.
func TestAListingCostsNamesAndSharesTheReadBudget(t *testing.T) {
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

	// WITHIN THE BUDGET: one record's subtree lists whole, and names what is under it.
	out, err := s.PathsAt([]string{"demo", "pr", "000"}, 0)
	if err != nil {
		t.Fatalf("PathsAt at one record: %v", err)
	}
	if len(out) != 2 { // stage, note
		t.Errorf("one record listed %d addresses, want 2: %v", len(out), kpaths(out))
	}

	// PAST IT: the read of the whole container is refused, and so -- for now -- is the listing,
	// because the level is enumerated by building the container (3kgxprskh12krjrmndn0).
	if _, _, err := s.ReadAt([]string{"demo", "pr"}); err == nil {
		t.Fatal("the read was answered; this test needs a container the budget refuses")
	}
	if _, err := s.PathsAt([]string{"demo", "pr"}, 0); err == nil {
		t.Fatal("the listing answered a container past the budget: 3kgxprskh12krjrmndn0 is fixed, " +
			"and this test should now say so")
	} else if !strings.Contains(err.Error(), "budget") {
		t.Fatalf("the listing failed for another reason: %v", err)
	}

	// The whole container within a bigger budget is the same walk, and answers every address.
	wide := openStoreAt(t, startDocd(t, startLogd(t)).ClientTCPAddr())
	for i := range n {
		ref := RefAt("demo", "pr", fmt.Sprintf("%03d", i))
		if _, _, err := wide.Write(Write{At: ref.At(), Value: Obj{"stage": "open", "note": body}.Payload()}); err != nil {
			t.Fatalf("seed %s: %v", ref, err)
		}
	}
	out, err = wide.PathsAt([]string{"demo", "pr"}, 0)
	if err != nil {
		t.Fatalf("PathsAt: %v", err)
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
	// And a record it named reads on its own, which is what the listing is for.
	if _, ok, err := wide.ReadAt([]string{"demo", "pr", "000"}); err != nil || !ok {
		t.Errorf("a listed record does not read: ok=%v err=%v", ok, err)
	}
}

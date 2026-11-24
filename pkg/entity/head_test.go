package entity_test

import (
	"fmt"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// Head tracks the store WITHOUT a watch, a poll or a read of its own.
//
// It is libctl's KnownCommit, raised by every answer carrying a commit — this store's own
// writes among them — which is what makes it safe for a caller on a timer. Rev is the one that
// syncs, and its sync opens a watch on the root that logd grants one of per call: a readiness
// probe calling it accumulated 158 idle watchers in 26 minutes on staging.
func TestHeadFollowsWritesWithoutASync(t *testing.T) {
	s := entitytest.OpenStore(t)
	var last int64
	for i := 0; i < 5; i++ {
		ref := entity.RefAt("demo", "pr", fmt.Sprintf("%d", i))
		_, commit, err := s.Write(entity.Write{At: ref.At(), Value: entity.Obj{"i": int64(i)}.Payload()})
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		last = commit
		if got := s.Head(); got < commit {
			t.Fatalf("after a write this store made at commit %d, Head() = %d", commit, got)
		}
	}
	if last == 0 {
		t.Fatal("no commits were made; the fixture is not testing anything")
	}
}

// And an IDLE store converges on the head somebody else moved, from the heartbeat alone — the
// case that made reporting Head unusable before v0.0.165, when it was only what this process
// had been told and read 0 over a full store.
func TestHeadConvergesOnAnotherWritersCommits(t *testing.T) {
	addr := entitytest.Addr(t)
	writer := entitytest.StoreAt(t, addr)
	idle := entitytest.StoreAt(t, addr) // never writes, never watches

	var commit int64
	for i := 0; i < 3; i++ {
		ref := entity.RefAt("demo", "pr", fmt.Sprintf("other%d", i))
		_, c, err := writer.Write(entity.Write{At: ref.At(), Value: entity.Obj{"i": int64(i)}.Payload()})
		if err != nil {
			t.Fatalf("write: %v", err)
		}
		commit = c
	}

	deadline := time.After(30 * time.Second)
	for {
		if idle.Head() >= commit {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("an idle store never learned of commit %d; Head() = %d — it is meant to "+
				"come from the ping, which needs no traffic of its own", commit, idle.Head())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

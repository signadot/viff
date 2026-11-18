package trigger

import (
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/task"
)

// THE REAP WATCHES WHERE TASK RECORDS ARE WRITTEN. A scoped engine retires a task when its
// record reaches a terminal status, seen on its own watch of the mirror. The record is the only
// way a task is retired, and a watch opened anywhere but under `verse` never sees it.
func TestAScopedEngineReapsFromTheTaskRecord(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.ObserveScoped(t.Context())

	const id = "run-reaped"
	eng.liveMu.Lock()
	eng.live[id] = flowTask{id: id, subject: "demo.pr.1"}
	eng.liveMu.Unlock()

	if _, _, err := store.Write(entity.Write{
		At: mirror.Ref(id).At(), Value: entity.Obj{"status": string(task.Done)}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		eng.liveMu.Lock()
		_, live := eng.live[id]
		eng.liveMu.Unlock()
		if !live {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine never retired %s after its record at %s went terminal", id, mirror.Ref(id))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

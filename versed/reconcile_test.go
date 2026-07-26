package versed

import (
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
)

// A run claim must not outlive the run it guards, and a restart ends every run: a resumed
// process is re-adopted WITHOUT its hooks, so nothing will ever advance the sequence a claim
// is holding. Left in place, the claim would block that sequence forever with no live process
// to explain why — the failure mode a durable lease has and an in-memory one does not.
func TestReconcileClearsRunClaims(t *testing.T) {
	srv := New(ScriptedBuilder)
	claim := mirror.ClaimRef("review@demo:pr:1")
	srv.Store.Put(claim, entity.Obj{"flow": "demo:pr:1", "chain": "review"}.Payload())

	srv.reconcileProcesses()

	if _, ok, err := srv.Store.Get(claim); err != nil || ok {
		t.Fatalf("claim survived reconcile (ok=%v err=%v); the sequence it guards is gone", ok, err)
	}
}

// Clearing claims must not disturb the reconcile it rides along with: a non-terminal local
// process is still orphaned, and a claim is not mistaken for one on the way past.
func TestReconcileStillOrphansAndIgnoresClaims(t *testing.T) {
	srv := New(ScriptedBuilder)
	live := mirror.Ref("review@demo:pr:2")
	srv.Store.Put(live, entity.Obj{
		"status": string(process.Running), "policy": "local", "flow": "demo:pr:2",
	}.Payload())
	claim := mirror.ClaimRef("review@demo:pr:2")
	srv.Store.Put(claim, entity.Obj{"flow": "demo:pr:2"}.Payload())

	srv.reconcileProcesses()

	e, ok, err := srv.Store.Get(live)
	if err != nil || !ok {
		t.Fatalf("process entity lost: ok=%v err=%v", ok, err)
	}
	if got := entity.Str(e.Payload, "status"); got != string(process.Orphaned) {
		t.Errorf("status = %q; want orphaned — a local process cannot outlive verse", got)
	}
	if _, ok, _ := srv.Store.Get(claim); ok {
		t.Error("claim survived alongside the orphaned process it guarded")
	}
}

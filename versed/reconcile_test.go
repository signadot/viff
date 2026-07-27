package versed

import (
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
)

// A run claim must not outlive the run it guards, and a restart ends every run: a resumed
// process is re-adopted WITHOUT its hooks, so nothing will ever advance the sequence a claim
// is holding. Left in place, the claim would block that sequence forever with no live process
// to explain why — the failure mode a durable lease has and an in-memory one does not.
func TestReconcileClearsRunClaims(t *testing.T) {
	srv := testServer(t)
	claim := mirror.ClaimRef("review@demo:pr:1")
	entitytest.Set(t, srv.Store, claim, entity.Obj{"flow": "demo:pr:1", "chain": "review"}.Payload())

	srv.reconcileProcesses()

	if _, ok, err := srv.Store.Get(claim); err != nil || ok {
		t.Fatalf("claim survived reconcile (ok=%v err=%v); the sequence it guards is gone", ok, err)
	}
}

// Clearing claims must not disturb the reconcile it rides along with: a non-terminal local
// process is still orphaned, and a claim is not mistaken for one on the way past.
func TestReconcileStillOrphansAndIgnoresClaims(t *testing.T) {
	srv := testServer(t)
	live := mirror.Ref("review@demo:pr:2")
	entitytest.Set(t, srv.Store, live, entity.Obj{
		"status": string(process.Running), "runner": "local", "flow": "demo:pr:2",
	}.Payload())
	claim := mirror.ClaimRef("review@demo:pr:2")
	entitytest.Set(t, srv.Store, claim, entity.Obj{"flow": "demo:pr:2"}.Payload())

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

// A claim is kept when the run it guards was RESUMED — its process re-adopted with hooks, so
// a completion is still coming and the sequence will advance. Dropping it would let a
// concurrent firing start a second run alongside the one still going.
//
// Note what "resumed" means here, because the claim inherits it exactly: the runner
// package's Attach returns a handle unconditionally, with no reachability check, so a remote-hosted
// process is re-adopted whether or not the remote is still there. The process stays
// `running` on that belief, and now its claim stays too. The optimism is pre-existing; this
// test pins that the claim follows the process rather than diverging from it.
func TestReconcileKeepsClaimsForResumedRuns(t *testing.T) {
	srv := testServer(t)
	live := mirror.Ref("review.seq.1@demo:pr:1")
	entitytest.Set(t, srv.Store, live, entity.Obj{
		"status": string(process.Running),
		"runner": "http://elsewhere.invalid", // remote → re-attached, believed alive
		"flow":   "demo:pr:1",
		"chain":  "review.seq[1]",
	}.Payload())
	claim := mirror.ClaimRef("review@demo:pr:1")
	entitytest.Set(t, srv.Store, claim, entity.Obj{"flow": "demo:pr:1", "chain": "review"}.Payload())

	srv.reconcileProcesses()

	if _, ok, _ := srv.Store.Get(claim); !ok {
		t.Error("claim dropped though its sequence's process was re-adopted and will complete")
	}
	e, _, _ := srv.Store.Get(live)
	if got := entity.Str(e.Payload, "status"); got != string(process.Running) {
		t.Errorf("re-adopted process status = %q; want it left running", got)
	}
}

// claimIsLive is the decision itself, tested directly: same subject and a chain at or below
// the claim's own means the sequence is still running.
func TestClaimIsLive(t *testing.T) {
	claim := entity.Entity{Payload: entity.Obj{"flow": "demo:pr:1", "chain": "review"}.Payload()}
	cases := []struct {
		name string
		res  []resumedRun
		want bool
	}{
		{"an element of this sequence", []resumedRun{{"demo:pr:1", "review.seq[1]"}}, true},
		{"the carrier itself", []resumedRun{{"demo:pr:1", "review"}}, true},
		{"another subject", []resumedRun{{"demo:pr:2", "review.seq[1]"}}, false},
		{"another trigger", []resumedRun{{"demo:pr:1", "audit.seq[0]"}}, false},
		{"a hook outside the sequence", []resumedRun{{"demo:pr:1", "other.onDone"}}, false},
		{"nothing resumed", nil, false},
	}
	for _, c := range cases {
		if got := claimIsLive(claim, c.res); got != c.want {
			t.Errorf("%s: claimIsLive = %v, want %v", c.name, got, c.want)
		}
	}
}

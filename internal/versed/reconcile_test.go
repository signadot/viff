package versed

import (
	"errors"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/task"
)

// A claim found at start whose rule is not in the charter is owed nothing that can be found,
// and is released: left in place it would hold its subject forever with no task to explain why.
func TestReconcileClearsRunClaims(t *testing.T) {
	srv := testServer(t)
	claim := mirror.ClaimRef("review@demo.pr.1")
	entitytest.Set(t, srv.Store, claim, entity.Obj{"flow": `demo.pr."1"`, "chain": "review"}.Payload())

	reconcile(t, srv)

	if _, ok, err := srv.Store.Get(claim); err != nil || ok {
		t.Fatalf("claim survived reconcile (ok=%v err=%v); the sequence it guards is gone", ok, err)
	}
}

// Clearing claims must not disturb the reconcile it rides along with: a non-terminal local
// task is still orphaned, and a claim is not mistaken for one on the way past.
func TestReconcileStillOrphansAndIgnoresClaims(t *testing.T) {
	srv := testServer(t)
	live := mirror.Ref("review@demo.pr.2")
	entitytest.Set(t, srv.Store, live, entity.Obj{
		"status": string(task.Running), "runner": "local", "flow": `demo.pr."2"`,
	}.Payload())
	claim := mirror.ClaimRef("review@demo.pr.2")
	entitytest.Set(t, srv.Store, claim, entity.Obj{"flow": `demo.pr."2"`}.Payload())

	reconcile(t, srv)

	e, ok, err := srv.Store.Get(live)
	if err != nil || !ok {
		t.Fatalf("task entity lost: ok=%v err=%v", ok, err)
	}
	if got := entity.Str(e.Payload, "status"); got != string(task.Failed) {
		t.Errorf("status = %q; want failed — a local task cannot outlive verse", got)
	}
	if o, _ := entity.ToObj(e.Payload)[mirror.OrphanedField].(bool); !o || entity.Str(e.Payload, "err") != mirror.OrphanedWithVerse {
		t.Errorf("record = %v; want it orphaned, saying its runtime ended with verse", entity.ToObj(e.Payload))
	}
	if _, ok, _ := srv.Store.Get(claim); ok {
		t.Error("claim survived alongside the orphaned task it guarded")
	}
}

// A claim is kept when the run it guards was RESUMED — its task re-adopted, so
// a completion is still coming and the sequence will advance. Dropping it would let a
// concurrent firing start a second run alongside the one still going.
//
// Note what "resumed" means here, because the claim inherits it exactly: the runner
// package's Attach returns a handle unconditionally, with no reachability check, so a remote-hosted
// task is re-adopted whether or not the remote is still there. The task stays
// `running` on that belief, and now its claim stays too. The optimism is pre-existing; this
// test pins that the claim follows the task rather than diverging from it.
func TestReconcileKeepsClaimsForResumedRuns(t *testing.T) {
	srv := testServer(t)
	live := mirror.Ref("review.seq.1@demo.pr.1")
	entitytest.Set(t, srv.Store, live, entity.Obj{
		"status": string(task.Running),
		"runner": "http://elsewhere.invalid", // remote → re-attached, believed alive
		"flow":   `demo.pr."1"`,
		"chain":  "review.seq[1]",
	}.Payload())
	claim := mirror.ClaimRef("review@demo.pr.1")
	entitytest.Set(t, srv.Store, claim, entity.Obj{"flow": `demo.pr."1"`, "chain": "review"}.Payload())

	reconcile(t, srv)

	if _, ok, _ := srv.Store.Get(claim); !ok {
		t.Error("claim dropped though its sequence's task was re-adopted and will complete")
	}
	e, _, _ := srv.Store.Get(live)
	if got := entity.Str(e.Payload, "status"); got != string(task.Running) {
		t.Errorf("re-adopted task status = %q; want it left running", got)
	}
}

// reconcile is what a start does for the tasks and claims a verse left behind, in a start's
// order: the tasks re-attached or ended and handed to the root package's engine, the engine
// started and open, its claims settled by then, and what belongs to no package stopped.
func reconcile(t *testing.T, srv *Server) {
	t.Helper()
	srv.Eng.AwaitTasks()
	if err := srv.reconcileTasks(); err != nil {
		t.Fatalf("reconcileTasks: %v", err)
	}
	srv.Eng.ObserveScoped(t.Context())
	select {
	case <-srv.Eng.Opened():
	case <-time.After(10 * time.Second):
		t.Fatal("the root package's engine did not open")
	}
	if err := srv.settleTasks(); err != nil {
		t.Fatalf("settleTasks: %v", err)
	}
}

// A resumed task whose id names a charter package that has no engine is not handed to another
// package's, whether or not the packages were restored. Its runner does not answer here, so it
// is not stopped, and its claim stands either way (TestATaskOfAPackageThatIsGoneIsStopped has
// the claim's removal).
func TestATaskOfAPackageThatIsGoneIsNotAnotherPackages(t *testing.T) {
	for name, restored := range map[string]bool{"the packages were restored": true, "they were not": false} {
		t.Run(name, func(t *testing.T) {
			srv := testServer(t)
			id := "gone:review@demo.pr.1"
			entitytest.Set(t, srv.Store, mirror.Ref(id), entity.Obj{
				"status": string(task.Running), "runner": "http://elsewhere.invalid",
				"flow": `demo.pr."1"`, "chain": "review",
			}.Payload())
			claim := mirror.ClaimRef(id)
			entitytest.Set(t, srv.Store, claim, entity.Obj{"flow": `demo.pr."1"`, "chain": "review", "of": "spawn"}.Payload())
			srv.packagesRestored.Store(restored)

			reconcile(t, srv)

			if _, live := srv.Eng.Live(id); live {
				t.Error("the root package's engine tracks a task of another package")
			}
			if _, ok, _ := srv.Store.Get(claim); !ok {
				t.Errorf("the claim of a task that was not stopped is gone, with the packages restored = %v", restored)
			}
		})
	}
}

// deafHandle is a task that a cancel does not reach.
type deafHandle struct{ *idleHandle }

func (deafHandle) Cancel(string, string) error { return errors.New("the runner did not answer") }

// A TASK WHOSE PACKAGE HAS NO ENGINE IS STOPPED, AND ITS CLAIMS REMOVED: its own, and its run's.
// One that could not be stopped is still running, so those claims stand for the next start,
// while a claim of the same package about other work is removed.
func TestATaskOfAPackageThatIsGoneIsStopped(t *testing.T) {
	for name, stopped := range map[string]bool{"the cancel reaches it": true, "the cancel fails": false} {
		t.Run(name, func(t *testing.T) {
			srv := testServer(t)
			id := "gone:deploy.seq.0@demo.pr.1"
			own, run, other := mirror.ClaimRef(id), mirror.ClaimRef("gone:deploy@demo.pr.1"), mirror.ClaimRef("gone:deploy@demo.pr.2")
			entitytest.Set(t, srv.Store, own, entity.Obj{"flow": `demo.pr."1"`, "chain": "deploy.seq.0", "of": "spawn"}.Payload())
			entitytest.Set(t, srv.Store, run, entity.Obj{"flow": `demo.pr."1"`, "chain": "deploy"}.Payload())
			entitytest.Set(t, srv.Store, other, entity.Obj{"flow": `demo.pr."2"`, "chain": "deploy"}.Payload())
			reconcile(t, srv) // the packages not restored: nothing is stopped or removed

			idle := &idleHandle{ch: make(chan task.Event), verbs: map[string]int{}}
			var h task.Handle = idle
			if !stopped {
				h = deafHandle{idle}
			}
			srv.leftMu.Lock()
			srv.left = append(srv.left, &resumedRun{id: id, subject: `demo.pr."1"`, chain: "deploy.seq.0", h: h})
			srv.leftMu.Unlock()
			srv.packagesRestored.Store(true)
			if err := srv.settleTasks(); err != nil {
				t.Fatalf("settleTasks: %v", err)
			}
			if stopped && idle.count("cancel") != 1 {
				t.Errorf("the task was cancelled %d times, want once", idle.count("cancel"))
			}
			for name, c := range map[string]struct {
				ref    entity.Ref
				stands bool
			}{"the task's own claim": {own, !stopped}, "its run's claim": {run, !stopped}, "a claim about other work": {other, false}} {
				if _, ok, _ := srv.Store.Get(c.ref); ok != c.stands {
					t.Errorf("%s stands = %v, want %v", name, ok, c.stands)
				}
			}
		})
	}
}

// A TASK WHOSE PACKAGE HAS AN ENGINE IS NOT STOPPED FOR WANT OF ONE. The engine has not taken
// the task yet, which nothing orders ahead of the stop: the task is left for it.
func TestATaskItsEngineHasNotTakenYetIsNotStopped(t *testing.T) {
	srv := testServer(t)
	reconcile(t, srv)
	srv.ctx = t.Context()
	eng, err := srv.packageEngine("demo")
	if err != nil {
		t.Fatal(err)
	}
	idle := &idleHandle{ch: make(chan task.Event), verbs: map[string]int{}}
	srv.leftMu.Lock()
	srv.left = append(srv.left, &resumedRun{id: eng.Owner() + "review@demo.pr.1", subject: `demo.pr."1"`, chain: "review", h: idle})
	srv.leftMu.Unlock()
	srv.packagesRestored.Store(true)
	if err := srv.settleTasks(); err != nil {
		t.Fatalf("settleTasks: %v", err)
	}
	if n := idle.count("cancel"); n != 0 {
		t.Fatalf("a task whose package has an engine was cancelled %d times", n)
	}
}

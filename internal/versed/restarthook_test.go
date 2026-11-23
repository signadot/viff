package versed

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/internal/trigger/charter"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd"
	"github.com/signadot/verse/pkg/runner/runnerd/runnerdtest"
	"github.com/signadot/verse/pkg/systems/systemstest"
	"github.com/signadot/verse/pkg/task"
)

// A TASK THAT OUTLIVES VERSE ENDS AS IT WOULD HAVE. A rule's task is running on a runner when
// verse restarts. When it ends, the rule's `onDone` runs and the rule lets go of the subject,
// whichever package the rule is in: the root, or one at an address. And whenever it ends: once
// verse is back, or while verse is down, where its end is the first thing the restarted verse
// hears of it, ahead of the charter its rule is in. And however long the charter takes: where
// the task ends once verse is back and the store does not answer the charter's watch until that
// end is on the task's record, the end waits for the rule, and the hook runs when the charter is
// in.
func TestATaskResumedAcrossARestartRunsItsHook(t *testing.T) {
	for _, c := range []struct {
		at   string
		down bool // the task ends while verse is down
		late bool // the charter is not read until the task has ended, on a verse that is back
	}{{"", false, false}, {"demo", false, false}, {"", true, false}, {"demo", true, false}, {"", false, true}, {"demo", false, true}} {
		at := c.at
		name := "a rule in the package at " + at
		if at == "" {
			name = "a rule in the root package"
		}
		if c.down {
			name += ", its task ending while verse is down"
		}
		if c.late {
			name += ", and the charter read late"
		}
		t.Run(name, func(t *testing.T) {
			addr := entitytest.Addr(t)
			release := make(chan struct{})
			build := func(l runner.Launch, _ *task.Registry) *task.Task {
				sup := systemstest.Opened(task.NewSupervisor(l.Environment, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{}), l.Environment)
				return task.NewTask(l.ID, sup, &blockedDriver{release: release}, okExecutor{})
			}

			first := issuing(t, serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr)))
			ctx, stop := context.WithCancel(t.Context())
			first.Start(ctx)
			ts := httptest.NewServer(first.Handler())
			released := false
			face := provenFace(t, ts.URL, offeringAll(runnerd.NewInProcessHandler(build), "fake").HTTPHandler(), func() {
				if !released {
					close(release)
				}
			})
			postTony(t, ts.URL+"/runners", map[string]any{"name": "gpu", "url": face.URL, "subject": face.Subject}).Body.Close()
			if code, msg := installAt(t, ts.URL, at, `{
				name: review,
				condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
				action: {
					patch: {stage: reviewing},
					spawn: {agent: {name: fake}, prompt: "review it", autonomy: [{then: allow}]},
					onDone: {patch: {stage: reviewed}},
				},
			}`); code != 201 {
				t.Fatalf("installing the rule = %d: %s", code, msg)
			}
			pr := entity.RefAt("demo", "pr", "1")
			testerOwns(t, first, pr) // the rule acts as who installed it, and writes what is theirs
			entitytest.Set(t, first.Store, pr, entity.Obj{"stage": "open"}.Payload())
			var id string
			waitUntil(t, "the rule's task to be running on the runner", func() bool {
				for _, rec := range entity.Below(first.Store, mirror.IsTask, 2, entity.VerseRoot, mirror.System) {
					if entity.Str(rec.Payload, "runner") == face.URL && entity.Str(rec.Payload, "status") == string(task.Running) {
						id = rec.Ref.Name()
					}
				}
				return id != ""
			})
			ts.Close()
			stop() // verse goes down; the task runs on

			if c.down {
				released = true
				close(release) // the task ends, and nobody is told
			}
			// The charter's watch is the test's to open: refused where the charter is read late.
			held := entitytest.NewPhased(entitytest.StoreAt(t, addr))
			held.Hold = func(prefix []string) bool { return len(prefix) >= 2 && prefix[1] == charter.Prefix[1] }
			if c.late {
				held.Set(entity.WatchRefused, errFor("context deadline exceeded"))
			}
			second := issuing(t, serverWith(t, runnerdtest.Scripted, held))
			second.Start(t.Context())
			if c.late && at == "" {
				// Its engine is tracking it, so that its end is the engine's to take.
				waitUntil(t, "the root package's engine to track the task", func() bool {
					_, live := second.Eng.Live(id)
					return live
				})
			}
			if !c.down {
				released = true
				close(release) // the task ends
			}
			if c.late {
				waitUntil(t, "the task's end to be on its record", func() bool {
					e, ok, _ := second.Store.Get(mirror.Ref(id))
					return ok && entity.Str(e.Payload, "status") == string(task.Done)
				})
				time.Sleep(300 * time.Millisecond) // long enough for an end taken with no rule to have been
				if e, _, _ := second.Store.Get(pr); entity.Str(e.Payload, "stage") != "reviewing" {
					t.Fatalf("subject = %v before the charter is in, want it as the firing left it", e.Payload)
				}
				if _, claimed, _ := second.Store.Get(mirror.ClaimRef(id)); !claimed {
					t.Fatal("the rule let go of the subject before its hook had run")
				}
				held.Set(entity.WatchOpen, nil)
			}
			waitUntil(t, "the rule's onDone to run", func() bool {
				e, ok, _ := second.Store.Get(pr)
				return ok && entity.Str(e.Payload, "stage") == "reviewed"
			})
			waitUntil(t, "the rule to let go of the subject", func() bool {
				_, held, _ := second.Store.Get(mirror.ClaimRef(id))
				return !held
			})
		})
	}
}

// runningPackage is a verse with a package installed at demo whose rule has a task running on a
// runner about demo.pr.1: the server, its url, and the task's id. While deaf is set the runner
// answers no cancel.
func runningPackage(t *testing.T, rule string, deaf *atomic.Bool) (*Server, string, string) {
	t.Helper()
	return runningPackageAt(t, "demo", rule, deaf)
}

// runningPackageAt is runningPackage with the package at an address, its task about at.pr.1.
func runningPackageAt(t *testing.T, at, rule string, deaf *atomic.Bool) (*Server, string, string) {
	t.Helper()
	release := make(chan struct{})
	build := func(l runner.Launch, _ *task.Registry) *task.Task {
		sup := systemstest.Opened(task.NewSupervisor(l.Environment, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{}), l.Environment)
		return task.NewTask(l.ID, sup, &blockedDriver{release: release}, okExecutor{})
	}
	srv := issuing(t, serverWith(t, runnerdtest.Scripted, entitytest.Store(t)))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	released := false
	end := func() {
		if !released {
			released = true
			close(release)
		}
	}
	runs := offeringAll(runnerd.NewInProcessHandler(build), "fake").HTTPHandler()
	face := provenFace(t, ts.URL, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if deaf.Load() && strings.HasSuffix(r.URL.Path, "/cancel") {
			http.Error(w, "the runner is not answering", http.StatusServiceUnavailable)
			return
		}
		runs.ServeHTTP(w, r)
	}), end)
	postTony(t, ts.URL+"/runners", map[string]any{"name": "gpu", "url": face.URL, "subject": face.Subject}).Body.Close()
	if code, msg := installAt(t, ts.URL, at, rule); code != 201 {
		t.Fatalf("installing the rule = %d: %s", code, msg)
	}
	pr := entity.RefAt(append(strings.Split(at, "."), "pr", "1")...)
	testerOwns(t, srv, pr)
	entitytest.Set(t, srv.Store, pr, entity.Obj{"stage": "open"}.Payload())
	var id string
	waitUntil(t, "the rule's task to be running on the runner", func() bool {
		for _, rec := range entity.Below(srv.Store, mirror.IsTask, 2, entity.VerseRoot, mirror.System) {
			if entity.Str(rec.Payload, "runner") == face.URL && entity.Str(rec.Payload, "status") == string(task.Running) {
				id = rec.Ref.Name()
			}
		}
		return id != ""
	})
	waitUntil(t, "the package's engine to track the task", func() bool {
		_, live := srv.packageEngineIfRunning(at).Live(id)
		return live
	})
	return srv, ts.URL, id
}

const reviewAtDemo = `{
	name: review,
	condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
	action: {
		patch: {stage: reviewing},
		spawn: {agent: {name: fake}, prompt: "review it", autonomy: [{then: allow}]},
		onDone: {patch: {stage: reviewed}},
		onFailed: {patch: {stage: broke}},
		onCancelled: {patch: {stage: dropped}},
	},
}`

// recordStatus is the status on a task's record, "" where it has none.
func recordStatus(srv *Server, id string) string {
	e, _, _ := srv.Store.Get(mirror.Ref(id))
	return entity.Str(e.Payload, "status")
}

// UNINSTALLING A PACKAGE CANCELS ITS TASKS, AND THEIR HOOKS RUN. A task of the package is
// running when the package is uninstalled: the task is cancelled, its `onCancelled` runs, its
// claim comes off, and then the package and its engine are gone. The same charter installs at
// the address again.
func TestUninstallingAPackageCancelsItsTasks(t *testing.T) {
	srv, url, id := runningPackage(t, reviewAtDemo, new(atomic.Bool))
	pr := entity.RefAt("demo", "pr", "1")
	if err := srv.uninstall(t.Context(), srv.Store, "demo"); err != nil {
		t.Fatalf("uninstalling the package: %v", err)
	}
	if got := recordStatus(srv, id); got != string(task.Cancelled) {
		t.Fatalf("the task's record says %q once its package is uninstalled, want cancelled", got)
	}
	if e, _, _ := srv.Store.Get(pr); entity.Str(e.Payload, "stage") != "dropped" {
		t.Fatalf("subject = %v, want what the rule's onCancelled writes", e.Payload)
	}
	if _, held, _ := srv.Store.Get(mirror.ClaimRef(id)); held {
		t.Fatal("the claim of an uninstalled package's task stands")
	}
	if srv.packageEngineIfRunning("demo") != nil || srv.packagesSeenAt("demo") {
		t.Fatal("the package stands after its uninstall")
	}
	if code, msg := installAt(t, url, "demo", reviewAtDemo); code != 201 {
		t.Fatalf("installing the rule again = %d: %s", code, msg)
	}
}

// A TASK THAT DOES NOT TAKE ITS CANCEL HOLDS THE UNINSTALL. The runner does not answer the
// cancel: the uninstall does not finish, the package stays installed and locked, and an install
// at the address is refused as a conflict. The request that asked may go away. The cancel is
// asked again, and once it lands the task's hook runs and the package is removed.
func TestATaskThatDoesNotTakeItsCancelHoldsTheUninstall(t *testing.T) {
	deaf := new(atomic.Bool)
	srv, url, id := runningPackage(t, reviewAtDemo, deaf)
	pr := entity.RefAt("demo", "pr", "1")
	deaf.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if err := srv.uninstall(ctx, srv.Store, "demo"); err == nil {
		t.Fatal("the uninstall answered as done while a task of the package was running")
	}
	if got := recordStatus(srv, id); got != string(task.Running) {
		t.Fatalf("the task's record says %q, want running: its cancel did not land", got)
	}
	if _, held, _ := srv.Store.Get(mirror.ClaimRef(id)); !held {
		t.Fatal("the claim of a task that is still running was removed")
	}
	if eng := srv.packageEngineIfRunning("demo"); eng == nil || !srv.packagesSeenAt("demo") || len(eng.Triggers()) != 1 {
		t.Fatal("the package was removed while a task of it was running")
	}
	if code, msg := installAt(t, url, "demo", reviewAtDemo); code != 429 {
		t.Fatalf("installing at a package that is being uninstalled = %d, want 429: %s", code, msg)
	}
	// Every change to the package asks, not a rule's alone.
	if code, msg := installAt(t, url, "demo", `{name: hourly, every: 1h}`); code != 429 {
		t.Fatalf("installing a schedule at a package that is being uninstalled = %d, want 429: %s", code, msg)
	}
	if resp := del(t, tester, url+"/charter?path=demo.schedules.hourly"); resp.StatusCode != 429 {
		t.Fatalf("removing a schedule of a package that is being uninstalled = %d, want 429", resp.StatusCode)
	}
	if code, msg := pruneAt(t.Context(), t, url, "demo", "at: demo\nrules: []\n"); code != 429 {
		t.Fatalf("a pruning install that keeps a package that is being uninstalled = %d, want 429: %s", code, msg)
	}
	if eng := srv.packageEngineIfRunning("demo"); eng == nil || len(eng.Triggers()) != 1 {
		t.Fatal("a pruning install took a rule out of a package that is being uninstalled")
	}
	// The subject comes round again, and the locked package's rule starts nothing about it.
	other := entity.RefAt("demo", "pr", "2")
	testerOwns(t, srv, other)
	entitytest.Set(t, srv.Store, other, entity.Obj{"stage": "open"}.Payload())
	time.Sleep(300 * time.Millisecond)
	if e, _, _ := srv.Store.Get(other); entity.Str(e.Payload, "stage") != "open" {
		t.Fatalf("a locked package's rule fired: %v", e.Payload)
	}

	deaf.Store(false)                                                     // the runner answers again
	if err := srv.uninstall(t.Context(), srv.Store, "demo"); err != nil { // joins the one under way
		t.Fatalf("the uninstall, once the runner answers: %v", err)
	}
	if e, _, _ := srv.Store.Get(pr); entity.Str(e.Payload, "stage") != "dropped" {
		t.Fatalf("subject = %v, want what the rule's onCancelled writes", e.Payload)
	}
	if _, held, _ := srv.Store.Get(mirror.ClaimRef(id)); held {
		t.Fatal("the task's claim stands after its hook ran")
	}
	if srv.packageEngineIfRunning("demo") != nil || srv.packagesSeenAt("demo") {
		t.Fatal("the package stands after its uninstall")
	}
	if code, msg := installAt(t, url, "demo", reviewAtDemo); code != 201 {
		t.Fatalf("installing at the address once the uninstall is over = %d: %s", code, msg)
	}
}

// pruneAt posts a pruning install of doc at root, as the tester: the status and the body, and 0
// where ctx ended before it answered.
func pruneAt(ctx context.Context, t *testing.T, url, root, doc string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/charter?prune="+root, strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/tony")
	resp, err := sendAsTester(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, err.Error()
		}
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

// A PRUNING INSTALL UNINSTALLS WHAT IT LEAVES OUT, FIRST. The package the directory leaves out
// has a task running: nothing of the request is installed while the task has not ended, and once
// it has, its onCancelled has run, the package is gone and the request's is in.
func TestAPruningInstallUninstallsWhatItLeavesOutFirst(t *testing.T) {
	deaf := new(atomic.Bool)
	srv, url, id := runningPackage(t, reviewAtDemo, deaf)
	const side = `at: demo.side
rules:
- name: note
  condition: {subject: {path: "demo.side.*", value: {stage: open}}}
  action: {patch: {stage: noted}}
`
	deaf.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if code, msg := pruneAt(ctx, t, url, "demo", side); code != 0 {
		t.Fatalf("the pruning install answered %d while a task of what it leaves out was running: %s", code, msg)
	}
	if srv.packagesSeenAt("demo.side") {
		t.Fatal("the request's package was installed before the one it leaves out was gone")
	}
	if !srv.packagesSeenAt("demo") {
		t.Fatal("the package was removed while a task of it was running")
	}

	deaf.Store(false)
	if code, msg := pruneAt(t.Context(), t, url, "demo", side); code != 201 {
		t.Fatalf("the pruning install, once the runner answers = %d: %s", code, msg)
	}
	if e, _, _ := srv.Store.Get(entity.RefAt("demo", "pr", "1")); entity.Str(e.Payload, "stage") != "dropped" {
		t.Fatalf("subject = %v, want what the rule's onCancelled writes", e.Payload)
	}
	if _, held, _ := srv.Store.Get(mirror.ClaimRef(id)); held {
		t.Fatal("the task's claim stands after its hook ran")
	}
	if srv.packagesSeenAt("demo") || !srv.packagesSeenAt("demo.side") {
		t.Fatalf("after the prune: demo installed = %v, demo.side installed = %v", srv.packagesSeenAt("demo"), srv.packagesSeenAt("demo.side"))
	}
}

// A PRUNE LOCKS EVERY PACKAGE IT LEAVES OUT AT ONCE. It leaves out two: top.zed, whose task does
// not take its cancel yet, and top.alpha, behind it deepest-first. An install at top.alpha while
// the prune waits on top.zed's task is refused, the package being uninstalled, or lands once that
// uninstall is over; and one that lands stands once the prune is through, which then refuses for
// it: the prune does not take down unasked what was installed while it waited.
func TestAPruneLocksEveryPackageItLeavesOutAtOnce(t *testing.T) {
	deaf := new(atomic.Bool)
	srv, url, _ := runningPackageAt(t, "top.zed", strings.Replace(reviewAtDemo, `"demo.pr.*"`, `"top.zed.pr.*"`, 1), deaf)
	const alpha = `{name: note, condition: {subject: {path: "top.alpha.*", value: {stage: open}}}, action: {patch: {stage: noted}}}`
	if code, msg := installAt(t, url, "top.alpha", alpha); code != 201 {
		t.Fatalf("installing at top.alpha = %d: %s", code, msg)
	}
	deaf.Store(true)
	type answer struct {
		code int
		msg  string
	}
	pruned := make(chan answer, 1)
	go func() {
		code, msg := pruneAt(t.Context(), t, url, "top", "at: top.keep\nrules: []\n")
		pruned <- answer{code, msg}
	}()
	waitUntil(t, "the prune to lock top.zed", func() bool { return srv.lockedAt("", "top.zed") != nil })
	time.Sleep(200 * time.Millisecond) // the cancel is asked, and not taken
	installed, msg := installAt(t, url, "top.alpha", alpha)
	if installed != 201 && installed != 429 {
		t.Fatalf("installing at top.alpha while the prune waits = %d: %s", installed, msg)
	}
	deaf.Store(false) // the runner answers, and the prune goes on
	got := <-pruned
	if srv.packagesSeenAt("top.zed") {
		t.Fatalf("top.zed stands after the prune (%d: %s)", got.code, got.msg)
	}
	if stands := srv.packagesSeenAt("top.alpha"); stands != (installed == 201) {
		t.Fatalf("top.alpha installed = %v after an install there answered %d while the prune waited; the prune answered %d: %s",
			stands, installed, got.code, got.msg)
	}
	t.Logf("the install at top.alpha answered %d, the prune %d", installed, got.code)
}

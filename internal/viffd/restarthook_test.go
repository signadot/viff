package viffd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.viff.dev/viff/internal/trigger/charter"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd"
	"go.viff.dev/viff/pkg/runner/runnerd/runnerdtest"
	"go.viff.dev/viff/pkg/systems/systemstest"
	"go.viff.dev/viff/pkg/task"
)

// A TASK THAT OUTLIVES VIFF ENDS AS IT WOULD HAVE. A rule's task is running on a runner when
// viff restarts. When it ends, the rule's `onDone` runs and the rule lets go of the subject,
// whichever package the rule is in: the root, or one at an address. And whenever it ends: once
// viff is back, or while viff is down, where its end is the first thing the restarted viff
// hears of it, ahead of the charter its rule is in. And however long the charter takes: where
// the task ends once viff is back and the store does not answer the charter's watch until that
// end is on the task's record, the end waits for the rule, and the hook runs when the charter is
// in.
func TestATaskResumedAcrossARestartRunsItsHook(t *testing.T) {
	for _, c := range []struct {
		at   string
		down bool // the task ends while viff is down
		late bool // the charter is not read until the task has ended, on a viff instance that is back
	}{{"", false, false}, {"demo", false, false}, {"", true, false}, {"demo", true, false}, {"", false, true}, {"demo", false, true}} {
		at := c.at
		name := "a rule in the package at " + at
		if at == "" {
			name = "a rule in the root package"
		}
		if c.down {
			name += ", its task ending while viff is down"
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
				for _, rec := range entity.Below(first.Store, mirror.IsTask, 2, entity.ViffRoot, mirror.System) {
					if entity.Str(rec.Payload, "runner") == face.URL && entity.Str(rec.Payload, "status") == string(task.Running) {
						id = rec.Ref.Name()
					}
				}
				return id != ""
			})
			ts.Close()
			stop() // viff goes down; the task runs on

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

// runningPackage is a viff instance with a package installed at demo whose rule has a task running on a
// runner about demo.pr.1: the server, its url, and the task's id. While deaf is set the runner
// answers no cancel.
func runningPackage(t *testing.T, rule string, deaf *atomic.Bool) (*Server, string, string) {
	t.Helper()
	return runningPackageAt(t, "demo", rule, deaf)
}

// runningPackageAt is runningPackage with the package at an address, its task about at.pr.1.
func runningPackageAt(t *testing.T, at, rule string, deaf *atomic.Bool) (*Server, string, string) {
	t.Helper()
	return runningPackageOn(t, entitytest.Store(t), at, rule, deaf)
}

// runningPackageOn is runningPackageAt on a store the test gives.
func runningPackageOn(t *testing.T, store entity.Store, at, rule string, deaf *atomic.Bool) (*Server, string, string) {
	t.Helper()
	return runningPackageGated(t, store, at, rule, func(r *http.Request) bool {
		return deaf.Load() && strings.HasSuffix(r.URL.Path, "/cancel")
	})
}

// runningPackageGated is runningPackageOn whose runner answers no request refuse says it does
// not: a 503 from in front of it, as from an ingress whose runner is not there.
func runningPackageGated(t *testing.T, store entity.Store, at, rule string, refuse func(*http.Request) bool) (*Server, string, string) {
	t.Helper()
	release := make(chan struct{})
	build := func(l runner.Launch, _ *task.Registry) *task.Task {
		sup := systemstest.Opened(task.NewSupervisor(l.Environment, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{}), l.Environment)
		return task.NewTask(l.ID, sup, &blockedDriver{release: release}, okExecutor{})
	}
	srv := issuing(t, serverWith(t, runnerdtest.Scripted, store))
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
		if refuse(r) {
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
		for _, rec := range entity.Below(srv.Store, mirror.IsTask, 2, entity.ViffRoot, mirror.System) {
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
	} else if resp.Header.Get("Retry-After") == "" {
		t.Fatal("the 429 for a package being uninstalled does not say when to ask again")
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

// A PRUNE LOCKS EVERY PACKAGE IT LEAVES OUT AT ONCE. It leaves out two, top.zed and top.alpha,
// each with a task that does not take its cancel yet, top.alpha behind top.zed deepest-first.
// While the prune waits on them an install at top.alpha is refused, as at top.zed: neither is
// open to an install the prune would then take down unasked. Once the runner answers, both are
// gone and the request is in.
func TestAPruneLocksEveryPackageItLeavesOutAtOnce(t *testing.T) {
	deaf := new(atomic.Bool)
	srv, url, _ := runningPackageAt(t, "top.zed", reviewAt("top.zed"), deaf)
	noLocalRunner(t, srv) // top.alpha's task runs on the runner top.zed's does
	if code, msg := installAt(t, url, "top.alpha", reviewAt("top.alpha")); code != 201 {
		t.Fatalf("installing at top.alpha = %d: %s", code, msg)
	}
	pr := entity.RefAt("top", "alpha", "pr", "1")
	testerOwns(t, srv, pr)
	entitytest.Set(t, srv.Store, pr, entity.Obj{"stage": "open"}.Payload())
	waitUntil(t, "top.alpha's task to be running", func() bool {
		eng := srv.packageEngineIfRunning("top.alpha")
		return eng != nil && len(eng.Running()) > 0
	})
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
	if code, msg := installAt(t, url, "top.alpha", reviewAt("top.alpha")); code != 429 {
		t.Fatalf("installing at top.alpha while the prune waits on top.zed = %d, want 429: %s", code, msg)
	}
	deaf.Store(false) // the runner answers, and the prune goes on
	select {
	case got := <-pruned:
		if got.code != 201 {
			t.Fatalf("the prune, once the runner answers = %d: %s", got.code, got.msg)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the prune did not answer once the runner did")
	}
	if srv.packagesSeenAt("top.zed") || srv.packagesSeenAt("top.alpha") {
		t.Fatalf("after the prune: top.zed installed = %v, top.alpha installed = %v", srv.packagesSeenAt("top.zed"), srv.packagesSeenAt("top.alpha"))
	}
}

// reviewAt is reviewAtDemo for a package at another address.
func reviewAt(at string) string {
	return strings.Replace(reviewAtDemo, `"demo.pr.*"`, `"`+at+`.pr.*"`, 1)
}

// failCharterListing is a store whose listing of the charter's records fails while on is set.
type failCharterListing struct {
	entity.Store
	on atomic.Bool
}

func (f *failCharterListing) ListAtErr(at []string, depth int, pred func(entity.Ref) bool) ([]entity.Entity, error) {
	if f.on.Load() && slices.Equal(at, []string{entity.ViffRoot, charter.System}) {
		return nil, errFor("context deadline exceeded")
	}
	return entity.ListAtErr(f.Store, at, depth, pred)
}

// AN INSTALL THAT CANNOT READ WHAT IS INSTALLED IS REFUSED, and takes nothing down. A package
// at demo has a task running; the listing of the charter fails as an install at demo is made
// that is then refused. Taken for fresh on that misread, the package would be stopped with the
// refusal and its task cancelled with no hook.
func TestAnInstallThatCannotListTheCharterLeavesARunningPackageAlone(t *testing.T) {
	store := &failCharterListing{Store: entitytest.Store(t)}
	srv, url, id := runningPackageOn(t, store, "demo", reviewAtDemo, new(atomic.Bool))
	store.on.Store(true)
	code, msg := installAt(t, url, "demo", `{name: broken, condition: {subject: {path: "demo.pr.*", value: {stage: open}}}, action: {patch: {stage: x}, onDone: {patch: {stage: y}}}}`)
	store.on.Store(false)
	if code < 400 {
		t.Fatalf("the install = %d, want it refused: %s", code, msg)
	}
	time.Sleep(300 * time.Millisecond)
	if got := recordStatus(srv, id); got != string(task.Running) {
		t.Fatalf("the package's task is %q after a refused install at it (%d: %s), want it running", got, code, msg)
	}
	if eng := srv.packageEngineIfRunning("demo"); eng == nil || len(eng.Triggers()) != 1 {
		t.Fatalf("the package's engine or rule is gone after a refused install at it (%d: %s)", code, msg)
	}
	t.Logf("refused %d: %s", code, msg)
}

// AN UNINSTALL WAITS OUT A RUNNER IT CANNOT REACH. Nothing the runner is asked is answered: the
// cancel, and the subscribes that keep the task. The uninstall does not finish and the task's
// record is not ended for it, the package stays installed and locked, and once the runner answers
// again the cancel lands, the task's onCancelled runs, and the package is removed.
func TestAnUninstallWaitsOutARunnerItCannotReach(t *testing.T) {
	down := new(atomic.Bool)
	srv, url, id := runningPackageGated(t, entitytest.Store(t), "demo", reviewAtDemo, func(*http.Request) bool { return down.Load() })
	pr := entity.RefAt("demo", "pr", "1")
	down.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := srv.uninstall(ctx, srv.Store, "demo"); err == nil {
		t.Fatal("the uninstall answered as done while its task's runner could not be reached")
	}
	if got := recordStatus(srv, id); got != string(task.Running) {
		t.Fatalf("the task's record says %q, want running: nothing has said it ended", got)
	}
	if eng := srv.packageEngineIfRunning("demo"); eng == nil || !srv.packagesSeenAt("demo") {
		t.Fatal("the package was removed while its task's runner could not be reached")
	}
	if code, msg := installAt(t, url, "demo", reviewAtDemo); code != 429 {
		t.Fatalf("installing at the package meanwhile = %d, want 429: %s", code, msg)
	}

	down.Store(false)
	if err := srv.uninstall(t.Context(), srv.Store, "demo"); err != nil {
		t.Fatalf("the uninstall, once the runner answers: %v", err)
	}
	if e, _, _ := srv.Store.Get(pr); entity.Str(e.Payload, "stage") != "dropped" {
		t.Fatalf("subject = %v, want what the rule's onCancelled writes", e.Payload)
	}
	if srv.packageEngineIfRunning("demo") != nil || srv.packagesSeenAt("demo") {
		t.Fatal("the package stands after its uninstall")
	}
}

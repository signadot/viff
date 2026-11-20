package versed

import (
	"context"
	"net/http/httptest"
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

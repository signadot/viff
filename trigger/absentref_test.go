package trigger

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"
)

// A SPAWN NAMING AN ABSENT FIELD UNDER A BOUND ONE IS REFUSED, and the refusal is a failed
// task: the rule's own patch lands, the action's onFailed runs with the refusal as `$err`, and
// nothing is started: the Spawner is given the refusal (launch.Spec.Refusal), to record the
// task as failed, and starts nothing.
//
// It used to launch. On a subject `{stage: absent}`, `deep=[$subject.value.never]` reached the
// agent as `deep=[stage: absent.never]` — the shorter key `subject.value` and the rest of the
// reference — which is how a pull request whose body read a cleared field got the whole work
// record as its body. A patch naming the same reference has always written nothing; a spawn
// refuses too, but after the firing's writes, so the subject is written down as failed by its
// own onFailed rather than left latched with nothing saying why.
func TestASpawnNamingAnAbsentPathIsRefusedAsAFailedTask(t *testing.T) {
	for _, c := range []struct{ name, prompt, ref string }{
		{"under subject.value", "never=[$never] deep=[$subject.value.never]", "$subject.value.never"},
		{"under a top-level field", "flat=[$changes.never]", "$changes.never"},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := entitytest.Store(t)
			eng := New(store, task.AutoResolver{})
			var mu sync.Mutex
			var launched, refused []string
			eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
				mu.Lock()
				defer mu.Unlock()
				// As versed's spawner does: a refused launch starts nothing and is a failed task.
				if spec.Refusal != "" {
					refused = append(refused, spec.Refusal)
					return task.FailedHandle(spec.Refusal)
				}
				launched = append(launched, spec.Prompt)
				return startHookTask(store, id, spec)
			})
			if err := eng.AddTrigger(mustParseCharter(t, `
- name: develop
  condition:
    subject: {path: "demo.pr.*", value: {stage: open}}
  action:
    patch: {stage: developing}
    spawn:
      agent: {name: verse}
      autonomy: [{then: allow}]
      prompt: "`+c.prompt+`"
    onFailed:
      patch: {stage: failed, why: $err}
`)); err != nil {
				t.Fatalf("AddTrigger: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			eng.Start(ctx)
			entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open", "changes": map[string]any{"a": 1}}.Payload())

			waitField(t, store, prRef("1"), "stage", "failed")
			e, _, _ := store.Get(prRef("1"))
			why := entity.Str(e.Payload, "why")
			if !strings.Contains(why, "prompt: "+c.ref) || !strings.Contains(why, "not present") {
				t.Errorf("why = %q, want it to name the reference, %s, and say it is not present", why, c.ref)
			}
			if strings.Contains(why, "$never]") || strings.Contains(why, "$never,") {
				t.Errorf("why = %q names the bare $never, which is left as written and not refused", why)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(launched) != 0 {
				t.Errorf("the spawner was asked to launch %q", launched)
			}
			if len(refused) != 1 || !strings.Contains(refused[0], c.ref) {
				t.Errorf("the spawner was given refusals %q, want the one naming %s, so it records the task", refused, c.ref)
			}
			found := false
			for _, tr := range eng.Troubles() {
				found = found || (tr.Rule == "develop" && strings.Contains(tr.Why, c.ref))
			}
			if !found {
				t.Errorf("the refusal is not kept on the rule: %+v", eng.Troubles())
			}
		})
	}
}

// A BARE `$name` NOTHING BINDS IS LEFT AS WRITTEN, and the spawn launches: in prose or a shell
// value it is as likely `$HOME` or `$5` as a reference, so it cannot be refused on. And what
// follows a field holding a leaf is prose: `$stage.go` is the stage and `.go`.
func TestASpawnNamingAnAbsentTopLevelFieldLaunchesWithItAsWritten(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	specs := make(chan launch.Spec, 4)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle { specs <- spec; return nil })
	if err := eng.AddTrigger(mustParseCharter(t, `
- name: develop
  condition:
    subject: {path: "demo.pr.*", value: {stage: open}}
  action:
    patch: {stage: developing}
    spawn:
      agent: {name: verse}
      autonomy: [{then: allow}]
      prompt: "never=[$never] home=[$HOME] stage=[$stage] file=[$stage.go]"
`)); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())

	select {
	case spec := <-specs:
		if want := "never=[$never] home=[$HOME] stage=[open] file=[open.go]"; spec.Prompt != want {
			t.Fatalf("prompt = %q, want %q", spec.Prompt, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no spawn")
	}
}

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
// A patch naming the same reference writes nothing; a spawn refuses too, but after the firing's
// writes, so the subject is written down as failed by its own onFailed rather than left latched
// with nothing saying why.
func TestASpawnNamingAnAbsentPathIsRefusedAsAFailedTask(t *testing.T) {
	for _, c := range []struct{ name, prompt, absent string }{
		{"under subject.value", "never=[$never] deep=[$[subject.value.never]]", `no "never" in subject.value`},
		{"under a top-level field", "flat=[$[changes.never]]", `no "never" in changes`},
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
			if !strings.Contains(why, "prompt: ") || !strings.Contains(why, c.absent) {
				t.Errorf("why = %q, want it to name the field and say %s", why, c.absent)
			}
			if strings.Contains(why, "$never]") || strings.Contains(why, "$never,") {
				t.Errorf("why = %q names the bare $never, which is left as written and not refused", why)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(launched) != 0 {
				t.Errorf("the spawner was asked to launch %q", launched)
			}
			if len(refused) != 1 || !strings.Contains(refused[0], c.absent) {
				t.Errorf("the spawner was given refusals %q, want the one saying %s, so it records the task", refused, c.absent)
			}
			found := false
			for _, tr := range eng.Troubles() {
				found = found || (tr.Rule == "develop" && strings.Contains(tr.Why, c.absent))
			}
			if !found {
				t.Errorf("the refusal is not kept on the rule: %+v", eng.Troubles())
			}
		})
	}
}

// A `$` THAT DOES NOT OPEN `$[` IS LEFT AS WRITTEN, and the spawn launches: in prose or a shell
// value it is `$HOME` or `$5`, not a reference. And what follows an expression is prose:
// `$[stage].go` is the stage and `.go`.
func TestADollarThatIsNotAnExpressionLaunchesAsWritten(t *testing.T) {
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
      prompt: "never=[$never] home=[$HOME] stage=[$[stage]] file=[$[stage].go]"
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

package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
)

// A HOOK NAMING A SUBJECT FIELD FLAT IS ADVISED AT INSTALL.
//
// `$covering` in an onDone is the subject's field written in the one place the subject's fields
// are not flat: a hook is handed the TASK's record. The reference does not resolve, the patch is
// refused, and the subject stays exactly where it was (git-issue 91jktrr3h12ksa8fgsn0).
func TestAHookNamingASubjectFieldIsAdvised(t *testing.T) {
	notes := advise(Trigger{
		Name:      "claim",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch:  entity.Obj{"stage": "claimed"}.Payload(),
			Spawn:  &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $[subject.ref]"},
			OnDone: &Action{Patch: entity.Obj{"sha": "$covering"}.Payload()},
		},
	})
	var found string
	for _, n := range notes {
		if strings.Contains(n, "$covering") {
			found = n
		}
	}
	if found == "" {
		t.Fatalf("no note about $covering in a hook:\n%s", strings.Join(notes, "\n"))
	}
	for _, want := range []string{"onDone", "$subject.value.covering", "writes nothing"} {
		if !strings.Contains(found, want) {
			t.Errorf("the note does not carry %q:\n%s", want, found)
		}
	}
}

// The task's OWN vocabulary is not advised: `$status`, `$result`, `$err` and the rest are what a
// hook is for, and a note on them would be noise on every correct charter in the tree.
func TestAHookNamingTheTaskIsNotAdvised(t *testing.T) {
	for name := range mirror.RecordFields {
		notes := advise(Trigger{
			Name:      "claim",
			Condition: Condition{Subject: prSubject("open")},
			Action: Action{
				Spawn:  &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $[subject.ref]"},
				OnDone: &Action{Patch: entity.Obj{"saw": "$" + name}.Payload()},
			},
		})
		for _, n := range notes {
			if strings.Contains(n, "$"+name+",") {
				t.Errorf("$%s is a task record field and earned a note: %s", name, n)
			}
		}
	}
}

// A dotted reference is not flat and is left alone — `$subject.value.x` is the spelling the note
// asks for, and `$data.x` walks the answer.
func TestADottedHookReferenceIsNotAdvised(t *testing.T) {
	notes := advise(Trigger{
		Name:      "claim",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $[subject.ref]"},
			OnDone: &Action{Patch: entity.Obj{
				"sha": "$subject.value.covering", "sev": "$data.severity",
			}.Payload()},
		},
	})
	for _, n := range notes {
		if strings.Contains(n, "a task record does not carry") {
			t.Errorf("a dotted reference earned a flat-name note: %s", n)
		}
	}
}

// AND THE FIRING SAYS SO WHEN IT HAPPENS, on the task's own record. The advice is for the
// charter being written; this is for the one already running.
func TestAHookThatCannotResolveWritesTheFailureDown(t *testing.T) {
	eng, store := structuredEngine(t, structured{r: "{ok: true}"})
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch:  entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn:  &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $[subject.ref]"},
			OnDone: &Action{Patch: entity.Obj{"sha": "$covering"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v — this is advised, not refused", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open", "covering": "aaa111"}.Payload())

	pr := mirror.Ref("review@demo.pr.1")
	waitPath(t, store, pr, "hook.status", "failed")
	e, _, _ := store.Get(pr)
	got, _ := e.Payload.GetKPath("hook.err")
	if got == nil || !strings.Contains(got.String, "$covering") {
		t.Fatalf("hook.err = %v, want it to name the reference that did not resolve", got)
	}
	// And the subject did NOT advance, which is the cost the note is about.
	s, _, _ := store.Get(prRef("1"))
	if stage := entity.Str(s.Payload, "stage"); stage != "reviewing" {
		t.Fatalf("stage = %q — the refused patch should have left the subject where it was", stage)
	}
}

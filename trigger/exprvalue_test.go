package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// THE OLD SPELLING IS REFUSED IN A DERIVE as in a patch (TestTheOldSpellingIsRefusedAtInstall): its
// content is written into the store as a patch's is.
func TestTheOldSpellingIsRefusedInADerive(t *testing.T) {
	trigs, _, strata, _, ok := parseBlock(`
- name: reflected
  level: 0
  match: {path: "git"}
- name: findings
  level: 10
  match: {path: "drift"}
---
- name: old
  stratum: reflected
  condition:
    subject: {path: "git.commit.*"}
  action:
    derive: [{path: "drift.head", value: {sha: $subject.value.sha}}]
`)
	if !ok {
		t.Fatal("did not parse as a charter")
	}
	eng := New(entitytest.Store(t), task.AutoResolver{})
	for _, s := range strata {
		if err := eng.addStratum(s); err != nil {
			t.Fatalf("stratum %q: %v", s.Name, err)
		}
	}
	err := eng.addTrigger(trigs[0], "", RunnersChecked)
	if err == nil || !strings.Contains(err.Error(), ".[subject.value.sha]") {
		t.Errorf("install = %v, want the old spelling refused with .[subject.value.sha]", err)
	}
}

// AN `!each` KEY IS AN EXPRESSION, so it may compose the element's fields; and a key that comes
// out null names no address, so the firing is refused rather than writing every such element to
// one place called "null".
func TestEachKeyComposesAndRefusesNull(t *testing.T) {
	engineFor := func(t *testing.T, key string) (*Engine, entity.Store, Trigger) {
		t.Helper()
		trigs, _, strata, _, ok := parseBlock(`
- name: reflected
  level: 0
  match: {path: "git"}
- name: findings
  level: 10
  match: {path: "drift"}
---
- name: fan
  stratum: reflected
  condition:
    subject: {path: "git.commit.*", value: {reviewed: pending}}
  action:
    patch: {reviewed: done}
    derive: [{path: "drift.finding", value: !each {over: .[subject.value.f], key: ` + key + `, value: {was: .[item.was]}}}]
`)
		if !ok {
			t.Fatal("did not parse as a charter")
		}
		store := entitytest.Store(t)
		eng := New(store, task.AutoResolver{})
		for _, s := range strata {
			if err := eng.addStratum(s); err != nil {
				t.Fatalf("stratum %q: %v", s.Name, err)
			}
		}
		if err := eng.addTrigger(trigs[0], "", RunnersChecked); err != nil {
			t.Fatalf("does not install: %v", err)
		}
		return eng, store, trigs[0]
	}
	subj := entity.RefAt("git", "commit", "verse/abc")
	fire := func(t *testing.T, eng *Engine, store entity.Store, tr Trigger, findings string) error {
		t.Helper()
		payload, err := codec.ParseNode([]byte(`{reviewed: pending, f: `+findings+`}`), codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Write(entity.Write{At: subj.At(), Value: payload}); err != nil {
			t.Fatal(err)
		}
		oc := occasion{commit: entity.Commit{Rev: store.Rev()}, snap: &snapCache{}, ev: entity.Event{Ref: subj, To: payload}}
		_, err = tr.Action.applyOne("fan", oc, subj, eng, fromSubject)
		return err
	}

	t.Run("composed", func(t *testing.T) {
		eng, store, tr := engineFor(t, `'.[item.sym + "-" + item.was]'`)
		if err := fire(t, eng, store, tr, `[{sym: a, was: x} {sym: b, was: y}]`); err != nil {
			t.Fatalf("firing failed: %v", err)
		}
		var keys []string
		for _, e := range store.ListAt([]string{"drift", "finding"}, 2+1+len(subj.Segs())+1, nil) {
			segs := e.Ref.Segs()
			keys = append(keys, segs[len(segs)-1])
		}
		if strings.Join(keys, ",") != "a-x,b-y" {
			t.Errorf("keys = %v, want a-x and b-y", keys)
		}
	})

	t.Run("null", func(t *testing.T) {
		eng, store, tr := engineFor(t, `.[item.sym]`)
		err := fire(t, eng, store, tr, `[{sym: a, was: x} {sym: null, was: y}]`)
		if err == nil {
			t.Fatal("the firing succeeded; an element whose key is null must fail it")
		}
		if !strings.Contains(err.Error(), "element 1") {
			t.Errorf("failed with %q, want it to name which element", err)
		}
		if got := store.ListAt([]string{"drift", "finding"}, 2+1+len(subj.Segs())+1, nil); len(got) != 0 {
			t.Errorf("a refused fan-out wrote %d entities", len(got))
		}
	})
}

// A HOOK'S `data` IS ALWAYS THERE: null for a task whose answer is no document. So `.[data]`
// writes null — the task said nothing structured — where reading into it would be a refusal.
func TestAHookWritesNullDataForATaskWithNoDocument(t *testing.T) {
	eng, store := hookResultEngine(t, "SHIPIT")
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch: entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $[subject.ref]"},
			OnDone: &Action{
				Patch: entity.Obj{"stage": "reviewed", "report": ".[data]", "said": ".[result]"}.Payload(),
			},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "reviewed")
	e, _, _ := store.Get(prRef("1"))
	report := entity.Get(e.Payload, "report")
	if report == nil || report.Type != ir.NullType {
		t.Errorf("report = %v, want a written null", report)
	}
	if s := entity.Str(e.Payload, "said"); s != "SHIPIT" {
		t.Errorf("said = %q, want the task's text", s)
	}
}

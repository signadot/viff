package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/launch"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/task"
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
    derive: [{path: "drift.finding", value: !each {over: .[subject.value.f], key: ` + key + `, value: {was: .[item.was], of: .[subject.value.reviewed]}}}]
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
			// Every element reads the one subject the firing read, at the causing commit.
			if of := entity.Str(e.Payload, "of"); of != "pending" {
				t.Errorf("%s: of = %q, want the subject's value at the firing", e.Ref, of)
			}
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

// AN EXPRESSION IS PARSED AT INSTALL, in a patch and in a derive: one that does not parse would
// fail every firing of the rule, and a charter that cannot fire is the author's to hear about.
func TestAnExpressionThatDoesNotParseIsRefusedAtInstall(t *testing.T) {
	for why, patch := range map[string]entity.Payload{
		"a whole value":  entity.Obj{"x": ".[subject.value.]"}.Payload(),
		"text":           entity.Obj{"x": "see $[subject.value.(]"}.Payload(),
		"under a field":  entity.Obj{"x": entity.Obj{"y": ".[1 +]"}.Payload()}.Payload(),
		"in a list item": entity.Obj{"x": []any{"ok", ".[)]"}}.Payload(),
	} {
		eng, _ := newEng(t)
		err := eng.AddTrigger(Trigger{
			Name:      "unparsed",
			Condition: Condition{Subject: prSubject("open")},
			Action:    Action{Patch: patch},
		})
		if err == nil || !strings.Contains(err.Error(), "does not parse") {
			t.Errorf("%s: install = %v, want it refused as not parsing", why, err)
		}
	}
}

// A WHOLE VALUE THAT IS A MAP OR A LIST LITERAL IS REFUSED: inside it, a field that is not there
// is null, and the literal would write that null where a plain path refuses the firing. The
// refusal spells the value in tony, which holds each field to the rule. A literal deeper in an
// expression builds what tony cannot, and installs.
func TestAWholeMapOrListLiteralIsRefusedAtInstall(t *testing.T) {
	charter := func(part, value string) string {
		return `
- name: reflected
  level: 0
  match: {path: "git"}
- name: findings
  level: 10
  match: {path: "drift"}
---
- name: lit
  stratum: reflected
  condition:
    subject: {path: "git.commit.*"}
  action:
` + map[string]string{
			"patch":  "    patch: {x: " + value + "}\n",
			"derive": "    derive: [{path: \"drift.head\", value: {x: " + value + "}}]\n",
		}[part]
	}
	install := func(t *testing.T, src string) error {
		t.Helper()
		trigs, _, strata, _, ok := parseBlock(src)
		if !ok {
			t.Fatalf("did not parse as a charter:\n%s", src)
		}
		eng := New(entitytest.Store(t), task.AutoResolver{})
		for _, s := range strata {
			if err := eng.addStratum(s); err != nil {
				t.Fatalf("stratum %q: %v", s.Name, err)
			}
		}
		return eng.addTrigger(trigs[0], "", RunnersChecked)
	}
	for _, part := range []string{"patch", "derive"} {
		for value, want := range map[string]string{
			`'.[{sha: subject.value.sha, n: subject.value.n}]'`: "{sha: .[subject.value.sha], n: .[subject.value.n]}",
			`'.[[subject.value.sha, subject.name]]'`:            "[.[subject.value.sha], .[subject.name]]",
		} {
			err := install(t, charter(part, value))
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s %s: install = %v, want it refused with the tony spelling %s", part, value, err, want)
			}
		}
		deeper := `'.[map(subject.value.items, {{id: #.id}})]'`
		if err := install(t, charter(part, deeper)); err != nil {
			t.Errorf("%s %s: refused (%v); a literal under a call builds what tony cannot", part, deeper, err)
		}
	}
}

// THE TONY SPELLING HOLDS EACH FIELD TO THE RULE: `{findings: .[…]}` with nothing there refuses
// the firing, where the expression-built map would have written `findings: null`.
func TestAFieldThatIsNotThereRefusesTheFiring(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	tr := Trigger{
		Name:      "absent",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{Patch: entity.Obj{
			"stage": "done",
			"f":     entity.Obj{"findings": ".[subject.value.missing]"}.Payload(),
		}.Payload()},
	}
	if err := eng.AddTrigger(tr); err != nil {
		t.Fatalf("does not install: %v", err)
	}
	subj := entity.RefAt("demo", "pr", "1")
	payload := entity.Obj{"stage": "open"}.Payload()
	if _, _, err := store.Write(entity.Write{At: subj.At(), Value: payload}); err != nil {
		t.Fatal(err)
	}
	oc := occasion{commit: entity.Commit{Rev: store.Rev()}, snap: &snapCache{}, ev: entity.Event{Ref: subj, To: payload}}
	_, err := tr.Action.applyOne("absent", oc, subj, eng, fromSubject)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("firing = %v, want it refused for the absent field", err)
	}
}

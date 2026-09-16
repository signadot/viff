package trigger

import (
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/mergeop"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// restart stands a second engine up over the same substrate, the way `verse up` does after
// `verse down`: a fresh store client, a fresh engine, the charter read back from the verse.
// The old engine's memory is gone; only what the store holds crosses the gap.
func restart(t *testing.T, addr string) (*Engine, entity.Store) {
	t.Helper()
	store := entitytest.StoreAt(t, addr)
	eng := New(store, task.AutoResolver{})
	eng.ObserveScoped(t.Context()) // versed's order: observe, then restore into it
	if err := eng.LoadCharter(); err != nil {
		t.Fatalf("LoadCharter: %v", err)
	}
	return eng, store
}

// A trigger installed before a restart still fires after it. This is the whole point: a
// verse whose tasks, sequence positions and claims all survive but whose CHARTER does
// not comes back up with its work in progress intact and nothing left to react to it.
func TestCharterSurvivesARestart(t *testing.T) {
	addr := entitytest.Addr(t)
	first := entitytest.StoreAt(t, addr)
	eng := New(first, task.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	_, store := restart(t, addr)

	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	waitStage(t, store, ref, "reviewed")
}

// A restored trigger acts on the backlog standing in the verse — the same level-triggered
// replay a fresh install has (docs/change-events.md). So a change that arrived while verse
// was down is not lost: the rule that comes back finds it waiting.
func TestRestoredTriggerActsOnWhatArrivedWhileDown(t *testing.T) {
	addr := entitytest.Addr(t)
	first := entitytest.StoreAt(t, addr)
	eng := New(first, task.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	// Down: nothing is observing. The PR opens anyway.
	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, first, ref, entity.Obj{"stage": "open"}.Payload())

	_, store := restart(t, addr)
	waitStage(t, store, ref, "reviewed")
}

// A removal is as durable as an install. Otherwise `verse c rm` is a pause: the
// rule stops firing until the next start, then comes back with nobody having reinstalled it.
func TestRemovedTriggerStaysRemoved(t *testing.T) {
	addr := entitytest.Addr(t)
	first := entitytest.StoreAt(t, addr)
	eng := New(first, task.AutoResolver{})
	trig := Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}
	if err := eng.AddTrigger(trig); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	if !eng.RemoveTrigger("review") {
		t.Fatal("RemoveTrigger reported the trigger did not exist")
	}

	back, store := restart(t, addr)
	if got := back.Triggers(); len(got) != 0 {
		t.Fatalf("charter after restart = %v; a removed rule came back", got)
	}
	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	time.Sleep(200 * time.Millisecond)
	if e, _, _ := store.Get(ref); entity.ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("%s stage = %v; the removed rule fires again after a restart", ref, entity.ToObj(e.Payload)["stage"])
	}
}

// The rule that comes back is the rule that went in — operators and all. A charter is full of
// tony tags (`id: !glob`, `patch: {tmp: !delete null}`), and they are what a structural
// payload could not survive until `!raw`: the store's write is a tony PATCH, so those tags
// were EXECUTED by the write rather than stored by it. Under the escape they are stored, so
// the rule is an ordinary document on its entity rather than a string holding one.
func TestCharterRoundTripsOperators(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	// Written the way a charter is, so the operators are the real thing.
	var trig Trigger
	if err := codec.Unmarshal([]byte(`{
		name: hotfix,
		condition: {
			when:    {path: "demo.pr.*", value: {stage: open}},
			subject: {path: "demo.pr.*", value: {stage: open, branch: !glob "hotfix-*"}},
		},
		gates:  [{name: approval, mode: ask}],
		action: {
			patch:  {stage: reviewed, tmp: !delete null},
			spawn:  {
				driver: llm,
				prompt: "review ",
				answer: {class: !or [bug, nit], severity: !or [low, high]},
			},
			onDone: {patch: {stage: shipped}},
		},
	}`), &trig, codec.Tony); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := eng.AddTrigger(trig); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	installed, _ := eng.Trigger("hotfix") // post-desugar: what the engine actually runs

	ent, ok, err := store.Get(CharterRef("hotfix"))
	if err != nil || !ok {
		t.Fatalf("the charter entity does not read back: ok=%v err=%v", ok, err)
	}
	// The spec is STRUCTURE on the entity — a document to navigate, not a string to re-parse.
	spec := entity.Get(ent.Payload, specField)
	if spec == nil || !entity.IsObj(spec) {
		t.Fatalf("%s = %v; want the rule as an object", specField, spec)
	}
	// The subject's path is stored as the kinded path it was written as, and the operator its
	// PREDICATE carries survives as a tag — which is where an operator lives now.
	subject := ir.Get(ir.Get(spec, "condition"), "subject")
	if path := ir.Get(subject, "path"); path == nil || path.String != "demo.pr.*" {
		t.Fatalf("condition.subject.path = %v; want the kinded path it was written with", path)
	}
	if br := ir.Get(ir.Get(subject, "value"), "branch"); br == nil || br.Tag != "!glob" {
		t.Fatalf("the predicate's branch = %v; the operator did not survive as a tag", br)
	}
	back, err := ruleOf(ent)
	if err != nil {
		t.Fatalf("ruleOf: %v", err)
	}
	want, err := installed.ToTonyIR()
	if err != nil {
		t.Fatal(err)
	}
	got, err := back.ToTonyIR()
	if err != nil {
		t.Fatal(err)
	}
	// Compared as DATA (mergeop.RawEqual: tags compared, not evaluated), because the store
	// keeps a value, not a rendering — the layout tags that decide whether an object is
	// written `{a: 1}` or over two lines are not part of what a rule says.
	if !mergeop.RawEqual(got, want) {
		gotDoc, _ := back.ToTony()
		wantDoc, _ := installed.ToTony()
		t.Fatalf("the rule came back changed:\n got: %s\nwant: %s", gotDoc, wantDoc)
	}
	doc, _ := back.ToTony()
	if !strings.Contains(string(doc), "!delete") || !strings.Contains(string(doc), "hotfix-*") {
		t.Fatalf("the round trip dropped an operator: %s", doc)
	}
	// A spawn's ANSWER CONTRACT is a pattern like any other, and it is the reason the field
	// is a document rather than the text of one: it survives the store as the operators it
	// was written as, so what the driver enforces is what the charter said.
	if _, err := answer.Parse(back.Action.Spawn.Answer); err != nil {
		t.Fatalf("the stored answer contract is not usable: %v", err)
	}
	if cls := ir.Get(ir.Get(ir.Get(ir.Get(spec, "action"), "spawn"), "answer"), "class"); cls == nil {
		t.Fatalf("action.spawn.answer.class is not on the entity: %s", doc)
	}
}

// A re-install REPLACES the stored rule rather than merging into it. The spec is written
// under entity.Raw, and that is what a record holding a document needs: merged, a field the
// new version does not have would go on standing in it — a rule firing on a condition nobody
// wrote, which nothing in the charter file would explain.
func TestReinstallReplacesTheRule(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	trig := Trigger{
		Name:      "review",
		Condition: Condition{Subject: mm("demo.pr.*")},
		Gates:     []Gate{{Name: "approval", Mode: "ask"}},
		Action:    setStage("reviewed"),
	}
	if err := eng.AddTrigger(trig); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	if !eng.RemoveTrigger("review") {
		t.Fatal("RemoveTrigger reported the trigger did not exist")
	}
	trig.Gates = nil // the gate is gone from the rule
	if err := eng.AddTrigger(trig); err != nil {
		t.Fatalf("AddTrigger (again): %v", err)
	}

	ent, ok, _ := store.Get(CharterRef("review"))
	if !ok {
		t.Fatal("the charter entity does not read back")
	}
	if g := ir.Get(entity.Get(ent.Payload, specField), "gates"); g != nil {
		t.Fatalf("gates = %v; the removed gate survived the re-install", g)
	}
}

// A charter entity nothing can read is reported and SKIPPED, not fatal: the rest of the
// charter is still a program worth running, and one unreadable rule must not cost it.
func TestUnreadableRuleIsReportedAndSkipped(t *testing.T) {
	addr := entitytest.Addr(t)
	first := entitytest.StoreAt(t, addr)
	eng := New(first, task.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    setStage("reviewed"),
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, first, CharterRef("junk"), entity.Obj{specField: "this is not a trigger: [ {"}.Payload())

	store := entitytest.StoreAt(t, addr)
	back := New(store, task.AutoResolver{})
	err := back.LoadCharter()
	if err == nil {
		t.Fatal("LoadCharter reported success over an undecodable rule")
	}
	if !strings.Contains(err.Error(), "junk") {
		t.Fatalf("LoadCharter error does not name the rule that failed: %v", err)
	}
	if got := back.Triggers(); len(got) != 1 || got[0].Name != "review" {
		t.Fatalf("charter = %v; one bad rule cost the good one", got)
	}
}

// A rule whose document disagrees with its address is refused rather than reconciled:
// installing under the name and deleting under the address would leave a rule that
// `verse c rm` cannot get rid of.
func TestRuleNameMustMatchItsAddress(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	trig := Trigger{
		Name:      "review",
		Condition: Condition{Subject: mm("demo.pr.*")},
		Action:    setStage("reviewed"),
	}
	doc, err := trig.ToTonyIR()
	if err != nil {
		t.Fatal(err)
	}
	entitytest.Set(t, store, CharterRef("elsewhere"), entity.Obj{specField: entity.Raw(doc)}.Payload())

	if err := eng.LoadCharter(); err == nil {
		t.Fatal("a rule named 'review' installed from trigger.rule.elsewhere")
	}
	if got := eng.Triggers(); len(got) != 0 {
		t.Fatalf("charter = %v; the misaddressed rule installed anyway", got)
	}
}

// THE ABSENCE LATCH SURVIVES BEING WRITTEN DOWN.
//
// trigger-model.md names `value: !not {x: null}` as the latch for a rule whose written value it
// cannot predict — "ask for the ABSENCE of what the action writes, which latches whatever value
// lands" — and every spawning rule needs a latch. It could not be stored: verse spelled a
// removed field with a bare null and hid every bare null it read, at any depth, `!raw` included,
// so the subject came back as `!not {}`.
//
// Which is the part that made it silent. `!not {}` is not a broken pattern; `{}` matches any
// OBJECT, so its negation matches every payload that is not a record — and since every entity in
// a slice like this is a record, the rule installed cleanly, watched the right subtree, and
// selected nothing. From the first restart onward, with no error anywhere.
//
// This test would have failed on every build verse has ever had.
func TestAbsenceLatchSurvivesTheCharterStore(t *testing.T) {
	store := entitytest.Store(t)
	src := `
- name: latched
  condition:
    subject:
      path: "drift.head.*"
      value: !not
        reviewed: null
  action:
    spawn: {driver: scripted, prompt: "review it"}
    onDone: {patch: {reviewed: done}}
`
	trigs, _, _, _, ok := parseBlock(src)
	if !ok || len(trigs) != 1 {
		t.Fatalf("parse: ok=%v n=%d", ok, len(trigs))
	}
	written, _ := codec.Marshal(trigs[0].Condition.Subject, codec.Tony)

	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(trigs[0]); err != nil { // durable: this is the write under test
		t.Fatalf("install: %v", err)
	}

	// What a restart does.
	back := New(store, task.AutoResolver{})
	if err := back.LoadCharter(); err != nil {
		t.Fatalf("load: %v", err)
	}
	loaded := back.Triggers()
	if len(loaded) != 1 {
		t.Fatalf("loaded %d rules", len(loaded))
	}
	restored, _ := codec.Marshal(loaded[0].Condition.Subject, codec.Tony)
	if string(restored) != string(written) {
		t.Fatalf("the subject did not survive the store:\n  written:  %s\n  restored: %s",
			written, restored)
	}

	// And it still means what it said: absent selects, present does not.
	m := loaded[0].Condition.Subject
	absent := entity.Event{
		Ref: entity.RefAt("drift", "head", "verse"),
		To:  entity.Obj{"sha": "abc"}.Payload(),
	}
	present := entity.Event{
		Ref: absent.Ref,
		To:  entity.Obj{"sha": "abc", "reviewed": "done"}.Payload(),
	}
	if !m.MatchEvent(absent) {
		t.Error("a subject with no `reviewed` is not selected — the latch would never fire")
	}
	if m.MatchEvent(present) {
		t.Error("a subject that HAS `reviewed` is still selected — the latch would never hold")
	}
}

// A RULE KEEPS THE COMMENTS IT WAS WRITTEN WITH, through the store and a restart.
//
// The record is meant to be the rule as its author left it, and until go-tony v0.0.142 it could
// not be: a rule is persisted from this struct (`t.ToTonyIR()`), so everything the struct itself
// carried lost its comments while a raw-node field like `action.patch` kept them — the arbitrary
// half of a document surviving, which is worse than none of it.
//
// The path this walks is the real one: decode as an install does, persist, then read back as
// LoadCharter does.
func TestARuleKeepsItsComments(t *testing.T) {
	eng, store := newEng(t)
	src := []byte("# why this rule exists, in the author's words\n" +
		"name: commented\n" +
		"condition:\n  subject: {path: \"demo.pr.*\", value: { stage: open }}\n" +
		"action:\n  patch: { stage: reviewing }\n")
	node, err := codec.ParseNode(src, codec.Tony)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var installed Trigger
	if err := installed.FromTonyIR(node); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(installed.Doc) == 0 {
		t.Fatal("the decode dropped the rule's comment — comment= is not reaching Trigger.Doc")
	}
	if err := eng.AddTrigger(installed); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	// …and back out the way a restart reads it.
	ent, ok, err := store.Get(CharterRef("commented"))
	if err != nil || !ok {
		t.Fatalf("the charter entity: ok=%v err=%v", ok, err)
	}
	back, err := ruleOf(ent)
	if err != nil {
		t.Fatalf("ruleOf: %v", err)
	}
	if len(back.Doc) == 0 {
		t.Fatal("the rule came back with no comment — the store or the read stripped it")
	}
	if back.Doc[0] != "# why this rule exists, in the author's words" {
		t.Fatalf("Doc = %q, want the line the author wrote", back.Doc)
	}
	// The comment is not DATA: it does not become a field, and the rule is otherwise identical.
	if back.Name != "commented" || !isPath(back.Condition.Subject, "demo.pr.*") {
		t.Fatalf("the rule itself did not survive: %+v", back)
	}
}

// EVERY POSITION A RULE HAS, head comment and line comment alike, through the store.
//
// The line beside a value was the half nothing carried. A struct-level `lineComment=` holds the
// note beside the RULE — `- {name: r} # …`, the way a list of them is written — and there is no
// such thing for `name: r # the short name`, because a scalar field has no type of its own to
// hang it on. So a charter installed from a file came back with the lines ABOVE its fields and
// none of the lines BESIDE them: the arbitrary half again, one position further in.
//
// The document below is the check, and it is deliberately written in the layout an operator
// uses rather than the compact one the other tests use — comments only exist in block form.
// What it does NOT assert is WHICH carrier holds a given line, for the reason
// TestAConditionKeepsItsFieldComments gives: a comment before a container's first entry heads
// the container, so the carrier is a function of the author's field order. Survival is the
// promise; placement is layout.
func TestARuleKeepsEveryCommentItWasWrittenWith(t *testing.T) {
	eng, store := newEng(t)
	src := "# why this rule exists\n" +
		"name: everything # the short name\n" +
		"# which level it lives in\n" +
		"stratum: base # the ground floor\n" +
		"# what this is about\n" +
		"condition: # cause and subject\n" +
		"  # the subject\n" +
		"  subject: demo.pr.* # any PR\n" +
		"  # what has to hold\n" +
		"  when: ci.status.* # a build says so\n" +
		"# who has to agree\n" +
		"gates: # conjunctive\n" +
		"# the first gate\n" +
		"- name: approval # by a human\n" +
		"  mode: ask # blocking\n" +
		"# what it does\n" +
		"action: # the write\n" +
		"  # the patch\n" +
		"  patch: # merged into the subject\n" +
		"    stage: reviewing # the new stage\n"
	if err := eng.AddStratum(Stratum{Name: "base", Level: 1, Match: []Match{mm("demo")}}); err != nil {
		t.Fatalf("AddStratum: %v", err)
	}
	node, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var installed Trigger
	if err := installed.FromTonyIR(node); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := eng.AddTrigger(installed); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	ent, ok, err := store.Get(CharterRef("everything"))
	if err != nil || !ok {
		t.Fatalf("the charter entity: ok=%v err=%v", ok, err)
	}
	back, err := ruleOf(ent)
	if err != nil {
		t.Fatalf("ruleOf: %v", err)
	}
	out, err := codec.Marshal(back, codec.Tony)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, line := range strings.Split(src, "\n") {
		i := strings.Index(line, "#")
		if i < 0 {
			continue
		}
		if want := strings.TrimSpace(line[i:]); !strings.Contains(string(out), want) {
			t.Errorf("the rule came back without %q:\n%s", want, out)
		}
	}
	// And the rule is still the rule: a comment is not data, and none of these carriers is a
	// field of the document.
	if back.Name != "everything" || back.Stratum != "base" ||
		!isPath(back.Condition.Subject, "demo.pr.*") || len(back.Gates) != 1 {
		t.Fatalf("the rule itself did not survive carrying its comments: %+v", back)
	}
}

// A comment above a field OF a hand-written codec survives too, which is the last position in a
// charter that nothing carried.
//
// `subject:`, `on:` and `when:` are fields of Condition, whose codec is written by hand — so
// codegen emits no `comment=` for them and the annotation that covers every other position
// cannot reach here. Condition's own codec captures and re-emits them instead.
//
// WHICHEVER FIELD COMES FIRST is carried by the CONDITION rather than by itself: a comment
// before a container's first entry heads the container. So the test asks that every comment
// survives, not that a particular carrier holds it — the carrier is a function of layout, and
// pinning it would pin the author's field order.
func TestAConditionKeepsItsFieldComments(t *testing.T) {
	for _, order := range []string{
		"  # about the subject\n  subject: {path: \"demo.pr.*\"}\n" +
			"  # about the when\n  when: {path: \"ci.status.*\"}\n",
		"  # about the when\n  when: {path: \"ci.status.*\"}\n" +
			"  # about the subject\n  subject: {path: \"demo.pr.*\"}\n",
	} {
		src := "name: r\ncondition:\n" + order + "action:\n  patch: { stage: x }\n"
		node, err := codec.ParseNode([]byte(src), codec.Tony)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		var tr Trigger
		if err := tr.FromTonyIR(node); err != nil {
			t.Fatalf("decode: %v", err)
		}
		back, err := tr.ToTonyIR()
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		out, err := codec.NodeBytes(back, codec.Tony)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		for _, want := range []string{"# about the subject", "# about the when"} {
			if !strings.Contains(string(out), want) {
				t.Errorf("lost %q from\n%s\ngot\n%s", want, src, out)
			}
		}
		// …and the condition itself still decodes to what it says.
		if !isPath(tr.Condition.Subject, "demo.pr.*") || tr.Condition.When == nil {
			t.Errorf("the condition did not survive carrying its comments: %+v", tr.Condition)
		}
	}
}

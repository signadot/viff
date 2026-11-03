package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"
)

// derefEnv is the environment the expression tests evaluate against.
//
// It goes through refEnv rather than writing `subject` by hand, because `subject` is a RECORD and
// a fixture that spelled it some other way would be testing a shape the engine never builds.
func derefEnv() launch.Binding {
	return refEnv(entity.Obj{
		"result": "SHIPIT",
		"status": "done",
		"count":  3,
		"ratio":  1.5,
		"ok":     true,
		"nul":    nil,
		"verse":  entity.Obj{"review": entity.Obj{"done": true, "step": 2}},
		"list":   []any{"a", "b"},
		"obj":    entity.Obj{"k": "v"},
	}.Payload(), subjectRecord(entity.RefAt("demo", "pr", "1"), nil, 0), fromSubject)
}

// valueOf is what one patch value comes to, through the walk a firing makes (expandPatch).
func valueOf(s string, env launch.Binding) (entity.Payload, error) {
	got, err := expandPatch(entity.Obj{"v": s}.Payload(), env, nil)
	if err != nil {
		return nil, err
	}
	return entity.Get(got, "v"), nil
}

// A patch value that is one `.[…]` and nothing else is the expression's VALUE, so it keeps its
// type. The reason it matters is the store's predicates are type-exact: a 3 written as "3" is a
// value `value: {count: 3}` does not match.
func TestWholeValueKeepsType(t *testing.T) {
	env := derefEnv()
	for _, tc := range []struct {
		in   string
		want ir.Type
	}{
		{".[result]", ir.StringType},
		{".[count]", ir.NumberType},
		{".[ok]", ir.BoolType},
		{".[nul]", ir.NullType},
		{".[obj]", ir.ObjectType},
		{".[list]", ir.ArrayType},
		{".[verse.review.done]", ir.BoolType},
		{".[verse.review.step]", ir.NumberType},
		{".[list[1]]", ir.StringType},
		{`.[verse.review.missing ?? "none"]`, ir.StringType},
		{`.[status == "done" ? count : nul]`, ir.NumberType},
	} {
		got, err := valueOf(tc.in, env)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if got.Type != tc.want {
			t.Errorf("%s: type = %v, want %v", tc.in, got.Type, tc.want)
		}
	}

	// the integer survives as an integer, not as text and not as a float
	n, err := valueOf(".[count]", env)
	if err != nil {
		t.Fatalf(".[count]: %v", err)
	}
	if n.Int64 == nil || *n.Int64 != 3 {
		t.Errorf(".[count]: Int64 = %v, want 3", n.Int64)
	}
}

// An expression embedded in text interpolates and is a string — it was written as one.
func TestTextInterpolates(t *testing.T) {
	env := derefEnv()
	for _, tc := range []struct{ in, want string }{
		{"got $[result]", "got SHIPIT"},
		{"$[status]/$[result]", "done/SHIPIT"},
		{"n=$[count] r=$[ratio] ok=$[ok]", "n=3 r=1.5 ok=true"},
		{"step $[verse.review.step] of 3", "step 2 of 3"},
		{"about $[subject.ref]", `about demo.pr."1"`},
		// A written null is `null` in text, as it is in a spawn's strings.
		{"x=$[nul]", "x=null"},
		// A composite is tony on one line, as it is in a spawn's strings.
		{"saw $[obj] here", "saw {k: v} here"},
	} {
		got, err := valueOf(tc.in, env)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got.Type != ir.StringType || got.String != tc.want {
			t.Errorf("%q -> %q, want %q", tc.in, got.String, tc.want)
		}
	}
}

// Absent and null are different answers. An absent plain path fails the firing; a field
// written null resolves to null, because the store records that difference and a match
// spells the two cases apart.
func TestAbsentErrorsNullResolves(t *testing.T) {
	env := derefEnv()
	for _, s := range []string{".[nope]", ".[verse.review.missing]", "saw $[nope]", ".[nul.x]"} {
		if _, err := valueOf(s, env); err == nil {
			t.Errorf("%s: an absent path should error, not silently vanish", s)
		}
	}
	got, err := valueOf(".[nul]", env)
	if err != nil {
		t.Fatalf(".[nul]: %v", err)
	}
	if got.Type != ir.NullType {
		t.Errorf(".[nul]: type = %v, want Null", got.Type)
	}
}

// A `$` that does not open `$[` is text, and so is a `.[` that is not the whole value. The old
// spelling, `$result`, is text here too: it is refused at install (legacyRef), not evaluated.
// `$$[` is a literal `$[`.
func TestTextThatIsNoExpressionIsLeftAsWritten(t *testing.T) {
	env := derefEnv()
	for _, s := range []string{"$5.00", "100$", "$", "$ result", "$-x", "50%$", "$result", "a.[result]", ".[0].name x"} {
		got, err := valueOf(s, env)
		if err != nil {
			t.Errorf("%q should not error: %v", s, err)
			continue
		}
		if got.String != s {
			t.Errorf("%q -> %q, want it unchanged", s, got.String)
		}
	}
	if got, err := valueOf("$$[result]", env); err != nil || got.String != "$[result]" {
		t.Errorf("$$[result] -> %v (err %v), want the literal $[result]", got, err)
	}
}

// expandPatch walks the whole document, and must not lose what makes a patch a patch: the
// mergeop tags that give it its write semantics. A dropped tag turns a deletion into a write
// of null.
func TestExpandPatchPreservesTagsAndNesting(t *testing.T) {
	patch := entity.Obj{
		"stage":   ".[status]",
		"verdict": ".[result]",
		"nested":  entity.Obj{"deep": ".[verse.review.step]"},
		"tmp":     nil,
		"gone":    ".[status]",
	}.Payload()
	entity.Get(patch, "tmp").Tag = "!delete"
	entity.Get(patch, "gone").Tag = "!delete"

	got, err := expandPatch(patch, derefEnv(), nil)
	if err != nil {
		t.Fatalf("expandPatch: %v", err)
	}
	if v := entity.Str(got, "stage"); v != "done" {
		t.Errorf("stage = %q, want done", v)
	}
	if v := entity.Str(got, "verdict"); v != "SHIPIT" {
		t.Errorf("verdict = %q, want SHIPIT", v)
	}
	deep, err := got.GetKPath("nested.deep")
	if err != nil || deep == nil || deep.Int64 == nil || *deep.Int64 != 2 {
		t.Errorf("nested.deep = %v (err %v), want 2", deep, err)
	}
	if tag := entity.Get(got, "tmp").Tag; tag != "!delete" {
		t.Errorf("mergeop tag lost: tmp.Tag = %q, want !delete", tag)
	}
	if g := entity.Get(got, "gone"); g.Tag != "!delete" || g.String != "done" {
		t.Errorf("an expression under !delete = %q tagged %q, want done tagged !delete", g.String, g.Tag)
	}
	// the original is untouched — a charter is installed once and fired many times
	if v := entity.Str(patch, "stage"); v != ".[status]" {
		t.Errorf("expandPatch mutated the installed patch: stage = %q", v)
	}
}

// A patch that names nothing is passed through, so the overwhelmingly common constant patch
// costs nothing and cannot fail.
func TestExpandPatchConstantIsUnchanged(t *testing.T) {
	patch := entity.Obj{"stage": "reviewed", "n": 7}.Payload()
	got, err := expandPatch(patch, derefEnv(), nil)
	if err != nil {
		t.Fatalf("expandPatch: %v", err)
	}
	if !entity.Equal(got, patch) {
		t.Errorf("a constant patch should be unchanged")
	}
}

// resolvedSubject asks the environment what `subject.ref` is, which is the contract — the record
// behind it is an implementation detail, and reading the field directly would test that instead.
func resolvedSubject(t *testing.T, env launch.Binding) string {
	t.Helper()
	v, err := valueOf(".[subject.ref]", env)
	if err != nil {
		t.Fatalf("subject.ref did not resolve: %v", err)
	}
	return v.String
}

// `subject` whole is the record {ref, name, value}, and never carries the firing's commit, so a
// whole-subject write does not stamp the subject anew on every firing.
func TestSubjectWholeIsTheRecord(t *testing.T) {
	ref := entity.RefAt("demo", "pr", "1")
	env := refEnv(nil, subjectRecord(ref, entity.Obj{"stage": "open"}.Payload(), 0), fromSubject)
	got, err := valueOf(".[subject]", env)
	if err != nil {
		t.Fatal(err)
	}
	if entity.Str(got, "ref") != ref.String() || entity.Str(got, "name") != "1" ||
		entity.Str(entity.Get(got, "value"), "stage") != "open" || entity.Get(got, "commit") != nil {
		t.Errorf(".[subject] = %v, want {ref, name, value} and no commit", got)
	}
	if w := wantsOf(entity.Obj{"s": ".[subject]"}.Payload()); !w.value || w.commit {
		t.Errorf("wants of .[subject] = %+v, want the value and not the commit", w)
	}
	if w := wantsOf(entity.Obj{"s": "at $[subject.commit]"}.Payload()); w.value || !w.commit {
		t.Errorf("wants of subject.commit = %+v, want the commit alone", w)
	}
	if w := wantsOf(entity.Obj{"s": ".[subject.ref + subject.name]"}.Payload()); w.value || w.commit {
		t.Errorf("wants of the identity = %+v, want nothing to read", w)
	}
}

// The environment must not round-trip a number into something a match reads differently: an int
// comes back an int.
func TestRefEnvKeepsIntegers(t *testing.T) {
	env := refEnv(entity.Obj{"count": 3}.Payload(), subjectRecord(entity.RefAt("demo", "pr", "1"), nil, 0), fromSubject)
	n, err := valueOf(".[count]", env)
	if err != nil || n.Int64 == nil || *n.Int64 != 3 {
		t.Fatalf("count did not survive as an integer: %v (err %v)", n, err)
	}
	if s := resolvedSubject(t, env); s != `demo.pr."1"` {
		t.Errorf("subject = %q, want demo.pr.1", s)
	}
}

// `subject` is the one reserved name and a payload field must not shadow it — the same rule
// bindingFor states for the spawn side.
func TestRefEnvSubjectWins(t *testing.T) {
	env := refEnv(entity.Obj{"subject": `demo.pr."999"`}.Payload(), subjectRecord(entity.RefAt("demo", "pr", "1"), nil, 0), fromSubject)
	if s := resolvedSubject(t, env); s != `demo.pr."1"` {
		t.Errorf("subject = %q, want the resolved subject demo.pr.1", s)
	}
}

// A scalar payload has no named parts, so a name read from one fails rather than binding the
// whole value to an invented name — the rule bindingFor states for the spawn side.
func TestRefEnvScalarPayload(t *testing.T) {
	env := refEnv(ir.FromInt(42), subjectRecord(entity.RefAt("demo", "count", "1"), nil, 0), fromSubject)
	if _, err := valueOf(".[anything]", env); err == nil {
		t.Error("a name read from a scalar payload should error")
	}
	if s := resolvedSubject(t, env); s != `demo.count."1"` {
		t.Errorf("subject.ref must still resolve for a scalar payload, got %q", s)
	}
}

// A completion hook's environment has every fact of its task, as its spawn's does: `data` is
// null where the task gave no document, and `result` and `err` are "".
func TestAHooksEnvironmentHasEveryTaskFact(t *testing.T) {
	env := refEnv(entity.Obj{"status": "done"}.Payload(), subjectRecord(entity.RefAt("demo", "pr", "1"), nil, 0), fromCause)
	for in, want := range map[string]ir.Type{".[data]": ir.NullType, ".[result]": ir.StringType, ".[err]": ir.StringType} {
		got, err := valueOf(in, env)
		if err != nil || got.Type != want {
			t.Errorf("%s = %v (err %v), want %v", in, got, err, want)
		}
	}
	if _, err := valueOf(".[data.findings]", env); err == nil {
		t.Error(".[data.findings] through a null data resolved; it should refuse")
	}
}

// THE OLD SPELLING IS REFUSED AT INSTALL, with what to write instead: `$subject.value.sha` in a
// patch is text now, and would land on the subject as text.
func TestTheOldSpellingIsRefusedAtInstall(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch string
		want  string // in the refusal; "" for none
	}{
		{"whole", "{sha: $subject.value.sha}", "write .[subject.value.sha]"},
		{"in text", `{note: "saw $result"}`, "$result"},
		{"in a read's address", "{r: !get-path {at: [git, repo, $subject.value.head]}}", "$subject.value.head"},
		{"in a dotted address", `{r: !get-path "git.repo.$subject.value.head"}`, `git.repo."$[subject.value.head]"`},
		{"in an at: string", `{r: !get-path {at: "git.repo.v$sha"}}`, `git.repo."v$[sha]"`},
		{"a literal dollar-word", `{home: "$HOME/x"}`, "'$[\"$HOME\"]'"},
		{"the new spelling", `{sha: .[subject.value.sha], note: "saw $[result]", lit: '$["$HOME"]'}`, ""},
		{"inside an expression", `{n: '.[$env["pr-number"]]'}`, ""},
		{"a dollar that names nothing", `{price: "$5", pct: "50%$", d: "$$"}`, ""},
	} {
		patch, err := codec.ParseNode([]byte(tc.patch), codec.Tony)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		err = Action{Patch: patch}.validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want it to say %q", tc.name, err, tc.want)
		}
	}
}

// An action's names read the SUBJECT, not the change that dispatched the firing. Those
// are the same entity for an ordinary rule, and different the moment a `when` puts the cause
// somewhere else — where binding off the cause meant writing a CI status's fields onto a PR.
func TestReferencesReadTheSubjectNotTheCause(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	pr := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, pr, entity.Obj{"stage": "reviewed", "branch": "hotfix-9"}.Payload())

	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		Condition: Condition{
			When:    MatchWhen(mm("ci.status.*")),
			Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "reviewed"}},
		},
		// branch is a field of the PR. The CI status has no such field — and carries a
		// `branch` of its own precisely to catch a binding that read the cause instead.
		Action: Action{Patch: entity.Obj{"shipped": ".[branch]"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, entity.RefAt("ci", "status", "build"),
		entity.Obj{"state": "green", "branch": "main"}.Payload())

	deadline := time.After(3 * time.Second)
	for {
		e, _, _ := store.Get(pr)
		got := entity.Str(e.Payload, "shipped")
		if got == "hotfix-9" {
			return // bound from the subject
		}
		if got == "main" {
			t.Fatalf("branch bound from the CAUSE (the ci status), not the subject")
		}
		select {
		case <-deadline:
			t.Fatalf("the trigger never wrote `shipped`: %#v", e.Payload)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// The ordinary rule is unaffected: with no `when`, the subject IS the changed entity, so its
// references read the event's own level payload — no store read, and exact at the commit.
func TestReferencesOnASubjectOnlyTriggerReadTheEvent(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})

	pr := entity.RefAt("demo", "pr", "1")
	if err := eng.AddTrigger(Trigger{
		Name:      "note",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    Action{Patch: entity.Obj{"noted": ".[branch]", "stage": "seen"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, pr, entity.Obj{"stage": "open", "branch": "hotfix-9"}.Payload())
	waitStage(t, store, pr, "seen")
	if e, _, _ := store.Get(pr); entity.Str(e.Payload, "noted") != "hotfix-9" {
		t.Fatalf("noted = %q, want hotfix-9", entity.Str(e.Payload, "noted"))
	}
}

// A composite payload field reaches a spawn's prompt as TONY, and parses back to what it came
// from: a format the rest of verse writes, which a person and a parser can both read.
//
// The test round-trips rather than comparing text, because the promise is "a document a parser
// can read", not any particular layout. An expression reads a document as Go values, so its
// fields come back in name order, which is the order the source is written in here.
func TestCompositeBindsAsTony(t *testing.T) {
	src := `{sha: abc, findings: {"entity.nodeToPayload": {status: open, was: "logd's tombstones"}}}`
	payload, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	text := bound(t, launch.Spec{Prompt: "$[findings]"}, bindingFor(payload, entity.RefAt("demo", "pr", "1"), payload)).Prompt

	got, err := codec.ParseNode([]byte(text), codec.Tony)
	if err != nil {
		t.Fatalf("the expansion does not parse as tony: %v\n%s", err, text)
	}
	want, _ := payload.GetKPath("findings")
	if !entity.Equal(got, want) {
		t.Errorf("round trip lost something:\n  bound:    %s\n  original: %v", text, want)
	}
}

// …and every LEAF is its own text: `3` and not `"3"`, since a prompt is text and a quoted number
// in a sentence is a different sentence. A written null is `null`.
func TestScalarBindingsAreTheirText(t *testing.T) {
	payload, err := codec.ParseNode([]byte(`{s: open, n: 3, f: 3.5, b: true, z: null}`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	b := bindingFor(payload, entity.RefAt("demo", "pr", "1"), payload)
	got := bound(t, launch.Spec{Prompt: "$[s] $[n] $[f] $[b] $[z]"}, b).Prompt
	if want := "open 3 3.5 true null"; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
}

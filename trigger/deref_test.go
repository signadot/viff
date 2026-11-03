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

// derefRoot is the payload the reference tests resolve against.
//
// It goes through refRoot rather than writing `subject` by hand, because $subject is a RECORD
// now and the bare form is the record's `ref`. A fixture that spelled it as a string would be
// testing a shape the engine never builds.
func derefRoot() entity.Payload {
	return refRoot(entity.Obj{
		"result": "SHIPIT",
		"status": "done",
		"count":  3,
		"ratio":  1.5,
		"ok":     true,
		"nul":    nil,
		"verse":  entity.Obj{"review": entity.Obj{"done": true, "step": 2}},
		"list":   []any{"a", "b"},
		"obj":    entity.Obj{"k": "v"},
	}.Payload(), subjectNode(entity.RefAt("demo", "pr", "1"), nil, 0))
}

// A patch value that is exactly one reference resolves to that reference's NODE, so the
// value keeps its type. This is the property string interpolation could not have, and the
// reason it matters is the store's predicates are type-exact: a 3 written as "3" (or as 3.0)
// is a value `value: {count: 3}` does not match.
func TestDerefWholeValueKeepsType(t *testing.T) {
	root := derefRoot()
	for _, tc := range []struct {
		ref  string
		want ir.Type
	}{
		{"$result", ir.StringType},
		{"$count", ir.NumberType},
		{"$ok", ir.BoolType},
		{"$nul", ir.NullType},
		{"$obj", ir.ObjectType},
		{"$list", ir.ArrayType},
		{"$verse.review.done", ir.BoolType},
		{"$verse.review.step", ir.NumberType},
		{"$list[1]", ir.StringType},
	} {
		got, err := expandString(tc.ref, root)
		if err != nil {
			t.Errorf("%s: %v", tc.ref, err)
			continue
		}
		if got.Type != tc.want {
			t.Errorf("%s: type = %v, want %v", tc.ref, got.Type, tc.want)
		}
	}

	// the integer survives as an integer, not as text and not as a float
	n, err := expandString("$count", root)
	if err != nil {
		t.Fatalf("$count: %v", err)
	}
	if n.Int64 == nil || *n.Int64 != 3 {
		t.Errorf("$count: Int64 = %v, want 3", n.Int64)
	}
}

// A reference embedded in text interpolates and is a string — it was written as one.
func TestDerefInterpolates(t *testing.T) {
	root := derefRoot()
	for _, tc := range []struct{ in, want string }{
		{"got $result", "got SHIPIT"},
		{"$status/$result", "done/SHIPIT"},
		{"n=$count r=$ratio ok=$ok", "n=3 r=1.5 ok=true"},
		{"step $verse.review.step of 3", "step 2 of 3"},
		{"about $subject", `about demo.pr."1"`},
	} {
		got, err := expandString(tc.in, root)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got.Type != ir.StringType || got.String != tc.want {
			t.Errorf("%q -> %q, want %q", tc.in, got.String, tc.want)
		}
	}
}

// A composite has no text form: inventing one would make "saw $result" mean something
// different depending on what a task happened to return.
func TestDerefCompositeInTextIsAnError(t *testing.T) {
	_, err := expandString("saw $obj here", derefRoot())
	if err == nil {
		t.Fatal("interpolating an object should error")
	}
	if !strings.Contains(err.Error(), "no text form") {
		t.Errorf("error should say why: %v", err)
	}
}

// Absent and null are different answers. An absent reference fails the firing; a field
// written null resolves to null, because the store records that difference and a match
// spells the two cases apart.
func TestDerefAbsentErrorsNullResolves(t *testing.T) {
	root := derefRoot()
	if _, err := expandString("$nope", root); err == nil {
		t.Error("an absent reference should error, not silently vanish")
	}
	if _, err := expandString("$verse.review.missing", root); err == nil {
		t.Error("an absent nested reference should error")
	}
	got, err := expandString("$nul", root)
	if err != nil {
		t.Fatalf("$nul: %v", err)
	}
	if got.Type != ir.NullType {
		t.Errorf("$nul: type = %v, want Null", got.Type)
	}
}

// A charter may hold a literal dollar, and a patch was never expanded before this change —
// so anything that is not a well-formed reference is left exactly as written.
//
// There is no escape: "$$" is not "a literal dollar", it is a dollar followed by whatever
// comes next, and "a$$b" therefore references $b. That matches launch.expand on the spawn
// side, which has no escape either — one rule for both halves is worth more here than a
// convention only one of them honours.
func TestDerefLeavesLiteralDollars(t *testing.T) {
	root := derefRoot()
	for _, s := range []string{"$5.00", "100$", "$", "$ result", "$-x", "50%$"} {
		got, err := expandString(s, root)
		if err != nil {
			t.Errorf("%q should not error: %v", s, err)
			continue
		}
		if got.String != s {
			t.Errorf("%q -> %q, want it unchanged", s, got.String)
		}
	}
}

// expandPatch walks the whole document, and must not lose what makes a patch a patch: the
// mergeop tags that give it its write semantics. A dropped tag turns a deletion into a write
// of null.
func TestExpandPatchPreservesTagsAndNesting(t *testing.T) {
	patch := entity.Obj{
		"stage":   "$status",
		"verdict": "$result",
		"nested":  entity.Obj{"deep": "$verse.review.step"},
		"tmp":     nil,
	}.Payload()
	entity.Get(patch, "tmp").Tag = "!delete"

	got, err := expandPatch(patch, derefRoot(), nil)
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
	// the original is untouched — a charter is installed once and fired many times
	if v := entity.Str(patch, "stage"); v != "$status" {
		t.Errorf("expandPatch mutated the installed patch: stage = %q", v)
	}
}

// A patch that names nothing is passed through, so the overwhelmingly common constant patch
// costs nothing and cannot fail.
func TestExpandPatchConstantIsUnchanged(t *testing.T) {
	patch := entity.Obj{"stage": "reviewed", "n": 7}.Payload()
	got, err := expandPatch(patch, derefRoot(), nil)
	if err != nil {
		t.Fatalf("expandPatch: %v", err)
	}
	if !entity.Equal(got, patch) {
		t.Errorf("a constant patch should be unchanged")
	}
}

// resolvedSubject asks the root what `$subject` means, which is the contract — the record
// behind it is an implementation detail, and reading the field directly would test that
// instead.
func resolvedSubject(t *testing.T, root entity.Payload) string {
	t.Helper()
	v, err := expandString("$subject", root)
	if err != nil {
		t.Fatalf("$subject did not resolve: %v", err)
	}
	return v.String
}

// refRoot must not round-trip through Go values: that turns a tony int into a float64, and
// the store's predicates are type-exact.
func TestRefRootPreservesNumberTypes(t *testing.T) {
	ev := entity.Event{To: entity.Obj{"count": 3}.Payload()}
	root := refRoot(ev.To, subjectNode(entity.RefAt("demo", "pr", "1"), nil, 0))
	n := entity.Get(root, "count")
	if n == nil || n.Int64 == nil || *n.Int64 != 3 {
		t.Fatalf("count did not survive as an integer: %v", n)
	}
	if s := resolvedSubject(t, root); s != `demo.pr."1"` {
		t.Errorf("subject = %q, want demo.pr.1", s)
	}
}

// $subject is the one reserved reference and a payload field must not shadow it — the same
// rule bindingFor states for the spawn side.
func TestRefRootSubjectWins(t *testing.T) {
	ev := entity.Event{To: entity.Obj{"subject": `demo.pr."999"`}.Payload()}
	root := refRoot(ev.To, subjectNode(entity.RefAt("demo", "pr", "1"), nil, 0))
	if s := resolvedSubject(t, root); s != `demo.pr."1"` {
		t.Errorf("subject = %q, want the resolved subject demo.pr.1", s)
	}
}

// A scalar payload has no named parts, so a reference into one fails rather than binding the
// whole value to an invented name — the rule bindingFor states for the spawn side.
func TestRefRootScalarPayload(t *testing.T) {
	ev := entity.Event{To: ir.FromInt(42)}
	root := refRoot(ev.To, subjectNode(entity.RefAt("demo", "count", "1"), nil, 0))
	if _, err := expandString("$anything", root); err == nil {
		t.Error("a reference into a scalar payload should error")
	}
	if s := resolvedSubject(t, root); s != `demo.count."1"` {
		t.Errorf("$subject must still resolve for a scalar payload, got %q", s)
	}
}

// An action's references read the SUBJECT, not the change that dispatched the firing. Those
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
		// $branch is a field of the PR. The CI status has no such field — and carries a
		// `branch` of its own precisely to catch a binding that read the cause instead.
		Action: Action{Patch: entity.Obj{"shipped": "$branch"}.Payload()},
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
			t.Fatalf("$branch bound from the CAUSE (the ci status), not the subject")
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
		Action:    Action{Patch: entity.Obj{"noted": "$branch", "stage": "seen"}.Payload()},
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

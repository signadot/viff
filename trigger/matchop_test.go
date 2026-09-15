package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/task"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// mustMatch decodes a charter-authored match, in whichever dialect it was written.
func mustMatch(t *testing.T, src string, f codec.Format) Match {
	t.Helper()
	var m Match
	if err := codec.Unmarshal([]byte(src), &m, f); err != nil {
		t.Fatalf("decoding %s: %v", src, err)
	}
	return m
}

// matchErr decodes and returns the error, for the cases that must be refused.
func matchErr(t *testing.T, src string, f codec.Format) string {
	t.Helper()
	var m Match
	err := codec.Unmarshal([]byte(src), &m, f)
	if err == nil {
		t.Fatalf("%s was accepted as %#v, want an error", src, map[string]any(m))
	}
	return err.Error()
}

// What an operator BUYS in a path segment: filepath.Match semantics, where "*" is bounded by "/"
// and "?"/"[…]" are metacharacters. A plain segment is a NAME: a `*` inside it is an ordinary
// character, and only a whole `*` segment is any one.
func TestPathSegmentOperatorGrammar(t *testing.T) {
	ev := func(id string) entity.Event {
		return entity.Event{Ref: entity.RefAt("demo", "pr", id), Op: entity.Set}
	}
	cases := []struct {
		src  string
		id   string
		want bool
	}{
		{`{path: [demo, pr, !glob "hotfix-*"]}`, "hotfix-1", true},
		{`{path: [demo, pr, !glob "hotfix-*"]}`, "cold-1", false},
		{`{path: [demo, pr, !glob "a?c"]}`, "abc", true},
		{`{path: [demo, pr, !glob "a?c"]}`, "ac", false},
		{`{path: [demo, pr, !glob "[ab]1"]}`, "b1", true},
		{`{path: [demo, pr, !glob "[ab]1"]}`, "c1", false},

		// The separator rule: a path-shaped name can be matched one piece at a time.
		{`{path: [demo, pr, !glob "a*"]}`, "a/b", false},
		{`{path: [demo, pr, !glob "a/*"]}`, "a/b", true},
		// A plain segment is the name it spells, `*` and all.
		{`{path: [demo, pr, "a*"]}`, "a/b", false},
		{`{path: [demo, pr, "a*"]}`, "a*", true},

		// Composition, which a name has no spelling for at all.
		{`{path: [demo, pr, !or ["1", "2"]]}`, "1", true},
		{`{path: [demo, pr, !or ["1", "2"]]}`, "3", false},
		{`{path: [demo, pr, !not "1"]}`, "2", true},
		{`{path: [demo, pr, !not "1"]}`, "1", false},
		{`{path: [demo, pr, !and [!glob "hotfix-*", !not "hotfix-9"]]}`, "hotfix-1", true},
		{`{path: [demo, pr, !and [!glob "hotfix-*", !not "hotfix-9"]]}`, "hotfix-9", false},
	}
	for _, c := range cases {
		m := mustMatch(t, c.src, codec.Tony)
		if got := m.MatchEvent(ev(c.id)); got != c.want {
			t.Errorf("%s vs %q = %v, want %v", c.src, c.id, got, c.want)
		}
	}
}

// The SET face has to apply the operator too: an operator that only worked on the event path
// would silently widen every fan-out to every node at the place.
func TestMatchOperatorSelects(t *testing.T) {
	store := entitytest.Store(t)
	for _, id := range []string{"hotfix-1", "hotfix-2", "feature-1"} {
		entitytest.Set(t, store, entity.RefAt("demo", "pr", id), entity.Obj{"stage": "open"}.Payload())
	}

	m := mustMatch(t, `{path: [demo, pr, !glob "hotfix-*"]}`, codec.Tony)
	refs := m.Select(store)
	if len(refs) != 2 {
		t.Fatalf("selected %v, want the two hotfix PRs", refs)
	}
	for _, r := range refs {
		if !strings.HasPrefix(r.Name(), "hotfix-") {
			t.Errorf("selected %s, which the glob excludes", r)
		}
	}
}

// An operator makes a match a SELECTION, never a NAME. This is the one case where getting
// it wrong does more than fail to match: an action's `set` on a named subject CREATES the
// entity, so a subject that looked named would conjure `demo.pr.!glob hotfix-*` into being.
func TestMatchOperatorIsNeverANamedRef(t *testing.T) {
	m := mustMatch(t, `{path: [demo, pr, !glob "hotfix-*"]}`, codec.Tony)
	if ref, named := m.Names(); named {
		t.Errorf("an operator segment was read as a name: %s", ref)
	}
	if !m.Selects() {
		t.Error("the first segment is literal, so this must select")
	}

	// And it must not render as an address, which would drop the operator entirely and
	// leave the widest possible match wearing the narrowest one's clothes.
	out, err := codec.MarshalWire(m, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "!glob") {
		t.Errorf("the operator did not survive rendering: %s", out)
	}
}

// A misspelled or unusable operator fails at INSTALL. The alternative is a charter that
// parses, deploys, and then matches nothing forever — the failure mode Validate exists to
// prevent, in its newest form.
func TestMatchOperatorErrorsAreEarly(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{`{path: [demo, pr, !gloob "hotfix-*"]}`, "unknown operator"},
		{`{path: [demo, pr, !glob 5]}`, "cannot glob non-string"},
		{`{path: [demo, pr, !or ["1", !gloob "2"]]}`, "unknown operator"}, // nested, not just at the top
	} {
		if got := matchErr(t, c.src, codec.Tony); !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want it to mention %q", c.src, got, c.want)
		}
	}
}

// A segment is a NAME or a MATCH OVER ONE, and a composite is neither: `{a: 1}` or `[a, b]` in a
// path is refused at install, however the document was laid out. Flow style is how nearly every
// charter writes a path, and the parser marks a flow collection with a layout tag — which is not
// an operator, and must not let a composite past the check a block-style one meets.
func TestAPathSegmentThatIsACompositeIsRefused(t *testing.T) {
	for _, c := range []struct {
		src string
		f   codec.Format
	}{
		{`{path: [demo, pr, {a: 1}]}`, codec.Tony},
		{`{path: [demo, pr, [a, b]]}`, codec.Tony},
		{`{"path":["demo","pr",{"a":1}]}`, codec.JSON},
	} {
		var m Match
		if err := codec.Unmarshal([]byte(c.src), &m, c.f); err == nil {
			t.Errorf("%s was accepted as a path; a composite segment names nothing", c.src)
		}
	}
}

// The unknown-tag check cannot be delegated to tony, and this is the pin on why:
// mergeop.SplitChild walks a tag CHAIN looking for a registered operator and silently skips
// everything it does not recognize. So `!gloob "x"` is not an error there — it matches by
// plain equality. If that ever changes, checkOps can go.
func TestUpstreamIgnoresUnknownTags(t *testing.T) {
	doc := ir.FromString("hotfix-1")
	pat := ir.FromString("hotfix-1").WithTag("!gloob")
	ok, err := tony.Match(doc, pat)
	if err != nil {
		t.Skipf("tony now reports unknown tags (%v) — checkOps may be retired", err)
	}
	if !ok {
		t.Fatalf("an unknown tag changed the match result; the skip rule changed")
	}

	// Which is exactly the silent failure: with a real operand it matches NOTHING, and
	// says nothing.
	ok, err = tony.Match(ir.FromString("hotfix-1"), ir.FromString("hotfix-*").WithTag("!gloob"))
	if err != nil || ok {
		t.Errorf("expected a silent non-match from an unknown tag, got (%v, %v)", ok, err)
	}
}

// An operator inside `value` WORKS. This test used to assert the opposite.
//
// The old rule was that a payload predicate is equality-only, and an operator was refused
// with a diagnostic saying so — because `{stage: !or [open, merged]}` would otherwise decode
// to the list ["open","merged"] and the trigger would simply never fire. Refusing it was the
// right answer while the predicate was a map of Go values. It is a tony match at the payload
// root now, so the operator is not something to reject, it is the thing being evaluated.
func TestMatchValueAcceptsOperators(t *testing.T) {
	ev := func(p entity.Obj) entity.Event {
		return entity.Event{Ref: entity.RefAt("demo", "pr", "1"), Op: entity.Set, To: p.Payload()}
	}
	cases := []struct {
		src   string
		match entity.Obj
		miss  entity.Obj
	}{
		{
			`{path: [demo, *, *], value: {stage: !or [open, merged]}}`,
			entity.Obj{"stage": "merged"},
			entity.Obj{"stage": "closed"},
		},
		{
			`{value: {stage: !glob "op*"}}`,
			entity.Obj{"stage": "open"},
			entity.Obj{"stage": "closed"},
		},
	}
	for _, c := range cases {
		m := mustMatch(t, c.src, codec.Tony)
		if !m.MatchEvent(ev(c.match)) {
			t.Errorf("%s did not match %v", c.src, c.match)
		}
		if m.MatchEvent(ev(c.miss)) {
			t.Errorf("%s wrongly matched %v", c.src, c.miss)
		}
	}

	// The tag-keyed form is NOT an operator here — inside a payload predicate it is an
	// ordinary field on a key that happens to be spelled like one. That is the ambiguity, and
	// reading it as data is the only reading that cannot silently change a charter.
	m := mustMatch(t, `{value: {or: [open, merged]}}`, codec.Tony)
	f := m.Predicate()
	if ir.Get(f, "or") == nil {
		t.Errorf("a payload key named \"or\" was eaten: %#v", f)
	}
}

// A record predicate still works normally — the refusal above must not have made the ordinary
// case harder to write.
func TestMatchValueStillPlain(t *testing.T) {
	m := mustMatch(t, `{path: [demo, pr, *], value: {stage: open, n: 2}}`, codec.Tony)
	ev := entity.Event{
		Ref: entity.RefAt("demo", "pr", "1"),
		Op:  entity.Set, To: entity.Obj{"stage": "open", "n": int64(2)}.Payload(),
	}
	if !m.MatchEvent(ev) {
		t.Errorf("%#v did not match %+v", map[string]any(m), ev.To)
	}
}

// `value` matches the payload ROOT, whatever shape it is — the scalar, the list and the
// operator-over-one that a payload may actually be, not only the record that almost every
// charter writes.
func TestMatchValueRootPredicate(t *testing.T) {
	ev := func(v any) entity.Event {
		return entity.Event{
			Ref: entity.RefAt("demo", "count", "1"),
			Op:  entity.Set, To: entity.Val(v),
		}
	}
	cases := []struct {
		src   string
		match any
		miss  any
	}{
		{`{path: [demo, *, *], value: 42}`, 42, 43},
		{`{value: true}`, true, false},
		{`{value: open}`, "open", "closed"},
		{`{value: !or [1, 2]}`, 2, 3},
		{`{value: !glob "hotfix-*"}`, "hotfix-9", "main"},
		// The object form is the same dimension, not a different predicate — and over an
		// object a tony match is a SUBSET constraint, which is the one place the name `value`
		// reads as more than it means.
		{`{value: {stage: open}}`, map[string]any{"stage": "open"}, map[string]any{"stage": "closed"}},
	}
	for _, c := range cases {
		m := mustMatch(t, c.src, codec.Tony)
		if !m.MatchEvent(ev(c.match)) {
			t.Errorf("%s did not match %v", c.src, c.match)
		}
		if m.MatchEvent(ev(c.miss)) {
			t.Errorf("%s wrongly matched %v", c.src, c.miss)
		}
	}
}

// A SCALAR predicate installs, and it means the payload IS that scalar. This is the guard the
// collapse to one dimension gave up, pinned as the behaviour that replaced it.
//
// `fields`/`entity` used to assert an object, so `fields: "stage=open"` — someone reaching for
// an expression language — failed at install instead of matching nothing forever. Under
// `value` it is a legitimate predicate on a string payload, and no static check can tell the
// two apart: `value: open` is exactly what you write against an entity whose payload is a bare
// state name. The assertion was not worth a second dimension either way, because it saw only
// the wrong SHAPE and never `{stge: open}` — a misspelled key, the typo people actually make,
// which matches nothing just as silently and which no shape check can catch.
func TestMatchValueScalarIsTheWholePayload(t *testing.T) {
	m := mustMatch(t, `{path: [demo, *, *], value: "stage=open"}`, codec.Tony)
	ev := func(v entity.Payload) entity.Event {
		return entity.Event{Ref: entity.RefAt("demo", "pr", "1"), Op: entity.Set, To: v}
	}
	if !m.MatchEvent(ev(entity.Val("stage=open"))) {
		t.Error(`value: "stage=open" did not match the payload "stage=open"`)
	}
	if m.MatchEvent(ev(entity.Obj{"stage": "open"}.Payload())) {
		t.Error(`value: "stage=open" wrongly matched the record {stage: open} — a scalar ` +
			`predicate is the whole payload, not a field expression`)
	}
}

// A patch against a non-object payload REPLACES it today: a trigger matching `value: 42`
// and patching a field turns the entity into a record, and the 42 is gone. tony.Patch does
// this silently, and it is reachable only because a payload can be a scalar at all.
//
// THIS PINS CURRENT BEHAVIOUR, NOT THE INTENDED ONE. docs/trigger-model.md settles the scalar
// case the other way — fail loudly, via a conditional write whose precondition is that the
// payload is object-shaped, so a scalar sitting where a flow's subject lives fails the commit
// instead of being overwritten. The framing there is invariant checking rather than a policy
// compromise, and it is right: an earlier version of this comment argued that refusing would
// "strand a flow with no way to say so", which mistakes a failed write that says exactly what
// is wrong for silence.
//
// The precondition rides the seq work and is not built, so the behaviour below is what
// happens. When it lands, this test inverts — it should assert the commit FAILS — and that is
// the signal that the plan's decision has arrived, not a regression.
func TestPatchOnAScalarPayloadReplacesItForNow(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.ObserveScoped(ctx)
	if err := eng.AddTrigger(Trigger{
		Name:      "milestone",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "count", "*"), "value": int64(100)}},
		Action:    Action{Patch: entity.Obj{"note": "milestone"}.Payload()},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}

	ref := entity.RefAt("demo", "count", "1")
	entitytest.Set(t, store, ref, entity.Val(100))
	deadline := time.After(3 * time.Second)
	for {
		got, ok, _ := store.Get(ref)
		if ok && entity.Str(got.Payload, "note") == "milestone" {
			if entity.IsObj(got.Payload) {
				return // the scalar became the record — replaced, as documented
			}
			t.Fatalf("payload is not the record the action described: %+v", got.Payload)
		}
		select {
		case <-deadline:
			t.Fatal("trigger never fired on a scalar payload")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// The three null idioms have to survive PARSE AND VALIDATE, not just the matcher: `!not
// {stage: null}` is a tagged object, so the codec must carry the tag through to tony.Match
// rather than decoding it away. entity.TestNullPresenceAbsenceIdioms pins the semantics; this
// pins that a charter can actually say them.
func TestNullIdiomsSurviveTheCharterLayer(t *testing.T) {
	ev := func(p entity.Obj) entity.Event {
		return entity.Event{
			Ref: entity.RefAt("demo", "pr", "1"),
			Op:  entity.Set, To: p.Payload(),
		}
	}
	var (
		absent  = entity.Obj{"other": 1}
		present = entity.Obj{"stage": "open"}
		isNull  = entity.Obj{"stage": nil}
	)
	cases := []struct {
		src                                 string
		wantAbsent, wantPresent, wantIsNull bool
	}{
		{`{path: [demo, *, *], value: {stage: null}}`, false, true, true},
		{`{path: [demo, *, *], value: !not {stage: null}}`, true, false, false},
		{`{path: [demo, *, *], value: {stage: !irtype null}}`, false, false, true},
	}
	for _, c := range cases {
		m := mustMatch(t, c.src, codec.Tony)
		if got := m.MatchEvent(ev(absent)); got != c.wantAbsent {
			t.Errorf("%s vs absent = %v, want %v", c.src, got, c.wantAbsent)
		}
		if got := m.MatchEvent(ev(present)); got != c.wantPresent {
			t.Errorf("%s vs present = %v, want %v", c.src, got, c.wantPresent)
		}
		if got := m.MatchEvent(ev(isNull)); got != c.wantIsNull {
			t.Errorf("%s vs null-valued = %v, want %v", c.src, got, c.wantIsNull)
		}
	}
}

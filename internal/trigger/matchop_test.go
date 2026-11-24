package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/task"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
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
// A PATH IS A KINDED PATH, and an operator cannot be written in one. What a `!glob` on a segment
// used to say is said by a predicate on the payload, which is where both live charters already
// say it (`value: {name: !glob "viff/auto/*/*"}`) — and a predicate is where a reader looks for
// a condition on what a node HOLDS.
//
// A path holding a match was the only thing the list form carried, and the list form is gone with
// it: one spelling for an address, the same one `viff data -at` takes.
func TestAPathTakesNoOperatorAndNoList(t *testing.T) {
	for _, src := range []string{
		`{path: [demo, pr, "*"]}`,              // the list form
		`{path: [demo, pr, !glob "hotfix-*"]}`, // …and what it carried
		`{path: [demo, pr, !not "1"]}`,
		`{path: [demo, pr, !or ["1", !glob "hf-*"]]}`,
	} {
		if msg := matchErr(t, src, codec.Tony); !strings.Contains(msg, "kinded path") {
			t.Errorf("%s refused with %q, want it to name the spelling an address has", src, msg)
		}
	}
	// A glob inside a segment is a NAME with an asterisk in it, as it is at a read.
	m := mustMatch(t, `"demo.pr.hotfix-*"`, codec.Tony)
	p, err := m.pattern()
	if err != nil {
		t.Fatal(err)
	}
	if !p.MatchRef(entity.RefAt("demo", "pr", "hotfix-*")) || p.MatchRef(entity.RefAt("demo", "pr", "hotfix-1")) {
		t.Error("a `*` inside a segment selected a family; it is a character of a name")
	}
}

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
			`{path: "demo.*.*", value: {stage: !or [open, merged]}}`,
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
	m := mustMatch(t, `{path: "demo.pr.*", value: {stage: open, n: 2}}`, codec.Tony)
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
		{`{path: "demo.*.*", value: 42}`, 42, 43},
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
	m := mustMatch(t, `{path: "demo.*.*", value: "stage=open"}`, codec.Tony)
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
		{`{path: "demo.*.*", value: {stage: null}}`, false, true, true},
		{`{path: "demo.*.*", value: !not {stage: null}}`, true, false, false},
		{`{path: "demo.*.*", value: {stage: !irtype null}}`, false, false, true},
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

// A tag in a `value` predicate that names no operator is refused where the rule is read, with
// the tag named: tony would skip it and compare by equality, so the rule would install and
// fire on nothing. An operator, and viff's own path reads, are read as what they are.
func TestAPredicateNamingNoOperatorIsRefused(t *testing.T) {
	for _, src := range []string{
		`{path: "demo.pr.*", value: {stage: !gloob "op*"}}`,
		`{path: "demo.pr.*", value: {labels: {area: !orr [auth, store]}}}`,
		`{path: "demo.pr.*", value: !nott {stage: merged}}`,
	} {
		if msg := matchErr(t, src, codec.Tony); !strings.Contains(msg, "unknown operator") ||
			!strings.Contains(msg, "value") {
			t.Errorf("%s: refused with %q, want the unknown operator named in the predicate", src, msg)
		}
	}
	for _, src := range []string{
		`{path: "demo.pr.*", value: {stage: !glob "op*"}}`,
		`{path: "demo.pr.*", value: {stage: !or [open, merged]}}`,
		`{path: "demo.pr.*", value: !not {stage: merged}}`,
		`{path: "demo.pr.*", value: {covers: !get-path {path: "git.viff.head"}}}`,
		`{path: "demo.pr.*", value: {files: !list-paths {path: "git.viff.files"}}}`,
	} {
		var m Match
		if err := codec.Unmarshal([]byte(src), &m, codec.Tony); err != nil {
			t.Errorf("%s was refused: %v", src, err)
		}
	}
}

package trigger

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// observe/sdlc/effectuate — the ordering the design note is written around, which is what
// most of these tests install.
func demoStrata() []Stratum {
	return []Stratum{
		{Name: "observe", Level: 0, Match: []Match{mm("gh")}},
		{Name: "sdlc", Level: 10, Match: []Match{mm("signadot")}},
		{Name: "effectuate", Level: 100, Match: []Match{mm("do")}},
	}
}

func withStrata(t *testing.T, eng *Engine, strata ...Stratum) {
	t.Helper()
	for _, s := range strata {
		if err := eng.AddStratum(s); err != nil {
			t.Fatalf("AddStratum(%s): %v", s.Name, err)
		}
	}
}

func newEng(t *testing.T) (*Engine, entity.Store) {
	t.Helper()
	store := entitytest.Store(t)
	return New(store, task.AutoResolver{}), store
}

// ── the ordering itself ───────────────────────────────────────────────────────────

// The level is the ordering, so two strata cannot hold one. This is the check that only
// exists because the NAME is the address: were the level the id, a duplicate could not be
// written in the first place.
func TestTwoStrataCannotShareALevel(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng, Stratum{Name: "a", Level: 10, Match: []Match{mm("x")}})
	err := eng.AddStratum(Stratum{Name: "b", Level: 10, Match: []Match{mm("y")}})
	if err == nil {
		t.Fatal("installed two strata at level 10")
	}
	if !strings.Contains(err.Error(), "level") {
		t.Fatalf("the refusal does not say what is wrong: %v", err)
	}
}

// Without disjointness the ordering is vacuous: an entity in the intersection is at two
// levels, and "strictly higher" has two answers for it.
func TestOverlappingStrataAreRefused(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng, Stratum{Name: "a", Level: 0, Match: []Match{mm("gh")}})
	for _, over := range []Stratum{
		{Name: "same", Level: 10, Match: []Match{mm("gh")}},
		{Name: "narrower", Level: 20, Match: []Match{mm("gh.pr")}},
		{Name: "everything", Level: 30, Match: []Match{{}}},
		{Name: "anykind", Level: 40, Match: []Match{mm("*.pr")}},
	} {
		if err := eng.AddStratum(over); err == nil {
			t.Errorf("%s: installed a stratum overlapping gh", over.Name)
		}
	}
	// Disjoint at any segment is fine.
	withStrata(t, eng,
		Stratum{Name: "other-place", Level: 50, Match: []Match{mm("linear")}},
		Stratum{Name: "other-subplace", Level: 60, Match: []Match{mm("linear2.issue")}},
	)
}

// A stratum's pattern names a place and nothing about what stands there: a payload predicate
// would make a LEVEL depend on a state that rules change as they fire.
func TestStratumMatchIsAddressOnly(t *testing.T) {
	eng, _ := newEng(t)
	bad := map[string]Stratum{
		"a predicate":   {Name: "c", Level: 0, Match: []Match{{"path": pathOf("gh"), "value": map[string]any{"stage": "open"}}}},
		"no match":      {Name: "d", Level: 0},
		"a bad name":    {Name: "a.b", Level: 0, Match: []Match{mm("gh")}},
		"a bad level":   {Name: "e", Level: -1, Match: []Match{mm("gh")}},
		"an empty name": {Name: "", Level: 0, Match: []Match{mm("gh")}},
	}
	for why, s := range bad {
		if err := eng.AddStratum(s); err == nil {
			t.Errorf("installed a stratum with %s", why)
		}
	}
}

// ── check #1: a rule's subject is inside its stratum ──────────────────────────────

func TestSubjectMustBeInsideItsStratum(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng, demoStrata()...)

	ok := []Trigger{
		{Name: "named", Stratum: "observe", Condition: Condition{Subject: mm("gh.issue.1")}, Action: seenAction()},
		{Name: "prs", Stratum: "observe", Condition: Condition{Subject: mm("gh.pr.*")}, Action: seenAction()},
		{Name: "all-of-gh", Stratum: "observe", Condition: Condition{Subject: mm("gh.*.*")}, Action: seenAction()},
		{Name: "deep", Stratum: "observe", Condition: Condition{Subject: mm("gh.pr.hot-1")}, Action: seenAction()},
	}
	for _, tr := range ok {
		if err := eng.AddTrigger(tr); err != nil {
			t.Errorf("%s: refused a rule inside its stratum: %v", tr.Name, err)
		}
	}

	bad := map[string]Trigger{
		"another stratum's place": {Name: "x1", Stratum: "observe", Condition: Condition{Subject: mm("signadot.feature.*")}},
		// The case containment exists for: this OVERLAPS gh and is not contained by it, so a
		// rule written this way would act on entities at every level.
		"any first segment": {Name: "x2", Stratum: "observe", Condition: Condition{Subject: mm("*.issue.*")}},
		"everything":        {Name: "x3", Stratum: "observe", Condition: Condition{Subject: Match{}}},
	}
	for why, tr := range bad {
		if err := eng.AddTrigger(tr); err == nil {
			t.Errorf("installed a rule whose subject is %s", why)
		}
	}
}

// An unstratified rule is checked against nothing, which is exactly today's semantics — and
// what makes the rest of the suite the oracle for every step of this work.
func TestTheImplicitStratumIsUnconstrained(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng, demoStrata()...)
	if err := eng.AddTrigger(Trigger{
		Name:      "unstratified",
		Condition: Condition{Subject: mm("anything.at-all.*")},
		Action:    seenAction(),
	}); err != nil {
		t.Fatalf("a rule declaring no stratum was refused: %v", err)
	}
}

// seenAction is an action for a rule whose test is about INSTALLING it — the level checks, the
// holding, the removal — and not about what it writes. A rule with no action at all is refused
// (git-issue b2g9hsnfh12ks5vqg9n0), and a `patch` is the one line that satisfies it without
// bringing a spawn, a derive or a level of its own into a test about something else.
func seenAction() Action { return Action{Patch: entity.Obj{"seen": true}.Payload()} }

// ── check #2: a derive lands strictly above ───────────────────────────────────────

// deriveAction is a one-entry derive: payload, written under the place a kinded path names.
func deriveAction(place string, payload entity.Obj) Action {
	segs, err := entity.PathSegs(place)
	if err != nil {
		panic(err)
	}
	return Action{Derive: ir.FromSlice([]*ir.Node{ir.FromKeyVals([]ir.KeyVal{
		{Key: ir.FromString("path"), Val: ir.FromSlice(pathOf(segs...))},
		{Key: ir.FromString("value"), Val: payload.Payload()},
	})})}
}

func TestDeriveMustLandStrictlyAbove(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng, demoStrata()...)

	if err := eng.AddTrigger(Trigger{
		Name:      "up",
		Stratum:   "observe",
		Condition: Condition{Subject: mm("gh.issue.*")},
		Action:    deriveAction("signadot.feature", entity.Obj{"priority": "urgent"}),
	}); err != nil {
		t.Fatalf("refused a derive that climbs a level: %v", err)
	}

	bad := map[string]Trigger{
		"sideways, into its own stratum": {
			Name: "x1", Stratum: "sdlc",
			Condition: Condition{Subject: mm("signadot.feature.*")},
			Action:    deriveAction("signadot.other", entity.Obj{"a": 1}),
		},
		"downward": {
			Name: "x2", Stratum: "sdlc",
			Condition: Condition{Subject: mm("signadot.feature.*")},
			Action:    deriveAction("gh.issue", entity.Obj{"a": 1}),
		},
		"into no stratum at all": {
			Name: "x3", Stratum: "observe",
			Condition: Condition{Subject: mm("gh.issue.*")},
			Action:    deriveAction("nowhere.thing", entity.Obj{"a": 1}),
		},
		// A derive inside a completion hook crosses a level exactly as one at the top does,
		// so the check has to see the whole action tree.
		"inside an onDone": {
			Name: "x4", Stratum: "sdlc",
			Condition: Condition{Subject: mm("signadot.feature.*")},
			Action: Action{
				Spawn:  &launch.Spec{Doer: runner.Doer{Agent: "verse"}, Prompt: "review $subject"},
				OnDone: &Action{Derive: deriveAction("gh.issue", entity.Obj{"a": 1}).Derive},
			},
		},
	}
	for why, tr := range bad {
		err := eng.AddTrigger(tr)
		if err == nil {
			t.Errorf("installed a derive %s", why)
			continue
		}
		if !strings.Contains(err.Error(), "derive") {
			t.Errorf("%s: the refusal does not name the derive: %v", why, err)
		}
	}
}

// The implicit stratum has no level, so "strictly above" has no answer rather than a
// permissive one. Reading it leniently would hand back the unchecked cross-entity write in
// the first release that has the feature.
func TestUnstratifiedRuleMayNotDerive(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng, demoStrata()...)
	err := eng.AddTrigger(Trigger{
		Name:      "loose",
		Condition: Condition{Subject: mm("gh.issue.*")},
		Action:    deriveAction("signadot.feature", entity.Obj{"a": 1}),
	})
	if err == nil {
		t.Fatal("a rule outside the ordering derived into it")
	}
	if !strings.Contains(err.Error(), "stratum") {
		t.Fatalf("the refusal does not say what is missing: %v", err)
	}
}

// The load-bearing one. A derive may reference the firing's IDENTITY and never its STATE:
// $subject is fixed by the same pair the derived address is fixed by, so a re-fire still writes
// identical bytes at an identical address, and the read side keeps the inertness it leans on. A
// value read from a payload moves as rules fire, and would take that away.
func TestDeriveContentMustBeLiteral(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng, demoStrata()...)
	for why, payload := range map[string]entity.Obj{
		"a bare reference":     {"who": "$result"},
		"one inside text":      {"note": "about $result now"},
		"one nested deeper":    {"a": entity.Obj{"b": "$stage"}},
		"one inside a list":    {"a": []any{"ok", "$result"}},
		"a dotted path":        {"a": "$verse.review.step"},
		"an indexed reference": {"a": "$result.findings[0].id"},
	} {
		err := eng.AddTrigger(Trigger{
			Name:      "ref-" + strings.ReplaceAll(why, " ", "-"),
			Stratum:   "observe",
			Condition: Condition{Subject: mm("gh.issue.*")},
			Action:    deriveAction("signadot.feature", payload),
		})
		if err == nil {
			t.Errorf("installed a derive carrying %s", why)
		}
	}
	// A literal dollar is not a reference, and never was — the same rule the expander uses.
	// $subject is, and is the one that is allowed.
	for why, payload := range map[string]entity.Obj{
		"a literal dollar":     {"price": "$5"},
		"$subject alone":       {"what": "$subject"},
		"$subject inside text": {"note": "raised from $subject"},
		"$subject nested":      {"a": entity.Obj{"b": "$subject"}},
		"$subject twice":       {"a": "$subject", "b": "$subject"},
		"$subject beside a $5": {"a": "$subject costs $5"},
	} {
		if err := eng.AddTrigger(Trigger{
			Name:      "ok-" + strings.ReplaceAll(strings.ReplaceAll(why, " ", "-"), "$", ""),
			Stratum:   "observe",
			Condition: Condition{Subject: mm("gh.issue.*")},
			Action:    deriveAction("signadot.feature", payload),
		}); err != nil {
			t.Errorf("%s was refused: %v", why, err)
		}
	}
	// The parts of $subject, which are all admissible in a derive EXCEPT the one that is a
	// function of the firing's occasion rather than of the subject.
	for _, part := range []string{"ref", "name", "value", "value.stage"} {
		if err := eng.AddTrigger(Trigger{
			Name:      "part-" + strings.ReplaceAll(part, ".", "-"),
			Stratum:   "observe",
			Condition: Condition{Subject: mm("gh.issue.*")},
			Action:    deriveAction("signadot.feature", entity.Obj{"x": "$subject." + part}),
		}); err != nil {
			t.Errorf("$subject.%s was refused in a derive: %v", part, err)
		}
	}
	// $subject.commit is refused, and it is the interesting one: it varies with WHEN the
	// firing happened, so a read from a higher level changes what the derive writes — which
	// is a cross-level cycle, the thing the ordering exists to rule out.
	if err := eng.AddTrigger(Trigger{
		Name:      "subject-commit",
		Stratum:   "observe",
		Condition: Condition{Subject: mm("gh.issue.*")},
		Action:    deriveAction("signadot.feature", entity.Obj{"at": "$subject.commit"}),
	}); err == nil {
		t.Error("$subject.commit was accepted in a derive; an upward read would then change " +
			"what it writes, and the loop that follows crosses a level")
	}
}

// $subject resolves, and to the same thing on every firing — which is what keeps a re-fire a
// no-op and the read side inert. Both halves are asserted here because only together do they
// make the reference safe.
func TestDeriveResolvesSubjectAndStaysInert(t *testing.T) {
	eng, store := newEng(t)
	withStrata(t, eng,
		Stratum{Name: "low", Level: 0, Match: []Match{mm("gh")}},
		Stratum{Name: "high", Level: 10, Match: []Match{mm("signadot")}},
	)
	if err := eng.AddTrigger(Trigger{
		Name:      "raise",
		Stratum:   "low",
		Condition: Condition{Subject: mm("gh.issue.*")},
		Action:    deriveAction("signadot.feature", entity.Obj{"what": "$subject", "note": "raised from $subject"}),
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.RefAt("gh", "issue", "1")
	entitytest.Set(t, store, ref, entity.Obj{"n": 1}.Payload())
	derived := waitDerived(t, store, []string{"signadot", "feature"}, "raise", ref)
	obj := entity.ToObj(derived.Payload)
	if obj["what"] != `gh.issue."1"` {
		t.Fatalf("$subject did not resolve to the ref: %+v", obj)
	}
	if obj["note"] != `raised from gh.issue."1"` {
		t.Fatalf("$subject did not resolve inside text: %+v", obj)
	}

	// Re-fire about the same subject: same bytes, same address. If $subject varied, this is
	// where the read side would stop being inert.
	rev := store.Rev()
	entitytest.Set(t, store, ref, entity.Obj{"n": 2}.Payload())
	waitCount(t, store, []string{"signadot", "feature"}, ref, 1)
	again, ok, _ := store.Get(derived.Ref)
	if !ok {
		t.Fatal("the derived entity went away on a re-fire")
	}
	if got := entity.ToObj(again.Payload)["what"]; got != `gh.issue."1"` {
		t.Fatalf("a re-fire changed what the derive wrote: %v", got)
	}
	_ = rev
}

// A derive is a list of {path, value} entries, and nothing else: the nested map that spelled a
// two-level address is refused, not read as one.
func TestDeriveShapeIsAListOfPaths(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng, demoStrata()...)
	bad := map[string]entity.Payload{
		"a scalar at the root": entity.Val("nope"),
		"a nested map":         entity.Obj{"signadot": entity.Obj{"feature": entity.Obj{"a": 1}}}.Payload(),
		"an empty list":        ir.FromSlice(nil),
		"an entry with no path": ir.FromSlice([]*ir.Node{
			entity.Obj{"value": entity.Obj{"a": 1}}.Payload()}),
		"an entry that is not an object": ir.FromSlice([]*ir.Node{ir.FromString("signadot.feature")}),
	}
	for why, d := range bad {
		err := eng.AddTrigger(Trigger{
			Name:      "shape-" + strings.ReplaceAll(why, " ", "-"),
			Stratum:   "observe",
			Condition: Condition{Subject: mm("gh.issue.*")},
			Action:    Action{Derive: d},
		})
		if err == nil {
			t.Errorf("installed a derive that is %s", why)
		}
	}
}

// A DERIVE WITH A COMMENT ABOVE IT INSTALLS AND FIRES. A head comment WRAPS the value it
// precedes, so `# why this ledger entry exists` above `derive:` makes the block a CommentType
// node holding the object — and the walks that read a derive's address asked the wrapper what
// shape it was, got Comment, and refused the rule.
//
// It refused a rule that is written correctly, and it refused it at INSTALL, which is where the
// charter staging runs stopped being installable from its own file: those rules carry a
// paragraph each saying why they derive what they do. A comment is not a container — asking what
// a value IS means stepping past what was said about it (ir/nav.go).
func TestADeriveKeepsItsComment(t *testing.T) {
	eng, store := newEng(t)
	withStrata(t, eng, demoStrata()...)
	src := "name: commented-derive\n" +
		"stratum: observe\n" +
		"condition:\n  subject: {path: \"gh.issue.*\"}\n" +
		// `patch:` first, deliberately: a comment before a container's FIRST entry heads the
		// container, so above `derive:` here it would land on the action and never wrap the
		// value. Second, it wraps — which is the shape a real charter has, and the one that
		// refused to install.
		"action:\n" +
		"  patch: {seen: true}\n" +
		"  # WHY THIS LEDGER ENTRY EXISTS, in the author's words — and the rule still installs.\n" +
		"  derive:\n  - path: \"signadot.feature\"\n    value:\n      urgent: true\n"
	node, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var tr Trigger
	if err := tr.FromTonyIR(node); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tr.Action.Derive == nil || tr.Action.Derive.Type != ir.CommentType {
		t.Fatalf("the derive is not comment-wrapped, so this test proves nothing: %v", tr.Action.Derive)
	}
	if err := eng.AddTrigger(tr); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	// …and it fires: the walk that reads the address is the one validate shares.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("gh", "issue", "1"),
		entity.Obj{"state": "open"}.Payload())
	deadline := time.After(3 * time.Second)
	for {
		// `signadot.feature`, the rule that derived, and the subject's own segments.
		for _, e := range store.ListAt([]string{"signadot", "feature"}, 2+1+3, nil) {
			if entity.ToObj(e.Payload)["urgent"] == true {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("the commented derive never wrote its entity")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// ── derive at fire time ───────────────────────────────────────────────────────────

// The whole point, end to end: a rule at level 0 writes an entity at level 10, the engine
// records what it is about, and the rule that owns level 10 acts on it.
func TestDeriveFiresAndCrossesTheLevel(t *testing.T) {
	eng, store := newEng(t)
	withStrata(t, eng, demoStrata()...)
	if err := eng.AddTrigger(Trigger{
		Name:      "urgent-label",
		Stratum:   "observe",
		Condition: Condition{Subject: mm("gh.issue.*")},
		Action: Action{
			Patch:  entity.Obj{"triaged": true}.Payload(),
			Derive: deriveAction("signadot.feature", entity.Obj{"priority": "urgent"}).Derive,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := eng.AddTrigger(Trigger{
		Name:    "request-approval",
		Stratum: "sdlc",
		// SIX SEGMENTS: the target `signadot.feature`, the rule that derived, and the three
		// of the subject it derived about. A derived entity is as deep as what it is about,
		// so a rule reading one says so.
		Condition: Condition{Subject: mustSteps(t, "signadot", "feature", "*", "*", "*", "*")},
		Action:    Action{Patch: entity.Obj{"stage": "awaiting-approval"}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("gh", "issue", "1"), entity.Obj{"label": "urgent"}.Payload())

	derived := waitDerived(t, store, []string{"signadot", "feature"}, "urgent-label",
		entity.RefAt("gh", "issue", "1"))
	obj := entity.ToObj(derived.Payload)
	if obj["priority"] != "urgent" {
		t.Fatalf("the derived entity does not carry what the charter wrote: %+v", obj)
	}
	// The linkage: the engine's, not the charter's, and it is how the higher stratum knows
	// WHICH issue this derivation is about.
	from := entity.Get(entity.Get(entity.Get(derived.Payload, entity.ReservedRoot), entity.MetaKey), deriveFromField)
	if got := entity.ToObj(from)["subject"]; got != `gh.issue."1"` {
		t.Fatalf("the derived entity does not record its subject: %+v", entity.ToObj(from))
	}
	// And the level above acted on it.
	waitStage(t, store, derived.Ref, "awaiting-approval")
}

// A derive is a function of (rule, subject), so re-firing writes the same address — which is
// what makes a re-fire idempotent, and what retires "two triggers, one field" for everything
// derived.
func TestDeriveIsIdempotentPerSubject(t *testing.T) {
	eng, store := newEng(t)
	withStrata(t, eng,
		Stratum{Name: "low", Level: 0, Match: []Match{mm("gh")}},
		Stratum{Name: "high", Level: 10, Match: []Match{mm("signadot")}},
	)
	if err := eng.AddTrigger(Trigger{
		Name:      "derive-one",
		Stratum:   "low",
		Condition: Condition{Subject: mm("gh.issue.*")},
		Action:    deriveAction("signadot.feature", entity.Obj{"seen": true}),
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.RefAt("gh", "issue", "1")
	entitytest.Set(t, store, ref, entity.Obj{"n": 1}.Payload())
	waitDerived(t, store, []string{"signadot", "feature"}, "derive-one", ref)
	// Change the subject again: the same rule about the same subject writes the same address.
	entitytest.Set(t, store, ref, entity.Obj{"n": 2}.Payload())
	entitytest.Set(t, store, entity.RefAt("gh", "issue", "2"), entity.Obj{"n": 1}.Payload())
	waitCount(t, store, []string{"signadot", "feature"}, ref, 2)
}

// ── holding a rule whose stratum has not arrived ──────────────────────────────────

// A rule and a stratum are separate entities and either can be installed first. Refusing here
// would make the ORDER of two independent writes matter, which is the property
// one-entity-per-rule exists to avoid.
func TestARuleIsHeldUntilItsStratumArrives(t *testing.T) {
	eng, store := newEng(t)
	tr := Trigger{
		Name:      "early",
		Stratum:   "not-yet",
		Condition: Condition{Subject: mm("gh.issue.*")},
		Action:    Action{Patch: entity.Obj{"stage": "seen"}.Payload()},
	}
	if err := eng.AddTrigger(tr); err != nil {
		t.Fatalf("a rule naming an absent stratum was refused rather than held: %v", err)
	}
	if len(eng.Triggers()) != 0 {
		t.Fatal("a held rule is installed; it must not fire until its stratum arrives")
	}
	// It is written down: held is not lost.
	if _, ok, _ := store.Get(CharterRef("early")); !ok {
		t.Fatal("a held rule was not persisted; a restart would lose it")
	}
	// A second rule under the same name is refused, held or not.
	if err := eng.AddTrigger(tr); err == nil {
		t.Fatal("two rules were held under one name")
	}
	// And it installs when its stratum does.
	withStrata(t, eng, Stratum{Name: "not-yet", Level: 0, Match: []Match{mm("gh")}})
	if len(eng.Triggers()) != 1 {
		t.Fatalf("the held rule did not install when its stratum arrived: %d installed", len(eng.Triggers()))
	}
}

// Removing a held rule has to remove it: it was written down, so leaving it behind would
// install it the moment its stratum arrived — a rule coming back after an operator removed it.
func TestAHeldRuleCanBeRemoved(t *testing.T) {
	eng, _ := newEng(t)
	if err := eng.AddTrigger(Trigger{
		Name:      "early",
		Stratum:   "not-yet",
		Condition: Condition{Subject: mm("gh.issue.*")},
		Action:    seenAction(),
	}); err != nil {
		t.Fatal(err)
	}
	if !eng.RemoveTrigger("early") {
		t.Fatal("removing a held rule reported that it did not exist")
	}
	withStrata(t, eng, Stratum{Name: "not-yet", Level: 0, Match: []Match{mm("gh")}})
	if len(eng.Triggers()) != 0 {
		t.Fatal("a removed rule came back when its stratum arrived")
	}
}

// ── removal ───────────────────────────────────────────────────────────────────────

// Removing a stratum out from under its rules would leave them installed and unchecked: they
// passed check #1 against a pattern that no longer exists, and nothing re-runs a check.
func TestAStratumWithRulesCannotBeRemoved(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng, demoStrata()...)
	if err := eng.AddTrigger(Trigger{
		Name:      "r",
		Stratum:   "observe",
		Condition: Condition{Subject: mm("gh.issue.*")},
		Action:    seenAction(),
	}); err != nil {
		t.Fatal(err)
	}
	err := eng.RemoveStratum("observe")
	if err == nil {
		t.Fatal("removed a stratum still holding a rule")
	}
	if !strings.Contains(err.Error(), "r") {
		t.Fatalf("the refusal does not name what is holding it: %v", err)
	}
	eng.RemoveTrigger("r")
	if err := eng.RemoveStratum("observe"); err != nil {
		t.Fatalf("could not remove an empty stratum: %v", err)
	}
	if _, ok := eng.Stratum("observe"); ok {
		t.Fatal("the stratum is still installed")
	}
}

// ── durability ────────────────────────────────────────────────────────────────────

// Strata are durable for the same reason rules are: a verse comes back up with its work in
// progress intact, and the ordering its rules were checked against has to come back with it.
func TestStrataSurviveARestart(t *testing.T) {
	store := entitytest.Store(t)
	first := New(store, task.AutoResolver{})
	withStrata(t, first, demoStrata()...)
	if err := first.AddTrigger(Trigger{
		Name:      "r",
		Stratum:   "observe",
		Condition: Condition{Subject: mm("gh.issue.*")},
		Action:    deriveAction("signadot.feature", entity.Obj{"a": 1}),
	}); err != nil {
		t.Fatal(err)
	}

	second := New(store, task.AutoResolver{})
	if err := second.LoadStrata(); err != nil {
		t.Fatalf("LoadStrata: %v", err)
	}
	if err := second.LoadCharter(); err != nil {
		t.Fatalf("LoadCharter: %v", err)
	}
	got := second.Strata()
	if len(got) != 3 || got[0].Name != "observe" || got[2].Level != 100 {
		t.Fatalf("the ordering did not come back: %+v", got)
	}
	if len(second.Triggers()) != 1 {
		t.Fatalf("the rule did not come back installed: %+v", second.Triggers())
	}
	// The match round-tripped as a match, not as whatever a naive encoding would have made
	// of it — this is what the raw escape on the stratum entity is for.
	if !Matches(got[0].Match).contains(mm("gh.issue.*")) {
		t.Fatalf("the stratum's pattern did not survive the round trip: %+v", got[0].Match)
	}
}

// The ordering has to load BEFORE the charter, or every restored rule is held and the verse
// comes up with a charter that does not fire.
func TestCharterLoadedBeforeStrataHoldsItsRules(t *testing.T) {
	store := entitytest.Store(t)
	first := New(store, task.AutoResolver{})
	withStrata(t, first, demoStrata()...)
	if err := first.AddTrigger(Trigger{
		Name:      "r",
		Stratum:   "observe",
		Condition: Condition{Subject: mm("gh.issue.*")},
		Action:    seenAction(),
	}); err != nil {
		t.Fatal(err)
	}
	// Deliberately the wrong order, to pin what it costs.
	second := New(store, task.AutoResolver{})
	if err := second.LoadCharter(); err != nil {
		t.Fatal(err)
	}
	if len(second.Triggers()) != 0 {
		t.Fatal("a rule installed before its stratum existed")
	}
	if err := second.LoadStrata(); err != nil {
		t.Fatal(err)
	}
	if len(second.Triggers()) != 1 {
		t.Fatal("the held rule did not install once the ordering loaded")
	}
}

// ── the tony surface ──────────────────────────────────────────────────────────────

func TestStratumRoundTrip(t *testing.T) {
	for _, s := range []Stratum{
		{Name: "one", Level: 0, Match: []Match{mm("gh")}},
		{Name: "union", Level: 10, Match: []Match{mm("gh"), mm("linear"), mm("slack")}},
		{Name: "kinded", Level: 20, Match: []Match{mm("do.approve")}},
	} {
		node, err := s.ToTonyIR()
		if err != nil {
			t.Fatalf("%s: ToTonyIR: %v", s.Name, err)
		}
		var back Stratum
		if err := back.FromTonyIR(node); err != nil {
			t.Fatalf("%s: FromTonyIR: %v", s.Name, err)
		}
		if back.Name != s.Name || back.Level != s.Level || len(back.Match) != len(s.Match) {
			t.Fatalf("%s: round trip changed it: %+v", s.Name, back)
		}
		for i := range s.Match {
			if !reflect.DeepEqual(back.Match[i].Segs(), s.Match[i].Segs()) {
				t.Fatalf("%s: match[%d] changed: %+v", s.Name, i, back.Match[i])
			}
		}
	}
}

// A misspelled field is named rather than dropped. A stratum has three fields and each one
// changes what the ordering IS, so `levle: 10` installing at level 0 would be a different
// charter than the one written, reported by nothing.
func TestStratumRefusesUnknownFields(t *testing.T) {
	var s Stratum
	err := s.FromTonyIR(entity.Obj{"name": "a", "levle": 10, "match": "gh"}.Payload())
	if err == nil {
		t.Fatal("a misspelled field was silently dropped")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────────

// waitDerived waits for the entity a rule derived about a subject, and NAMES it rather than
// searching for one: a derived entity stands at `<target>.<rule>.<subject…>`, so the address is
// a function of what the test already knows.
//
// Naming it is a stronger assertion as well as a simpler one — a scan of the target passes on any
// derived entity under it, this passes only on the right one.
func waitDerived(t *testing.T, s entity.Store, target []string, rule string, subj entity.Ref) entity.Entity {
	t.Helper()
	ref := entity.RefAt(append(append([]string{}, target...), deriveSegs(rule, subj)...)...)
	deadline := time.After(3 * time.Second)
	for {
		if e, ok, _ := s.Get(ref); ok {
			return e
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for the entity %s derived at %s", rule, ref)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// waitCount waits for a count of derived entities under a target, at the depth a derivation
// about `subj` lands at — which is what a listing has to say now that entity-hood is a lens.
func waitCount(t *testing.T, s entity.Store, target []string, subj entity.Ref, want int) {
	t.Helper()
	depth := len(target) + 1 + len(subj.Segs()) // the target, the rule segment, and the subject
	deadline := time.After(3 * time.Second)
	for {
		ents := s.ListAt(target, depth, nil)
		if len(ents) == want {
			return
		}
		if len(ents) > want {
			var refs []string
			for _, e := range ents {
				refs = append(refs, e.Ref.String())
			}
			t.Fatalf("%d derived entities, want %d: %v", len(ents), want, refs)
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d derived under %v (have %d)", want, target, len(ents))
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// An unknown field decodes silently — gomap ignores what it does not recognise and has no
// strict-unmap option — so the field that says which guarantees apply is exactly what a
// misspelling discards. This is the check at the decode boundary that names it instead.
func TestUnknownRuleFieldsAreNamed(t *testing.T) {
	bad := map[string]entity.Obj{
		"a misspelled stratum": {"name": "r", "startum": "observe", "condition": entity.Obj{"subject": "gh.issue.*"}},
		"a misspelled derive":  {"name": "r", "condition": entity.Obj{"subject": "gh.issue.*"}, "action": entity.Obj{"derve": []any{entity.Obj{"path": "a.b", "value": entity.Obj{}}}}},
		"one in a condition":   {"name": "r", "condition": entity.Obj{"subjekt": "gh.issue.*"}},
		"one in a hook":        {"name": "r", "condition": entity.Obj{"subject": "gh.issue.*"}, "action": entity.Obj{"onDone": entity.Obj{"patsh": entity.Obj{}}}},
		"one in a seq element": {"name": "r", "condition": entity.Obj{"subject": "gh.issue.*"}, "action": entity.Obj{"seq": []any{entity.Obj{"patsh": entity.Obj{}}}}},
	}
	for why, doc := range bad {
		if err := CheckFields(doc.Payload()); err == nil {
			t.Errorf("%s was accepted; it would install as a rule that does less than it says", why)
		}
	}
	// And the shapes a real charter comes in are untouched.
	ok := entity.Obj{
		"name": "r", "stratum": "observe",
		"condition": entity.Obj{"subject": "gh.issue.*", "when": entity.Obj{}, "on": entity.Obj{}},
		"gates":     []any{entity.Obj{"mode": "ask"}},
		"action": entity.Obj{
			"patch":  entity.Obj{"stage": "x"},
			"derive": []any{entity.Obj{"path": "sig.feature", "value": entity.Obj{"a": 1}}},
			"seq":    []any{entity.Obj{"patch": entity.Obj{"a": 1}}},
			"onDone": entity.Obj{"patch": entity.Obj{"b": 2}},
		},
	}
	if err := CheckFields(ok.Payload()); err != nil {
		t.Fatalf("a well-formed rule was refused: %v", err)
	}
}

// The causal chain crosses the level. A derive is written with the firing's own commit as its
// parent, so the rule woken at the level ABOVE continues the chain rather than starting one.
//
// This is the one thing here that would fail quietly: losing the link does not loosen the
// throttle's bound — a chain never returns to a stratum it has left, so a per-segment count
// still trips — but it truncates the report that names the rules which looped, so a cascade
// would be described as though it began at the crossing.
//
// It takes THREE hops to see, and the shorter version of this test is worthless: an absent
// parent reads as depth 0, so a derive stamped with no parent still lands at depth 1 when the
// thing that woke it was an external write. The chain has to be deep enough on the low side
// that a lost link shows up as a shortfall.
func TestTheCausalChainCrossesTheLevel(t *testing.T) {
	eng, store := newEng(t)
	withStrata(t, eng,
		Stratum{Name: "low", Level: 0, Match: []Match{mm("gh")}},
		Stratum{Name: "high", Level: 10, Match: []Match{mm("signadot")}},
	)
	// gh.issue.1 stage a → b (depth 1), b → derive (depth 2), and the level above acts off
	// the derive (depth 3).
	for _, tr := range []Trigger{{
		Name:      "advance",
		Stratum:   "low",
		Condition: Condition{Subject: Match{"path": pathOf("gh", "issue", "*"), "value": map[string]any{"stage": "a"}}},
		Action:    Action{Patch: entity.Obj{"stage": "b"}.Payload()},
	}, {
		Name:      "derives",
		Stratum:   "low",
		Condition: Condition{Subject: Match{"path": pathOf("gh", "issue", "*"), "value": map[string]any{"stage": "b"}}},
		Action:    deriveAction("signadot.feature", entity.Obj{"priority": "urgent"}),
	}, {
		Name:    "consumes",
		Stratum: "high",
		// The target, the rule that derived, and the subject it derived about: a rule reading
		// a derivation says how deep one is.
		Condition: Condition{Subject: withValue(mustSteps(t, "signadot", "feature", "*", "*", "*", "*"),
			map[string]any{"priority": "urgent"})},
		Action: Action{Patch: entity.Obj{"stage": "awaiting-approval"}.Payload()},
	}} {
		if err := eng.AddTrigger(tr); err != nil {
			t.Fatalf("%s: %v", tr.Name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("gh", "issue", "1"), entity.Obj{"stage": "a"}.Payload())
	derived := waitDerived(t, store, []string{"signadot", "feature"}, "derives",
		entity.RefAt("gh", "issue", "1"))
	waitStage(t, store, derived.Ref, "awaiting-approval")

	// THE LINK IS WAITED FOR, as the stage was. A write is committed and then its link is
	// recorded (causal.Write), so a reader that has just seen the commit may be looking before
	// the link is there. What is asked here is how deep the chain goes, not when it was written
	// down.
	depth := func() int {
		eng.chainMu.Lock()
		defer eng.chainMu.Unlock()
		deepest := 0
		for _, l := range eng.causes {
			if l.depth > deepest {
				deepest = l.depth
			}
		}
		return deepest
	}
	deepest := depth()
	for deadline := time.Now().Add(2 * time.Second); deepest < 3 && time.Now().Before(deadline); deepest = depth() {
		time.Sleep(5 * time.Millisecond)
	}
	if deepest < 3 {
		t.Fatalf("the chain did not continue across the level: deepest recorded depth is %d, "+
			"want at least 3 (advance, derive, then the rule the derive woke above) — a derive "+
			"written without its parent starts a fresh chain, and the throttle's report would "+
			"name the crossing as the origin", deepest)
	}
}

// $subject's parts, in a SPAWN string. Spawn templates do not go through the reference
// machinery at all — launch.Bind is a flat ReplaceAll — so the dotted forms only work if
// bindingFor publishes them as keys. Forgetting that does not error: `$subject.name` substitutes
// the bare key and leaves the text "demo.pr.1.name", which is why this is pinned.
func TestSubjectPartsBindInSpawnStrings(t *testing.T) {
	b := bindingFor(entity.Obj{"stage": "open"}.Payload(), entity.RefAt("demo", "pr", "1"))
	spec := launch.Spec{Doer: runner.Doer{Agent: "verse"}, Prompt: "review $subject.name at $subject.ref ($subject) at $stage"}
	got := spec.Bind(b).Prompt
	if want := `review 1 at demo.pr."1" (demo.pr."1") at open`; got != want {
		t.Fatalf("task = %q, want %q", got, want)
	}
}

// The record's parts resolve in a patch, and the bare form still means the address — which is
// what keeps every charter written before the record existed meaning what it meant.
func TestSubjectPartsResolveInAPatch(t *testing.T) {
	root := refRoot(entity.Obj{"stage": "open"}.Payload(),
		subjectNode(entity.RefAt("demo", "pr", "7"), entity.Obj{"stage": "open"}.Payload(), 42))
	for ref, want := range map[string]string{
		"$subject":             `demo.pr."7"`,
		"$subject.ref":         `demo.pr."7"`,
		"$subject.name":        "7",
		"$subject.commit":      "42",
		"$subject.value.stage": "open",
	} {
		got, err := expandString(ref, root)
		if err != nil {
			t.Errorf("%s: %v", ref, err)
			continue
		}
		if txt, _ := scalarText(got); txt != want {
			t.Errorf("%s = %q, want %q", ref, txt, want)
		}
	}
}

// A FIRING HELD AT A GATE WITHDRAWS when its subject moves out of the rule's selection before
// the answer comes.
//
// $subject.value reads the subject as of the commit that caused the firing (the case above),
// so a late answer could act from that snapshot. It does not: a gate asks its question of a
// world, and the answer applies to that world. A subject that the rule would no longer select
// is one the charter has moved on from, and acting on the old snapshot anyway is how a merge
// landed on a branch a sweep had retired (git-issue 8wt1ta8r). The question
// leaves the docket with the reason (see gatemoot_test.go), nothing is decided, and the
// patch never lands.
func TestAFiringWithdrawsWhenItsSubjectMovesOnUnderTheGate(t *testing.T) {
	store := entitytest.Store(t)
	release := make(chan struct{})
	eng := New(store, blockingResolver{release: release})
	if err := eng.AddTrigger(Trigger{
		Name:      "record",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Gates:     []Gate{{Mode: "ask"}},
		Action:    Action{Patch: entity.Obj{"sawStage": "$subject.value.stage", "sawAt": "$subject.commit"}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	// Let the firing reach its gate, then move the subject on underneath it.
	time.Sleep(150 * time.Millisecond)
	entitytest.Set(t, store, ref, entity.Obj{"stage": "moved-on"}.Payload())
	time.Sleep(100 * time.Millisecond)
	close(release)

	time.Sleep(500 * time.Millisecond)
	e, _, _ := store.Get(ref)
	got := entity.ToObj(e.Payload)
	if got["sawStage"] != nil {
		t.Fatalf("the firing acted on a subject the rule no longer selects: %+v", got)
	}
	if got["stage"] != "moved-on" {
		t.Fatalf("the subject = %+v, want it left where the world put it", got)
	}
}

// blockingResolver holds every gate until release is closed, then proceeds — standing in for
// the approval that takes long enough for the world to move.
type blockingResolver struct{ release chan struct{} }

func (r blockingResolver) Resolve(ctx context.Context, q task.GateQuestion, tu task.ToolUse) (task.Resolution, error) {
	select {
	case <-r.release:
	case <-ctx.Done():
		return task.Resolution{}, ctx.Err()
	}
	return task.Resolution{Proceed: true, By: "test"}, nil
}

// mustSteps builds a path match from steps — the spelling a charter writes as
// `path: "a.b.*"`, for a test assembling a rule in Go.
func mustSteps(t *testing.T, steps ...string) Match {
	t.Helper()
	m, err := ParseSteps(steps)
	if err != nil {
		t.Fatalf("ParseSteps(%v): %v", steps, err)
	}
	return m
}

// withValue puts a payload predicate on a match — the `value:` a charter writes beside its
// `path:`.
func withValue(m Match, pred map[string]any) Match {
	m["value"] = pred
	return m
}

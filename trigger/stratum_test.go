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

// observe/sdlc/effectuate — the ordering the design note is written around, which is what
// most of these tests install.
func demoStrata() []Stratum {
	return []Stratum{
		{Name: "observe", Level: 0, Match: []Match{{"system": "gh"}}},
		{Name: "sdlc", Level: 10, Match: []Match{{"system": "signadot"}}},
		{Name: "effectuate", Level: 100, Match: []Match{{"system": "do"}}},
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
	withStrata(t, eng, Stratum{Name: "a", Level: 10, Match: []Match{{"system": "x"}}})
	err := eng.AddStratum(Stratum{Name: "b", Level: 10, Match: []Match{{"system": "y"}}})
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
	withStrata(t, eng, Stratum{Name: "a", Level: 0, Match: []Match{{"system": "gh"}}})
	for _, over := range []Stratum{
		{Name: "same", Level: 10, Match: []Match{{"system": "gh"}}},
		{Name: "narrower", Level: 20, Match: []Match{{"system": "gh", "kind": "pr"}}},
		{Name: "everything", Level: 30, Match: []Match{{}}},
		{Name: "anykind", Level: 40, Match: []Match{{"kind": "pr"}}},
	} {
		if err := eng.AddStratum(over); err == nil {
			t.Errorf("%s: installed a stratum overlapping gh", over.Name)
		}
	}
	// Disjoint at either dimension is fine.
	withStrata(t, eng,
		Stratum{Name: "other-system", Level: 50, Match: []Match{{"system": "linear"}}},
		Stratum{Name: "other-kind", Level: 60, Match: []Match{{"system": "linear2", "kind": "issue"}}},
	)
}

// A stratum's pattern names an address and nothing below it. An id would make check #2
// undecidable at install — the derived id is the engine's and does not exist yet — and a
// payload predicate would make a LEVEL depend on a state that rules change as they fire.
func TestStratumMatchIsAddressOnly(t *testing.T) {
	eng, _ := newEng(t)
	bad := map[string]Stratum{
		"an id":         {Name: "a", Level: 0, Match: []Match{{"system": "gh", "kind": "pr", "id": "1"}}},
		"an id glob":    {Name: "b", Level: 0, Match: []Match{{"system": "gh", "kind": "pr", "id": "hot-*"}}},
		"a predicate":   {Name: "c", Level: 0, Match: []Match{{"system": "gh", "value": map[string]any{"stage": "open"}}}},
		"no match":      {Name: "d", Level: 0},
		"a bad name":    {Name: "a.b", Level: 0, Match: []Match{{"system": "gh"}}},
		"a bad level":   {Name: "e", Level: -1, Match: []Match{{"system": "gh"}}},
		"an empty name": {Name: "", Level: 0, Match: []Match{{"system": "gh"}}},
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
		{Name: "named", Stratum: "observe", Condition: Condition{Subject: Match{"system": "gh", "kind": "issue", "id": "1"}}},
		{Name: "kind", Stratum: "observe", Condition: Condition{Subject: Match{"system": "gh", "kind": "pr"}}},
		{Name: "system", Stratum: "observe", Condition: Condition{Subject: Match{"system": "gh"}}},
		{Name: "glob", Stratum: "observe", Condition: Condition{Subject: Match{"system": "gh", "kind": "pr", "id": "hot-*"}}},
	}
	for _, tr := range ok {
		if err := eng.AddTrigger(tr); err != nil {
			t.Errorf("%s: refused a rule inside its stratum: %v", tr.Name, err)
		}
	}

	bad := map[string]Trigger{
		"another stratum's system": {Name: "x1", Stratum: "observe", Condition: Condition{Subject: Match{"system": "signadot", "kind": "feature"}}},
		// The case containment exists for: this OVERLAPS gh and is not contained by it, so a
		// rule written this way would act on entities at every level.
		"any system": {Name: "x2", Stratum: "observe", Condition: Condition{Subject: Match{"kind": "issue"}}},
		"everything": {Name: "x3", Stratum: "observe", Condition: Condition{Subject: Match{}}},
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
		Condition: Condition{Subject: Match{"system": "anything", "kind": "at-all"}},
	}); err != nil {
		t.Fatalf("a rule declaring no stratum was refused: %v", err)
	}
}

// ── check #2: a derive lands strictly above ───────────────────────────────────────

func deriveAction(system, kind string, payload entity.Obj) Action {
	return Action{Derive: entity.Obj{system: entity.Obj{kind: payload}}.Payload()}
}

func TestDeriveMustLandStrictlyAbove(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng, demoStrata()...)

	if err := eng.AddTrigger(Trigger{
		Name:      "up",
		Stratum:   "observe",
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
		Action:    deriveAction("signadot", "feature", entity.Obj{"priority": "urgent"}),
	}); err != nil {
		t.Fatalf("refused a derive that climbs a level: %v", err)
	}

	bad := map[string]Trigger{
		"sideways, into its own stratum": {
			Name: "x1", Stratum: "sdlc",
			Condition: Condition{Subject: Match{"system": "signadot", "kind": "feature"}},
			Action:    deriveAction("signadot", "other", entity.Obj{"a": 1}),
		},
		"downward": {
			Name: "x2", Stratum: "sdlc",
			Condition: Condition{Subject: Match{"system": "signadot", "kind": "feature"}},
			Action:    deriveAction("gh", "issue", entity.Obj{"a": 1}),
		},
		"into no stratum at all": {
			Name: "x3", Stratum: "observe",
			Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
			Action:    deriveAction("nowhere", "thing", entity.Obj{"a": 1}),
		},
		// A derive inside a completion hook crosses a level exactly as one at the top does,
		// so the check has to see the whole action tree.
		"inside an onDone": {
			Name: "x4", Stratum: "sdlc",
			Condition: Condition{Subject: Match{"system": "signadot", "kind": "feature"}},
			Action: Action{
				Spawn:  &launch.Spec{Prompt: "review $subject"},
				OnDone: &Action{Derive: entity.Obj{"gh": entity.Obj{"issue": entity.Obj{"a": 1}}}.Payload()},
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
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
		Action:    deriveAction("signadot", "feature", entity.Obj{"a": 1}),
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
			Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
			Action:    deriveAction("signadot", "feature", payload),
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
			Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
			Action:    deriveAction("signadot", "feature", payload),
		}); err != nil {
			t.Errorf("%s was refused: %v", why, err)
		}
	}
	// The parts of $subject, which are all admissible in a derive EXCEPT the one that is a
	// function of the firing's occasion rather than of the subject.
	for _, part := range []string{"ref", "system", "kind", "id", "value", "value.stage"} {
		if err := eng.AddTrigger(Trigger{
			Name:      "part-" + strings.ReplaceAll(part, ".", "-"),
			Stratum:   "observe",
			Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
			Action:    deriveAction("signadot", "feature", entity.Obj{"x": "$subject." + part}),
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
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
		Action:    deriveAction("signadot", "feature", entity.Obj{"at": "$subject.commit"}),
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
		Stratum{Name: "low", Level: 0, Match: []Match{{"system": "gh"}}},
		Stratum{Name: "high", Level: 10, Match: []Match{{"system": "signadot"}}},
	)
	if err := eng.AddTrigger(Trigger{
		Name:      "raise",
		Stratum:   "low",
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
		Action:    deriveAction("signadot", "feature", entity.Obj{"what": "$subject", "note": "raised from $subject"}),
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.Ref{System: "gh", Kind: "issue", ID: "1"}
	entitytest.Set(t, store, ref, entity.Obj{"n": 1}.Payload())
	derived := waitDerived(t, store, "signadot", "feature")
	obj := entity.ToObj(derived.Payload)
	if obj["what"] != "gh:issue:1" {
		t.Fatalf("$subject did not resolve to the ref: %+v", obj)
	}
	if obj["note"] != "raised from gh:issue:1" {
		t.Fatalf("$subject did not resolve inside text: %+v", obj)
	}

	// Re-fire about the same subject: same bytes, same address. If $subject varied, this is
	// where the read side would stop being inert.
	rev := store.Rev()
	entitytest.Set(t, store, ref, entity.Obj{"n": 2}.Payload())
	waitCount(t, store, "signadot", "feature", 1)
	again, ok, _ := store.Get(derived.Ref)
	if !ok {
		t.Fatal("the derived entity went away on a re-fire")
	}
	if got := entity.ToObj(again.Payload)["what"]; got != "gh:issue:1" {
		t.Fatalf("a re-fire changed what the derive wrote: %v", got)
	}
	_ = rev
}

func TestDeriveShapeIsTwoLevels(t *testing.T) {
	eng, _ := newEng(t)
	withStrata(t, eng, demoStrata()...)
	bad := map[string]entity.Payload{
		"a scalar at the root": entity.Val("nope"),
		"one level":            entity.Obj{"signadot": "feature"}.Payload(),
		"empty":                entity.Obj{}.Payload(),
		"a scalar payload":     entity.Obj{"signadot": entity.Obj{"feature": "urgent"}}.Payload(),
		"an empty kind object": entity.Obj{"signadot": entity.Obj{}}.Payload(),
	}
	for why, d := range bad {
		err := eng.AddTrigger(Trigger{
			Name:      "shape-" + strings.ReplaceAll(why, " ", "-"),
			Stratum:   "observe",
			Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
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
		"condition:\n  subject: {system: gh, kind: issue}\n" +
		// `patch:` first, deliberately: a comment before a container's FIRST entry heads the
		// container, so above `derive:` here it would land on the action and never wrap the
		// value. Second, it wraps — which is the shape a real charter has, and the one that
		// refused to install.
		"action:\n" +
		"  patch: {seen: true}\n" +
		"  # WHY THIS LEDGER ENTRY EXISTS, in the author's words — and the rule still installs.\n" +
		"  derive:\n    signadot:\n      feature:\n        urgent: true\n"
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
	entitytest.Set(t, store, entity.Ref{System: "gh", Kind: "issue", ID: "1"},
		entity.Obj{"state": "open"}.Payload())
	deadline := time.After(3 * time.Second)
	for {
		for _, e := range store.List(func(r entity.Ref) bool {
			return r.System == "signadot" && r.Kind == "feature"
		}) {
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
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
		Action: Action{
			Patch:  entity.Obj{"triaged": true}.Payload(),
			Derive: entity.Obj{"signadot": entity.Obj{"feature": entity.Obj{"priority": "urgent"}}}.Payload(),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := eng.AddTrigger(Trigger{
		Name:      "request-approval",
		Stratum:   "sdlc",
		Condition: Condition{Subject: Match{"system": "signadot", "kind": "feature"}},
		Action:    Action{Patch: entity.Obj{"stage": "awaiting-approval"}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.Ref{System: "gh", Kind: "issue", ID: "1"}, entity.Obj{"label": "urgent"}.Payload())

	derived := waitDerived(t, store, "signadot", "feature")
	obj := entity.ToObj(derived.Payload)
	if obj["priority"] != "urgent" {
		t.Fatalf("the derived entity does not carry what the charter wrote: %+v", obj)
	}
	// The linkage: the engine's, not the charter's, and it is how the higher stratum knows
	// WHICH issue this derivation is about.
	from := entity.Get(entity.Get(entity.Get(derived.Payload, entity.ReservedRoot), entity.MetaKey), deriveFromField)
	if got := entity.ToObj(from)["subject"]; got != "gh:issue:1" {
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
		Stratum{Name: "low", Level: 0, Match: []Match{{"system": "gh"}}},
		Stratum{Name: "high", Level: 10, Match: []Match{{"system": "signadot"}}},
	)
	if err := eng.AddTrigger(Trigger{
		Name:      "derive-one",
		Stratum:   "low",
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
		Action:    deriveAction("signadot", "feature", entity.Obj{"seen": true}),
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.Ref{System: "gh", Kind: "issue", ID: "1"}
	entitytest.Set(t, store, ref, entity.Obj{"n": 1}.Payload())
	waitDerived(t, store, "signadot", "feature")
	// Change the subject again: the same rule about the same subject writes the same address.
	entitytest.Set(t, store, ref, entity.Obj{"n": 2}.Payload())
	entitytest.Set(t, store, entity.Ref{System: "gh", Kind: "issue", ID: "2"}, entity.Obj{"n": 1}.Payload())
	waitCount(t, store, "signadot", "feature", 2)
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
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
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
	withStrata(t, eng, Stratum{Name: "not-yet", Level: 0, Match: []Match{{"system": "gh"}}})
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
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
	}); err != nil {
		t.Fatal(err)
	}
	if !eng.RemoveTrigger("early") {
		t.Fatal("removing a held rule reported that it did not exist")
	}
	withStrata(t, eng, Stratum{Name: "not-yet", Level: 0, Match: []Match{{"system": "gh"}}})
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
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
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
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
		Action:    deriveAction("signadot", "feature", entity.Obj{"a": 1}),
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
	if !Matches(got[0].Match).contains(Match{"system": "gh", "kind": "issue"}) {
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
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue"}},
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
		{Name: "one", Level: 0, Match: []Match{{"system": "gh"}}},
		{Name: "union", Level: 10, Match: []Match{{"system": "gh"}, {"system": "linear"}, {"system": "slack"}}},
		{Name: "kinded", Level: 20, Match: []Match{{"system": "do", "kind": "approve"}}},
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
			if back.Match[i].System() != s.Match[i].System() || back.Match[i].Kind() != s.Match[i].Kind() {
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

func waitDerived(t *testing.T, s entity.Store, system, kind string) entity.Entity {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		ents := s.List(func(r entity.Ref) bool { return r.System == system && r.Kind == kind })
		if len(ents) > 0 {
			return ents[0]
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for a derived %s:%s", system, kind)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func waitCount(t *testing.T, s entity.Store, system, kind string, want int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		ents := s.List(func(r entity.Ref) bool { return r.System == system && r.Kind == kind })
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
			t.Fatalf("timed out waiting for %d derived %s:%s (have %d)", want, system, kind, len(ents))
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// An unknown field decodes silently — gomap ignores what it does not recognise and has no
// strict-unmap option — so the field that says which guarantees apply is exactly what a
// misspelling discards. This is the check at the decode boundary that names it instead.
func TestUnknownRuleFieldsAreNamed(t *testing.T) {
	bad := map[string]entity.Obj{
		"a misspelled stratum": {"name": "r", "startum": "observe", "condition": entity.Obj{"subject": "gh:issue:*"}},
		"a misspelled derive":  {"name": "r", "condition": entity.Obj{"subject": "gh:issue:*"}, "action": entity.Obj{"derve": entity.Obj{"a": entity.Obj{"b": entity.Obj{}}}}},
		"one in a condition":   {"name": "r", "condition": entity.Obj{"subjekt": "gh:issue:*"}},
		"one in a hook":        {"name": "r", "condition": entity.Obj{"subject": "gh:issue:*"}, "action": entity.Obj{"onDone": entity.Obj{"patsh": entity.Obj{}}}},
		"one in a seq element": {"name": "r", "condition": entity.Obj{"subject": "gh:issue:*"}, "action": entity.Obj{"seq": []any{entity.Obj{"patsh": entity.Obj{}}}}},
	}
	for why, doc := range bad {
		if err := CheckFields(doc.Payload()); err == nil {
			t.Errorf("%s was accepted; it would install as a rule that does less than it says", why)
		}
	}
	// And the shapes a real charter comes in are untouched.
	ok := entity.Obj{
		"name": "r", "stratum": "observe",
		"condition": entity.Obj{"subject": "gh:issue:*", "when": entity.Obj{}, "on": entity.Obj{}},
		"gates":     []any{entity.Obj{"mode": "ask"}},
		"action": entity.Obj{
			"patch":  entity.Obj{"stage": "x"},
			"derive": entity.Obj{"sig": entity.Obj{"feature": entity.Obj{"a": 1}}},
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
		Stratum{Name: "low", Level: 0, Match: []Match{{"system": "gh"}}},
		Stratum{Name: "high", Level: 10, Match: []Match{{"system": "signadot"}}},
	)
	// gh:issue:1 stage a → b (depth 1), b → derive (depth 2), and the level above acts off
	// the derive (depth 3).
	for _, tr := range []Trigger{{
		Name:      "advance",
		Stratum:   "low",
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue", "value": map[string]any{"stage": "a"}}},
		Action:    Action{Patch: entity.Obj{"stage": "b"}.Payload()},
	}, {
		Name:      "derives",
		Stratum:   "low",
		Condition: Condition{Subject: Match{"system": "gh", "kind": "issue", "value": map[string]any{"stage": "b"}}},
		Action:    deriveAction("signadot", "feature", entity.Obj{"priority": "urgent"}),
	}, {
		Name:      "consumes",
		Stratum:   "high",
		Condition: Condition{Subject: Match{"system": "signadot", "kind": "feature", "value": map[string]any{"priority": "urgent"}}},
		Action:    Action{Patch: entity.Obj{"stage": "awaiting-approval"}.Payload()},
	}} {
		if err := eng.AddTrigger(tr); err != nil {
			t.Fatalf("%s: %v", tr.Name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.Ref{System: "gh", Kind: "issue", ID: "1"}, entity.Obj{"stage": "a"}.Payload())
	derived := waitDerived(t, store, "signadot", "feature")
	waitStage(t, store, derived.Ref, "awaiting-approval")

	eng.chainMu.Lock()
	defer eng.chainMu.Unlock()
	deepest := 0
	for _, l := range eng.causes {
		if l.depth > deepest {
			deepest = l.depth
		}
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
// bindingFor publishes them as keys. Forgetting that does not error: `$subject.id` substitutes
// the bare key and leaves the text "demo:pr:1.id", which is why this is pinned.
func TestSubjectPartsBindInSpawnStrings(t *testing.T) {
	b := bindingFor(entity.Obj{"stage": "open"}.Payload(), entity.Ref{System: "demo", Kind: "pr", ID: "1"})
	spec := launch.Spec{Prompt: "review $subject.id in $subject.system ($subject) at $stage"}
	got := spec.Bind(b).Prompt
	if want := "review 1 in demo (demo:pr:1) at open"; got != want {
		t.Fatalf("task = %q, want %q", got, want)
	}
}

// The record's parts resolve in a patch, and the bare form still means the address — which is
// what keeps every charter written before the record existed meaning what it meant.
func TestSubjectPartsResolveInAPatch(t *testing.T) {
	root := refRoot(entity.Obj{"stage": "open"}.Payload(),
		subjectNode(entity.Ref{System: "demo", Kind: "pr", ID: "7"}, entity.Obj{"stage": "open"}.Payload(), 42))
	for ref, want := range map[string]string{
		"$subject":             "demo:pr:7",
		"$subject.ref":         "demo:pr:7",
		"$subject.system":      "demo",
		"$subject.kind":        "pr",
		"$subject.id":          "7",
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

// $subject.value reads the subject AS OF THE COMMIT THAT CAUSED THE FIRING, not as of the
// moment the action runs.
//
// The difference is a gate. A firing held for an approval acts whenever the answer comes, and
// the subject may have moved on meanwhile — so a late read would write from a state nobody
// evaluated, and the model's rule is that a firing is settled at one snapshot. Here the gate is
// answered only after the subject has been changed underneath it, and the patch must still
// carry what was true when the rule fired.
func TestSubjectValueReadsAtTheCausingCommit(t *testing.T) {
	store := entitytest.Store(t)
	release := make(chan struct{})
	eng := New(store, blockingResolver{release: release})
	if err := eng.AddTrigger(Trigger{
		Name:      "record",
		Condition: Condition{Subject: Match{"system": "demo", "kind": "pr", "value": map[string]any{"stage": "open"}}},
		Gates:     []Gate{{Mode: "ask"}},
		Action:    Action{Patch: entity.Obj{"sawStage": "$subject.value.stage", "sawAt": "$subject.commit"}.Payload()},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ref := entity.Ref{System: "demo", Kind: "pr", ID: "1"}
	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())
	// Let the firing reach its gate, then move the subject on underneath it.
	time.Sleep(150 * time.Millisecond)
	entitytest.Set(t, store, ref, entity.Obj{"stage": "moved-on"}.Payload())
	time.Sleep(100 * time.Millisecond)
	close(release)

	deadline := time.After(3 * time.Second)
	for {
		e, ok, _ := store.Get(ref)
		if ok {
			if saw := entity.ToObj(e.Payload)["sawStage"]; saw != nil {
				if saw != "open" {
					t.Fatalf("$subject.value.stage = %v, want \"open\" — it was read at the "+
						"moment the gate was answered rather than at the commit that caused "+
						"the firing", saw)
				}
				return
			}
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(ref)
			t.Fatalf("the gated firing never landed its patch; have %+v", entity.ToObj(e.Payload))
		case <-time.After(10 * time.Millisecond):
		}
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

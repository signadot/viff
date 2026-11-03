package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/systems"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// The latch check, over the shapes a charter actually comes in. It is advisory today (see
// AddTrigger) — what is pinned here is WHICH shapes it names, since that is the judgement.
func TestLatchCheck(t *testing.T) {
	pr := func(pred map[string]any) Match {
		m := mm("demo.pr.*")
		if pred != nil {
			m["value"] = pred
		}
		return m
	}
	spawn := &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $[subject.ref]"}

	ok := map[string]Trigger{
		// The canonical shape: spawn the work, let its completion advance the subject past
		// the state that asked for it.
		"spawn with an onDone that advances the subject": {
			Condition: Condition{Subject: pr(map[string]any{"stage": "open"})},
			Action:    Action{Spawn: spawn, OnDone: &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()}},
		},
		"spawn with a patch beside it": {
			Condition: Condition{Subject: pr(map[string]any{"stage": "open"})},
			Action:    Action{Spawn: spawn, Patch: entity.Obj{"stage": "reviewing"}.Payload()},
		},
		"a sequence whose last element latches": {
			Condition: Condition{Subject: pr(map[string]any{"stage": "open"})},
			Action: Action{Seq: []Action{
				{Spawn: spawn},
				{Patch: entity.Obj{"stage": "done"}.Payload()},
			}},
		},
		// Nothing to diverge: a write is inert on repetition.
		"no spawn at all": {
			Condition: Condition{Subject: pr(nil)},
			Action:    Action{Patch: entity.Obj{"stage": "seen"}.Payload()},
		},
	}
	for name, tr := range ok {
		t.Run("ok/"+name, func(t *testing.T) {
			if err := checkLatch(tr.Condition, tr.Action); err != nil {
				t.Fatalf("flagged a converging charter: %v", err)
			}
		})
	}

	bad := map[string]Trigger{
		"spawn with nothing written at all": {
			Condition: Condition{Subject: pr(map[string]any{"stage": "open"})},
			Action:    Action{Spawn: spawn},
		},
		"spawn writing a field the subject does not read": {
			Condition: Condition{Subject: pr(map[string]any{"stage": "open"})},
			Action:    Action{Spawn: spawn, OnDone: &Action{Patch: entity.Obj{"note": "x"}.Payload()}},
		},
		"spawn on a subject with no predicate to latch": {
			Condition: Condition{Subject: pr(nil)},
			Action:    Action{Spawn: spawn, OnDone: &Action{Patch: entity.Obj{"stage": "done"}.Payload()}},
		},
	}
	for name, tr := range bad {
		t.Run("bad/"+name, func(t *testing.T) {
			err := checkLatch(tr.Condition, tr.Action)
			if err == nil {
				t.Fatal("a charter that re-spawns forever was not flagged")
			}
			t.Log(err)
		})
	}
}

// A per-file validator is the charter this check was written for: one that spawned per file
// with nothing to stop it. The shape that works asks for the ABSENCE of a `validated` field and
// writes one, so a file is validated once rather than once per commit — and that is a latch the
// key intersection could not see, since the predicate's only top-level "key" was the tag `not`.
//
// Both halves of the absence idiom are pinned here because the check now reasons about what the
// predicate MEANS: `!not {validated: null}` is "no such field" (a null in a match value is a
// wildcard, so `{validated: null}` is presence), and writing ANY value falsifies it.
func TestAbsenceLatchShapeLatches(t *testing.T) {
	unvalidated := Condition{Subject: Match{
		"path": pathOf("demo", "file", "*"),
		"value": ir.FromKeyVals([]ir.KeyVal{
			{Key: ir.FromString("validated"), Val: ir.Null()},
		}).WithTag("!not"),
	}}
	spawn := &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: `Read the file at path "$[path]" and report any problems.`}

	// A literal latch.
	act := Action{Spawn: spawn, OnDone: &Action{Patch: entity.Obj{"validated": true}.Payload()}}
	if err := checkLatch(unvalidated, act); err != nil {
		t.Fatalf("the validator still re-spawns: %v", err)
	}
	// And a $reference, which is the case that needs the fold rather than a value comparison:
	// what lands is unknown, but that SOMETHING lands is not, and an absence constraint is
	// decided by presence alone. (An unresolved reference fails the firing rather than being
	// dropped, so if the patch applied at all, a value is there.)
	byHash := Action{Spawn: spawn, OnDone: &Action{Patch: entity.Obj{"validated": "$hash"}.Payload()}}
	if err := checkLatch(unvalidated, byHash); err != nil {
		t.Fatalf("a latch on a referenced value was not seen: %v", err)
	}
	// And it installs.
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	tr := Trigger{Name: "validate-abc", Condition: unvalidated, Action: act}
	if err := eng.AddTrigger(tr); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
}

// The four shapes the key intersection got wrong, each measured before it was fixed
// (git-issue 2xth4bxsh12krsfwdxn0). A payload is any tony value, and both sides of that check
// were collapsed to the top-level keys of an object.
func TestLatchDecidesShapesKeysCouldNot(t *testing.T) {
	spawn := &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $[subject.ref]"}
	subject := func(pred *ir.Node) Condition {
		return Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": pred}}
	}
	nested := subject(entity.Obj{"labels": entity.Obj{"urgent": true}}.Payload())

	// FALSE PASS: the write is a SIBLING of what the predicate reads. Both are under the key
	// `labels`, so the intersection succeeded — and `labels.urgent` is untouched, so the
	// subject still matches and it spawns at every commit forever.
	sibling := Action{Spawn: spawn,
		OnDone: &Action{Patch: entity.Obj{"labels": entity.Obj{"seen": true}}.Payload()}}
	err := checkLatch(nested, sibling)
	if err == nil {
		t.Fatal("a spawn that writes a sibling of what its subject reads was accepted")
	}
	if !strings.Contains(err.Error(), "labels.urgent") {
		t.Errorf("the diagnostic did not name the path the subject reads: %v", err)
	}
	t.Log(err)

	// FALSE REFUSAL: a whole-value replace has no top-level keys to intersect, and it is the
	// most total latch there is — nothing of the old payload survives it.
	if err := checkLatch(nested, Action{Spawn: spawn,
		OnDone: &Action{Patch: entity.Val("done")}}); err != nil {
		t.Fatalf("a whole-value replace was refused: %v", err)
	}

	// FALSE PASS: writing the same value back. The keys intersect, and nothing about the
	// subject changes — which converges for a WRITE, since rewriting a value raises no commit,
	// and not for a SPAWN, which repeats however little changed.
	open := subject(entity.Obj{"stage": "open"}.Payload())
	if err := checkLatch(open, Action{Spawn: spawn,
		OnDone: &Action{Patch: entity.Obj{"stage": "open"}.Payload()}}); err == nil {
		t.Fatal("a spawn that writes its subject's own state back was accepted")
	} else {
		t.Log(err)
	}

	// SYNTAX MISTAKEN FOR STRUCTURE: a root operator's key was its TAG NAME, so this was
	// refused unless the action wrote a field literally called `and`. An operator is just the
	// union of its operands.
	andPred := ir.FromSlice([]*ir.Node{
		entity.Obj{"stage": "open"}.Payload(),
		entity.Obj{"urgent": true}.Payload(),
	}).WithTag("!and")
	if err := checkLatch(subject(andPred), Action{Spawn: spawn,
		OnDone: &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()}}); err != nil {
		t.Fatalf("a subject under a root operator could not latch: %v", err)
	}
	// …and the same predicate with a write to neither operand is still refused.
	if err := checkLatch(subject(andPred), Action{Spawn: spawn,
		OnDone: &Action{Patch: entity.Obj{"note": "x"}.Payload()}}); err == nil {
		t.Fatal("a root operator accepted a spawn that writes neither of its operands")
	}
}

// The fold, over the boolean structure a predicate is allowed to have. Each of these is a
// question the key intersection could not ask at all: what a write does to the predicate's
// TRUTH, rather than whether its name appears on both sides.
func TestLatchFoldsThroughPredicateStructure(t *testing.T) {
	tag := func(t string, ns ...*ir.Node) *ir.Node {
		if len(ns) == 1 {
			return ns[0].DeepCopy().WithTag(t)
		}
		return ir.FromSlice(ns).WithTag(t)
	}
	stage := func(v string) *ir.Node { return entity.Obj{"stage": v}.Payload() }

	cases := []struct {
		name  string
		pred  *ir.Node
		patch entity.Payload
		want  latch
	}{
		{"an or, advanced past every branch",
			tag("!or", stage("open"), stage("blocked")), stage("reviewed"), latchHolds},
		{"an or, advanced into another branch",
			tag("!or", stage("open"), stage("blocked")), stage("blocked"), latchMatches},
		{"a not, left alone",
			tag("!not", stage("open")), entity.Obj{"note": "x"}.Payload(), latchUntouched},
		{"a not, written to the value it refuses",
			tag("!not", stage("open")), stage("open"), latchHolds},
		{"an operator at a leaf",
			entity.Obj{"stage": tag("!or", ir.FromString("open"), ir.FromString("merged"))}.Payload(),
			stage("reviewed"), latchHolds},
		{"a leaf operator the write still satisfies",
			entity.Obj{"stage": tag("!or", ir.FromString("open"), ir.FromString("merged"))}.Payload(),
			stage("merged"), latchMatches},
		{"a scalar payload predicate against a record write",
			ir.FromInt(42), stage("open"), latchHolds},
		{"a nested field, advanced",
			entity.Obj{"labels": entity.Obj{"urgent": true}}.Payload(),
			entity.Obj{"labels": entity.Obj{"urgent": false}}.Payload(), latchHolds},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			act := Action{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "work"}, OnDone: &Action{Patch: c.patch}}
			if got := latchOf(c.pred, act); got != c.want {
				t.Fatalf("latchOf = %d, want %d", got, c.want)
			}
		})
	}
}

// The three answers the relation gives, and which of them a REFUSAL rests on. Undecided is not
// a no: a check that refused everything it could not decide would refuse the ordinary shape of
// writing a value out of a task (`patch: {stage: $result.status}`), which may well latch.
func TestLatchRefusesOnlyWhatItCanSee(t *testing.T) {
	pred := entity.Obj{"stage": "open"}.Payload()
	spawn := &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $[subject.ref]"}
	hook := func(p entity.Payload) Action { return Action{Spawn: spawn, OnDone: &Action{Patch: p}} }

	cases := []struct {
		name string
		act  Action
		want latch
	}{
		{"advances the field", hook(entity.Obj{"stage": "reviewed"}.Payload()), latchHolds},
		{"writes elsewhere", hook(entity.Obj{"note": "x"}.Payload()), latchUntouched},
		{"writes nothing", Action{Spawn: spawn}, latchUntouched},
		{"writes the same value", hook(entity.Obj{"stage": "open"}.Payload()), latchMatches},
		{"writes a reference", hook(entity.Obj{"stage": "$result.status"}.Payload()), latchUnsure},
		{"deletes the field", hook(ir.FromKeyVals([]ir.KeyVal{
			{Key: ir.FromString("stage"), Val: ir.Null().WithTag("!delete")},
		})), latchHolds},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := latchOf(pred, c.act); got != c.want {
				t.Fatalf("latchOf = %d, want %d", got, c.want)
			}
			if err := checkLatch(Condition{Subject: Match{
				"path": pathOf("demo", "pr", "*"), "value": pred}}, c.act); (err != nil) != (c.want == latchUntouched || c.want == latchMatches) {
				t.Fatalf("refusal disagrees with the verdict: %v", err)
			}
		})
	}
}

// A firing whose work is already running does not ask again. The subject stays selected for as
// long as the task runs — the write that would release it is what the task has not
// finished doing — so every commit meanwhile re-selects it.
//
// spawnHooked has always refused the second task; what this pins is that the refusal now
// happens BEFORE the gates, so a firing for work already under way does not put a question on
// the docket or spend budget on its way to being told no.
func TestAFiringDoesNotAskAgainWhileItsWorkRuns(t *testing.T) {
	store := entitytest.Store(t)
	reg := task.NewRegistry()
	eng := New(store, reg)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		sup := task.NewSupervisor(systems.Environment{}, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, task.AutoResolver{})
		return task.NewTask(id, sup, blockForever{}, noExec{})
	})

	ref := entity.RefAt("demo", "pr", "1")
	if err := eng.AddTrigger(Trigger{
		Name: "review",
		// Latches through its onDone — but the task blocks forever, so the latch never
		// lands and the subject stays selected the whole time.
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"),
			"value": map[string]any{"stage": "open"}}},
		Gates: []Gate{{Name: "go", Mode: "ask"}},
		Action: Action{
			Spawn:  &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $[subject.ref]"},
			OnDone: &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, ref, entity.Obj{"stage": "open"}.Payload())

	// The first firing asks; approve it so the task starts.
	var id string
	deadline := time.After(3 * time.Second)
	for id == "" {
		if p := reg.Pending(); len(p) == 1 {
			id = p[0].Question.ID
			break
		}
		select {
		case <-deadline:
			t.Fatal("no gate question appeared")
		case <-time.After(10 * time.Millisecond):
		}
	}
	reg.Answer(id, task.Resolution{Proceed: true, By: "alice"})
	waitFlow(t, eng, ref, 1)

	fired := eng.Fired()
	// Now stir the subtree while the task runs. The subject still matches — the latch is
	// waiting on a completion that never comes — so every one of these re-selects it.
	for i := range 4 {
		entitytest.Set(t, store, entity.RefAt("demo", "pr", "other"),
			entity.Obj{"stage": "closed", "n": i}.Payload())
	}
	time.Sleep(500 * time.Millisecond)

	if p := reg.Pending(); len(p) != 0 {
		t.Fatalf("asked again for work already running: %+v", p)
	}
	if now := eng.Fired(); now != fired {
		t.Fatalf("budget spent %d more times on work already running", now-fired)
	}
	if n := len(eng.matchFlow(mustExpr(t, `demo.pr."1"`))); n != 1 {
		t.Fatalf("%d tasks for one subject, want 1", n)
	}
}

// waitFlow blocks until the subject has n live tasks.
func waitFlow(t *testing.T, eng *Engine, ref entity.Ref, n int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if len(eng.matchFlow(mustSteps(t, ref.Segs()...))) == n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d live task(es) about %s", n, ref)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A SEQUENCE bounds itself, so the check does not demand a subject latch of one. startSeq
// refuses to re-run a sequence that has completed for this subject at this shape, and that
// claim is a fact in the store rather than a live-task check — so unlike a bare spawn it
// survives the reap. Demanding a subject latch as well would be demanding the same guarantee
// twice, and it is the shape half the sequence tests are written in.
func TestLatchCheckLeavesSequencesAlone(t *testing.T) {
	cond := Condition{Subject: mm("demo.pr.*")} // no predicate at all
	seq := Action{Seq: []Action{{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "one"}}, {Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "two"}}}}
	if err := checkLatch(cond, seq); err != nil {
		t.Fatalf("a sequence was asked to latch: %v", err)
	}
	// A bare spawn under the same subject is not excused.
	if err := checkLatch(cond, Action{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "one"}}); err == nil {
		t.Fatal("a bare spawn with nothing to latch on was accepted")
	}
}

// The check REFUSES now rather than warning, and it runs last so a charter that is wrong some
// other way is told about that instead.
func TestAddTriggerRefusesAnUnlatchedSpawn(t *testing.T) {
	eng := New(entitytest.Store(t), task.AutoResolver{})
	err := eng.AddTrigger(Trigger{
		Name:      "spin",
		Condition: Condition{Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "open"}}},
		Action:    Action{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $[subject.ref]"}},
	})
	if err == nil {
		t.Fatal("installed a charter that cannot stop spawning")
	}
	t.Log(err)

	// A charter that is wrong in a STRUCTURAL way hears about that, not about latching —
	// a nested sequence trips both checks, and only one of them is the answer.
	err = eng.AddTrigger(Trigger{
		Name:      "nested",
		Condition: Condition{Subject: mm("demo.pr.*")},
		Action:    Action{Seq: []Action{{Seq: []Action{{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "inner"}}}}}},
	})
	if err == nil {
		t.Fatal("installed a nested sequence")
	}
	if !strings.Contains(err.Error(), "sequence inside a sequence") {
		t.Fatalf("a nested sequence was reported as something else: %v", err)
	}
}

// A narrow `on` SAMPLES, and the note says which of the two sentences the charter wrote.
func TestSamplingAdvice(t *testing.T) {
	ship := Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "reviewed"}}
	// `on` covers only ci.status, and the subject reads demo.pr — so a PR arriving after the
	// edge waits for CI to go red and green again.
	narrow := Trigger{Name: "ship", Condition: Condition{
		On:      MatchWhen(Match{"path": pathOf("ci", "status", "*"), "value": map[string]any{"state": "green"}}),
		Subject: ship,
	}}
	notes := advise(narrow)
	if len(notes) != 1 || !strings.Contains(notes[0], "demo.pr") {
		t.Fatalf("no note about the subtree the cause does not cover: %v", notes)
	}
	t.Log(notes[0])

	// No `on` at all: every commit re-evaluates, so nothing is sampled and there is nothing
	// to say.
	if notes := advise(Trigger{Name: "ship", Condition: Condition{Subject: ship}}); len(notes) != 0 {
		t.Fatalf("advised a rule that samples nothing: %v", notes)
	}
	// An `on` that covers what the rule reads is not sampling either.
	full := Trigger{Name: "ship", Condition: Condition{
		On:      MatchWhen(Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "reviewed"}}),
		Subject: ship,
	}}
	if notes := advise(full); len(notes) != 0 {
		t.Fatalf("advised a rule whose cause covers what it reads: %v", notes)
	}
}

package trigger

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"

	"github.com/signadot/verse/codec"
)

// prs seeds n demo:pr entities at a stage, returning their refs.
func prs(t *testing.T, store entity.Store, stage string, ids ...string) []entity.Ref {
	t.Helper()
	out := make([]entity.Ref, 0, len(ids))
	for _, id := range ids {
		ref := entity.RefAt("demo", "pr", id)
		entitytest.Set(t, store, ref, entity.Obj{"stage": stage}.Payload())
		out = append(out, ref)
	}
	return out
}

// BROADCAST — the genuinely new dispatch path. A change UNRELATED to the subject fires once,
// and the set-valued Subject fans the action out over every matching entity: one action per
// member, each about exactly one subject.
func TestSetValuedSubjectFansOut(t *testing.T) {
	store := entitytest.Store(t)
	reviewed := prs(t, store, "reviewed", "1", "2", "3")
	prs(t, store, "open", "4") // not selected — wrong stage

	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name: "ship-all-reviewed",
		Condition: Condition{
			When:    MatchWhen(mm("ci.status.*")),
			Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "reviewed"}},
		},
		Action: Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// ONE change, about nothing in particular — the fan-out comes from the Subject.
	entitytest.Set(t, store, entity.RefAt("ci", "status", "build"), entity.Obj{"state": "green"}.Payload())

	for _, ref := range reviewed {
		waitStage(t, store, ref, "shipped")
	}
	// The non-matching PR is untouched: the Subject selected a set, not everything.
	if e, _, _ := store.Get(entity.RefAt("demo", "pr", "4")); entity.ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("an unselected entity was acted on: %#v", e.Payload)
	}
	// Budget metering is per entity acted on, so three members cost at least three. It can
	// cost MORE: each firing's write is a commit, which re-evaluates the trigger, and a member
	// whose own write has not landed yet is still selected at that commit. The extra firing
	// writes the same value, so nothing moves — the reconciler converges, it does not
	// minimise — but the backstop counts the work, not the distinct subjects.
	if f := eng.Fired(); f < 3 {
		t.Fatalf("fired = %d, want at least 3 (one per selected subject)", f)
	}
}

// Each fanned-out member gets its own task, with $subject bound to THAT member — so a
// broadcast spawn is N tasks each about one subject, not one task about a set.
func TestFanOutSpawnsPerMember(t *testing.T) {
	store := entitytest.Store(t)
	prs(t, store, "reviewed", "1", "2", "3")

	eng := New(store, task.AutoResolver{})
	type spawned struct {
		id   string
		spec launch.Spec
	}
	got := make(chan spawned, 8)
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		got <- spawned{id, spec}
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
		return task.NewTask(id, sup, blockForever{}, noExec{})
	})
	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		Condition: Condition{
			When:    MatchWhen(mm("ci.status.*")),
			Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "reviewed"}},
		},
		Action: Action{
			Spawn:  &launch.Spec{Prompt: "ship $subject"},
			OnDone: &Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("ci", "status", "build"), entity.Obj{"state": "green"}.Payload())

	seen := map[string]string{} // subject -> task
	ids := map[string]bool{}
	// Until all three members have spawned, not exactly three sends: a re-evaluation about a
	// member whose task is already live is deduped by spawnHooked and never reaches the
	// spawner, but the count is about members, so count members.
	for len(seen) < 3 {
		select {
		case s := <-got:
			seen[s.spec.Subject] = s.spec.Prompt
			ids[s.id] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of 3 members spawned: %v", len(seen), seen)
		}
	}
	for _, id := range []string{"1", "2", "3"} {
		ref := `demo.pr."` + id + `"`
		if seen[ref] != "ship "+ref {
			t.Errorf("member %s: task = %q, want $subject bound to that member", ref, seen[ref])
		}
	}
	if len(ids) != 3 {
		t.Fatalf("members shared a spawn id (%v) — control would be cross-wired", ids)
	}
	// And each is its own flow, addressable as a set.
	//
	// Polled, because the spawner runs BEFORE the flow is tracked (spawnHooked calls the
	// spawner and only then track()s the handle) — so the third send arriving on `got` says a
	// task started, not that the engine has finished registering it. Asserting straight off the
	// last send is a race the test loses about one run in five.
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := len(eng.matchFlow(mustExpr(t, "demo.pr.*")))
		if n == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d live tasks for 3 members", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A re-fire over the same set must be IDEMPOTENT per member: spawnID is deterministic per
// (trigger, subject), so the second broadcast re-uses each member's id rather than starting a
// duplicate. This is what makes a broadcast safe to re-trigger.
func TestFanOutRefireIsIdempotentPerMember(t *testing.T) {
	store := entitytest.Store(t)
	prs(t, store, "reviewed", "1", "2")

	eng := New(store, task.AutoResolver{})
	// The spawner runs on an engine goroutine while the test polls, so the counter must be
	// atomic — a plain int here is a data race, not a flake.
	var spawns atomic.Int64
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		spawns.Add(1)
		sup := task.NewSupervisor(&task.Vector{Default: task.Auto}, task.AutoResolver{})
		return task.NewTask(id, sup, blockForever{}, noExec{})
	})
	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		Condition: Condition{
			When:    MatchWhen(mm("ci.status.*")),
			Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "reviewed"}},
		},
		Action: Action{
			Spawn:  &launch.Spec{Prompt: "ship $subject"},
			OnDone: &Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	ci := entity.RefAt("ci", "status", "build")
	entitytest.Set(t, store, ci, entity.Obj{"state": "green"}.Payload())
	eventuallyN(t, "2 spawns", func() bool { return spawns.Load() == 2 })

	// Fire again over the same set — same members, same ids, so no new tasks.
	entitytest.Set(t, store, ci, entity.Obj{"state": "green", "run": "2"}.Payload())
	time.Sleep(300 * time.Millisecond)
	if n := spawns.Load(); n != 2 {
		t.Fatalf("a re-fire spawned %d tasks; want 2 (idempotent per member)", n)
	}
}

// A FAN-OUT ACTS ON EVERY MEMBER, AND WIDTH IS NOT DEPTH. One rising edge selects ten entities
// and all ten advance — no member is dropped, and the cycle throttle does not read a wide firing
// as a deep one.
//
// This was TestFanOutBudgetMetersEntities, and it asserted the opposite: that a fan-out wider
// than the fire budget was TRUNCATED to the cap, acting on 4 of 10. The budget is gone (see
// countFire), so truncation is gone with it, and what is left is the property worth pinning.
// This is also the design note's burst case at its exact shape — one chain of length 1 fanning
// out to n firings (docs/cycle-throttle.md) — and the per-subject chain rule is what
// keeps the ninth member from reading as the ninth hop of a runaway.
func TestFanOutActsOnEveryMemberWithoutTripping(t *testing.T) {
	store := entitytest.Store(t)
	ids := make([]string, 0, 10)
	for i := range 10 {
		ids = append(ids, fmt.Sprint(i))
	}
	prs(t, store, "reviewed", ids...)

	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		Condition: Condition{
			When:    MatchWhen(mm("ci.status.*")),
			Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "reviewed"}},
		},
		Action: Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("ci", "status", "build"), entity.Obj{"state": "green"}.Payload())

	eventuallyN(t, "all 10 members to advance", func() bool {
		shipped := 0
		for _, id := range ids {
			if e, _, _ := store.Get(entity.RefAt("demo", "pr", id)); entity.ToObj(e.Payload)["stage"] == "shipped" {
				shipped++
			}
		}
		return shipped == len(ids)
	})
	// At LEAST one firing per member, and generally more: each member's write is a commit that
	// re-evaluates the rule, and a member whose own write has not landed yet is still selected,
	// so it is fired for again. Those extra firings write the same value and produce no commit,
	// which is how this settles — the ordinary convergence story, not a fault. It is only
	// visible now because the old fire budget capped this test at 4 and hid it.
	if f := eng.Fired(); f < len(ids) {
		t.Fatalf("fired = %d, want at least %d — every member of the set must be acted on", f, len(ids))
	}
	if n := eng.tripCount(); n != 0 {
		t.Fatalf("a fan-out over %d members tripped %d times; width is not depth", len(ids), n)
	}
}

// A set-valued Subject that currently matches nothing is a no-op, not an error: the firing
// simply has no members to be about.
func TestFanOutOverEmptySetIsANoOp(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		Condition: Condition{
			When:    MatchWhen(mm("ci.status.*")),
			Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "reviewed"}},
		},
		Action: Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("ci", "status", "build"), entity.Obj{"state": "green"}.Payload())
	time.Sleep(250 * time.Millisecond)
	if f := eng.Fired(); f != 0 {
		t.Fatalf("fired %d times over an empty subject set", f)
	}
}

// Select is sorted, so a truncated fan-out is deterministic rather than picking arbitrary
// members from a map iteration.
func TestSubjectSelectionIsDeterministic(t *testing.T) {
	store := entitytest.Store(t)
	prs(t, store, "reviewed", "b", "a", "c")
	m := Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "reviewed"}}
	want := []string{"demo.pr.a", "demo.pr.b", "demo.pr.c"}
	for range 20 { // map iteration order varies per range, so repeat
		refs := m.Select(store)
		for i, r := range refs {
			if r.String() != want[i] {
				t.Fatalf("Select order = %v, want %v", refs, want)
			}
		}
	}
}

// A gate is raised PER SUBJECT, not once for a whole broadcast. A firing is about one entity —
// its spawn id, its gate question and its motion all key on that entity — so "ship every
// reviewed PR" is three decisions, and presenting them as one was a summary standing in for
// three (which is what `summarize` existed to render).
//
// This test asserted the opposite while a firing could be about a set. The regression is real
// and user-visible: three PRs are three questions on the docket, and answering them one at a
// time is the honest arity until `verse gate approve` grows a bulk form.
func TestEachSubjectRaisesItsOwnGate(t *testing.T) {
	store := entitytest.Store(t)
	prs(t, store, "reviewed", "1", "2", "3")

	asked := make(chan task.GateQuestion, 32)
	eng := New(store, countingResolver{asked})
	if err := eng.AddTrigger(Trigger{
		Name: "ship",
		Condition: Condition{
			When:    MatchWhen(mm("ci.status.*")),
			Subject: Match{"path": pathOf("demo", "pr", "*"), "value": map[string]any{"stage": "reviewed"}},
		},
		Gates:  []Gate{{Name: "ship", Mode: "ask"}},
		Action: Action{Patch: entity.Obj{"stage": "shipped"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	entitytest.Set(t, store, entity.RefAt("ci", "status", "build"), entity.Obj{"state": "green"}.Payload())

	for _, id := range []string{"1", "2", "3"} {
		waitStage(t, store, entity.RefAt("demo", "pr", id), "shipped")
	}
	// Each question names the one subject it decides about, and no question names a set.
	subjects := map[string]bool{}
	for len(asked) > 0 {
		q := <-asked
		if strings.Contains(q.Subject, ",") {
			t.Fatalf("a gate question named several subjects: %q", q.Subject)
		}
		subjects[q.Subject] = true
	}
	if len(subjects) != 3 {
		t.Fatalf("gates asked about %d distinct subjects, want 3: %v", len(subjects), subjects)
	}
}

// countingResolver approves everything and records what it was asked.
type countingResolver struct{ asked chan task.GateQuestion }

func (c countingResolver) Resolve(_ context.Context, q task.GateQuestion, _ task.ToolUse) (task.Resolution, error) {
	c.asked <- q
	return task.Resolution{Proceed: true, By: "test"}, nil
}

func eventuallyN(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", why)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A WILDCARD SEGMENT is the string spelling of a set-valued Subject, and it must dispatch
// identically to the object form — `"demo.pr.*"` is every pr, exactly as `verse flow demo.pr.*`
// addresses their live tasks. Parsed instead as a name for a node literally called "*", nothing
// fans out and `set` creates `demo.pr.*`.
func TestGlobSubjectFansOut(t *testing.T) {
	store := entitytest.Store(t)
	open := prs(t, store, "open", "1", "2", "3")

	eng := New(store, task.AutoResolver{})
	var tr Trigger
	if err := codec.Unmarshal([]byte(`{"name":"ship-all",
		"condition":{"when":"ci.status.*","subject":"demo.pr.*"},
		"action":{"patch":{"stage":"shipped"}}}`), &tr, codec.JSON); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := eng.AddTrigger(tr); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, entity.RefAt("ci", "status", "build"), entity.Obj{"state": "green"}.Payload())

	for _, ref := range open {
		waitStage(t, store, ref, "shipped")
	}
	// And no entity called "*" was conjured by the action's `set`.
	if e, ok, _ := store.Get(entity.RefAt("demo", "pr", "*")); ok {
		t.Fatalf("the glob was named instead of selected: created demo.pr.* = %#v", e.Payload)
	}
}

// A PREDICATE SELECTS A FAMILY at a place, where the path selects the place. A name pattern is a
// condition on what a node HOLDS — which is where both live charters say it
// (`value: {name: !glob "verse/auto/*/*"}`) — and a path takes no operator.
func TestAPredicateSelectsAFamily(t *testing.T) {
	store := entitytest.Store(t)
	for _, id := range []string{"hotfix-1", "hotfix-2", "feature-9"} {
		entitytest.Set(t, store, entity.RefAt("demo", "pr", id),
			entity.Obj{"stage": "open", "branch": id}.Payload())
	}

	eng := New(store, task.AutoResolver{})
	var tr Trigger
	if err := codec.Unmarshal([]byte(`{name: ship-hotfixes,
		condition: {when: ci.status.*, subject: {path: "demo.pr.*", value: {branch: !glob "hotfix-*"}}},
		action: {patch: {stage: shipped}}}`), &tr, codec.Tony); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := eng.AddTrigger(tr); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, entity.RefAt("ci", "status", "build"), entity.Obj{"state": "green"}.Payload())

	waitStage(t, store, entity.RefAt("demo", "pr", "hotfix-1"), "shipped")
	waitStage(t, store, entity.RefAt("demo", "pr", "hotfix-2"), "shipped")
	if e, _, _ := store.Get(entity.RefAt("demo", "pr", "feature-9")); entity.ToObj(e.Payload)["stage"] != "open" {
		t.Fatalf("the glob overreached beyond its family: %#v", e.Payload)
	}
}

// A CONCRETE address still NAMES its entity, so a `set` brings it into existence — the way a
// trigger publishes a fact nothing has written yet. Routing the string form through the address
// grammar must not quietly turn this into a select-what-exists no-op.
func TestConcreteSubjectNamesAndCreates(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	var tr Trigger
	if err := codec.Unmarshal([]byte(`{"name":"record",
		"condition":{"when":"ci.status.*","subject":"audit.record.latest"},
		"action":{"patch":{"stage":"noted"}}}`), &tr, codec.JSON); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := eng.AddTrigger(tr); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	entitytest.Set(t, store, entity.RefAt("ci", "status", "build"), entity.Obj{"state": "green"}.Payload())
	waitStage(t, store, entity.RefAt("audit", "record", "latest"), "noted")
}

// A subject selects what its pattern names and nothing else — including when the change that
// dispatched the firing points somewhere else entirely.
//
// This replaces a test of the `flow` redirect, which asserted the filtering half of it: a
// task whose flow was not a demo:pr was a no-op. With the redirect gone the payload has no
// say at all, and the property that remains is the plainer one — a subject is what the charter
// says. The entity the task named is untouched not because it was filtered out, but because
// the charter never named it.
func TestSubjectSelectsOnlyWhatItNames(t *testing.T) {
	store := entitytest.Store(t)
	other := entity.RefAt("other", "thing", "1")
	entitytest.Set(t, store, other, entity.Obj{"stage": "x"}.Payload())

	eng := New(store, task.AutoResolver{})
	if err := eng.AddTrigger(Trigger{
		Name: "reviewed",
		Condition: Condition{
			When:    MatchWhen(Match{"path": pathOf("verse", "task", "instance", "*"), "value": map[string]any{"status": "done"}}),
			Subject: mm("demo.pr.*"), // acts on demo:pr, whatever the cause carries
		},
		Action: Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)

	// A completed task whose payload names `other` — which the subject does not select.
	entitytest.Set(t, store, entity.VerseRef("task", "instance", "rev-1"),
		entity.Obj{"status": "done", "flow": other.String()}.Payload())

	eventuallyN(t, "the firing to be observed and discarded", func() bool { return eng.Fired() == 0 })
	time.Sleep(50 * time.Millisecond) // give a wrong action time to land, if it were going to
	if e, _, _ := store.Get(other); entity.ToObj(e.Payload)["stage"] != "x" {
		t.Fatalf("acted on an entity the subject does not name: %s = %#v", other, e.Payload)
	}
}

// The whole value of publishing a spawn id on the gate is that it is the SAME id the spawn
// will use — an approver lining a gate up with the work it authorises. Two call sites compute
// it (the gate in fireOne, the spawn in apply), so nothing but a test stops them drifting into
// two schemes that merely look alike. A firing is one subject now, so there is one id.
func TestSpawnLabelIsTheIDApplyWillUse(t *testing.T) {
	refs := []entity.Ref{
		entity.RefAt("demo", "pr", "1"),
		entity.RefAt("demo", "pr", "path/with/slashes"), // safeSeg rewrites → hashed
	}
	trig := Trigger{Name: "review", Action: Action{Spawn: &launch.Spec{Driver: "scripted"}}}

	for _, ref := range refs {
		announced := New(nil, nil).spawnLabel(trig, []entity.Ref{ref})
		if want := spawnID("", trig.Name, ref); announced != want {
			t.Errorf("gate announced %q but apply will spawn %q", announced, want)
		}
	}

	// A set-only action starts nothing, and must not invent an id for a task that will
	// never exist.
	if got := New(nil, nil).spawnLabel(Trigger{Name: "reviewed"}, refs); got != "" {
		t.Errorf("a set-only trigger announced a spawn: %q", got)
	}

}

package trigger

import (
	"context"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/launch"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/task"
)

// treeEngine is the onhook harness plus a mirrored task, which is what a tree renders.
func treeEngine(t *testing.T, store *entity.LogdStore) *Engine {
	t.Helper()
	eng := New(store, task.AutoResolver{})
	eng.SetSpawner(func(id string, spec launch.Spec) task.Handle {
		return startHookTask(store, id, spec)
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng.ObserveScoped(ctx)
	return eng
}

// find walks a tree for the node at a chain — the tree's nesting IS kpath extension, so
// this is the assertion helper the shape deserves.
func find(nodes []FlowNode, chain string) (FlowNode, bool) {
	for _, n := range nodes {
		if n.Chain == chain {
			return n, true
		}
		if c, ok := find(n.Children, chain); ok {
			return c, true
		}
	}
	return FlowNode{}, false
}

func onlyTree(t *testing.T, eng *Engine, addr string) FlowTree {
	t.Helper()
	m, err := ParseExpr(addr)
	if err != nil {
		t.Fatalf("ParseExpr(%q): %v", addr, err)
	}
	trees := eng.FlowTrees(m)
	if len(trees) != 1 {
		t.Fatalf("FlowTrees(%q) = %d trees; want 1 (%+v)", addr, len(trees), trees)
	}
	return trees[0]
}

// The tree is the join of history and charter: a finished chain is read back from the
// mirror (the live registry has already retired it), and the branch the charter declares
// but nothing reached is present as a node rather than missing.
func TestFlowTreeJoinsHistoryAndCharter(t *testing.T) {
	store := entitytest.Store(t)
	eng := treeEngine(t, store)

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch: entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $[subject.ref]"},
			OnDone: &Action{
				Patch:    entity.Obj{"stage": "reviewed"}.Payload(),
				Spawn:    &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "merge $[subject.ref]"},
				OnDone:   &Action{Patch: entity.Obj{"stage": "merged"}.Payload()},
				OnFailed: &Action{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "escalate $[subject.ref]"}},
			},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "merged")

	tree := onlyTree(t, eng, `demo.pr."1"`)
	if len(tree.Roots) != 1 || tree.Roots[0].Chain != "review" || tree.Roots[0].Kind != "root" {
		t.Fatalf("want one root `review`; got %+v", tree.Roots)
	}

	// What ran: both spawns, read back from the mirror after retirement.
	for _, chain := range []string{"review", "review.onDone"} {
		n, ok := find(tree.Roots, chain)
		if !ok {
			t.Fatalf("node %q missing from tree %+v", chain, tree.Roots)
		}
		if n.Status != string(task.Done) {
			t.Errorf("node %q status = %q; want done", chain, n.Status)
		}
		if n.ID != chain+ChainSep+"demo.pr.1" {
			t.Errorf("node %q id = %q; want %q", chain, n.ID, chain+ChainSep+"demo.pr.1")
		}
		if n.Prompt == "" {
			t.Errorf("node %q has no task", chain)
		}
	}

	// The branch that did not run because its sibling status won: declared in the charter,
	// so it is a node — and SKIPPED, not pending, because its parent is done, not failed.
	esc, ok := find(tree.Roots, "review.onDone.onFailed")
	if !ok {
		t.Fatalf("declared onFailed branch missing from tree %+v", tree.Roots)
	}
	if esc.Status != StatusSkipped {
		t.Errorf("onFailed branch status = %q; want %q (its parent completed)", esc.Status, StatusSkipped)
	}
	if esc.ID != "" {
		t.Errorf("unrun branch carries a task id %q", esc.ID)
	}
	if esc.Prompt != "escalate $[subject.ref]" {
		t.Errorf("unrun branch task = %q; want the charter's declared (unbound) task", esc.Prompt)
	}
	// A set-only hook still appears — a node is a position in the charter, not only a spawn.
	// It spawned no task, so nothing observes it directly; its parent ended on exactly
	// the status it answers to, so it fired, and saying "pending" would be plainly wrong.
	set, ok := find(tree.Roots, "review.onDone.onDone")
	if !ok {
		t.Fatal("set-only onDone hook missing from the tree")
	}
	if set.Status != StatusApplied {
		t.Errorf("set-only hook status = %q; want %q — it fired but spawned nothing",
			set.Status, StatusApplied)
	}
}

// A `seq` carrier's own hooks read against the status the SEQUENCE gave it. The carrier
// spawns nothing, so its status arrives from the progress facts after its children were
// built — and a tree that did not revisit them showed a finished sequence's `onDone` as
// `pending` while the patch it applied was already on the subject.
func TestFlowTreeSeqCarrierHooks(t *testing.T) {
	store := entitytest.Store(t)
	eng := treeEngine(t, store)

	if err := eng.AddTrigger(Trigger{
		Name:      "build",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Seq: []Action{
				{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "lint $[subject.ref]"}},
				{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "test $[subject.ref]"},
					OnFailed: &Action{Patch: entity.Obj{"stage": "broken"}.Payload()}},
			},
			OnDone:   &Action{Patch: entity.Obj{"stage": "built"}.Payload()},
			OnFailed: &Action{Patch: entity.Obj{"stage": "broken"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	entitytest.Set(t, store, prRef("1"), entity.Obj{"stage": "open"}.Payload())
	waitField(t, store, prRef("1"), "stage", "built") // only the carrier's onDone writes this

	tree := onlyTree(t, eng, `demo.pr."1"`)
	done, ok := find(tree.Roots, "build.onDone")
	if !ok {
		t.Fatalf("carrier onDone missing from tree %+v", tree.Roots)
	}
	if done.Status != StatusApplied {
		t.Errorf("carrier onDone status = %q; want %q — the sequence finished and it wrote `built`",
			done.Status, StatusApplied)
	}
	// And the branch the sequence's outcome did not answer to is dead, not waiting.
	failed, ok := find(tree.Roots, "build.onFailed")
	if !ok {
		t.Fatalf("carrier onFailed missing from tree %+v", tree.Roots)
	}
	if failed.Status != StatusSkipped {
		t.Errorf("carrier onFailed status = %q; want %q", failed.Status, StatusSkipped)
	}
}

// While a chain is still moving, the branch below it is PENDING (it may yet run) — the
// distinction from skipped is the whole reason both statuses exist.
func TestFlowTreePendingWhileRunning(t *testing.T) {
	store := entitytest.Store(t)
	eng := treeEngine(t, store)

	// A gate the test never resolves would be one way to hold it; simpler and race-free:
	// mirror a running task by hand, exactly as a Spawner would, and read the tree.
	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("never")},
		Action: Action{
			Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $[subject.ref]"},
			OnDone: &Action{
				Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "merge $[subject.ref]"},
				Patch: entity.Obj{"stage": "merging"}.Payload(),
			},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, entity.VerseRef("task", "instance", "review@demo.pr.2"),
		entity.Obj{"status": "running", "flow": `demo.pr."2"`, "chain": "review", "trigger": "review"}.Payload())

	tree := onlyTree(t, eng, `demo.pr."2"`)
	root, ok := find(tree.Roots, "review")
	if !ok || root.Status != "running" {
		t.Fatalf("root not running: %+v", tree.Roots)
	}
	next, ok := find(tree.Roots, "review.onDone")
	if !ok {
		t.Fatalf("onDone branch missing: %+v", tree.Roots)
	}
	if next.Status != StatusPending {
		t.Errorf("onDone status = %q; want %q — its parent has not finished", next.Status, StatusPending)
	}
}

// A task with no trigger behind it (a bare `verse task run`) is still part of the
// subject's flow. It has no charter position, so it is a root of its own rather than being
// dropped — a tree that silently omits work is worse than one with a bare node in it.
func TestFlowTreeIncludesChainlessTasks(t *testing.T) {
	store := entitytest.Store(t)
	eng := treeEngine(t, store)
	entitytest.Set(t, store, entity.VerseRef("task", "instance", "run-abc"),
		entity.Obj{"status": "running", "flow": `demo.pr."3"`, "prompt": "poke at it"}.Payload())

	tree := onlyTree(t, eng, `demo.pr."3"`)
	if len(tree.Roots) != 1 {
		t.Fatalf("want one root; got %+v", tree.Roots)
	}
	n := tree.Roots[0]
	if n.Kind != "task" || n.ID != "run-abc" || n.Chain != "" {
		t.Errorf("chainless task rendered as %+v; want kind=task id=run-abc chain=\"\"", n)
	}
}

// History outlives the charter: a trigger removed after its tasks ran leaves a tree
// that still shows what happened, with no shape to extend it by.
func TestFlowTreeSurvivesTriggerRemoval(t *testing.T) {
	store := entitytest.Store(t)
	eng := treeEngine(t, store)
	entitytest.Set(t, store, entity.VerseRef("task", "instance", "gone@demo.pr.4"),
		entity.Obj{"status": "done", "flow": `demo.pr."4"`, "chain": "gone", "trigger": "gone"}.Payload())

	tree := onlyTree(t, eng, `demo.pr."4"`)
	n, ok := find(tree.Roots, "gone")
	if !ok {
		t.Fatalf("history lost when the trigger was gone: %+v", tree.Roots)
	}
	if n.Status != "done" || len(n.Children) != 0 {
		t.Errorf("node = %+v; want the run task with no declared children", n)
	}
}

// The address grammar is the same one every other flow command speaks, so a pattern yields
// one tree per subject — and a subject nothing has touched yields none.
func TestFlowTreeAddressing(t *testing.T) {
	store := entitytest.Store(t)
	eng := treeEngine(t, store)
	for _, id := range []string{"1", "2"} {
		entitytest.Set(t, store, entity.VerseRef("task", "instance", "review@demo.pr."+id),
			entity.Obj{"status": "done", "flow": `demo.pr."` + id + `"`, "chain": "review", "trigger": "review"}.Payload())
	}
	m, _ := ParseExpr("demo.pr.*")
	if trees := eng.FlowTrees(m); len(trees) != 2 {
		t.Fatalf("pattern selected %d trees; want 2", len(trees))
	}
	m, _ = ParseExpr(`demo.pr."99"`)
	if trees := eng.FlowTrees(m); len(trees) != 0 {
		t.Fatalf("untouched subject yielded %d trees; want 0", len(trees))
	}
}

// A set-only hook is rendered `applied` because it left no task to say so — an inference
// that is wrong exactly when the hook's own write did not land. The engine records that
// failure on the task whose completion fired the hook, so the tree has it in hand and must
// not report the act as done: a `derive` that never resolved is the case this exists for.
func TestFlowTreeHookFailure(t *testing.T) {
	store := entitytest.Store(t)
	eng := treeEngine(t, store)

	if err := eng.AddTrigger(Trigger{
		Name:      "review",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch:  entity.Obj{"stage": "reviewing"}.Payload(),
			Spawn:  &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "review $[subject.ref]"},
			OnDone: &Action{Patch: entity.Obj{"stage": "reviewed"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	// The task that ran, plus the failure its completion hook recorded — written here rather
	// than provoked, because what the tree owes is to READ the record wherever it comes from.
	entitytest.Set(t, store, entity.VerseRef("task", "instance", "review@demo.pr.7"),
		entity.Obj{
			"status": "done", "flow": `demo.pr."7"`, "chain": "review", "trigger": "review",
			"hook": entity.Obj{"chain": "review.onDone", "status": "failed", "err": "subject.value: no causing commit"},
		}.Payload())

	n, ok := find(onlyTree(t, eng, `demo.pr."7"`).Roots, "review.onDone")
	if !ok {
		t.Fatalf("hook node missing from the tree")
	}
	if n.Status != StatusFailed {
		t.Errorf("hook status = %q; want %q — the tree reported a hook that did not land", n.Status, StatusFailed)
	}
	if n.Err == "" {
		t.Errorf("hook node carries no err; the reason is the point of reporting the failure")
	}
}

// The same, for the hook a SEQUENCE carries: it fires when the last element completes, so
// the record lands on that element's task and not on the chain above it. The tree looks by
// the chain the record names, which is what makes those two cases one.
func TestFlowTreeSeqCarrierHookFailure(t *testing.T) {
	store := entitytest.Store(t)
	eng := treeEngine(t, store)

	if err := eng.AddTrigger(Trigger{
		Name:      "rollout",
		Condition: Condition{Subject: prSubject("open")},
		Action: Action{
			Patch:  entity.Obj{"stage": "deploying"}.Payload(),
			Seq:    []Action{{Spawn: &launch.Spec{Agent: &runner.Agent{Name: "verse"}, Prompt: "canary $[subject.ref]"}}},
			OnDone: &Action{Patch: entity.Obj{"stage": "deployed"}.Payload()},
		},
	}); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}
	entitytest.Set(t, store, entity.VerseRef("task", "instance", "rollout.seq.0@demo.pr.8"),
		entity.Obj{
			"status": "done", "flow": `demo.pr."8"`, "chain": "rollout.seq[0]", "trigger": "rollout",
			"hook": entity.Obj{"chain": "rollout.onDone", "status": "failed", "err": "derive did not resolve"},
		}.Payload())

	n, ok := find(onlyTree(t, eng, `demo.pr."8"`).Roots, "rollout.onDone")
	if !ok {
		t.Fatalf("carrier hook node missing from the tree")
	}
	if n.Status != StatusFailed || n.Err == "" {
		t.Errorf("carrier hook = %+v; want failed with a reason", n)
	}
}
